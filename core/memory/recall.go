package memory

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"kedalio/core/memory/search"
)

// Recall is everything the memory holds about a subject, in one call and within a budget. The hybrid search's pages are the seeds; a personalized PageRank spreads from
// them along links and relations, so a page that never says the query's words but sits next to what does (the people
// of a game, another project on the same API) comes up too. The answer is a pack: the strongest pages in full, the
// others by title and lede with the edge that brought them. An empty query is the core: who the owner is, what moved
// lately.
//
// The memory imports search for its types and its index; search does not import the memory (only its external
// tests do), so the packages stay acyclic.

// RecallWeights are the constants that tune recall, in one place.
type RecallWeights struct {
	// Damping is how far the activation walks before it jumps back to the seeds (HippoRAG's 0.5 keeps it near them).
	Damping float64
	// WRel, WLink and WMeaning weigh an edge: a named relation, a link dropped in a sentence, a nearness of meaning
	// (scaled by how near).
	WRel, WLink, WMeaning float64
	// Episode: what happened (the diary) bridges subjects but answers few questions by itself.
	Episode float64
	// SeedMin: a hit under this share of the best one is noise, not a seed.
	SeedMin float64
	// Strong: a seed under this share of the best one is weak; it stays only when a written link joins it to a strong
	// one.
	Strong float64
	// FullShare is the share of the budget the pages in full may take.
	FullShare float64
	// EpisodesMax is the diary days among the related pages.
	EpisodesMax int
	// RecentSeed and RecentEpisodes: a question that speaks of time («ieri», «questa settimana»): the window's
	// episodes about the subject join the seeds at this share of the best, and this many of them may sit among the
	// related pages.
	RecentSeed     float64
	RecentEpisodes int
	// OutsideWindow: an episode outside that window counts for this share of its activation.
	OutsideWindow float64
	// FloorWords and FloorMeaning are the floor under which the memory has nothing about the question: the share of
	// the query's words some seed must hold, or else the cosine it must reach, which is the account's «near»
	// threshold less this margin.
	FloorWords, FloorMeaning float64
	// ChildPrefer: 1: a hub's child that holds a query word the hub lacks leads it and the hub's other children are
	// left out; 0: off.
	ChildPrefer float64
	// ChildSpread: a seed under this share of the best one does not hand its activation to its children.
	ChildSpread float64
	// CoverageWeight: a search hit counts for this share of its score by how many of the query's words its page
	// holds (0: by its rank alone).
	CoverageWeight float64
	// HubFocus: 1: when a page linked to a seed hub holds more of the question's words than the hub, the hub's plain
	// links to pages that hold fewer stay out; 0: off.
	HubFocus                   float64
	Seeds, FullMax, RelatedMax int
}

// DefaultRecallWeights are today's weights.
var DefaultRecallWeights = RecallWeights{
	Damping:        0.5,
	WRel:           1,
	WLink:          0.5,
	WMeaning:       0.5,
	Episode:        0.5,
	SeedMin:        0.4,
	Strong:         0.8,
	FullShare:      0.6,
	EpisodesMax:    2,
	RecentSeed:     0.8,
	RecentEpisodes: 8,
	OutsideWindow:  0.2,
	FloorWords:     0.6,
	FloorMeaning:   0.15,
	ChildPrefer:    1,
	ChildSpread:    0.8,
	CoverageWeight: 0.5,
	HubFocus:       1,
	Seeds:          15,
	FullMax:        4,
	RelatedMax:     6,
}

const (
	iterations = 20
	// docFloorWords is the share of a multi-word query's words a document's passage must hold to be shown.
	docFloorWords = 0.75
	// meaningTimeout is how long recall waits for the model before it goes on with the words: longer than a search's,
	// because a recall that loses the meaning lane may then say "nothing relevant" about what the memory holds.
	meaningTimeout = 10 * time.Second
	// titleBonus is the share a page's score grows by when its title holds all the query's words (less by its share of
	// them): enough to settle a close call, not to put a page that holds few of the words over one that holds them all.
	titleBonus = 0.3
	// namedInside is the weight of a page named by a run of words inside a longer question, over the best hit's 1: it
	// leads the pack, yet a specific child of it the question also asks for (found by its own words) comes next to it.
	namedInside = 1.5
	// taskClosed: a task that is done or dropped is history; it says little about the subject beside the open work.
	taskClosed = 0.2
	// supersededShare: a page its front matter marks superseded or obsolete counts for this share of its score, so the
	// page that replaced it comes first while it stays findable as history. Read from the page, not the index: no
	// chunk carries the flag.
	supersededShare = 0.3
	// ledeTokens is the length of the passage a related page shows in place of its lede.
	ledeTokens = 60
	// childrenMax is the children of a hub looked at for the question's words, childShare the share of the hub's
	// weight a child it brings in starts from.
	childrenMax = 40
	childShare  = 0.9
	// subjectsMax is the pages named by a question of time, besides the seeds, whose episodes are looked for.
	subjectsMax = 8
	// recordShare: an episode that is only a record counts for this share of its activation, so the page it is about
	// comes first.
	recordShare = 0.4
)

// stopwords say nothing about the subject of a question, Italian and English.
var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Split("il lo la i gli le un uno una di a da in con su per tra fra e ed o ma che chi cosa come dove quando quale quali "+
		"mi ti ci si me te mio mia miei mie suo sua del dello della dei degli delle al allo alla ai agli alle dal dalla "+
		"nel nella nei nelle sul sulla non ho ha è sono the an of to on for and or with my is are what who "+
		"how where when which about dell all nell sull dall quell", " ") {
		m[w] = true
	}
	return m
}()

// utf16Len is a string's length in UTF-16 units, which the tokens are counted and cut in (the clients measure them so).
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// prefixUnits is the first n UTF-16 units of s (a pair cut in half is dropped).
func prefixUnits(s string, n int) string {
	if n <= 0 {
		return ""
	}
	u := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if u+w > n {
			return s[:i]
		}
		u += w
	}
	return s
}

// Tokens is a rough count of tokens: four characters each, which is what the models' tokenizers average on prose.
func Tokens(s string) int { return (utf16Len(s) + 3) / 4 }

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }

// wordMatcher finds a set of literal phrases that stand alone: not inside a longer word. Go's regexp has no
// look-around, so the boundaries are checked here.
type wordMatcher struct{ re *regexp.Regexp }

func newWordMatcher(alts string) wordMatcher {
	return wordMatcher{regexp.MustCompile("(?i)^(?:" + alts + ")")}
}

// find returns the byte ranges of the matches, left to right, none overlapping.
func (m wordMatcher) find(s string) [][2]int {
	var out [][2]int
	prev := rune(-1)
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		if prev == -1 || !isWordRune(prev) {
			if loc := m.re.FindStringIndex(s[i:]); loc != nil && loc[1] > 0 {
				end := i + loc[1]
				if next, _ := utf8.DecodeRuneInString(s[end:]); end == len(s) || !isWordRune(next) {
					out = append(out, [2]int{i, end})
					prev, _ = utf8.DecodeLastRuneInString(s[:end])
					i = end
					continue
				}
			}
		}
		prev = r
		i += w
	}
	return out
}

func (m wordMatcher) replace(s, with string) string {
	var b strings.Builder
	at := 0
	for _, loc := range m.find(s) {
		b.WriteString(s[at:loc[0]])
		b.WriteString(with)
		at = loc[1]
	}
	b.WriteString(s[at:])
	return b.String()
}

// when is an expression of time, each with the days back from today it covers: [first, last]. Italian and English.
var when = []struct {
	m    wordMatcher
	back [2]int
}{
	{newWordMatcher("oggi|today"), [2]int{0, 0}},
	{newWordMatcher("ieri|yesterday"), [2]int{1, 1}},
	{newWordMatcher("la settimana scorsa|scorsa settimana|last week|previous week"), [2]int{13, 7}},
	{newWordMatcher("questa settimana|quest'ultima settimana|ultima settimana|ultimi giorni|ultimi tempi|ultimamente|di recente|" +
		"recentemente|this week|past week|last few days|past few days|last days|recent days|lately|recently"), [2]int{6, 0}},
	{newWordMatcher("questo mese|this month"), [2]int{29, 0}},
}

