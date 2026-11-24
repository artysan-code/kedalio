package search

import (
	"container/list"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Options of a search; the zero value of each field means its default.
type Options struct {
	// Limit is how many pages to return (10).
	Limit int
	// Embedder adds meaning to the words; nil searches words alone.
	Embedder Embedder
	// Timeout is how long the models may take before search answers without them (4 s).
	Timeout time.Duration
	// Citations is how many passages to return per page (3).
	Citations int
	// Scope narrows what is searched: everything (the zero value), the wiki only, or some collections.
	Scope Scope
	// Version, when set, reads the documents of the collections as they were at that version, the superseded ones too,
	// and leaves the wiki out. By default the superseded documents are left out.
	Version string
}

// ScopeKind says where a search looks.
type ScopeKind int

const (
	// ScopeAll is the wiki and the collections.
	ScopeAll ScopeKind = iota
	// ScopeWiki is the pages only.
	ScopeWiki
	// ScopeCollections is the documents of the collections in Scope.IDs.
	ScopeCollections
)

// Scope is where a search looks.
type Scope struct {
	Kind ScopeKind
	// IDs are the collections of ScopeCollections.
	IDs []string
}

// DocumentRef says which document a result is: a result is a page or a document.
type DocumentRef struct {
	ID string
	// Collection is the collection's name, CollectionID its id.
	Collection, CollectionID string
	Origin                   string
	// Version is the collection's version when the document was written.
	Version string
	// Superseded: a newer version or the end of its origin replaced the document; its passages are how it was.
	Superseded bool
}

// where is the SQL that keeps a search to its scope, over chunks aliased c; it begins with « and ».
func (o Options) where() (string, []any) {
	var sql string
	var args []any
	switch o.Scope.Kind {
	case ScopeWiki:
		sql = " and c.path not like 'doc:%'"
	case ScopeCollections:
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(o.Scope.IDs)), ", ")
		if marks == "" {
			marks = "null"
		}
		sql = " and c.path in (select 'doc:' || id from search_documents where collection in (" + marks + "))"
		for _, id := range o.Scope.IDs {
			args = append(args, id)
		}
	}
	if o.Version != "" {
		sql += " and c.path in (select 'doc:' || id from search_documents where version = ? or seen_version = ?)"
		args = append(args, o.Version, o.Version)
	} else {
		sql += " and c.path not in (select 'doc:' || id from search_documents where superseded is not null)"
	}
	return sql, args
}

// Citation is a passage of a page that matched.
type Citation struct {
	// Chunk is the chunk's id in the index.
	Chunk int64
	// Position is the chunk's place in its page, from 0.
	Position int
	Text     string
	// Heading is the heading the passage sits under; empty when none does.
	Heading string
	// Context is the heading path, «Title › Heading › Subheading»; empty when no heading sits above the chunk.
	Context string
	// At is where the passage sits in its source: the PDF page, the heading, the address.
	At Locator
}

// Result is a page, or a document of a collection, that matched.
type Result struct {
	// Path is the page's path; for a document, «doc:<id>» (Document says more).
	Path  string
	Title string
	// Document is set when the result is a document of a collection.
	Document  *DocumentRef
	Score     float64
	Citations []Citation
	// Words and Meaning say which rankings held a chunk of this page: a page only the words found, or only the
	// meaning, is a weaker hit.
	Words, Meaning bool
	// Cosine is the cosine between the query and the page's nearest chunk, when the meaning ranked one of them
	// (recall's floor); nil otherwise.
	Cosine *float64
}

// Answer is what a search returns.
type Answer struct {
	Results []Result
	// Meaning says whether meaning took part; a model that failed is named in Notes.
	Meaning bool
	// Waiting is how many chunks still wait for the model, when meaning took part: a page whose chunks are among them
	// has no nearness to show, which says nothing about whether it is near.
	Waiting int
	Notes   []string
}

const (
	// perRanking is how many chunks each ranking hands to the fusion.
	perRanking = 60
	// candidates is how many the int8 first pass keeps for the exact float32 score.
	candidates = 400
	// perSource is how many chunks of one page rank among the candidates before the others have had theirs: a long
	// page must not fill the answer with its own pieces. Each ranking reads pool times more rows than it hands on,
	// to have others to put in the places the held-back ones leave.
	perSource = 2
	pool      = 4
	// windowPerSource is how many chunks of one page a ranking reads at most, before it hands on its best: a page with
	// hundreds of matching chunks would otherwise fill the whole window and hide every other page. It is over perSource
	// because the chunks past that fill the places the other pages leave, and a page cites up to three.
	windowPerSource = 4
	// prefixMin: from this many characters a query word also matches the words it starts (typing «config» finds
	// «configuration»). A short one matches itself only: `meta*` reached «metadati» and brought a page about a browser
	// into a question on a company.
	prefixMin = 5

	queryCacheSize = 256

	// rrfK is the constant of the reciprocal rank fusion: the larger it is, the less the first places weigh.
	rrfK = 60
)

