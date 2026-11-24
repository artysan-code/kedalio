package memory

import (
	"regexp"
	"strings"
	"testing"

	"kedalio/core/memory/docs"
	"kedalio/core/memory/search"
)

var nonSlug = regexp.MustCompile(`[^a-z0-9/]+`)

func testSlug(p string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(p), "-"), "-") + ".md"
}

func newCollections(t *testing.T) (*Collections, *Store) {
	t.Helper()
	s, ix := recallStore(t)
	return &Collections{Store: s, Index: ix, Fetch: &docs.Fetcher{}, Slug: testSlug}, s
}

func mdFile(name, text string) docs.Source {
	return docs.Source{File: &docs.File{Name: name, Bytes: []byte(text)}}
}

func hubOf(t *testing.T, s *Store, path string) *Doc {
	t.Helper()
	d, err := s.Get(ctx, path)
	if err != nil || d == nil {
		t.Fatalf("hub %s: %v %v", path, d, err)
	}
	return d
}

func TestSaveMakesTheCollectionAndItsHubPageAndKeepsThemInStep(t *testing.T) {
	c, s := newCollections(t)
	v := "5.4"
	col, err := c.Save(ctx, "alice", CollectionInput{Name: "Stack Docs", Description: "The docs of the stack.", Version: &v}, "")
	if err != nil {
		t.Fatal(err)
	}
	if col.Hub != "sources/stack-docs.md" || col.Version != "5.4" {
		t.Fatalf("got %+v", col)
	}
	hub := hubOf(t, s, col.Hub)
	if hub.Kind != KindSource || !strings.Contains(hub.Body, "# Stack Docs") ||
		!strings.Contains(hub.Body, "Collection of documents, version 5.4. Search it") || !strings.Contains(hub.Body, "The docs of the stack.") {
		t.Fatalf("hub %q (%s)", hub.Body, hub.Kind)
	}
	// renamed, versioned and marked: the hub's title and managed line follow, the description stays
	v2, yes, name := "6.0", true, "Stack Manual"
	col, err = c.Save(ctx, "alice", CollectionInput{Name: name, Version: &v2, Important: &yes}, col.ID)
	if err != nil {
		t.Fatal(err)
	}
	hub = hubOf(t, s, col.Hub)
	if !strings.Contains(hub.Body, "# Stack Manual") || !strings.Contains(hub.Body, "Collection of documents, version 6.0, marked important.") ||
		!strings.Contains(hub.Body, "The docs of the stack.") || hub.Rev != 2 {
		t.Fatalf("hub %q rev %d", hub.Body, hub.Rev)
	}
	// nothing changed: no new revision
	if _, err := c.Save(ctx, "alice", CollectionInput{}, col.ID); err != nil {
		t.Fatal(err)
	}
	if hubOf(t, s, col.Hub).Rev != 2 {
		t.Fatal("a save that changed nothing wrote the hub")
	}
}

