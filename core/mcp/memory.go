package mcp

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"kedalio/core/clip"
	"kedalio/core/memory"
	"kedalio/core/memory/search"
	"kedalio/core/tasks"
	"kedalio/protocol"
)

// The memory's tools. Collections and their documents are sources.go's.

const tasksOnly = "tasks/ pages belong to the tasks tools: use tasks_save"

var kinds = []string{"entity", "concept", "episode", "source"}

func (s *Server) memoryTools() []*tool {
	pathArg := str("path")
	cardArg := str("card", "project path")
	return []*tool{
		{
			name: protocol.ToolMemoryRecall, kind: read, upFront: true,
			description: "What the memory holds about a subject; recall before assuming. mode search: matching pages and passages.",
			schema: object(nil,
				str("query", "empty: who the owner is, what changed lately"),
				choice("mode", []string{"recall", "search"}),
				integer("budget", 300, 12000, "default 2000"),
				integer("limit", 1, 50, "search only"),
				boolean("include_tasks", "search only"),
				str("version", "search only: as of this version")),
			run: s.recall,
		},
		{
			name: protocol.ToolMemoryRead, kind: read,
			description: "A page (path) or project card (card): Markdown, rev (base_rev of a write), links both ways. rev: an old version; history: list them. Cut at ~4000 chars with the length; read on with offset, section or max_chars 0. Once per session; for a subject memory_recall is shorter.",
			schema: object(nil, pathArg, cardArg, integer("rev", 1, 0), boolean("history"),
				integer("offset", 0, 0, "from character"),
				str("section", "part under this heading"),
				integer("max_chars", 0, 0, "default 4000; 0 all"),
				boolean("links", "list the pages linking here and related ones, else only counted")),
			run: s.readPage,
		},
		{
			name: protocol.ToolMemoryList, kind: read,
			description: "Browse: pages under a folder (newest first), collections, a collection's documents, or health (orphans, broken links, pages that look secret).",
			schema: object(nil,
				choice("what", []string{"pages", "collections", "documents", "health"}, "default pages"),
				str("folder"),
				choice("kind", kinds),
				integer("limit", 1, 1000),
				str("collection", "documents: collection id")),
			run: s.listPages,
		},
		{
			name: protocol.ToolMemoryWrite, kind: write,
			description: "Change the memory. write: create or replace a page whole (read first, pass base_rev). edit: a passage occurring once. append: timed line to diary or inbox. move: links follow. restore: a version. remember: keep a fact, the server places it.",
			schema: object([]string{"op"},
				choice("op", []string{"write", "edit", "append", "move", "restore", "remember"}),
				pathArg, cardArg,
				strings_("kept_in", "upkeep only: pages the removed text went to"),
				str("body", "write: whole Markdown. A card: max 150 words: Last time, Next (refs only), Urgent, Memory"),
				integer("base_rev", 0, 0, "rev from memory_read, required to replace or write a card; 0 creates"),
				boolean("distinct", "write: similar title exists, other subject"),
				str("find", "edit: the passage"),
				str("replace", "edit: its replacement"),
				choice("where", []string{"diary", "diario", "inbox"}, "default diary"),
				str("text", "append: a line; remember: the fact"),
				str("about", "remember: page or name"),
				str("from", "move: the page"),
				str("to", "move: its new path"),
				integer("rev", 1, 0, "restore: the version")),
			run: s.writeMemory,
		},
		{
			name: protocol.ToolMemoryDelete, kind: destroy,
			description: "Remove a page (restorable with memory_write restore), a document or a collection (final: ask the owner first).",
			schema: object([]string{"what"},
				choice("what", []string{"page", "document", "collection"}),
				str("path", "page"),
				str("collection", "document: collection id"),
				str("id", "its id"),
				strings_("kept_in", "upkeep only: pages its text went to")),
			run: s.deletePage,
		},
	}
}

// refused is the answer to a write the rules refuse: the reasons, for the writer to fix.
type refused struct {
	Refused bool      `json:"refused"`
	Errors  []string  `json:"errors"`
	Similar []pageRef `json:"similar,omitempty"`
}

func refuse(errs []string, similar ...pageRef) refused { return refused{true, errs, similar} }

func kindp(k memory.Kind) *string {
	if k == "" {
		return nil
	}
	s := string(k)
	return &s
}

func isTask(path string) bool {
	return isTaskPath(path) || isTaskPath(slugPath(path))
}

func isTaskPath(path string) bool {
	p := strings.ToLower(strings.TrimLeft(strings.ReplaceAll(strings.TrimSpace(path), "\\", "/"), "/"))
	return strings.HasPrefix(p, "tasks/")
}

// isTaskBookPage is a path these tools refuse because the tasks tools own it: a task page. A task's log (an episode)
// may be edited here, so a summary can be written above its entries. A path is judged as spelled and as the memory
// reads it (slugPath folds accents, spaces and case), for the page written or removed is the one that reading finds.
func isTaskBookPage(path string) bool {
	one := func(p string) bool {
		return isTaskPath(p) && !tasks.IsLogPath(strings.TrimLeft(strings.ReplaceAll(strings.TrimSpace(p), "\\", "/"), "/"))
	}
	return one(path) || one(slugPath(path))
}