const dayLayout = "2006-01-02"

// dayOf is the calendar day of t in its own zone.
func dayOf(t time.Time) string { return t.Format(dayLayout) }

type timeWin struct{ From, To, Rest string }

var spaces = regexp.MustCompile(`\s+`)

// timeWindow is the days a question speaks of («ieri», «questa settimana», «lately»), as From and To (YYYY-MM-DD,
// both included), and the question without those words; nil when it names no time.
func timeWindow(q string, now time.Time) *timeWin {
	reach, end, rest := -1, math.MaxInt, q
	for _, w := range when {
		if len(w.m.find(q)) == 0 {
			continue
		}
		reach = max(reach, w.back[0])
		end = min(end, w.back[1])
		rest = w.m.replace(rest, " ")
	}
	if reach < 0 {
		return nil
	}
	y, m, d := now.Date()
	at := func(back int) string { return time.Date(y, m, d-back, 0, 0, 0, 0, now.Location()).Format(dayLayout) }
	// the widest reach back and the nearest end: «ieri e questa settimana» is the week
	return &timeWin{From: at(reach), To: at(end), Rest: strings.TrimSpace(spaces.ReplaceAllString(rest, " "))}
}

var past = newWordMatcher("prima|una volta|un tempo|quando|storia|storico|cronologia|passato|history|historical|before|used to|" +
	"formerly|previously|back then|when|(?:a|ad|in|di|da|nel|ai|of|during|since|from)\\s+(?:gennaio|febbraio|marzo|aprile|" +
	"maggio|giugno|luglio|agosto|settembre|ottobre|novembre|dicembre|january|february|march|april|may|june|july|" +
	"august|september|october|november|december)")

// aboutPast: the question looks back — a time window («ieri») or a cue of the past («prima», «una volta»,
// «a settembre», «quando», «storia», «used to», «in March»): then a page's history is part of the answer.
func aboutPast(q string, now time.Time) bool {
	return timeWindow(q, now) != nil || len(past.find(q)) > 0
}

var historyHead = regexp.MustCompile(`(?i)^##\s+(storia|history)\s*$`)

// withoutHistory is the body without its `## Storia` / `## History` section (up to the next `## ` heading), and
// whether it had one.
func withoutHistory(body string) (string, bool) {
	var out []string
	skipping, had := false, false
	for _, l := range strings.Split(body, "\n") {
		if h2.MatchString(l) {
			skipping = historyHead.MatchString(l)
		}
		if skipping {
			had = true
		} else {
			out = append(out, l)
		}
	}
	if !had {
		return body, false
	}
	return strings.TrimRightFunc(strings.Join(out, "\n"), unicode.IsSpace) + "\n", true
}

type edge struct {
	to int
	w  float64
	// rel is the relation's name, "" for a plain link; meaning marks a nearness of meaning, not a link.
	rel     string
	meaning bool
}

// graph is the pages and their edges, both ways. adj is per node index: its neighbours.
type graph struct {
	nodes []string
	adj   [][]edge
	index map[string]int
}

// seedSet is the seeds (node index → weight) in the order they were added, which the walk over them depends on.
type seedSet struct {
	keys []int
	w    map[int]float64
}

func newSeeds() *seedSet { return &seedSet{w: map[int]float64{}} }

func (s *seedSet) set(i int, w float64) {
	if _, ok := s.w[i]; !ok {
		s.keys = append(s.keys, i)
	}
	s.w[i] = w
}

func (s *seedSet) has(i int) bool { _, ok := s.w[i]; return ok }

func (s *seedSet) del(i int) {
	if _, ok := s.w[i]; !ok {
		return
	}
	delete(s.w, i)
	for k, x := range s.keys {
		if x == i {
			s.keys = append(s.keys[:k:k], s.keys[k+1:]...)
			break
		}
	}
}

func (s *seedSet) size() int { return len(s.keys) }

// filter is a new set of the seeds keep accepts.
func (s *seedSet) filter(keep func(i int, w float64) bool) *seedSet {
	out := newSeeds()
	for _, i := range s.keys {
		if keep(i, s.w[i]) {
			out.set(i, s.w[i])
		}
	}
	return out
}

// activate is the personalized PageRank of seeds over an undirected weighted graph.
func activate(g *graph, seeds *seedSet, d float64, n int) []float64 {
	size := len(g.nodes)
	total := 0.0
	for _, i := range seeds.keys {
		total += seeds.w[i]
	}
	if total == 0 {
		total = 1
	}
	p := make([]float64, size)
	for _, i := range seeds.keys {
		p[i] = seeds.w[i] / total
	}
	deg := make([]float64, size)
	for u, es := range g.adj {
		for _, e := range es {
			deg[u] += e.w
		}
	}
	r := append([]float64(nil), p...)
	for k := 0; k < n; k++ {
		next := make([]float64, size)
		for u := 0; u < size; u++ {
			if r[u] == 0 {
				continue
			}
			// a node with no edges gives its share back to the seeds, so nothing leaks out of the walk
			if deg[u] == 0 {
				for v := 0; v < size; v++ {
					next[v] += d * r[u] * p[v]
				}
				continue
			}
			for _, e := range g.adj[u] {
				next[e.to] += d * r[u] * e.w / deg[u]
			}
		}
		for v := 0; v < size; v++ {
			next[v] += (1 - d) * p[v]
		}
		r = next
	}
	return r
}

// nextTo is `from` and the nodes a written link (not a nearness of meaning) joins to them.
func nextTo(g *graph, from []int) map[int]bool {
	out := map[int]bool{}
	var list []int
	for _, i := range from {
		if !out[i] {
			out[i] = true
			list = append(list, i)
		}
	}
	for _, u := range list {
		for _, e := range g.adj[u] {
			if !e.meaning {
				out[e.to] = true
			}
		}
	}
	return out
}

// noSeed stands for «no page» where a seed index is optional.
const noSeed = -1

// dropStranded is seeds without the weak ones (under strong) that no written link joins to a strong one; keep names
// the seed that stays whatever it weighs. A seed's weight is the search's score, which in a long question can favour a
// page that holds two of its words: one that holds more of the query's words (cover) than any strong seed is not weak
// evidence, and stays.
func dropStranded(g *graph, seeds *seedSet, keep int, strong float64, cover map[int]int) *seedSet {
	var from []int
	bar := 0
	for _, i := range seeds.keys {
		if seeds.w[i] >= strong {
			from = append(from, i)
			bar = max(bar, cover[i])
		}
	}
	reached := nextTo(g, from)
	return seeds.filter(func(i int, w float64) bool {
		return w >= strong || i == keep || reached[i] || cover[i] > bar
	})
}

