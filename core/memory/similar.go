package memory

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"

	"kedalio/core/memory/search"
)

// The pages near each other by meaning, kept as edges. A page's vector is the unit mean of its chunks' (search embeds them in the
// background); once all of a page's chunks carry the model's vectors, the page's nearest pages above a threshold are
// stored as `similar` edges with the cosine as weight. Recall walks them like links, so a page nobody linked still
// sits next to its subject. They stay apart from `links`: those are what writers wrote, these are the core's. Diary
// days are left out: a day mixes subjects, so its mean is near everything. The threshold is the account's own: a
// model's cosines sit higher or lower, and on the owner's memory a fixed 0.6 gave nearly every page its five
// neighbours.

const (
	// Neighbours are the nearest pages kept per page; two pages count as near from the account's 98th simPercentile of
	// the cosines between its pages, never below the floor.
	Neighbours = 5
	// Similar is the threshold while there are too few pages to measure one.
	Similar       = 0.6
	simPercentile = 0.98
	simFloor      = 0.5
	// simSample is how many pages the simPercentile is measured on (all pairs among them); simDrift is the change in it that
	// has every page compared again.
	simSample = 300
	simDrift  = 0.02
	// simPerRound is how many pages are refreshed at once: a first import has them all.
	simPerRound = 100
)

// similarThreshold is the key in memory_meta where the account's threshold is kept.
const similarThreshold = "similar_threshold"

// meanUnit is the unit-length mean of unit vectors; nil for none.
func meanUnit(vs [][]float32) []float32 {
	if len(vs) == 0 {
		return nil
	}
	sum := make([]float32, len(vs[0]))
	for _, v := range vs {
		for i := range v {
			sum[i] += v[i]
		}
	}
	return search.Normalize(sum)
}

type pageVec struct {
	path string
	vec  []float32
}

type near struct {
	path string
	w    float64
}

// nearest returns the k nearest of others to v at min or above, nearest first; the page at self is not its own
// neighbour.
func nearest(v []float32, others []pageVec, self string, k int, min float64) []near {
	var out []near
	for _, o := range others {
		if o.path == self {
			continue
		}
		w := search.Dot(v, o.vec)
		if w < min {
			continue
		}
		out = append(out, near{o.path, w})
		if len(out) > k*4 {
			sort.SliceStable(out, func(a, b int) bool { return out[a].w > out[b].w })
			out = out[:k]
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].w > out[b].w })
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// quantile is the p quantile of vals (0..1) by the nearest rank; NaN for none.
func quantile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	return s[max(0, min(len(s)-1, int(math.Ceil(p*float64(len(s))))-1))]
}

// thresholdOf is the threshold of «near» for a set of page vectors: the high quantile of the cosines between them
// (a sample of `simSample` pages when there are more), never below the floor; the fixed Similar while there are too few
// to measure.
func thresholdOf(vecs [][]float32) float64 {
	if len(vecs) < 10 {
		return Similar
	}
	step := max(1, len(vecs)/simSample)
	var pick [][]float32
	for i, v := range vecs {
		if i%step == 0 && len(pick) < simSample {
			pick = append(pick, v)
		}
	}
	var cos []float64
	for i := range pick {
		for j := i + 1; j < len(pick); j++ {
			cos = append(cos, search.Dot(pick[i], pick[j]))
		}
	}
	return math.Max(simFloor, quantile(cos, simPercentile))
}