// --- recall and search ---

var dottedTool = regexp.MustCompile(`\bmemory\.(search|recall|read|list|write|sources|delete)\b`)

// toolNames writes the memory's tools by the name they have here: a hub written by the old server says
// "memory.search", and there is no such tool (memory_recall, mode search). The page is kept as it was; this is how it
// is shown.
func toolNames(body string) string {
	return dottedTool.ReplaceAllStringFunc(body, func(m string) string {
		if m == "memory.search" {
			return "memory_recall"
		}
		return strings.Replace(m, ".", "_", 1)
	})
}

type recallPageOut struct {
	Path     string           `json:"path"`
	Title    string           `json:"title"`
	Kind     *string          `json:"kind,omitempty"`
	Body     string           `json:"body"`
	Cut      bool             `json:"cut,omitempty"`
	Passages []memory.Passage `json:"passages,omitempty"`
}

// recallRelatedOut is a page near the subject, by name only: what it says is a memory_read away, and its lede, the way
// it was reached and its passages were most of the answer's weight.
type recallRelatedOut struct {
	Path  string `json:"path"`
	Title string `json:"title"`
}

// recallDocOut is a document's passage as recall shows it: what it is and says, not the ids of its place in the
// store (memory_recall mode search has them). A collection stands for the origin when the document has none.
type recallDocOut struct {
	Title   string `json:"title"`
	Origin  string `json:"origin,omitempty"`
	Section string `json:"section,omitempty"`
	Text    string `json:"text"`
}

const (
	// recallDocChars is the most characters of a document's passage in a recall, recallPageChars of a page's body:
	// memory_read has the rest.
	recallDocChars  = 400
	recallPageChars = 1500
)

func (s *Server) recall(ctx context.Context, raw []byte) (any, error) {
	a, err := decode[protocol.MemoryRecall](raw)
	if err != nil {
		return nil, err
	}
	if a.Mode == "search" {
		return s.search(ctx, a)
	}
	ans, err := s.cfg.Memory.Recall(ctx, a.Query, memory.RecallOptions{
		Index: s.cfg.Search, Budget: a.Budget, Embedder: s.cfg.Embedder,
		TaskGroups: s.cfg.Tasks.TaskGroups, Now: s.cfg.Now,
	})
	if err != nil {
		return nil, err
	}
	out := recallOut{Query: ans.Query, Pages: []recallPageOut{}, Related: []recallRelatedOut{}, Documents: []recallDocOut{},
		Tokens: ans.Tokens, Meaning: ans.Meaning, Notes: ans.Notes}
	for _, d := range ans.Documents {
		out.Documents = append(out.Documents, recallDocOut{Title: d.Title, Origin: cmp.Or(d.Origin, d.Collection), Section: d.Section,
			Text: clip.Words(d.Text, recallDocChars)})
	}
	cutPage := false
	for _, p := range ans.Pages {
		body, cut := toolNames(p.Body), p.Cut
		if c, _ := clip.WindowJSON(body, 0, recallPageChars); utf8.RuneCountInString(c) < utf8.RuneCountInString(body) {
			body, cut = c, true
		}
		cutPage = cutPage || cut
		out.Pages = append(out.Pages, recallPageOut{p.Path, p.Title, kindp(p.Kind), body, cut, p.Passages})
	}
	if cutPage {
		out.Notes = append(out.Notes, "a page marked cut goes on: memory_read has it whole")
	}
	for _, r := range ans.Related {
		out.Related = append(out.Related, recallRelatedOut{r.Path, r.Title})
	}
	// the floor that says "nothing relevant" is a judgment on a few signals: when it is wrong the question is answered by
	// nothing, so the search's best three go along, said to be a last resort
	if len(out.Pages) == 0 && slices.Contains(out.Notes, "nothing relevant") && s.cfg.Search != nil {
		if found, _, err := s.searchPages(ctx, a.Query, 3, ""); err == nil && len(found) > 0 {
			out.Nearest = found
			out.Notes = append(out.Notes, "nearest by search, weak: read them before trusting")
		}
	}
	s.fitRecall(&out, a.Budget)
	return out, nil
}

// recallOut is memory_recall's answer.
type recallOut struct {
	Query     string             `json:"query"`
	Pages     []recallPageOut    `json:"pages"`
	Related   []recallRelatedOut `json:"related"`
	Documents []recallDocOut     `json:"documents"`
	Tokens    int                `json:"tokens"`
	Meaning   bool               `json:"meaning"`
	Notes     []string           `json:"notes,omitempty"`
	Nearest   []foundOut         `json:"nearest,omitempty"`
}

// maxBudget is the most tokens memory_recall answers with whatever the caller asks: 12000 was 48k characters into the
// caller's context.
const maxBudget = 4000

// charsPerToken is what a token of the budget is worth in characters of the answer as sent.
const charsPerToken = 4

