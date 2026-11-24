package memory

import (
	"context"
	"sort"
	"strings"
)

const (
	// A pair of pages is a likely duplicate when the text is all but the same (cosine of their `similar` edge from
	// duplicateSure), or the meaning is near (from duplicateNear) and their titles and file names share enough words:
	// two pages about one subject from another side are related, not doubles. A pair with no edge over duplicateNear
	// is never listed, however alike its titles: a hub and its parts have alike titles by design.
	duplicateNear = 0.92
	duplicateSure = 0.97
	duplicateName = 0.34
)

// Duplicate is a pair of pages that may be one page twice.
type Duplicate struct {
	// Key is the two paths, sorted, joined by `|`: what upkeep remembers when the pair is rejected.
	Key  string
	A, B string
	// Reason says which rule found it: "same text" or "near meaning, alike names".
	Reason string
	// Weight is the cosine of the pair's `similar` edge.
	Weight float64
}

type dupPage struct {
	path, title, kind, stem string
	name, titleWords        map[string]bool
	stemWords               map[string]bool
}

// DuplicateCandidates lists the likely duplicate pairs, the surest first (then by key), leaving out the keys in skip
// and stopping at limit (all when limit <= 0). Tasks, episodes, sources and the pages of the diary, the inbox and the
// projects' archive (the areas a) are not compared: a day or an inbox entry is a record, not a subject, and an archived
// page is a copy of one that lives elsewhere. Pages that link to each other are a family, not doubles.
func (s *Store) DuplicateCandidates(ctx context.Context, a Areas, skip map[string]bool, limit int) ([]Duplicate, error) {
	rows, err := s.db.QueryContext(ctx, `select path, title, coalesce(kind, '') from docs
     where deleted = 0 and path not like 'tasks/%' and coalesce(kind, '') not in ('episode', 'source', 'task') order by path`)
	if err != nil {
		return nil, err
	}
	var pages []*dupPage
	byPath := map[string]int{}
	for rows.Next() {
		var p dupPage
		if err := rows.Scan(&p.path, &p.title, &p.kind); err != nil {
			rows.Close()
			return nil, err
		}
		if strings.HasPrefix(p.path, a.Diary+"/") || strings.HasPrefix(p.path, a.Inbox+"/") || strings.HasPrefix(p.path, a.Projects+"/archivio/") {
			continue
		}
		p.stem = strings.ReplaceAll(strings.TrimSuffix(p.path[strings.LastIndex(p.path, "/")+1:], ".md"), "-", " ")
		p.name = TitleWords(p.title + " " + p.stem)
		p.titleWords = TitleWords(p.title)
		p.stemWords = TitleWords(p.stem)
		byPath[p.path] = len(pages)
		pages = append(pages, &p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Only pairs with a `similar` edge are looked at: every page against every other costs a regex and a fold per
	// pair, which is minutes on a few thousand pages.
	weight := map[pair]float64{}
	edges, err := s.db.QueryContext(ctx, "select src, dst, w from similar")
	if err != nil {
		return nil, err
	}
	for edges.Next() {
		var src, dst string
		var w float64
		if err := edges.Scan(&src, &dst, &w); err != nil {
			edges.Close()
			return nil, err
		}
		i, ok1 := byPath[src]
		j, ok2 := byPath[dst]
		if !ok1 || !ok2 || i == j {
			continue
		}
		k := pair{min(i, j), max(i, j)}
		weight[k] = max(weight[k], w)
	}
	edges.Close()
	if err := edges.Err(); err != nil {
		return nil, err
	}

	linked, err := s.linkedPairs(ctx, byPath)
	if err != nil {
		return nil, err
	}

	var out []Duplicate
	for c := range weight {
		x, y := pages[c.i], pages[c.j]
		w := weight[c]
		reason := ""
		switch {
		case w >= duplicateSure:
			reason = "same text"
		case w >= duplicateNear && jaccard(x.name, y.name) >= duplicateName:
			reason = "near meaning, alike names"
		default:
			continue
		}
		if isCardOf(x.path, y.path) || isCardOf(y.path, x.path) || linked[c] {
			continue
		}
		// A hub and its parts are one family by design. Text all but identical is a double even inside a family
		// of file names (`anna.md`, `anna-rossi.md`), but never when one page sits under the other's folder.
		nested, family := pageFamily(x.path, y.path)
		if nested || (family && reason != "same text") {
			continue
		}
		k := dupKey(x.path, y.path)
		if skip[k] {
			continue
		}
		out = append(out, Duplicate{Key: k, A: min(x.path, y.path), B: max(x.path, y.path), Reason: reason, Weight: w})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return out[i].Key < out[j].Key
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// isCardOf: card is the project card of page, `X/card.md` beside `X.md`. They are two pages by design, however alike.
func isCardOf(card, page string) bool {
	x, ok := strings.CutSuffix(card, "/card.md")
	return ok && x+".md" == page
}

// pageFamily says how two pages of one subject relate by path. Take each page's bases: its path without `.md` and
// every folder above it (`a/b/x.md` has `a`, `a/b`, `a/b/x`). nested: one page's own base is the other's folder
// (`X.md` and `X/…`, at any depth). family: nested, or one page's own base `X` is, or is followed by `-<more>` as,
// a base of the other (`X.md` and `X-more.md` or `X-more/…`), or two bases in one folder have names that start with
// the same word (`X-a.md` and `X-b.md`, `X-a/…` and `X-b.md`): the family root is that shared run of words.
func pageFamily(p, q string) (nested, family bool) {
	bp, bq := pageBases(p), pageBases(q)
	op, oq := bp[len(bp)-1], bq[len(bq)-1]
	for _, v := range bq[:len(bq)-1] {
		if v == op {
			nested = true
		}
	}
	for _, v := range bp[:len(bp)-1] {
		if v == oq {
			nested = true
		}
	}
	if nested {
		return true, true
	}
	for _, v := range bq {
		if v == op || strings.HasPrefix(v, op+"-") {
			return false, true
		}
	}
	for _, v := range bp {
		if v == oq || strings.HasPrefix(v, oq+"-") {
			return false, true
		}
	}
	for _, u := range bp {
		for _, v := range bq {
			if u != v && sameRoot(u, v) {
				return false, true
			}
		}
	}
	return false, false
}

// sameRoot: two base paths in one folder whose names start with the same word.
func sameRoot(u, v string) bool {
	du, nu := u[:strings.LastIndex(u, "/")+1], u[strings.LastIndex(u, "/")+1:]
	dv, nv := v[:strings.LastIndex(v, "/")+1], v[strings.LastIndex(v, "/")+1:]
	if du != dv {
		return false
	}
	wu, _, _ := strings.Cut(nu, "-")
	wv, _, _ := strings.Cut(nv, "-")
	return wu != "" && wu == wv
}

func pageBases(path string) []string {
	segs := strings.Split(strings.TrimSuffix(path, ".md"), "/")
	out := make([]string, len(segs))
	for i := range segs {
		out[i] = strings.Join(segs[:i+1], "/")
	}
	return out
}

// dupKey is the two paths sorted and joined by `|`.
func dupKey(a, b string) string {
	if b < a {
		a, b = b, a
	}
	return a + "|" + b
}

// jaccard is the share of the words that two sets have in common; 0 when they share none.
func jaccard(x, y map[string]bool) float64 {
	both := 0
	for w := range x {
		if y[w] {
			both++
		}
	}
	if both == 0 {
		return 0
	}
	return float64(both) / float64(len(x)+len(y)-both)
}

// pair is two pages by their index, the lower first.
type pair struct{ i, j int }

// linkedPairs are the pairs of pages, among byPath, of which one links to the other (a hub's list, `part_of` either
// way): a hub the upkeep split into children is alike its children by construction.
func (s *Store) linkedPairs(ctx context.Context, byPath map[string]int) (map[pair]bool, error) {
	resolve, err := s.Resolver(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "select src, dst from links")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[pair]bool{}
	for rows.Next() {
		var src, dst string
		if err := rows.Scan(&src, &dst); err != nil {
			return nil, err
		}
		i, ok1 := byPath[src]
		j, ok2 := byPath[resolve(dst)]
		if ok1 && ok2 && i != j {
			out[pair{min(i, j), max(i, j)}] = true
		}
	}
	return out, rows.Err()
}