type ranked struct {
	id   int64
	path string
}

// diversify returns the first keep of r (ids with their page), at most max of each page; the chunks past that come
// only after all the others, to fill what is left. Order inside each group is kept.
func diversify(r []ranked, keep, max int) []int64 {
	count := map[string]int{}
	var first, rest []int64
	for _, x := range r {
		n := count[x.path]
		count[x.path] = n + 1
		if n < max {
			first = append(first, x.id)
		} else {
			rest = append(rest, x.id)
		}
	}
	all := append(first, rest...)
	return all[:min(keep, len(all))]
}

// fuse merges rankings by reciprocal rank (k is rrfK): no scores to calibrate against each other. Ties keep the order in
// which an item first appeared.
func fuse[T comparable](lists [][]T, k float64) []scored[T] {
	index := map[T]int{}
	var out []scored[T]
	for _, l := range lists {
		for i, x := range l {
			j, ok := index[x]
			if !ok {
				j = len(out)
				index[x] = j
				out = append(out, scored[T]{item: x})
			}
			out[j].score += 1 / (k + float64(i) + 1)
		}
	}
	slices.SortStableFunc(out, func(a, b scored[T]) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		}
		return 0
	})
	return out
}

var terms = regexp.MustCompile(`[\p{L}\p{N}]+`)

// matchExpression is a query's words as an FTS5 expression, the long ones as prefixes, any of them matching.
func matchExpression(q string) string {
	var parts []string
	for _, w := range terms.FindAllString(strings.ToLower(q), -1) {
		if utf8.RuneCountInString(w) >= prefixMin {
			parts = append(parts, `"`+w+`"*`)
		} else {
			parts = append(parts, `"`+w+`"`)
		}
	}
	return strings.Join(parts, " OR ")
}

func (ix *Index) byWords(ctx context.Context, q string, o Options) ([]int64, error) {
	match := matchExpression(q)
	if match == "" {
		return nil, nil
	}
	return ix.byMatch(ctx, match, o)
}

// byTitle ranks the chunks of the pages whose title holds all of the query's words: a page named for what is asked
// leads the pages that only mention it. Any one word would do for the other lanes, but here it would lift a page for
// one word of a long question over the page that covers more of them. It filters the FTS column, so it needs no table
// of its own.
func (ix *Index) byTitle(ctx context.Context, q string, o Options) ([]int64, error) {
	match := matchExpression(q)
	if match == "" {
		return nil, nil
	}
	return ix.byMatch(ctx, "title : ("+strings.ReplaceAll(match, " OR ", " AND ")+")", o)
}

func (ix *Index) byMatch(ctx context.Context, match string, o Options) ([]int64, error) {
	scope, sargs := o.where()
	rows, err := ix.db.QueryContext(ctx,
		// bm25 is only usable in the FTS query itself, so the rank is computed there and the window runs over it
		`with hits as materialized (
		   select c.id as id, c.path as path, bm25(search_chunks_fts, 4, 1) as rank
		   from search_chunks_fts f join search_chunks c on c.id = f.rowid
		   where search_chunks_fts match ?`+scope+`)
		 select id, path from (
		   select id, path, rank, row_number() over (partition by path order by rank) as nth from hits)
		 where nth <= ? order by rank limit ?`,
		append(append([]any{match}, sargs...), windowPerSource, perRanking*pool)...)
	if err != nil {
		return nil, err
	}
	var found []ranked
	for rows.Next() {
		var r ranked
		if err := rows.Scan(&r.id, &r.path); err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return diversify(found, perRanking, perSource), nil
}

// queryCache keeps the vectors of recent queries by model and text: a query asked again (a client retrying, the same
// question in a session) costs no model call, and the vector is the one the model gave.
type queryCache struct {
	max   int
	order *list.List // most recent last
	items map[string]*list.Element
}

type cached struct {
	key string
	vec []float32
}

func newQueryCache(max int) *queryCache {
	return &queryCache{max: max, order: list.New(), items: map[string]*list.Element{}}
}

func (c *queryCache) get(key string) ([]float32, bool) {
	e, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToBack(e)
	return e.Value.(cached).vec, true
}

func (c *queryCache) put(key string, vec []float32) {
	c.items[key] = c.order.PushBack(cached{key, vec})
	if c.order.Len() > c.max {
		first := c.order.Front()
		delete(c.items, first.Value.(cached).key)
		c.order.Remove(first)
	}
}

// pendingVector is a query being embedded: the search that asked waits for done only as long as its own timeout.
type pendingVector struct {
	done chan struct{}
	vec  []float32
	err  error
}

// queryVectorBudget is how long a query's embedding may take on its own, whoever waits for it: a model that is busy or
// still loading answers in a minute rather than never, and a search that gave up on it finds the vector in the cache
// when it is asked again.
const queryVectorBudget = 60 * time.Second

// queryVector embeds q (or takes it from the cache). The embedding runs apart from the search's deadline: a search that
// stops waiting loses the meaning lane for that one answer only, not again at the next ask.
func (ix *Index) queryVector(ctx context.Context, e Embedder, q string) ([]float32, error) {
	key := e.Model() + "\x00" + q
	ix.mu.Lock()
	if hit, ok := ix.queries.get(key); ok {
		ix.mu.Unlock()
		return hit, nil
	}
	p, running := ix.embedding[key]
	if !running {
		p = &pendingVector{done: make(chan struct{})}
		ix.embedding[key] = p
		go func() {
			bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), queryVectorBudget)
			defer cancel()
			vecs, err := e.Embed(bctx, []string{q})
			if err == nil && len(vecs) != 1 {
				err = errBadAnswer
			}
			ix.mu.Lock()
			if err == nil {
				p.vec = vecs[0]
				ix.queries.put(key, p.vec)
			}
			p.err = err
			delete(ix.embedding, key)
			ix.mu.Unlock()
			close(p.done)
		}()
	}
	ix.mu.Unlock()
	select {
	case <-p.done:
		return p.vec, p.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// noteFor says in plain words why a model's lane was left out: never the address, which is the deployment's business.
func noteFor(lane string, err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return lane + " left out: the model was too slow just now; ask again in a moment"
	}
	if errors.Is(err, errBadAnswer) {
		return lane + " left out: the model gave no usable answer"
	}
	return lane + " left out: the model could not be reached"
}