// fitRecall holds the answer to its budget as it is sent. The memory counts tokens on the text of what it packs; the
// answer is that text in JSON, escaped and indented, and ran 20% over (9.5k characters for the default 2000 tokens).
// So the answer is measured as rendered, and over budget it loses its related pages, then its documents, then the end
// of its pages, and says so.
func (s *Server) fitRecall(out *recallOut, budget int) {
	budget = max(300, min(cmp.Or(budget, 2000), maxBudget))
	limit := budget * charsPerToken
	size := func() int {
		text, err := s.render(out)
		if err != nil {
			return 0
		}
		return utf8.RuneCountInString(text)
	}
	if size() <= limit {
		return
	}
	// the note that says what was cut takes room too
	limit -= 300
	related, documents, cutPages := 0, 0, 0
	for n := size(); n > limit; n = size() {
		switch {
		case len(out.Related) > 0:
			out.Related = out.Related[:len(out.Related)-1]
			related++
		case len(out.Documents) > 0:
			out.Documents = out.Documents[:len(out.Documents)-1]
			documents++
		case len(out.Pages) > 0:
			last := &out.Pages[len(out.Pages)-1]
			enc, _ := json.Marshal(last.Body)
			target := len(enc) - 2 - (n - limit) - 20
			if target < 400 && len(out.Pages) > 1 {
				out.Pages = out.Pages[:len(out.Pages)-1]
				cutPages++
				continue
			}
			target = max(target, 200)
			if len(enc)-2 <= target {
				// nothing left to take from the body: what is over is the page's passages and fields
				if len(last.Passages) == 0 {
					return
				}
				last.Passages = nil
				continue
			}
			last.Body, _ = clip.WindowJSON(last.Body, 0, target)
			last.Cut = true
		default:
			return
		}
	}
	var what []string
	if related > 0 {
		what = append(what, fmt.Sprintf("%d related pages", related))
	}
	if documents > 0 {
		what = append(what, fmt.Sprintf("%d documents", documents))
	}
	if cutPages > 0 {
		what = append(what, fmt.Sprintf("%d pages", cutPages))
	}
	cut := "cut to the budget of " + strconv.Itoa(budget) + " tokens"
	if len(what) > 0 {
		cut += ", left out: " + strings.Join(what, ", ")
	}
	out.Notes = append(out.Notes, cut+"; a bigger budget (up to "+strconv.Itoa(maxBudget)+") or memory_read has the rest")
}

const (
	// searchCitations is the most passages shown for one search result, citationChars the most characters of each:
	// a result is told apart by its title and a line or two, memory_read has the page.
	searchCitations = 2
	citationChars   = 200
	// searchChars is the most characters a search answer carries; results past it are counted, not sent.
	searchChars = 6000
)

type citationOut struct {
	Text    string         `json:"text"`
	At      map[string]any `json:"at,omitempty"`
	Section string         `json:"section,omitempty"`
}

// foundOut is a page, a document or a task the search found: a page has a path, a document its id and collection.
type foundOut struct {
	Path       string        `json:"path,omitempty"`
	Title      string        `json:"title"`
	Document   string        `json:"document,omitempty"`
	Collection string        `json:"collection,omitempty"`
	Origin     string        `json:"origin,omitempty"`
	Version    *string       `json:"version,omitempty"`
	Superseded bool          `json:"superseded,omitempty"`
	Citations  []citationOut `json:"citations,omitempty"`
	Excerpt    string        `json:"excerpt,omitempty"`
}

func (s *Server) search(ctx context.Context, a protocol.MemoryRecall) (any, error) {
	if err := needs("mode search", f("query", a.Query)); err != nil {
		return nil, err
	}
	limit := a.Limit
	if limit == 0 {
		limit = 10
	}
	results, ans, err := s.searchPages(ctx, a.Query, limit, a.Version)
	if err != nil {
		return nil, err
	}
	if a.IncludeTasks {
		found, err := s.searchTasks(ctx, a.Query, limit)
		if err != nil {
			return nil, err
		}
		results = append(results, found...)
	}
	out := struct {
		Results []foundOut `json:"results"`
		Meaning bool       `json:"meaning"`
		Notes   []string   `json:"notes,omitempty"`
	}{results, ans.Meaning, ans.Notes}
	size := func() int {
		text, err := s.render(out)
		if err != nil {
			return 0
		}
		return utf8.RuneCountInString(text)
	}
	// the note that says what was left out takes room too
	left := 0
	for size() > searchChars-200 && len(out.Results) > 1 {
		out.Results = out.Results[:len(out.Results)-1]
		left++
	}
	if left > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d results left out to keep the answer within %d characters: a narrower query or a lower limit shows them, memory_read a page in full", left, searchChars))
	}
	return out, nil
}

// searchPages is the search's results as the tools show them.
func (s *Server) searchPages(ctx context.Context, query string, limit int, version string) ([]foundOut, search.Answer, error) {
	ans, err := s.cfg.Search.Search(ctx, query, search.Options{Limit: limit, Embedder: s.cfg.Embedder, Version: version})
	if err != nil {
		return nil, ans, err
	}
	results := []foundOut{}
	for _, r := range ans.Results {
		o := foundOut{Path: r.Path, Title: r.Title, Citations: []citationOut{}}
		if d := r.Document; d != nil {
			v := d.Version
			o = foundOut{Title: r.Title, Document: d.ID, Collection: d.Collection, Origin: d.Origin, Version: &v,
				Superseded: d.Superseded, Citations: []citationOut{}}
		}
		for _, c := range r.Citations[:min(len(r.Citations), searchCitations)] {
			o.Citations = append(o.Citations, citationOut{Text: clip.Words(c.Text, citationChars), At: locatorOut(c.At), Section: c.Context})
		}
		results = append(results, o)
	}
	return results, ans, nil
}