func TestSaveRefusals(t *testing.T) {
	c, s := newCollections(t)
	if _, err := c.Save(ctx, "a", CollectionInput{}, ""); err == nil || !strings.Contains(err.Error(), "has a name") {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Save(ctx, "a", CollectionInput{Name: "x"}, "nope"); err == nil || !strings.Contains(err.Error(), "no collection nope") {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Save(ctx, "a", CollectionInput{Name: "Same"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Save(ctx, "a", CollectionInput{Name: "Same"}, ""); err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Fatalf("got %v", err)
	}
	// a page already at the hub's path: the hub takes a suffixed one
	write(t, s, "sources/taken.md", "# Taken\n\nA page.")
	col, err := c.Save(ctx, "a", CollectionInput{Name: "Taken"}, "")
	if err != nil || !strings.HasPrefix(col.Hub, "sources/taken-") {
		t.Fatalf("got %+v %v", col, err)
	}
}

func TestAddDocumentListsItOnTheHubAndRemovingItTakesItOff(t *testing.T) {
	c, s := newCollections(t)
	col, _ := c.Save(ctx, "a", CollectionInput{Name: "Guides"}, "")
	a, err := c.AddDocument(ctx, "a", col.ID, mdFile("setup.md", "# Setup\n\nInstall the thing."))
	if err != nil || a.Change != search.Added || a.Kind != search.KindMarkdown || a.Chunks != 1 || a.Original == nil {
		t.Fatalf("got %+v %v", a, err)
	}
	if _, err := c.AddDocument(ctx, "a", col.ID, mdFile("use.md", "# Use\n\nRun the thing.")); err != nil {
		t.Fatal(err)
	}
	hub := hubOf(t, s, col.Hub)
	if !strings.Contains(hub.Body, "## Documents\n\n- setup.md (markdown)\n- use.md (markdown)\n") {
		t.Fatalf("hub %q", hub.Body)
	}
	// the same file again: nothing changes, nothing is written to the hub
	rev := hub.Rev
	if r, err := c.AddDocument(ctx, "a", col.ID, mdFile("use.md", "# Use\n\nRun the thing.")); err != nil || r.Change != search.Unchanged {
		t.Fatalf("got %+v %v", r, err)
	}
	if hubOf(t, s, col.Hub).Rev != rev {
		t.Fatal("an unchanged document wrote the hub")
	}
	title, err := c.RemoveDocument(ctx, "a", col.ID, a.ID)
	if err != nil || title != "setup.md" {
		t.Fatalf("got %q %v", title, err)
	}
	if body := hubOf(t, s, col.Hub).Body; strings.Contains(body, "setup.md") || !strings.Contains(body, "- use.md") {
		t.Fatalf("hub %q", body)
	}
	if _, err := c.RemoveDocument(ctx, "a", col.ID, "nope"); err == nil || !strings.Contains(err.Error(), "no document nope") {
		t.Fatalf("got %v", err)
	}
	if _, err := c.AddDocument(ctx, "a", "nope", mdFile("x.md", "x")); err == nil || !strings.Contains(err.Error(), "no collection") {
		t.Fatalf("got %v", err)
	}
}

func TestTheHubListsAtMostHubListedDocumentsAndCountsTheRest(t *testing.T) {
	var ds []search.Document
	for i := 0; i < HubListed+3; i++ {
		ds = append(ds, search.Document{Title: "d", Kind: search.KindText})
	}
	got := DocumentsSection(ds)
	if strings.Count(got, "- d (text)") != HubListed || !strings.Contains(got, "- and 3 more: memory_list lists them all") {
		t.Fatalf("got %q", got)
	}
	if DocumentsSection(nil) != "" {
		t.Fatal("a section with no documents")
	}
}

func TestWithDocumentsReplacesOnlyItsOwnSection(t *testing.T) {
	body := "# Hub\n\nIntro.\n\n## Documents\n\n- old (text)\n\n## Notes\n\nKept.\n"
	got := withDocuments(body, "## Documents\n\n- new (text)\n")
	want := "# Hub\n\nIntro.\n\n## Notes\n\nKept.\n\n## Documents\n\n- new (text)\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
	if got := withDocuments(want, ""); got != "# Hub\n\nIntro.\n\n## Notes\n\nKept.\n" {
		t.Fatalf("got %q", got)
	}
}

func TestDeleteTakesTheDocumentsAndTheHubWithIt(t *testing.T) {
	c, s := newCollections(t)
	col, _ := c.Save(ctx, "a", CollectionInput{Name: "Temp"}, "")
	c.AddDocument(ctx, "a", col.ID, mdFile("a.md", "# A\n\nbody"))
	name, n, err := c.Delete(ctx, col.ID, "a")
	if err != nil || name != "Temp" || n != 1 {
		t.Fatalf("got %q %d %v", name, n, err)
	}
	if d, _ := s.Get(ctx, col.Hub); d != nil {
		t.Fatal("the hub is still there")
	}
	if cols, _ := c.Index.Collections(ctx); len(cols) != 0 {
		t.Fatalf("collections %+v", cols)
	}
	if _, _, err := c.Delete(ctx, col.ID, "a"); err == nil {
		t.Fatal("deleted twice")
	}
}

func TestRecallCitesDocumentsAndTheirHub(t *testing.T) {
	c, s := newCollections(t)
	col, _ := c.Save(ctx, "a", CollectionInput{Name: "Telescope Manual", Description: "How the telescope works."}, "")
	if _, err := c.AddDocument(ctx, "a", col.ID, mdFile("focus.md",
		"# Focusing\n\n## Fine focus\n\nTurn the zorblax knob slowly until the star is sharp.")); err != nil {
		t.Fatal(err)
	}
	write(t, s, "notes/cat.md", pageOf("Cat", "The cat sleeps all day."))
	ans, err := s.Recall(ctx, "zorblax knob", RecallOptions{Index: c.Index})
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Documents) != 1 {
		t.Fatalf("documents %+v", ans.Documents)
	}
	p := ans.Documents[0]
	if p.Collection != "Telescope Manual" || p.Origin != "focus.md" || p.Section != "focus.md › Focusing › Fine focus" ||
		p.At.Heading != "Fine focus" || p.Chunk != 1 || p.Version != "" || p.Superseded || !strings.Contains(p.Text, "zorblax") {
		t.Fatalf("passage %+v", p)
	}
	// the hub is a page of the graph; the passage the pack carries is not repeated under it
	for _, pg := range ans.Pages {
		if len(pg.Passages) > 1 {
			t.Fatalf("passages %+v", pg.Passages)
		}
	}
	// nothing about it in the pages: a query the documents answer and no page does still returns the passage
	ans, err = s.Recall(ctx, "sharp star", RecallOptions{Index: c.Index})
	if err != nil || len(ans.Documents) != 1 {
		t.Fatalf("got %+v %v", ans, err)
	}
}

func TestRecallPassagesCostTheBudget(t *testing.T) {
	c, s := newCollections(t)
	col, _ := c.Save(ctx, "a", CollectionInput{Name: "Venus"}, "")
	c.AddDocument(ctx, "a", col.ID, mdFile("a.md", "# A\n\n"+strings.Repeat("quartz ", 400)))
	ans, err := s.Recall(ctx, "quartz", RecallOptions{Index: c.Index})
	if err != nil || len(ans.Documents) == 0 {
		t.Fatalf("got %+v %v", ans, err)
	}
	// a passage is cut to passageTokens
	if n := Tokens(ans.Documents[0].Text); n > passageTokens {
		t.Fatalf("%d tokens", n)
	}
	if ans.Tokens < ans.Documents[0].cost() {
		t.Fatalf("tokens %d", ans.Tokens)
	}
}

func TestCutPassage(t *testing.T) {
	long := strings.Repeat("Alpha beta gamma. ", 40)
	got := cutPassage(long, 20)
	if !strings.HasSuffix(got, "gamma.") || Tokens(got) > 20 {
		t.Fatalf("got %q", got)
	}
	if got := cutPassage("short", 20); got != "short" {
		t.Fatalf("got %q", got)
	}
	words := strings.Repeat("word ", 100)
	if got := cutPassage(words, 10); strings.HasSuffix(got, " ") || Tokens(got) > 10 {
		t.Fatalf("got %q", got)
	}
}

func TestSaveThatCannotWriteTheHubLeavesNoCollectionBehind(t *testing.T) {
	c, _ := newCollections(t)
	c.Slug = func(string) string { return "" }
	if _, err := c.Save(ctx, "a", CollectionInput{Name: "Orphan"}, ""); err == nil {
		t.Fatal("a hub with no path must fail the save")
	}
	cols, err := c.Index.Collections(ctx)
	if err != nil || len(cols) != 0 {
		t.Fatalf("collections left behind: %+v %v", cols, err)
	}
}

func TestRenamingTheCollectionTouchesOnlyTheFirstTitleAndManagedLine(t *testing.T) {
	c, s := newCollections(t)
	col, err := c.Save(ctx, "a", CollectionInput{Name: "Guide"}, "")
	if err != nil {
		t.Fatal(err)
	}
	cur := hubOf(t, s, col.Hub)
	extra := cur.Body + "\n# My own heading\n\nCollection of documents I keep elsewhere.\n"
	if _, err := s.WriteAt(ctx, col.Hub, extra, "a", cur.Rev); err != nil {
		t.Fatal(err)
	}
	name := "Manual"
	if _, err := c.Save(ctx, "a", CollectionInput{Name: name}, col.ID); err != nil {
		t.Fatal(err)
	}
	body := hubOf(t, s, col.Hub).Body
	if !strings.Contains(body, "# Manual") || !strings.Contains(body, "# My own heading") ||
		!strings.Contains(body, "Collection of documents I keep elsewhere.") {
		t.Fatalf("hub %q", body)
	}
}