func (ix *Index) byMeaning(ctx context.Context, q string, e Embedder, o Options) ([]int64, map[int64]float64, error) {
	qv, err := ix.queryVector(ctx, e, q)
	if err != nil {
		return nil, nil, err
	}
	unit := Normalize(qv)
	q8 := quantize(unit)
	// first pass on the int8 copies: a quarter of the bytes, enough to find the candidates
	scope, sargs := o.where()
	rows, err := ix.db.QueryContext(ctx,
		"select c.id, c.path, c.q8 from search_chunks c where c.model = ? and c.q8 is not null"+scope, append([]any{e.Model()}, sargs...)...)
	if err != nil {
		return nil, nil, err
	}
	// each page keeps its own best few, so a page with hundreds of near chunks cannot fill all the candidates
	perPage := map[string]*topK[int64]{}
	for rows.Next() {
		var id int64
		var path string
		var b sql.RawBytes
		if err := rows.Scan(&id, &path, &b); err != nil {
			rows.Close()
			return nil, nil, err
		}
		t := perPage[path]
		if t == nil {
			t = newTop[int64](windowPerSource)
			perPage[path] = t
		}
		t.add(id, float64(dotI8(q8, b)))
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, nil, err
	}
	first := newTop[int64](candidates)
	for _, t := range perPage {
		for _, c := range t.best {
			first.add(c.item, c.score)
		}
	}
	if len(first.best) == 0 {
		return nil, map[int64]float64{}, nil
	}
	args := make([]any, len(first.best))
	marks := make([]string, len(first.best))
	for i, c := range first.best {
		args[i], marks[i] = c.item, "?"
	}
	rows, err = ix.db.QueryContext(ctx, "select id, path, vec from search_chunks where id in ("+strings.Join(marks, ", ")+")", args...)
	if err != nil {
		return nil, nil, err
	}
	exact := newTop[ranked](perRanking * pool)
	for rows.Next() {
		var r ranked
		var b sql.RawBytes
		if err := rows.Scan(&r.id, &r.path, &b); err != nil {
			rows.Close()
			return nil, nil, err
		}
		exact.add(r, dotF32(unit, b))
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, nil, err
	}
	// nothing in common is no ranking at all: it would only add noise to the fusion
	var scoredRanked []ranked
	score := map[int64]float64{}
	for _, c := range exact.best {
		if c.score > 0 {
			scoredRanked = append(scoredRanked, c.item)
			score[c.item.id] = c.score
		}
	}
	ids := diversify(scoredRanked, perRanking, perSource)
	cosine := make(map[int64]float64, len(ids))
	for _, id := range ids {
		cosine[id] = score[id]
	}
	return ids, cosine, nil
}

type group struct {
	path, title    string
	score          float64
	citations      []Citation
	words, meaning bool
	cosine         *float64
}