// searchTasks: the tasks whose title or text hold every word of the query, most recently changed first. A task is not in
// the search index; the task book finds them by its own query.
func (s *Server) searchTasks(ctx context.Context, query string, limit int) ([]foundOut, error) {
	terms := wordRe.FindAllString(strings.ToLower(query), -1)
	if len(terms) == 0 {
		return nil, nil
	}
	all, err := s.cfg.Tasks.List(ctx)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(all, func(a, b tasks.Task) int { return cmp.Compare(b.Updated, a.Updated) })
	var out []foundOut
	for _, t := range all {
		text := strings.ToLower(t.Ref + " " + t.Title + " " + t.Notes)
		if !slices.ContainsFunc(terms, func(w string) bool { return !strings.Contains(text, w) }) {
			// found in the very text the excerpt is cut from: an offset in `text` would point elsewhere in it
			shown := t.Notes + " " + t.Title
			at := strings.Index(strings.ToLower(shown), terms[0])
			if at < 0 || at > len(shown) { // the word is in the ref only (or lower-casing changed a length)
				at = 0
			}
			out = append(out, foundOut{Path: "tasks/" + t.ID + ".md", Title: t.Title, Excerpt: excerpt([]rune(shown), len([]rune(shown[:at])))})
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func excerpt(r []rune, at int) string {
	from := max(0, min(at, len(r))-60)
	return strings.Join(strings.Fields(string(r[from:min(len(r), from+180)])), " ")
}

// --- read and list ---

// linkOut is an outgoing link that does not fit a bare path: one with a relation, or whose target is no page.
type linkOut struct {
	Path    string `json:"path,omitempty"`
	Target  string `json:"target,omitempty"`
	Missing bool   `json:"missing,omitempty"`
	Rel     string `json:"rel,omitempty"`
}

type relatedOut struct {
	From string `json:"from"`
	Rel  string `json:"rel"`
}

// readLinks is the most entries memory_read gives in each of its link lists; the rest is counted in links.more.
const readLinks = 30

// isLogPath is a page that piles up under a project: a task, a task's log, a diary day or week.
func (s *Server) isLogPath(p string) bool {
	return strings.HasPrefix(p, "tasks/") || strings.HasPrefix(p, s.areas.Diary+"/")
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// pagesFirst is the paths with those that are not logs first, each part in its order.
func pagesFirst(paths []string, isLog func(string) bool) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !isLog(p) {
			out = append(out, p)
		}
	}
	for _, p := range paths {
		if isLog(p) {
			out = append(out, p)
		}
	}
	return out
}

// pathOf is the page a call is about: its path, or the card of its project.
func (s *Server) pathOf(path, card string) (string, error) {
	if path == "" && card == "" {
		return "", needs("this call", field{"path or card", true})
	}
	if path != "" {
		return path, nil
	}
	return s.cardPath(card), nil
}

// find is a page by its path, else by the name a link would use.
func (s *Server) find(ctx context.Context, path string) (*memory.Doc, error) {
	d, err := s.cfg.Memory.Get(ctx, path)
	if err != nil || d != nil {
		return d, err
	}
	resolve, err := s.cfg.Memory.Resolver(ctx)
	if err != nil {
		return nil, err
	}
	if p := resolve(strings.TrimSuffix(path, ".md")); p != "" {
		return s.cfg.Memory.Get(ctx, p)
	}
	return nil, nil
}

func (s *Server) readPage(ctx context.Context, raw []byte) (any, error) {
	a, err := decode[protocol.MemoryRead](raw)
	if err != nil {
		return nil, err
	}
	path, err := s.pathOf(a.Path, a.Card)
	if err != nil {
		return nil, err
	}
	if a.History {
		h, err := s.cfg.Memory.History(ctx, path)
		if err != nil {
			return nil, err
		}
		type version struct {
			Path string    `json:"path"`
			Rev  int       `json:"rev"`
			At   string    `json:"at"`
			By   string    `json:"by"`
			Op   memory.Op `json:"op"`
		}
		versions := []version{}
		for _, e := range h {
			versions = append(versions, version{e.Path, e.Rev, e.At, e.By, e.Op})
		}
		return map[string]any{"path": path, "versions": versions}, nil
	}
	if a.Rev > 0 {
		r, err := s.cfg.Memory.Revision(ctx, path, a.Rev)
		if err != nil {
			return nil, err
		}
		if r == nil {
			return nil, clientErrorf("no version %d of %s", a.Rev, path)
		}
		body, cut, err := pieceOf(r.Body, a)
		if err != nil {
			return nil, err
		}
		return struct {
			Path string    `json:"path"`
			Rev  int       `json:"rev"`
			At   string    `json:"at"`
			By   string    `json:"by"`
			Op   memory.Op `json:"op"`
			Body string    `json:"body"`
			*bodyCut
		}{r.Path, r.Rev, r.At, r.By, r.Op, body, cut}, nil
	}
	// A card is one project's page: found by name, a project with none would get another project's card.
	var d *memory.Doc
	if a.Path == "" {
		d, err = s.cfg.Memory.Get(ctx, path)
		if err == nil && d == nil {
			return nil, clientErrorf("project %s has no card yet: create it with memory_write op write", a.Card)
		}
	} else {
		d, err = s.find(ctx, path)
	}
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, clientErrorf("no document %s: search for it with memory_recall, or list a folder with memory_list", path)
	}
	l, err := s.cfg.Memory.Links(ctx, d.Path, nil)
	if err != nil {
		return nil, err
	}
	links := struct {
		// Out is the pages linked to: the path as a string, an object when it has a relation or resolves to no page.
		Out []any `json:"out"`
		// Back and Related are lists with links true, else their counts.
		Back    any `json:"back"`
		Related any `json:"related"`
		// More counts what was left out of each list past readLinks, when anything was.
		More map[string]int `json:"more,omitempty"`
	}{Out: []any{}}
	var related []relatedOut
	for _, o := range l.Out {
		switch {
		case o.Path == "":
			links.Out = append(links.Out, linkOut{Target: o.Target, Missing: true, Rel: o.Rel})
		case o.Rel != "":
			links.Out = append(links.Out, linkOut{Path: o.Path, Rel: o.Rel})
		default:
			links.Out = append(links.Out, o.Path)
		}
	}
	for _, r := range l.Related {
		related = append(related, relatedOut{r.From, r.Rel})
	}
	// a project's page is linked back by every task and diary day it ever had: 200 paths in one read, all of it into the
	// reader's context; the pages that are not tasks or days go first, and the rest is a count
	back := pagesFirst(l.Back, s.isLogPath)
	slices.SortStableFunc(related, func(a, b relatedOut) int {
		return cmp.Compare(btoi(s.isLogPath(a.From)), btoi(s.isLogPath(b.From)))
	})
	more := map[string]int{}
	if n := len(links.Out) - readLinks; n > 0 {
		links.Out, more["out"] = links.Out[:readLinks], n
	}
	if a.Links {
		if n := len(back) - readLinks; n > 0 {
			back, more["back"] = back[:readLinks], n
		}
		if n := len(related) - readLinks; n > 0 {
			related, more["related"] = related[:readLinks], n
		}
		links.Back, links.Related = append([]string{}, back...), append([]relatedOut{}, related...)
	} else {
		links.Back, links.Related = len(back), len(related)
	}
	if len(more) > 0 {
		links.More = more
	}
	body, cut, err := pieceOf(toolNames(d.Body), a)
	if err != nil {
		return nil, err
	}
	return struct {
		Path    string  `json:"path"`
		Title   string  `json:"title"`
		Kind    *string `json:"kind,omitempty"`
		Body    string  `json:"body"`
		Rev     int     `json:"rev"`
		Updated string  `json:"updated"`
		Links   any     `json:"links"`
		*bodyCut
	}{d.Path, d.Title, kindp(d.Kind), body, d.Rev, d.Updated, links, cut}, nil
}

// readBudget is the most characters of a page's body one memory_read answer carries unless max_chars says otherwise.
const readBudget = 4000

// bodyCut says the body is a piece of the page (cut to the cap, from an offset or one section) and how to get the rest.
type bodyCut struct {
	Truncated string `json:"truncated"`
	Total     int    `json:"total_chars"`
	Offset    int    `json:"offset"`
	Next      int    `json:"next_offset,omitempty"`
	Section   string `json:"section,omitempty"`
	// Sections are the page's headings with their offsets, when the body stopped short, to choose a section from.
	Sections []clip.Heading `json:"sections,omitempty"`
}

// pieceOf is the part of a page's body a read answers with: the whole of it when it fits the cap, else a piece, with
// the cut said. offset and section choose the piece; max_chars 0 lifts the cap.
func pieceOf(body string, a protocol.MemoryRead) (string, *bodyCut, error) {
	src, section := body, ""
	if a.Section != "" {
		b, _, ok := clip.Section(body, a.Section)
		if !ok {
			var titles []string
			for _, h := range clip.Headings(body) {
				titles = append(titles, strconv.Quote(h.Title))
			}
			if len(titles) > maxSections {
				titles = append(titles[:maxSections], "…")
			}
			return "", nil, clientErrorf("no section %q in %s (none, or more than one has that title): its headings are %s",
				a.Section, a.Path+a.Card, strings.Join(titles, ", "))
		}
		src, section = b, strings.TrimSpace(a.Section)
	}
	room := readBudget
	if a.MaxChars != nil {
		room = max(*a.MaxChars, 0)
	}
	piece, c := clip.WindowJSON(src, a.Offset, room)
	if !c.Cut && a.Offset == 0 && section == "" {
		return piece, nil, nil
	}
	cut := &bodyCut{Total: c.Total, Offset: c.Offset, Section: section}
	switch {
	case c.Cut:
		cut.Next = c.Next
		cut.Sections = clip.Headings(body)
		cut.Sections = cut.Sections[:min(len(cut.Sections), maxSections)]
		cut.Truncated = fmt.Sprintf("truncated, %d characters in total, characters %d-%d shown: memory_read again with offset %d "+
			"for the rest, section for one part by its heading, or max_chars 0 for all of it", c.Total, c.Offset, c.Next, c.Next)
	case section != "":
		cut.Truncated = fmt.Sprintf("only the section %q, %d characters (the page has more)", section, c.Total)
	default:
		cut.Truncated = fmt.Sprintf("from character %d to the end, %d characters in total", c.Offset, c.Total)
	}
	return piece, cut, nil
}

// listLimit is how many documents of a collection memory_list shows unless a limit says otherwise; pagesLimit how many
// pages (the newest ones: a longer list is asked for with a limit, and the answer says there is more).
const (
	listLimit  = 50
	pagesLimit = 20
)

// maxSections is how many of a page's headings a cut answer lists.
const maxSections = 40

func (s *Server) listPages(ctx context.Context, raw []byte) (any, error) {
	a, err := decode[protocol.MemoryList](raw)
	if err != nil {
		return nil, err
	}
	switch a.What {
	case "collections":
		return s.listCollections(ctx)
	case "documents":
		return s.listDocuments(ctx, a.Collection, a.Limit)
	case "health":
		return s.health(ctx)
	}
	prefix := strings.Trim(a.Folder, "/")
	if prefix != "" {
		prefix += "/"
	}
	limit := a.Limit
	if limit == 0 {
		// A list with no folder measured about 15,800 tokens at 200 pages, more than every MCP server of a session.
		limit = pagesLimit
	}
	infos, err := s.cfg.Memory.List(ctx, prefix, memory.ListOpts{Limit: limit, Kind: memory.Kind(a.Kind)})
	if err != nil {
		return nil, err
	}
	type docOut struct {
		Path    string  `json:"path"`
		Title   string  `json:"title"`
		Kind    *string `json:"kind,omitempty"`
		Rev     int     `json:"rev"`
		Updated string  `json:"updated"`
	}
	docs := []docOut{}
	for _, i := range infos {
		docs = append(docs, docOut{i.Path, i.Title, kindp(i.Kind), i.Rev, i.Updated})
	}
	folder := prefix
	if folder == "" {
		folder = "/"
	}
	out := map[string]any{"folder": folder, "documents": docs}
	if len(docs) == limit {
		out["more"] = fmt.Sprintf("only the newest %d: name a folder, or pass a limit up to 1000", limit)
	}
	return out, nil
}

// health is the memory against its rules: pages nothing links to, links to pages that do not exist, pages too long.
func (s *Server) health(ctx context.Context) (any, error) {
	h, err := s.cfg.Memory.Health(ctx, s.areas, s.maxWords, s.secretKinds)
	if err != nil {
		return nil, err
	}
	type broken struct {
		Page string `json:"page"`
		Link string `json:"link"`
	}
	type long struct {
		Page  string `json:"page"`
		Words int    `json:"words"`
	}
	b, l := []broken{}, []long{}
	for _, x := range h.BrokenLinks {
		b = append(b, broken{x.Page, x.Link})
	}
	for _, x := range h.TooLong {
		l = append(l, long{x.Page, x.Words})
	}
	// the secrets are paths only: the text stays where it is, for the owner to read and decide
	return map[string]any{"pages": h.Pages, "orphans": h.Orphans, "broken_links": b, "too_long": l,
		"secrets": h.Secrets, "secret_revisions": h.SecretRevisions, "secret_kinds": h.SecretKinds}, nil
}

// --- write ---

type written struct {
	Written string `json:"written"`
	Rev     int    `json:"rev"`
	Title   string `json:"title"`
}

// writePage writes a page by the memory's rules: what memory_write op write does. The reasons come in the verdict
// when a rule refuses it; a base that is no longer the page's is an error.
func (s *Server) writePage(ctx context.Context, path, body string, base *int, distinct bool, keptIn []string) (*written, verdict, error) {
	p := slugPath(path)
	cur, err := s.cfg.Memory.Get(ctx, p)
	if err != nil {
		return nil, verdict{}, err
	}
	if cur != nil && areaOf(p) == s.areas.Inbox {
		return nil, verdict{errors: []string{p + " is only added to: use memory_write op append"}}, nil
	}
	if cur != nil && areaOf(p) == s.areas.Diary && !s.isWeekPath(p) {
		if e := diaryRewriteErrors(cur.Body, body); len(e) > 0 {
			return nil, verdict{errors: e}, nil
		}
	}
	v, err := s.check(ctx, p, body, bodyOf(cur), cur == nil, distinct)
	if err != nil || len(v.errors) > 0 {
		return nil, v, err
	}
	if cur != nil {
		if err := s.survives(ctx, p, cur.Body, body, keptIn); err != nil {
			return nil, v, err
		}
	}
	var d *memory.Doc
	if base == nil {
		d, err = s.cfg.Memory.Write(ctx, p, body, s.by(ctx))
	} else {
		d, err = s.cfg.Memory.WriteAt(ctx, p, body, s.by(ctx), *base)
	}
	if err != nil {
		return nil, v, err
	}
	s.markWeek(ctx, p, body)
	return &written{d.Path, d.Rev, d.Title}, v, nil
}

func (s *Server) writeMemory(ctx context.Context, raw []byte) (any, error) {
	a, err := decode[protocol.MemoryWrite](raw)
	if err != nil {
		return nil, err
	}
	if s.by(ctx) == upkeepAuthor {
		if err := capList("kept_in", a.KeptIn); err != nil {
			return nil, err
		}
	}
	if err := s.checkWrite(ctx, a); err != nil {
		return nil, err
	}
	switch a.Op {
	case "write":
		if err := needs("op write", f("body", a.Body)); err != nil {
			return nil, err
		}
		if a.Card != "" {
			return s.saveCard(ctx, a.Card, a.Body, a.BaseRev, a.KeptIn)
		}
		path, err := s.pathOf(a.Path, a.Card)
		if err != nil {
			return nil, err
		}
		if isTaskBookPage(path) {
			return refuse([]string{tasksOnly}), nil
		}
		r, v, err := s.writePage(ctx, path, a.Body, a.BaseRev, a.Distinct, a.KeptIn)
		if err != nil {
			return nil, err
		}
		if len(v.errors) > 0 {
			return refuse(v.errors, v.similar...), nil
		}
		return r, nil
	case "edit":
		return s.editPage(ctx, a)
	case "append":
		if err := needs("op append", f("text", a.Text)); err != nil {
			return nil, err
		}
		if n := len([]rune(a.Text)); n > 1000 {
			return nil, refusal("text", fmt.Sprintf("%d characters: at most 1000", n), "one line; a longer text is a page")
		}
		why := ""
		if isCloud(ctx) {
			why = s.cloudWhy(ctx)
		}
		r, errs, err := s.appendLine(ctx, a.Where == "inbox", []string{a.Text}, time.Now(), why)
		if err != nil {
			return nil, err
		}
		if errs != nil {
			return refuse(errs), nil
		}
		return map[string]any{"added": r.path, "rev": r.rev}, nil
	case "move":
		return s.movePage(ctx, a)
	case "restore":
		if err := needs("op restore", field{"rev", a.Rev == 0}); err != nil {
			return nil, err
		}
		// a restore puts back a whole older body: nothing checks that what the page holds now survives it
		if s.by(ctx) == upkeepAuthor {
			return refuse([]string{"restore is the owner's: the upkeep does not restore pages"}), nil
		}
		path, err := s.pathOf(a.Path, a.Card)
		if err != nil {
			return nil, err
		}
		d, err := s.cfg.Memory.Restore(ctx, path, a.Rev, s.by(ctx))
		if err != nil {
			return nil, err
		}
		return map[string]any{"restored": d.Path, "from": a.Rev, "rev": d.Rev}, nil
	case "remember":
		if err := needs("op remember", f("text", a.Text)); err != nil {
			return nil, err
		}
		r, errs, err := s.remember(ctx, a.Text, a.About)
		if err != nil {
			return nil, err
		}
		if errs != nil {
			return refuse(errs), nil
		}
		return r, nil
	}
	return nil, refusal("op", "unknown", "use write, edit, append, move, restore or remember")
}

func (s *Server) editPage(ctx context.Context, a protocol.MemoryWrite) (any, error) {
	if err := needs("op edit", f("find", a.Find), field{"replace", false}); err != nil {
		return nil, err
	}
	if a.Card != "" {
		cur, err := s.cfg.Memory.Get(ctx, s.cardPath(a.Card))
		if err != nil {
			return nil, err
		}
		if cur == nil {
			return nil, refusal("card", "no card yet", "create it with op write")
		}
		body, err := editedCard(cur.Body, a.Find, a.Replace)
		if err != nil {
			return nil, err
		}
		return s.saveCard(ctx, a.Card, body, a.BaseRev, a.KeptIn)
	}
	path, err := s.pathOf(a.Path, a.Card)
	if err != nil {
		return nil, err
	}
	if isTaskBookPage(path) {
		return refuse([]string{tasksOnly}), nil
	}
	d, err := s.cfg.Memory.Get(ctx, path)
	if err == nil && d == nil {
		d, err = s.cfg.Memory.Get(ctx, slugPath(path))
	}
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, clientErrorf("no document %s", path)
	}
	if isTaskBookPage(d.Path) {
		return refuse([]string{tasksOnly}), nil
	}
	if s.isLog(areaOf(d.Path)) {
		return refuse([]string{d.Path + " is only added to: use memory_write op append"}), nil
	}
	switch n := strings.Count(d.Body, a.Find); {
	case n == 0:
		return nil, clientErrorf("the passage is not in the document: read it again")
	case n > 1:
		return nil, clientErrorf("the passage occurs %d times: include more context", n)
	}
	next := strings.Replace(d.Body, a.Find, a.Replace, 1)
	v, err := s.check(ctx, d.Path, next, d.Body, false, false)
	if err != nil {
		return nil, err
	}
	if len(v.errors) > 0 {
		return refuse(v.errors), nil
	}
	if err := s.survives(ctx, d.Path, d.Body, next, a.KeptIn); err != nil {
		return nil, err
	}
	base := d.Rev
	if a.BaseRev != nil {
		base = *a.BaseRev
	}
	w, err := s.cfg.Memory.WriteAt(ctx, d.Path, next, s.by(ctx), base)
	if err != nil {
		return nil, err
	}
	return map[string]any{"edited": w.Path, "rev": w.Rev}, nil
}