// dropPartial: when some seed holds every term of the query, a seed that holds only some of them goes unless the
// meaning also found it or a written link joins it to a seed that holds them all («Meta e WhatsApp» is not a page
// that says «WhatsApp» once in a list). cover is the terms each seed holds, total the terms of the query.
func dropPartial(g *graph, seeds *seedSet, cover map[int]int, total int, byMeaning map[int]bool, keep int) *seedSet {
	var full []int
	for _, i := range seeds.keys {
		if cover[i] >= total {
			full = append(full, i)
		}
	}
	if len(full) == 0 {
		return seeds
	}
	reached := nextTo(g, full)
	return seeds.filter(func(i int, _ float64) bool {
		return cover[i] >= total || byMeaning[i] || i == keep || reached[i]
	})
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

func wordsOf(s string) []string { return wordRe.FindAllString(strings.ToLower(s), -1) }

// contentTerms are the content words of a query: no stopwords, none under two letters.
func contentTerms(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range wordsOf(q) {
		if utf16Len(w) > 1 && !stopwords[w] && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// coveredTerms are the terms the text holds; a long term also matches the words that start with it less its last
// letter (gioca, giocare).
func coveredTerms(text string, terms []string) map[string]bool {
	words := wordsOf(text)
	out := map[string]bool{}
	for _, t := range terms {
		stem := ""
		if utf16Len(t) >= 5 {
			_, w := utf8.DecodeLastRuneInString(t)
			stem = t[:len(t)-w]
		}
		for _, w := range words {
			if w == t || (stem != "" && strings.HasPrefix(w, stem)) {
				out[t] = true
				break
			}
		}
	}
	return out
}

func coverage(text string, terms []string) int { return len(coveredTerms(text, terms)) }

// preferChildren is the order with the children that hold a query word their hub lacks lifted to just before it. lift
// maps a hub to those children.
func preferChildren(order []int, lift map[int][]int) []int {
	out := append([]int(nil), order...)
	indexOf := func(x int) int {
		for k, y := range out {
			if y == x {
				return k
			}
		}
		return -1
	}
	for _, hub := range order {
		kids, ok := lift[hub]
		if !ok {
			continue
		}
		at := indexOf(hub)
		var moved []int
		for k, i := range out {
			if k > at && slices.Contains(kids, i) {
				moved = append(moved, i)
			}
		}
		if len(moved) == 0 {
			continue
		}
		for _, k := range moved {
			j := indexOf(k)
			out = append(out[:j], out[j+1:]...)
		}
		h := indexOf(hub)
		out = append(out[:h], append(append([]int(nil), moved...), out[h:]...)...)
	}
	return out
}

// siblingCut is, for a hub, all its children and those that keep their edge.
type siblingCut struct {
	hub        int
	kids, keep []int
}

// withoutSiblings is the graph without the edges that go from a hub to those of its children it is not to spread to.
// The reverse edges stay.
func withoutSiblings(g *graph, cuts []siblingCut) *graph {
	if len(cuts) == 0 {
		return g
	}
	adj := append([][]edge(nil), g.adj...)
	for _, c := range cuts {
		var kept []edge
		for _, e := range adj[c.hub] {
			if !slices.Contains(c.kids, e.to) || slices.Contains(c.keep, e.to) {
				kept = append(kept, e)
			}
		}
		adj[c.hub] = kept
	}
	return &graph{nodes: g.nodes, adj: adj, index: g.index}
}

// recaller reads the memory for one recall: the pages it looks at twice are read once.
type recaller struct {
	s     *Store
	ctx   context.Context
	docs  map[string]*Doc
	known map[string]bool
}

func (s *Store) recaller(ctx context.Context) *recaller {
	return &recaller{s: s, ctx: ctx, docs: map[string]*Doc{}, known: map[string]bool{}}
}

func (r *recaller) get(path string) (*Doc, error) {
	if r.known[path] {
		return r.docs[path], nil
	}
	d, err := r.s.Get(r.ctx, path)
	if err != nil {
		return nil, err
	}
	r.known[path], r.docs[path] = true, d
	return d, nil
}

// pairs reads two-column rows of a query into memory, so no cursor stays open while the next query runs.
func (r *recaller) pairs(q string, args ...any) ([][2]string, error) {
	rows, err := r.s.db.QueryContext(r.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		out = append(out, [2]string{a, b})
	}
	return out, rows.Err()
}

var (
	folderRe  = regexp.MustCompile(`^(.+)/[^/]+\.md$`)
	handoffRe = regexp.MustCompile(`(?i)handoff`)
)

var parentRe = regexp.MustCompile(`(?i)(?:pagina madre|pagina padre|madre|parent page|parent)[^\[\n]{0,12}\[\[([^\]|#]+)`)

// childrenOf is a hub's children (never tasks or episodes): the pages that say they are `part_of` it or name it as
// their parent page, those that link to it and are linked back by a hub that links out more, and the pages in the
// folder named like it (`infra/venus.md` holds `infra/venus/*.md`). Node indexes, in the order they were found.
func (r *recaller) childrenOf(g *graph, kindOf map[string]Kind) (map[int][]int, error) {
	out := map[int][]int{}
	seen := map[[2]int]bool{}
	add := func(hub, kid int) {
		if hub < 0 || kid < 0 || hub == kid || seen[[2]int{hub, kid}] {
			return
		}
		// a task or a diary day that is «part of» a project is its work or its history, not a page about a part of it
		if k := kindOf[g.nodes[kid]]; k == KindTask || k == KindEpisode {
			return
		}
		seen[[2]int{hub, kid}] = true
		out[hub] = append(out[hub], kid)
	}
	idx := func(p string) int {
		if i, ok := g.index[p]; ok {
			return i
		}
		return -1
	}
	resolve, err := r.s.Resolver(r.ctx)
	if err != nil {
		return nil, err
	}
	resolveIdx := func(target string) int {
		if p := resolve(target); p != "" {
			return idx(p)
		}
		return -1
	}
	parts, err := r.pairs("select src, dst from links where rel = 'part_of' order by src, dst")
	if err != nil {
		return nil, err
	}
	for _, l := range parts {
		add(resolveIdx(l[1]), idx(l[0]))
	}
	// a hub and a child that link to each other: the one that links out at least twice as much is the hub
	all, err := r.pairs("select src, dst from links order by src, dst")
	if err != nil {
		return nil, err
	}
	var sentOrder [][2]int
	sent := map[[2]int]bool{}
	outDeg := map[int]int{}
	for _, l := range all {
		from, dst := idx(l[0]), resolveIdx(l[1])
		if from < 0 || dst < 0 || from == dst {
			continue
		}
		if !sent[[2]int{from, dst}] {
			sent[[2]int{from, dst}] = true
			sentOrder = append(sentOrder, [2]int{from, dst})
		}
		outDeg[from]++
	}
	for _, k := range sentOrder {
		x, y := k[0], k[1]
		if x < y && sent[[2]int{y, x}] {
			dx, dy := outDeg[x], outDeg[y]
			if dx >= 2*dy {
				add(x, y)
			} else if dy >= 2*dx {
				add(y, x)
			}
		}
	}
	// …and a page that names another as its parent page («pagina madre → [[x]]»)
	bodies, err := r.pairs(`select path, body from docs where deleted = 0
		and (body like '%madre%' or body like '%padre%' or body like '%parent%') order by path`)
	if err != nil {
		return nil, err
	}
	for _, row := range bodies {
		for _, m := range parentRe.FindAllStringSubmatch(row[1], -1) {
			add(resolveIdx(mdExt.ReplaceAllString(strings.TrimSpace(m[1]), "")), idx(row[0]))
		}
	}
	for kid, path := range g.nodes {
		if m := folderRe.FindStringSubmatch(path); m != nil {
			add(idx(m[1]+".md"), kid)
		}
	}
	return out, nil
}

// episodeDay is an episode and the day it belongs to: a diary day by its name, any other episode by its last change.
type episodeDay struct{ path, day string }

var dayInPath = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

func (r *recaller) episodeDays() ([]episodeDay, error) {
	rows, err := r.pairs("select path, updated from docs where deleted = 0 and kind = 'episode' order by path")
	if err != nil {
		return nil, err
	}
	out := make([]episodeDay, len(rows))
	for i, row := range rows {
		day := dayInPath.FindString(row[0])
		if day == "" {
			day = row[1][:min(10, len(row[1]))]
		}
		out[i] = episodeDay{row[0], day}
	}
	return out, nil
}

// recordPages are the episodes that are only a record of a subject, not its story: a task's log, a handoff, a diary
// day already folded into its week page (front matter `summarised:`, the value being that page). The value is the page
// they stand for, "" when they have none.
func (r *recaller) recordPages() (map[string]string, error) {
	rows, err := r.pairs(`select path, body from docs where deleted = 0 and kind = 'episode' and
       (path like '%handoff%' or path like 'tasks/%/log.md' or body like '%summarised:%') order by path`)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, row := range rows {
		if week := FrontMatter(row[1])["summarised"]; week != "" {
			out[row[0]] = mdExt.ReplaceAllString(week, "") + ".md"
		} else if handoffRe.MatchString(row[0]) || isLogPath(row[0]) {
			out[row[0]] = ""
		}
	}
	return out, nil
}

// episodesIn are the episodes (diary days, task logs) of a window of days that link to, or name, one of the subjects
// (or a task written into one of them): the diary days first, most recent first, max at most.
func (r *recaller) episodesIn(g *graph, subjects []int, from, to string, max int) ([]int, error) {
	isSubject := map[int]bool{}
	var titles []string
	for _, i := range subjects {
		isSubject[i] = true
		d, err := r.get(g.nodes[i])
		if err != nil {
			return nil, err
		}
		if d != nil {
			if t := strings.ToLower(d.Title); utf16Len(t) >= 3 {
				titles = append(titles, t)
			}
		}
	}
	a := r.s.Areas
	days, err := r.episodeDays()
	if err != nil {
		return nil, err
	}
	type hit struct {
		i     int
		day   string
		diary bool
	}
	var found []hit
	for _, e := range days {
		i, ok := g.index[e.path]
		// the inbox is an episode but not a day of anything
		if strings.HasPrefix(e.path, a.Inbox+"/") {
			continue
		}
		if !ok || e.day < from || e.day > to || isSubject[i] {
			continue
		}
		// a diary day that only logs a task of the subject («- 14:53 [[t-…]] …: completata») is about it too: one hop
		linked := false
		for _, ed := range g.adj[i] {
			if ed.meaning {
				continue
			}
			if isSubject[ed.to] {
				linked = true
				break
			}
			t, err := r.get(g.nodes[ed.to])
			if err != nil {
				return nil, err
			}
			if t != nil && t.Kind == KindTask {
				for _, f := range g.adj[ed.to] {
					if !f.meaning && isSubject[f.to] {
						linked = true
					}
				}
			}
			if linked {
				break
			}
		}
		d, err := r.get(e.path)
		if err != nil {
			return nil, err
		}
		body := ""
		if d != nil {
			body = d.Body
		}
		if !linked {
			lower := strings.ToLower(body)
			named := false
			for _, t := range titles {
				if strings.Contains(lower, t) {
					named = true
					break
				}
			}
			if !named {
				continue
			}
		}
		// a day folded into its week is brought by the week page
		at := i
		if folded := FrontMatter(body)["summarised"]; folded != "" {
			if w, ok := g.index[mdExt.ReplaceAllString(folded, "")+".md"]; ok {
				at = w
			}
		}
		dup := false
		for _, f := range found {
			dup = dup || f.i == at
		}
		if !dup {
			found = append(found, hit{at, e.day, strings.HasPrefix(e.path, a.Diary+"/")})
		}
	}
	// the diary days first, then the other episodes (task logs, which every task of the subject writes, would crowd
	// the days out by sharing today's date)
	sort.SliceStable(found, func(x, y int) bool {
		if found[x].diary != found[y].diary {
			return found[x].diary
		}
		return found[x].day > found[y].day
	})
	var out []int
	for _, f := range found[:min(max, len(found))] {
		out = append(out, f.i)
	}
	return out, nil
}

// belowFloor: whether the memory has nothing about the question: no seed holds minWords of the query's total words,
// and none sits as near in meaning as near. A floor of 0 words, or a query with no content word, never says so.
func belowFloor(seeds []floorSeed, total int, minWords, near float64) bool {
	if total == 0 || minWords <= 0 {
		return false
	}
	for _, s := range seeds {
		if s.named || (s.cover > 0 && float64(s.cover)/float64(total) >= minWords) || (s.cosine != nil && *s.cosine >= near) {
			return false
		}
	}
	return true
}

type floorSeed struct {
	cover  int
	cosine *float64
	// named: the page's title or path holds a word of the query: the question is about that page even when its body
	// holds few of the other words (or says them in another language).
	named bool
}

// namesTerm: whether the page's title or file name holds a term of the query.
func namesTerm(path, title string, terms []string) bool {
	return coverage(title+"\n"+strings.NewReplacer("/", " ", "-", " ", "_", " ").Replace(stripMD(path)), terms) > 0
}

// seedable: whether a page the search found may be a seed. The inbox is notes waiting for their page, never an
// answer; an episode (a diary day, a task's log) runs over many subjects, so its meaning sits near many questions:
// it seeds only when the words found it too.
func seedable(path string, kind Kind, words bool, inbox string) bool {
	if strings.HasPrefix(path, inbox+"/") {
		return false
	}
	return kind != KindEpisode || words
}

// nodeKinds are the kinds of the pages that are nodes of the graph and their titles.
func (r *recaller) nodeKinds() (map[string]Kind, map[string]string, error) {
	rows, err := r.s.db.QueryContext(r.ctx, `select path, title, kind from docs
		where deleted = 0 and (path not like 'tasks/%' or kind in ('task', 'episode')) order by path`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	kinds, titles := map[string]Kind{}, map[string]string{}
	for rows.Next() {
		var p, t string
		var k sql.NullString
		if err := rows.Scan(&p, &t, &k); err != nil {
			return nil, nil, err
		}
		kinds[p], titles[p] = Kind(k.String), t
	}
	return kinds, titles, rows.Err()
}

// cachedGraph is a graph with what it was built from: the version of the writes, the weights, and the day (a relation
// that ended is judged against it).
type cachedGraph struct {
	ver   dbState
	w8    RecallWeights
	today string
	g     *graph
	// what recall derives from the same pages, read once per graph: nil until asked for
	kinds  map[string]Kind
	titles map[string]string
	kids   map[int][]int
}

// hitOf is the cache entry of a graph this store handed out, nil for any other.
func (s *Store) hitOf(g *graph) *cachedGraph {
	if h := s.graphHit; h != nil && h.g == g {
		return h
	}
	return nil
}

// nodeKindsOf is nodeKinds, kept with the graph it belongs to. Callers only read the maps.
func (r *recaller) nodeKindsOf(g *graph) (map[string]Kind, map[string]string, error) {
	s := r.s
	s.graphMu.Lock()
	if h := s.hitOf(g); h != nil && h.kinds != nil {
		defer s.graphMu.Unlock()
		return h.kinds, h.titles, nil
	}
	s.graphMu.Unlock()
	kinds, titles, err := r.nodeKinds()
	if err != nil {
		return nil, nil, err
	}
	s.graphMu.Lock()
	if h := s.hitOf(g); h != nil {
		h.kinds, h.titles = kinds, titles
	}
	s.graphMu.Unlock()
	return kinds, titles, nil
}

// childrenFor is childrenOf, kept with the graph it belongs to (the kinds are the graph's too).
func (r *recaller) childrenFor(g *graph, kindOf map[string]Kind) (map[int][]int, error) {
	s := r.s
	s.graphMu.Lock()
	if h := s.hitOf(g); h != nil && h.kids != nil {
		defer s.graphMu.Unlock()
		return h.kids, nil
	}
	s.graphMu.Unlock()
	kids, err := r.childrenOf(g, kindOf)
	if err != nil {
		return nil, err
	}
	s.graphMu.Lock()
	if h := s.hitOf(g); h != nil {
		h.kids = kids
	}
	s.graphMu.Unlock()
	return kids, nil
}

// dbState says which state of the database a cache entry was built from: this process's own writes, and the commits
// the database saw from other connections (zero on a single-connection database, which only this process writes).
//
// data_version is a counter of one connection: gen says which probe connection it came from, so a new probe's
// counter cannot be taken for an old one's. doubt is the time when the state could not be read: it never repeats.
type dbState struct{ local, disk, gen, doubt uint64 }

// Close releases the connection kept for the data_version probe; the store still works after it (a probe is opened
// again on the next recall).
func (s *Store) Close() error {
	s.verMu.Lock()
	defer s.verMu.Unlock()
	return s.dropProbe()
}

// dropProbe closes the probe connection and starts a new generation; verMu is held.
func (s *Store) dropProbe() error {
	s.verGen++
	if s.verConn == nil {
		return nil
	}
	err := s.verConn.Close()
	s.verConn = nil
	return err
}

// graphStamp reads the state before a build, so a write that lands during it leaves the entry stale. When the
// data_version cannot be read (the context is done, no connection, a failed query) the state is the time, so
// nothing is ever served from cache on a doubt.
func (s *Store) graphStamp(ctx context.Context) dbState {
	st := dbState{local: s.graphVer.Load()}
	if s.db.Stats().MaxOpenConnections == 1 {
		return st
	}
	doubt := func() dbState { st.doubt = uint64(time.Now().UnixNano()); return st }
	if ctx.Err() != nil {
		return doubt()
	}
	s.verMu.Lock()
	defer s.verMu.Unlock()
	if s.verConn == nil {
		c, err := s.db.Conn(ctx)
		if err != nil {
			return doubt()
		}
		s.verGen++
		s.verConn = c
	}
	st.gen = s.verGen
	if err := s.verConn.QueryRowContext(ctx, "pragma data_version").Scan(&st.disk); err != nil {
		s.dropProbe()
		return doubt()
	}
	return st
}

// graphChanged is called after every commit that can change the graph: a page, its links, the similarity rows.
func (s *Store) graphChanged() { s.graphVer.Add(1) }

// graph is loadGraph, kept until a write changes it: the graph is only read (withoutSiblings copies), and rebuilding
// it reads every page, link and similarity row. The version is read before the load, so a write that lands during it
// leaves the entry already stale.
func (r *recaller) graph(w8 RecallWeights, today string) (*graph, error) {
	s := r.s
	ver := s.graphStamp(r.ctx)
	s.graphMu.Lock()
	if h := s.graphHit; h != nil && h.ver == ver && h.w8 == w8 && h.today == today {
		s.graphMu.Unlock()
		return h.g, nil
	}
	s.graphMu.Unlock()
	g, err := r.loadGraph(w8, today)
	if err != nil {
		return nil, err
	}
	s.graphMu.Lock()
	s.graphHit = &cachedGraph{ver: ver, w8: w8, today: today, g: g}
	s.graphMu.Unlock()
	return g, nil
}

// loadGraph is the pages and their edges, both ways, links resolved. The tasks are nodes too: reached
// through their relations, not found by words, and ranked down once closed.
func (r *recaller) loadGraph(w8 RecallWeights, today string) (*graph, error) {
	paths, err := r.pairs(`select path, '' from docs
		where deleted = 0 and (path not like 'tasks/%' or kind in ('task', 'episode')) order by path`)
	if err != nil {
		return nil, err
	}
	g := &graph{index: map[string]int{}}
	for i, p := range paths {
		g.nodes = append(g.nodes, p[0])
		g.index[p[0]] = i
	}
	g.adj = make([][]edge, len(g.nodes))
	resolve, err := r.s.Resolver(r.ctx)
	if err != nil {
		return nil, err
	}
	type best struct {
		w       float64
		rel     string
		meaning bool
	}
	var keys [][2]int
	bests := map[[2]int]best{}
	// a relation that ended (`(fino al 2026-09)`) keeps its line on the page but stops spreading activation
	ended := map[string]bool{}
	bodies, err := r.pairs(`select path, body from docs where deleted = 0 and (body like '%fino al%' or body like '%until%')
		order by path`)
	if err != nil {
		return nil, err
	}
	for _, row := range bodies {
		for _, e := range EdgesOf(row[1]) {
			if e.Rel != "" && EndedBefore(e.Note, today) {
				ended[row[0]+"\n"+e.Rel+"\n"+e.Target] = true
			}
		}
	}
	rows, err := r.s.db.QueryContext(r.ctx, "select src, dst, rel from links order by src, dst, rel")
	if err != nil {
		return nil, err
	}
	type link struct{ src, dst, rel string }
	var links []link
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.src, &l.dst, &l.rel); err != nil {
			rows.Close()
			return nil, err
		}
		links = append(links, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, l := range links {
		a, ok := g.index[l.src]
		dst := resolve(l.dst)
		b, ok2 := g.index[dst]
		if !ok || dst == "" || !ok2 || a == b {
			continue
		}
		if l.rel != "" && ended[l.src+"\n"+l.rel+"\n"+l.dst] {
			continue
		}
		key := [2]int{min(a, b), max(a, b)}
		w := w8.WLink
		if l.rel != "" {
			w = w8.WRel
		}
		cur, seen := bests[key]
		// one edge per pair: the strongest of the links between them
		if !seen {
			keys = append(keys, key)
		}
		if !seen || w > cur.w {
			bests[key] = best{w: w, rel: l.rel}
		}
	}
	weights, err := r.s.db.QueryContext(r.ctx, "select src, dst, w from similar order by src, dst")
	if err != nil {
		return nil, err
	}
	defer weights.Close()
	for i := 0; weights.Next(); i++ {
		var s, d string
		var w float64
		if err := weights.Scan(&s, &d, &w); err != nil {
			return nil, err
		}
		a, ok := g.index[s]
		b, ok2 := g.index[d]
		if !ok || !ok2 || a == b {
			continue
		}
		key := [2]int{min(a, b), max(a, b)}
		// a link between the same two pages wins: what a writer wrote says more than the core's guess
		if _, seen := bests[key]; !seen {
			keys = append(keys, key)
			bests[key] = best{w: w8.WMeaning * w, meaning: true}
		}
	}
	for _, key := range keys {
		e := bests[key]
		g.adj[key[0]] = append(g.adj[key[0]], edge{to: key[1], w: e.w, rel: e.rel, meaning: e.meaning})
		g.adj[key[1]] = append(g.adj[key[1]], edge{to: key[0], w: e.w, rel: e.rel, meaning: e.meaning})
	}
	return g, weights.Err()
}

// RecallPage is a page of the pack, in full.
type RecallPage struct {
	Path  string
	Title string
	// Kind is "" for a page that has none.
	Kind Kind
	Body string
	// Cut says the body was cut to the budget: memory.read has the rest.
	Cut bool
	// Passages are, for a collection's source page (its hub), its best passages for the query.
	Passages []Passage
}

// Via says why a related page is here.
type Via struct {
	// From is the page it was reached from, Rel the relation between them when one is named («plays_with»), Meaning
	// that no page links them and they are only near in meaning.
	From    string `json:"from,omitempty"`
	Rel     string `json:"rel,omitempty"`
	Meaning bool   `json:"meaning,omitempty"`
	// Found is, for a page the search itself found that no path reaches, whether the "words" or the "meaning" found it.
	Found string `json:"found,omitempty"`
}

// RecallRelated is a page of the pack by title and lede.
type RecallRelated struct {
	Path  string
	Title string
	// Kind is "" for a page that has none.
	Kind Kind
	Lede string
	Via  Via
	// Passages are, for a collection's source page (its hub), its best passages for the query.
	Passages []Passage
}

// wire is the form a related page takes when it is sent: what the budget is counted on.
func (r RecallRelated) wire() any {
	var kind *string
	if r.Kind != "" {
		k := string(r.Kind)
		kind = &k
	}
	return struct {
		Path     string    `json:"path"`
		Title    string    `json:"title"`
		Kind     *string   `json:"kind,omitempty"`
		Lede     string    `json:"lede"`
		Via      Via       `json:"via"`
		Passages []Passage `json:"passages,omitempty"`
	}{r.Path, r.Title, kind, r.Lede, r.Via, r.Passages}
}

// RecallAnswer is what recall returns.
type RecallAnswer struct {
	Query   string
	Pages   []RecallPage
	Related []RecallRelated
	// Documents are the passages of documents that lead the pack, one per document.
	Documents []Passage
	// Tokens is the pack's size, in the same rough tokens as the budget.
	Tokens  int
	Meaning bool
	Notes   []string
}

// RecallOptions of a recall; the zero value of each field means its default.
type RecallOptions struct {
	// Index is the search whose hits are the seeds; required for a query (an empty query is the core and needs none).
	Index *search.Index
	// Budget is the pack's size in tokens (2000), held between 300 and 12000.
	Budget   int
	Embedder search.Embedder
	// Now is the clock for the questions of time and for the relations that ended (the current time).
	Now time.Time
	// TaskGroups says, per task page, the group it is in ("active", "closed"…): the active ones seed the core and the
	// closed ones weigh little. Nil: no task has a group.
	TaskGroups func(ctx context.Context) (map[string]string, error)
}

// nameCandidates are the page names a query may be: the whole of it when it reads like a name, then the runs of two or
// more of its words, longest first (one word of a longer question names a page too often by chance).
func nameCandidates(q string) []string {
	var out []string
	if namedRe.MatchString(q) {
		out = append(out, strings.ToLower(spaces.ReplaceAllString(q, "-")))
	}
	words := wordsOf(q)
	for n := len(words) - 1; n >= 2; n-- {
		for i := 0; i+n <= len(words); i++ {
			out = append(out, strings.Join(words[i:i+n], "-"))
		}
	}
	return out
}

var namedRe = regexp.MustCompile(`^[\p{L}\p{N}\s/_.-]{1,80}$`)
var linkText = regexp.MustCompile(`\[\[[^\]]*\]\]`)

// Recall answers what the memory holds about query within the budget.
func (s *Store) Recall(ctx context.Context, query string, opts RecallOptions) (RecallAnswer, error) {
	rc := s.recaller(ctx)
	w := DefaultRecallWeights
	budget := max(300, min(cmp.Or(opts.Budget, 2000), 12000))
	q := strings.TrimSpace(query)
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	g, err := rc.graph(w, dayOf(now))
	if err != nil {
		return RecallAnswer{}, err
	}
	seeds := newSeeds()
	// the window's episodes about the subject, most recent first: they reach the pack before a hub's many neighbours
	var inWindow []int
	// the page the query names, which leads the pack whatever its neighbours weigh
	pinned := noSeed
	meaning := false
	var notes []string
	var window *timeWin
	if q != "" {
		window = timeWindow(q, now)
	}
	asked := q
	if window != nil && window.Rest != "" {
		asked = window.Rest
	}
	isPast := aboutPast(q, now)
	// hub → its children the question asks for; and the hubs whose other children get no activation from them
	lift := map[int][]int{}
	var cuts []siblingCut
	// the passages of documents that lead the pack, and the best one's score over the best page's (0 when not both)
	var documents []Passage
	docLead, docBest := 0.0, 0.0
	// the search's own score of each page, as a passage's is: the coverage share only shapes the seeds, and a document
	// is weighed against a page by what the search gave both
	pageScore := map[string]float64{}
	// the seeds the meaning also found; the others only the words did
	byMeaning := map[int]bool{}
	meaningMissing := false
	// the best passage the search found in each page: what answers the question, where the lede only says what the page is
	cited := map[string]search.Citation{}
	kindOf, titleOf, err := rc.nodeKindsOf(g)
	if err != nil {
		return RecallAnswer{}, err
	}
	episodeAt := map[string]bool{}
	for p, k := range kindOf {
		if k == KindEpisode {
			episodeAt[p] = true
		}
	}
	var groups map[string]string
	if opts.TaskGroups != nil {
		if groups, err = opts.TaskGroups(ctx); err != nil {
			return RecallAnswer{}, err
		}
	}

	if q != "" {
		if opts.Index == nil {
			return RecallAnswer{}, errNoIndex
		}
		found, err := opts.Index.Search(ctx, asked, search.Options{
			Limit: w.Seeds, Embedder: opts.Embedder, Citations: 1, Timeout: meaningTimeout,
		})
		if err != nil {
			return RecallAnswer{}, err
		}
		meaning = found.Meaning
		// a model that was asked and did not answer, or has not seen every chunk, leaves the nearness out of the seeds:
		// the words floor alone would then call a question unanswerable for a lack the memory does not have
		meaningMissing = opts.Embedder != nil && (!found.Meaning || found.Waiting > 0)
		for _, r := range found.Results {
			if r.Document == nil && len(r.Citations) > 0 {
				cited[r.Path] = r.Citations[0]
			}
		}
		notes = append(notes, found.Notes...)
		terms := contentTerms(asked)
		held := map[int]int{}
		// how many of the query's words the page holds
		coverOf := func(i int) (int, error) {
			if c, ok := held[i]; ok {
				return c, nil
			}
			d, err := rc.get(g.nodes[i])
			if err != nil {
				return 0, err
			}
			c := 0
			if d != nil {
				c = coverage(d.Title+"\n"+d.Body, terms)
			}
			held[i] = c
			return c, nil
		}
		// a hit counts for the share of the query's words its page holds: the words' frequency in the page decided the
		// search's rank, and a page that says one of them twenty times is not the one that says most of them
		counted := func(r search.Result) (float64, error) {
			i, ok := g.index[r.Path]
			if !ok || len(terms) < 2 {
				return r.Score, nil
			}
			c, err := coverOf(i)
			return r.Score * (1 - w.CoverageWeight*(1-float64(c)/float64(len(terms)))), err
		}
		inbox := s.Areas.Inbox
		scores := make([]float64, len(found.Results))
		top, topScore, docTop, pageHits := 0.0, 0.0, 0.0, 0
		for k, r := range found.Results {
			if r.Document != nil {
				docTop = math.Max(docTop, r.Score)
				continue
			}
			pageHits++
			pageScore[r.Path] = r.Score
			topScore = math.Max(topScore, r.Score)
			if scores[k], err = counted(r); err != nil {
				return RecallAnswer{}, err
			}
			if i, ok := g.index[r.Path]; ok {
				d, err := rc.get(g.nodes[i])
				if err != nil {
					return RecallAnswer{}, err
				}
				if d != nil && isSuperseded(d.Body) {
					scores[k] *= supersededShare
				}
			}
			top = math.Max(top, scores[k])
		}
		if top == 0 {
			top = 1
		}
		// the best document passage's score over the best page's, when the search found both
		// pages the search did not return have pageScore 0, so a leading document keeps them out of full (they stay as related lines)
		if pageHits > 0 && docTop > 0 && topScore > 0 {
			docLead = docTop / topScore
			docBest = docTop
		}
		// a document is shown when its passage holds docFloorWords of the query's words (stricter than the memory's own
		// floor: three of five words are the generic ones, «stato» and «prossimi passi», of another project's file), or
		// when it is the best hit of all (then it was found by meaning). The search's
		// score cannot tell them apart: it is a rank fusion, 1/61 and 1/62 for the first two hits of the same list.
		bestHit := math.Max(topScore, docTop)
		for _, r := range found.Results {
			if r.Document == nil || len(documents) == docsMax || len(r.Citations) == 0 {
				continue
			}
			p := PassageOf(r, r.Citations[0], passageTokens)
			if len(terms) >= 2 && r.Score < bestHit &&
				float64(coverage(p.Title+"\n"+p.Text, terms)) < docFloorWords*float64(len(terms)) {
				continue
			}
			documents = append(documents, *p)
		}
		cosines := map[int]*float64{}
		for k, r := range found.Results {
			if r.Document != nil || scores[k] < w.SeedMin*top {
				continue
			}
			i, ok := g.index[r.Path]
			if !ok || !seedable(r.Path, kindOf[r.Path], r.Words, inbox) {
				continue
			}
			seeds.set(i, scores[k]/top)
			if r.Meaning {
				byMeaning[i] = true
			}
			if r.Cosine != nil {
				cosines[i] = r.Cosine
			}
		}
		// a query that names a page starts from it, as strongly as from the best hit: the whole query is the name, or
		// else the longest run of its words that is (a page «Nome progetto» in «scadenza del nome progetto»)
		resolve, err := s.Resolver(ctx)
		if err != nil {
			return RecallAnswer{}, err
		}
		for k, name := range nameCandidates(q) {
			if p := resolve(name); p != "" {
				if ni, ok := g.index[p]; ok {
					lead := 1.0
					if k > 0 || !namedRe.MatchString(q) {
						lead = namedInside
					}
					seeds.set(ni, math.Max(lead, seeds.w[ni]))
					// only a query that is the name leads whatever its neighbours weigh; a name inside a longer question
					// is a seed like any other, so the specific child the question asks for still beats its hub
					if k == 0 && namedRe.MatchString(q) {
						pinned = ni
					}
					break
				}
			}
		}
		cover := map[int]int{}
		for _, i := range seeds.keys {
			if cover[i], err = coverOf(i); err != nil {
				return RecallAnswer{}, err
			}
		}
		if len(terms) > 1 {
			seeds = dropPartial(g, seeds, cover, len(terms), byMeaning, pinned)
		}
		seeds = dropStranded(g, seeds, pinned, w.Strong, cover)
		// a question the memory has nothing about: the best weak hit is still not an answer. Not for a question of
		// time (its words are «cambiato», «settimana»: the episodes answer)
		if pinned == noSeed && window == nil && seeds.size() > 0 && !meaningMissing {
			threshold, err := s.NearThreshold(ctx)
			if err != nil {
				return RecallAnswer{}, fmt.Errorf("recall: similar threshold: %w", err)
			}
			cutoff := threshold - w.FloorMeaning
			var fs []floorSeed
			for _, i := range seeds.keys {
				fs = append(fs, floorSeed{cover[i], cosines[i], namesTerm(g.nodes[i], titleOf[g.nodes[i]], terms)})
			}
			if belowFloor(fs, len(terms), w.FloorWords, cutoff) {
				seeds = newSeeds()
				notes = append(notes, "nothing relevant")
			}
		}
		// the question's words a page says itself: a hub names its children in links, and those names are not what it
		// says
		saidCache := map[int]map[string]bool{}
		said := func(i int) (map[string]bool, error) {
			if m, ok := saidCache[i]; ok {
				return m, nil
			}
			d, err := rc.get(g.nodes[i])
			if err != nil {
				return nil, err
			}
			m := map[string]bool{}
			if d != nil {
				m = coveredTerms(d.Title+"\n"+linkText.ReplaceAllString(d.Body, " "), terms)
			}
			saidCache[i] = m
			return m, nil
		}
		saidN := func(i int) (int, error) { m, err := said(i); return len(m), err }
		if w.HubFocus != 0 && len(terms) > 1 {
			// a page written link to a seed hub that holds more of the question than the hub is what is asked for: the
			// hub's plain links to pages that hold less are its table of contents, not answers
			for _, hub := range append([]int(nil), seeds.keys...) {
				if hub == pinned || episodeAt[g.nodes[hub]] {
					continue
				}
				own, err := saidN(hub)
				if err != nil {
					return RecallAnswer{}, err
				}
				bar := 0
				better := false
				for _, e := range g.adj[hub] {
					if e.meaning || !seeds.has(e.to) {
						continue
					}
					n, err := saidN(e.to)
					if err != nil {
						return RecallAnswer{}, err
					}
					if n > own {
						better = true
						bar = max(bar, n)
					}
				}
				if !better {
					continue
				}
				var drop []int
				for _, e := range g.adj[hub] {
					if e.meaning || e.rel != "" || seeds.has(e.to) {
						continue
					}
					n, err := saidN(e.to)
					if err != nil {
						return RecallAnswer{}, err
					}
					if n < bar {
						drop = append(drop, e.to)
					}
				}
				if len(drop) > 0 {
					cuts = append(cuts, siblingCut{hub: hub, kids: drop})
				}
			}
		}
		if w.ChildPrefer != 0 && len(terms) > 0 && seeds.size() > 0 && !meaningMissing {
			// a specific question: a hub's child that holds a query word the hub lacks is what is asked for; the hub's
			// other children are not, and a hub far under the best seed does not hand its activation to any of its
			// children
			top := 0.0
			for _, i := range seeds.keys {
				top = math.Max(top, seeds.w[i])
			}
			kids, err := rc.childrenFor(g, kindOf)
			if err != nil {
				return RecallAnswer{}, err
			}
			// the seeds as they stand before the lifts below (search results, name-resolved and pinned pages)
			bySearch := map[int]bool{}
			for _, i := range seeds.keys {
				bySearch[i] = true
			}
			for _, hub := range append([]int(nil), seeds.keys...) {
				weight := seeds.w[hub]
				all := kids[hub]
				if all == nil || hub == pinned || episodeAt[g.nodes[hub]] {
					continue
				}
				has, err := said(hub)
				if err != nil {
					return RecallAnswer{}, err
				}
				var extra []int
				for _, k := range all[:min(childrenMax, len(all))] {
					// only a child the search found itself: a word the hub lacks in a child nothing else pointed to is
					// no evidence that the question is about that child
					if !bySearch[k] {
						continue
					}
					sk, err := said(k)
					if err != nil {
						return RecallAnswer{}, err
					}
					for t := range sk {
						if !has[t] {
							extra = append(extra, k)
							break
						}
					}
				}
				// among them the ones that hold the most of the question: a sibling that shares one stray word is no answer
				best := 0
				for _, k := range extra {
					n, _ := saidN(k)
					best = max(best, n)
				}
				var specific []int
				for _, k := range extra {
					if n, _ := saidN(k); n == best {
						specific = append(specific, k)
					}
				}
				if len(specific) > 0 {
					lift[hub] = specific
					// the question is about this hub: its other children are not what is asked for. A hub far under the
					// best seed is no subject of the question, and a child the search itself found keeps its own score.
					if weight/top >= w.ChildSpread {
						for _, k := range all {
							if !slices.Contains(specific, k) && k != pinned {
								seeds.del(k)
							}
						}
					}
					for _, k := range specific {
						seeds.set(k, math.Max(seeds.w[k], weight*childShare))
					}
				}
				if len(specific) > 0 || weight/top < w.ChildSpread {
					cuts = append(cuts, siblingCut{hub: hub, kids: all, keep: specific})
				}
			}
		}
		if window != nil {
			// what happened in the window about the subject: its episodes are seeds, whatever else the words found
			var subjects []int
			for _, i := range seeds.keys {
				if !episodeAt[g.nodes[i]] {
					subjects = append(subjects, i)
				}
			}
			// …and the pages named by the question, even when the words ranked another page above them: the titles
			// that hold most of the question's words, then the shortest: «Kedalio» before «Kedalio: …»
			type titled struct{ i, held, len int }
			var named []titled
			for i, path := range g.nodes {
				if k := kindOf[path]; k == KindEpisode || k == KindTask || slices.Contains(subjects, i) {
					continue
				}
				title := titleOf[path]
				if h := coverage(title, terms); h > 0 {
					named = append(named, titled{i, h, utf16Len(title)})
				}
			}
			sort.SliceStable(named, func(x, y int) bool {
				if named[x].held != named[y].held {
					return named[x].held > named[y].held
				}
				return named[x].len < named[y].len
			})
			for _, n := range named[:min(subjectsMax, len(named))] {
				subjects = append(subjects, n.i)
			}
			eps, err := rc.episodesIn(g, subjects, window.From, window.To, w.RecentEpisodes)
			if err != nil {
				return RecallAnswer{}, err
			}
			for _, i := range eps {
				seeds.set(i, w.RecentSeed)
				inWindow = append(inWindow, i)
			}
		}
	} else {
		// the core: the pages about the owner, then what changed lately
		self, err := s.List(ctx, s.Areas.Self+"/", ListOpts{Limit: 10})
		if err != nil {
			return RecallAnswer{}, err
		}
		for _, d := range self {
			if i, ok := g.index[d.Path]; ok {
				seeds.set(i, 1)
			}
		}
		// the work in hand: the tasks being done now (the open and paused ones are reached through what they are about)
		active := make([]string, 0, len(groups))
		for p, grp := range groups {
			if grp == "active" {
				active = append(active, p)
			}
		}
		sort.Strings(active)
		for _, p := range active {
			if i, ok := g.index[p]; ok {
				seeds.set(i, 0.3)
			}
		}
		lately, err := s.List(ctx, "", ListOpts{Limit: 30})
		if err != nil {
			return RecallAnswer{}, err
		}
		k := 0
		for _, d := range lately {
			if d.Kind == KindEpisode {
				continue
			}
			if k == 8 {
				break
			}
			if i, ok := g.index[d.Path]; ok && !seeds.has(i) {
				seeds.set(i, 0.5/float64(k+1))
			}
			k++
		}
	}
	if seeds.size() == 0 {
		used := 0
		for _, d := range documents {
			used += d.cost()
		}
		return RecallAnswer{Query: q, Pages: []RecallPage{}, Related: []RecallRelated{}, Documents: documents, Tokens: used,
			Meaning: meaning, Notes: notes}, nil
	}

	r := activate(withoutSiblings(g, cuts), seeds, w.Damping, iterations)
	var days map[string]string
	if window != nil {
		eds, err := rc.episodeDays()
		if err != nil {
			return RecallAnswer{}, err
		}
		days = map[string]string{}
		for _, e := range eds {
			days[e.path] = e.day
		}
	}
	records, err := rc.recordPages()
	if err != nil {
		return RecallAnswer{}, err
	}
	queryTerms := contentTerms(asked)
	score := func(i int) float64 {
		path := g.nodes[i]
		f := 1.0
		// the page the question is about carries its subject in the title: a hub, a person, over a sub-page, a log or a
		// note that only says the words more often
		if len(queryTerms) > 0 {
			f += titleBonus * float64(coverage(titleOf[path], queryTerms)) / float64(len(queryTerms))
		}
		if _, ok := records[path]; ok {
			f = recordShare
		}
		switch {
		case kindOf[path] == KindEpisode:
			e := w.Episode
			if window != nil && !(days[path] >= window.From && days[path] <= window.To) {
				e *= w.OutsideWindow
			}
			if groups[path] == "closed" {
				e *= taskClosed
			}
			return r[i] * f * e
		case kindOf[path] == KindTask && groups[path] == "closed":
			return r[i] * f * taskClosed
		}
		return r[i] * f
	}
	var order []int
	scored := make([]float64, len(g.nodes))
	for i := range g.nodes {
		if r[i] > 0 {
			order = append(order, i)
			scored[i] = score(i)
		}
	}
	sort.SliceStable(order, func(x, y int) bool {
		a, b := order[x], order[y]
		if (a == pinned) != (b == pinned) {
			return a == pinned
		}
		return scored[a] > scored[b]
	})
	order = preferChildren(order, lift)

	used := 0
	for _, d := range documents {
		used += d.cost()
	}
	// a source page brings what its collection says about the query, passages the pack does not carry already
	hubs := map[string]string{}
	if q != "" {
		cols, err := opts.Index.Collections(ctx)
		if err != nil {
			return RecallAnswer{}, err
		}
		for _, c := range cols {
			if c.Hub != "" {
				hubs[c.Hub] = c.ID
			}
		}
	}
	type carriedKey struct {
		doc   string
		chunk int
	}
	carried := map[carriedKey]bool{}
	// …and none past docPassages of one document, those the pack carries already counted
	perDoc := map[string]int{}
	for _, d := range documents {
		carried[carriedKey{d.Document, d.Chunk}] = true
		perDoc[d.Document]++
	}
	passagesOf := func(path string) ([]Passage, error) {
		id, ok := hubs[path]
		if !ok {
			return nil, nil
		}
		found, err := opts.Index.Search(ctx, q, search.Options{
			Limit: passagesMax + len(documents), Embedder: opts.Embedder, Citations: 2,
			Scope: search.Scope{Kind: search.ScopeCollections, IDs: []string{id}},
		})
		if err != nil {
			return nil, err
		}
		var out []Passage
		for _, r := range found.Results {
			for _, c := range r.Citations {
				p := PassageOf(r, c, passageTokens)
				if p == nil || carried[carriedKey{p.Document, p.Chunk}] || len(out) >= passagesMax || perDoc[p.Document] >= docPassages {
					continue
				}
				perDoc[p.Document]++
				carried[carriedKey{p.Document, p.Chunk}] = true
				out = append(out, *p)
			}
		}
		return out, nil
	}
	pages := []RecallPage{}
	shown := map[int]bool{}
	// the pages in full: the strongest, while they fit their share; the first always, cut if it must be
	for _, i := range order {
		if len(pages) >= w.FullMax {
			break
		}
		// a document answers better than the pages: a page the search held under docPageShare of the document's score
		// does not come in full (it can still follow as a line in the related pages); a collection's source page is the
		// document's own page, it stays; so does the best counted page (seed weight 1), or a document leading it by more than
		// 1/docPageShare would leave the pack without a page in full
		if _, isHub := hubs[g.nodes[i]]; docLead > 1 && i != pinned && !isHub && seeds.w[i] < 1 && pageScore[g.nodes[i]]/docBest < docPageShare {
			continue
		}
		d, err := rc.get(g.nodes[i])
		if err != nil {
			return RecallAnswer{}, err
		}
		if d == nil {
			continue
		}
		room := int(math.Floor(float64(budget)*w.FullShare)) - used
		body := fmBlock.ReplaceAllString(d.Body, "")
		// over the share, what held before goes first, unless the question looks back
		if Tokens(body) > room && !isPast {
			body, _ = withoutHistory(body)
		}
		// a page that does not fit ends the pages in full: `order` is relevance, and a smaller page after it (often a task)
		// must not take the place of the one that did not fit. It leads the related lines, with the rest.
		if len(pages) > 0 && Tokens(body) > room {
			break
		}
		cutBody, wasCut := cutTo(body, max(room, 200))
		passages, err := passagesOf(d.Path)
		if err != nil {
			return RecallAnswer{}, err
		}
		pages = append(pages, RecallPage{Path: d.Path, Title: d.Title, Kind: d.Kind, Body: cutBody, Cut: wasCut, Passages: passages})
		used += Tokens(cutBody) + 20
		for _, p := range passages {
			used += p.cost()
		}
		shown[i] = true
	}
	related := []RecallRelated{}
	episodes := 0
	// a question about the last days is about the diary: the window's days are not held to the usual two
	episodesMax := w.EpisodesMax
	if window != nil {
		episodesMax = max(w.EpisodesMax, w.RecentEpisodes)
	}
	for _, i := range append(append([]int(nil), inWindow...), order...) {
		if len(related) >= w.RelatedMax {
			break
		}
		if shown[i] {
			continue
		}
		d, err := rc.get(g.nodes[i])
		if err != nil {
			return RecallAnswer{}, err
		}
		if d == nil {
			continue
		}
		if d.Kind == KindEpisode {
			episodes++
			if episodes > episodesMax {
				continue
			}
		}
		// the page it came through; else what the search found it by; else its strongest neighbour, shown or not
		var via *Via
		if v := viaOf(g, i, shown, r); v != nil {
			via = v
		} else if seeds.has(i) {
			found := "words"
			if byMeaning[i] {
				found = "meaning"
			}
			via = &Via{Found: found}
		} else if v := viaOf(g, i, nil, r); v != nil {
			via = v
		} else {
			via = &Via{Found: "words"}
		}
		passages, err := passagesOf(d.Path)
		if err != nil {
			return RecallAnswer{}, err
		}
		lede := LedeOf(d.Body)
		// the first chunk is the page's opening, which the lede already is
		if c, ok := cited[d.Path]; ok && c.Position > 0 {
			lede = passageLede(c)
		}
		item := RecallRelated{Path: d.Path, Title: d.Title, Kind: d.Kind, Lede: lede, Via: *via, Passages: passages}
		sent, err := wireTokens(item.wire())
		if err != nil {
			return RecallAnswer{}, err
		}
		cost := sent + 5
		if used+cost > budget {
			continue
		}
		related = append(related, item)
		used += cost
		shown[i] = true
	}
	return RecallAnswer{Query: q, Pages: pages, Related: related, Documents: documents, Tokens: used, Meaning: meaning, Notes: notes}, nil
}

// isSuperseded says whether the page's front matter marks it as replaced.
func isSuperseded(body string) bool {
	switch strings.ToLower(FrontMatter(body)["status"]) {
	case "superseded", "obsolete":
		return true
	}
	return false
}

// passageLede is a cited passage as a related page shows it: its heading path, then its text on one line, cut.
func passageLede(c search.Citation) string {
	text := strings.Join(strings.Fields(cutPassage(c.Text, ledeTokens)), " ")
	if c.Context != "" {
		return c.Context + ": " + text
	}
	return text
}

// cutTo is a body cut at a line so it fits max tokens, and whether it was. Lines, not paragraphs: a page's sections
// are long lists, and a cut at the first paragraph that does not fit would leave most of the room unused.
func cutTo(body string, max int) (string, bool) {
	if Tokens(body) <= max {
		return body, false
	}
	out := ""
	for _, l := range strings.Split(body, "\n") {
		next := l
		if out != "" {
			next = out + "\n" + l
		}
		if Tokens(next) > max {
			break
		}
		out = next
	}
	if out == "" {
		out = prefixUnits(body, max*4)
	}
	return strings.TrimRightFunc(out, unicode.IsSpace), true
}

// viaOf is the page already in the pack (any page when shown is nil) that most likely brought i up, and the name of
// the edge between them; nil when there is none.
func viaOf(g *graph, i int, shown map[int]bool, r []float64) *Via {
	var best *edge
	bestScore := 0.0
	for k := range g.adj[i] {
		e := &g.adj[i][k]
		if shown != nil && !shown[e.to] {
			continue
		}
		if s := r[e.to] * e.w; best == nil || s > bestScore {
			best, bestScore = e, s
		}
	}
	if best == nil {
		return nil
	}
	return &Via{From: g.nodes[best.to], Rel: best.rel, Meaning: best.meaning}
}

var errNoIndex = errors.New("recall: a query needs the search index")