// Search ranks the pages for query: words always, meaning when opts.Embedder is set and answers.
func (ix *Index) Search(ctx context.Context, query string, opts Options) (Answer, error) {
	limit, citations, timeout := opts.Limit, opts.Citations, opts.Timeout
	if limit == 0 {
		limit = 10
	}
	if citations == 0 {
		citations = 3
	}
	if timeout == 0 {
		timeout = 4 * time.Second
	}
	var ans Answer
	words, err := ix.byWords(ctx, query, opts)
	if err != nil {
		return ans, err
	}
	titled, err := ix.byTitle(ctx, query, opts)
	if err != nil {
		return ans, err
	}
	var meaning []int64
	cosine := map[int64]float64{}
	if opts.Embedder != nil && strings.TrimSpace(query) != "" {
		mctx, cancel := context.WithTimeout(ctx, timeout)
		ids, cos, err := ix.byMeaning(mctx, query, opts.Embedder, opts)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ans, ctx.Err()
			}
			ans.Notes = append(ans.Notes, noteFor("meaning", err))
		} else {
			meaning, cosine, ans.Meaning = ids, cos, true
			// a core still embedding (a fresh import, a model that was down) answers worse than either lane alone:
			// say so, so a measurement taken now is not read as the quality
			var waiting, total int
			if err := ix.db.QueryRowContext(ctx,
				"select coalesce(sum(model is null or model <> ?), 0), count(*) from search_chunks", opts.Embedder.Model()).
				Scan(&waiting, &total); err == nil && waiting > 0 {
				ans.Waiting = waiting
				ans.Notes = append(ans.Notes, fmt.Sprintf("meaning partial: %d of %d chunks still wait for the model", waiting, total))
			}
		}
	}
	fused := fuse([][]int64{words, titled, meaning}, rrfK)
	if len(fused) == 0 {
		return ans, nil
	}

	args := make([]any, len(fused))
	marks := make([]string, len(fused))
	for i, f := range fused {
		args[i], marks[i] = f.item, "?"
	}
	rows, err := ix.db.QueryContext(ctx,
		"select id, path, ord, title, text, context, locator from search_chunks where id in ("+strings.Join(marks, ", ")+")", args...)
	if err != nil {
		return ans, err
	}
	type chunkRow struct {
		path, title string
		c           Citation
	}
	byID := map[int64]chunkRow{}
	for rows.Next() {
		var r chunkRow
		var locator string
		if err := rows.Scan(&r.c.Chunk, &r.path, &r.c.Position, &r.title, &r.c.Text, &r.c.Context, &locator); err != nil {
			rows.Close()
			return ans, err
		}
		_ = json.Unmarshal([]byte(locator), &r.c.At)
		r.c.Heading = r.c.At.Heading
		byID[r.c.Chunk] = r
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return ans, err
	}

	// grouped by page: a page scores by its best chunk, its chunks in fused order become its citations
	inWords, inMeaning := map[int64]bool{}, map[int64]bool{}
	for _, id := range words {
		inWords[id] = true
	}
	for _, id := range meaning {
		inMeaning[id] = true
	}
	groups := map[string]*group{}
	var order []string
	for _, f := range fused {
		c, ok := byID[f.item]
		if !ok {
			continue
		}
		g := groups[c.path]
		if g == nil {
			g = &group{path: c.path, title: c.title, score: f.score}
			groups[c.path] = g
			order = append(order, c.path)
		}
		g.words = g.words || inWords[f.item]
		g.meaning = g.meaning || inMeaning[f.item]
		if cos, ok := cosine[f.item]; ok && (g.cosine == nil || cos > *g.cosine) {
			g.cosine = &cos
		}
		if len(g.citations) < citations {
			g.citations = append(g.citations, c.c)
		}
	}
	results := make([]Result, 0, len(order))
	for _, p := range order {
		g := groups[p]
		r := Result{Path: g.path, Title: g.title, Score: g.score, Citations: g.citations,
			Words: g.words, Meaning: g.meaning, Cosine: g.cosine}
		if id, ok := strings.CutPrefix(g.path, "doc:"); ok {
			ref, err := ix.documentRef(ctx, id)
			if err != nil {
				return ans, err
			}
			if ref == nil {
				continue
			}
			r.Document = ref
		}
		results = append(results, r)
	}

	ans.Results = results[:min(limit, len(results))]
	return ans, nil
}

// documentRef is the document with this id as a result names it; nil when it is gone.
func (ix *Index) documentRef(ctx context.Context, id string) (*DocumentRef, error) {
	var r DocumentRef
	var superseded sql.NullString
	err := ix.db.QueryRowContext(ctx,
		`select d.id, k.name, k.id, d.origin, d.version, d.superseded from search_documents d
		 join search_collections k on k.id = d.collection where d.id = ?`, id).
		Scan(&r.ID, &r.Collection, &r.CollectionID, &r.Origin, &r.Version, &superseded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	r.Superseded = superseded.Valid
	return &r, err
}