func (s *Server) movePage(ctx context.Context, a protocol.MemoryWrite) (any, error) {
	if err := needs("op move", f("from", a.From), f("to", a.To)); err != nil {
		return nil, err
	}
	if isTask(a.From) || isTask(a.To) {
		return refuse([]string{tasksOnly}), nil
	}
	d, err := s.cfg.Memory.Get(ctx, a.From)
	if err == nil && d == nil {
		d, err = s.cfg.Memory.Get(ctx, slugPath(a.From))
	}
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, clientErrorf("no document %s", a.From)
	}
	dest := slugPath(a.To)
	if isTaskBookPage(d.Path) || isTask(dest) {
		return refuse([]string{tasksOnly}), nil
	}
	// the inbox and the diary's days are written by append only: a move would rewrite or leave a record
	for _, p := range []string{d.Path, dest} {
		if s.isLog(areaOf(p)) && !s.isWeekPath(p) {
			return refuse([]string{p + " is a log (the inbox or a diary day): it is only added to, never moved"}), nil
		}
	}
	if ex, err := s.cfg.Memory.Get(ctx, dest); err != nil {
		return nil, err
	} else if ex != nil {
		return nil, clientErrorf("%s exists already", dest)
	}
	v, err := s.check(ctx, dest, d.Body, d.Body, false, false)
	if err != nil {
		return nil, err
	}
	if len(v.errors) > 0 {
		return refuse(v.errors), nil
	}
	// the pages that link here, found before the move while their links still resolve to the old path
	links, err := s.cfg.Memory.Links(ctx, d.Path, nil)
	if err != nil {
		return nil, err
	}
	resolveOld, err := s.cfg.Memory.Resolver(ctx)
	if err != nil {
		return nil, err
	}
	type rewrite struct{ src, body string }
	var rewritten []rewrite
	for _, src := range links.Back {
		b, err := s.cfg.Memory.Get(ctx, src)
		if err != nil {
			return nil, err
		}
		if b != nil {
			rewritten = append(rewritten, rewrite{src, relink(b.Body, d.Path, dest, resolveOld)})
		}
	}
	if _, err := s.cfg.Memory.Write(ctx, dest, d.Body, s.by(ctx)); err != nil {
		return nil, err
	}
	if err := s.cfg.Memory.Remove(ctx, d.Path, s.by(ctx)); err != nil {
		return nil, err
	}
	// the page has moved by now: a link that cannot be followed is reported, not turned into a failed move
	updated := []string{}
	var notUpdated []string
	for _, r := range rewritten {
		src := r.src
		if src == d.Path {
			src = dest
		}
		cur, err := s.cfg.Memory.Get(ctx, src)
		if err == nil && cur != nil && cur.Body != r.body {
			_, err = s.cfg.Memory.WriteAt(ctx, cur.Path, r.body, s.by(ctx), cur.Rev)
		}
		switch {
		case err != nil:
			notUpdated = append(notUpdated, r.src+" ("+err.Error()+")")
		case cur == nil:
			notUpdated = append(notUpdated, r.src+" (the page is gone)")
		default:
			updated = append(updated, r.src)
		}
	}
	out := map[string]any{"moved": d.Path, "to": dest, "links_updated": updated}
	if len(notUpdated) > 0 {
		out["links_not_updated"] = notUpdated
	}
	return out, nil
}