// RefreshSimilar brings the page vectors and their `similar` edges up to date with the pages and the model's chunks:
// the pages written since (another rev), a round at a time, and none of the pages gone. A page whose chunks are not
// all embedded yet waits for the next round. It returns how many pages were refreshed. The search index must have
// been migrated on the same database.
// A collection's source hub is left out like an episode: it lists its documents' titles, so it sits near whatever
// they are about and would bridge pages that have nothing to do with each other.
func (s *Store) RefreshSimilar(ctx context.Context, model string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
delete from page_vectors where path not in (
  select path from docs where deleted = 0 and path not like 'tasks/%' and coalesce(kind, '') not in ('episode', 'source'));
delete from similar where src not in (select path from page_vectors) or dst not in (select path from page_vectors);`); err != nil {
		return 0, err
	}
	// pages compared before the account had its own threshold are compared again
	var one int
	switch err := tx.QueryRowContext(ctx, "select 1 from memory_meta where k = ?", similarThreshold).Scan(&one); err {
	case sql.ErrNoRows:
		if _, err := tx.ExecContext(ctx, "update page_vectors set rev = -1"); err != nil {
			return 0, err
		}
	case nil:
	default:
		return 0, err
	}
	err = tx.Commit()
	s.graphChanged() // the first statement drops the similarities of pages that are gone
	if err != nil {
		return 0, err
	}

	type stale struct {
		path string
		rev  int
	}
	rows, err := s.db.QueryContext(ctx, `select d.path, d.rev from docs d left join page_vectors v on v.path = d.path
     where d.deleted = 0 and d.path not like 'tasks/%' and coalesce(d.kind, '') not in ('episode', 'source')
       and (v.path is null or v.rev <> d.rev or v.model <> ?)
     order by d.updated desc, d.path`, model)
	if err != nil {
		return 0, err
	}
	var todo []stale
	for rows.Next() {
		var st stale
		if err := rows.Scan(&st.path, &st.rev); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var fresh []pageVec
	for _, st := range todo {
		if len(fresh) >= simPerRound {
			break
		}
		vec, err := s.pageVector(ctx, st.path, model)
		if err != nil {
			return 0, err
		}
		if vec == nil {
			continue
		}
		if _, err := s.db.ExecContext(ctx,
			`insert into page_vectors (path, rev, model, vec) values (?, ?, ?, ?)
     on conflict (path) do update set rev = excluded.rev, model = excluded.model, vec = excluded.vec`,
			st.path, st.rev, model, search.Float32Bytes(vec)); err != nil {
			return 0, err
		}
		fresh = append(fresh, pageVec{st.path, vec})
	}
	if len(fresh) == 0 {
		return 0, nil
	}

	rows, err = s.db.QueryContext(ctx, "select path, vec from page_vectors where model = ? order by path", model)
	if err != nil {
		return 0, err
	}
	var all []pageVec
	for rows.Next() {
		var p string
		var b []byte
		if err := rows.Scan(&p, &b); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, pageVec{p, search.Float32s(b)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// the threshold moves with the pages; once it has moved enough, every page is compared again, a round at a time
	vecs := make([][]float32, len(all))
	for i, a := range all {
		vecs[i] = a.vec
	}
	cut := thresholdOf(vecs)
	was := math.NaN()
	var raw string
	if err := s.db.QueryRowContext(ctx, "select v from memory_meta where k = ?", similarThreshold).Scan(&raw); err == nil {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			was = f
		}
	} else if err != sql.ErrNoRows {
		return 0, err
	}
	if !(math.Abs(cut-was) <= simDrift) {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, "insert or replace into memory_meta (k, v) values (?, ?)",
			similarThreshold, strconv.FormatFloat(cut, 'g', -1, 64)); err != nil {
			return 0, err
		}
		// the pages not compared in this round lose their rev, so the next rounds take them again
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(fresh)), ", ")
		args := make([]any, len(fresh))
		for i, f := range fresh {
			args[i] = f.path
		}
		if _, err := tx.ExecContext(ctx, "update page_vectors set rev = -1 where path not in ("+marks+")", args...); err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
	}
	for _, p := range fresh {
		if err := s.setSimilar(ctx, p.path, nearest(p.vec, all, p.path, Neighbours, cut)); err != nil {
			return 0, err
		}
	}
	return len(fresh), nil
}

// pageVector is the mean of a page's chunk vectors, nil while any of its chunks is not embedded by model (or it has
// none).
func (s *Store) pageVector(ctx context.Context, path, model string) ([]float32, error) {
	rows, err := s.db.QueryContext(ctx, "select model, vec from search_chunks where path = ? order by ord", path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var vs [][]float32
	for rows.Next() {
		var m sql.NullString
		var b []byte
		if err := rows.Scan(&m, &b); err != nil {
			return nil, err
		}
		if m.String != model || len(b) == 0 {
			return nil, rows.Err()
		}
		vs = append(vs, search.Float32s(b))
	}
	return meanUnit(vs), rows.Err()
}

func (s *Store) setSimilar(ctx context.Context, src string, ns []near) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "delete from similar where src = ?", src); err != nil {
		return err
	}
	for _, n := range ns {
		if _, err := tx.ExecContext(ctx, "insert or replace into similar (src, dst, w) values (?, ?, ?)",
			src, n.path, n.w); err != nil {
			return err
		}
	}
	err = tx.Commit()
	s.graphChanged()
	return err
}

// NearThreshold is the cosine from which two pages of this account count as near: the account's own once measured
// (a stored value that is not a positive number is no measure), Similar before. Recall and remember both read it here.
func (s *Store) NearThreshold(ctx context.Context) (float64, error) {
	var raw string
	switch err := s.db.QueryRowContext(ctx, "select v from memory_meta where k = ?", similarThreshold).Scan(&raw); {
	case err == nil:
		if f, err := strconv.ParseFloat(raw, 64); err == nil && f > 0 {
			return f, nil
		}
		return Similar, nil
	case errors.Is(err, sql.ErrNoRows):
		return Similar, nil
	default:
		return 0, err
	}
}

// NearestPage is the page whose vector (made by model) is nearest to a unit vector, with the cosine; "" when none
// reaches floor. Pages without a vector yet (diary days, tasks, pages the background embedding has not reached) are not
// candidates.
func (s *Store) NearestPage(ctx context.Context, unit []float32, model string, floor float64) (string, float64, error) {
	rows, err := s.db.QueryContext(ctx, "select path, vec from page_vectors where model = ?", model)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	best, bestW := "", floor
	for rows.Next() {
		var path string
		var b []byte
		if err := rows.Scan(&path, &b); err != nil {
			return "", 0, err
		}
		if w := search.Dot(unit, search.Float32s(b)); w >= bestW && (best == "" || w > bestW) {
			best, bestW = path, w
		}
	}
	return best, bestW, rows.Err()
}