func (s *Server) deletePage(ctx context.Context, raw []byte) (any, error) {
	a, err := decode[protocol.MemoryDelete](raw)
	if err != nil {
		return nil, err
	}
	if s.by(ctx) == upkeepAuthor {
		if err := capList("kept_in", a.KeptIn); err != nil {
			return nil, err
		}
	}
	if a.What != "page" && s.by(ctx) == upkeepAuthor {
		return nil, refusal("what", "the upkeep deletes pages only", "use what page")
	}
	switch a.What {
	case "page":
		if err := needs("what page", f("path", a.Path)); err != nil {
			return nil, err
		}
		if isTask(a.Path) {
			return refuse([]string{tasksOnly}), nil
		}
		path := a.Path
		if s.by(ctx) == upkeepAuthor {
			// the check needs the page: found as editPage finds it, and that path is the one removed, so the check
			// and the removal are of one page
			cur, err := s.cfg.Memory.Get(ctx, a.Path)
			if err == nil && cur == nil {
				cur, err = s.cfg.Memory.Get(ctx, slugPath(a.Path))
			}
			if err != nil {
				return nil, err
			}
			if cur != nil {
				path = cur.Path
				if isTaskBookPage(path) || isTask(path) {
					return refuse([]string{tasksOnly}), nil
				}
				if s.isLogDay(path) {
					return refuse([]string{path + " is a log (the inbox or a diary day): the upkeep does not delete it"}), nil
				}
				if err := s.survives(ctx, cur.Path, cur.Body, "", a.KeptIn); err != nil {
					return nil, err
				}
			}
		}
		if err := s.cfg.Memory.Remove(ctx, path, s.by(ctx)); err != nil {
			return nil, err
		}
		return map[string]any{"removed": path}, nil
	default:
		return s.deleteSource(ctx, a)
	}
}
