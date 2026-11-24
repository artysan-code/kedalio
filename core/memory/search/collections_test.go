package search

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func newCollection(t *testing.T, ix *Index, name, version string) string {
	t.Helper()
	id, err := ix.CreateCollection(context.Background(), name, CollectionFields{Version: &version})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func addDoc(t *testing.T, ix *Index, col, origin, title string, sections ...Section) AddedDocument {
	t.Helper()
	r, err := ix.AddDocument(context.Background(), NewDocument{
		Collection: col, Kind: KindMarkdown, Title: title, Origin: origin, Sections: sections,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestChunkSectionsCarryPageAndHeadingAcrossSections(t *testing.T) {
	got := chunkSections([]Section{
		{Text: "# Guide\n\nfirst page", Page: 1},
		{Text: "still the guide, on page two", Page: 2},
	}, defaultSize, "Manual")
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got[0].page != 1 || got[1].page != 2 {
		t.Fatalf("pages %d %d", got[0].page, got[1].page)
	}
	// the chapter goes on across the page break, breadcrumb included
	if got[1].heading != "Guide" || got[1].context != "Manual › Guide" {
		t.Fatalf("second: %+v", got[1])
	}
	if want := `{"page":2,"heading":"Guide"}`; got[1].locator() != want {
		t.Fatalf("locator %s", got[1].locator())
	}
}

func TestAddDocumentIsSearchedWithItsCollectionAndLocator(t *testing.T) {
	ix := newTestIndex(t)
	ctx := context.Background()
	col := newCollection(t, ix, "Rust Book", "1.0")
	r := addDoc(t, ix, col, "ownership.pdf", "Ownership",
		Section{Text: "# Borrowing\n\nA reference borrows a value without owning it.", Page: 12})
	if r.Change != Added || r.Chunks != 1 {
		t.Fatalf("got %+v", r)
	}
	ix.page(t, "notes/a.md", "Notes", "# Notes\n\nnothing relevant here, only a cat")
	ans, err := ix.Search(ctx, "borrows reference", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Results) != 1 || ans.Results[0].Document == nil {
		t.Fatalf("got %+v", ans.Results)
	}
	res := ans.Results[0]
	if d := res.Document; d.ID != r.ID || d.Collection != "Rust Book" || d.Origin != "ownership.pdf" || d.Version != "1.0" || d.Superseded {
		t.Fatalf("document %+v", d)
	}
	c := res.Citations[0]
	if c.At.Page != 12 || c.At.Heading != "Borrowing" || c.Context != "Ownership › Borrowing" || c.Position != 0 {
		t.Fatalf("citation %+v", c)
	}
}

func TestSameTextAgainIsUnchangedAndNewTextUnderTheSameVersionIsRewritten(t *testing.T) {
	ix := newTestIndex(t)
	col := newCollection(t, ix, "Docs", "1")
	a := addDoc(t, ix, col, "a.md", "A", Section{Text: "# A\n\nfirst text"})
	b := addDoc(t, ix, col, "a.md", "A", Section{Text: "# A\n\nfirst text"})
	if b.Change != Unchanged || b.ID != a.ID {
		t.Fatalf("got %+v", b)
	}
	c := addDoc(t, ix, col, "a.md", "A", Section{Text: "# A\n\nsecond text"})
	if c.Change != Updated || c.ID != a.ID {
		t.Fatalf("got %+v", c)
	}
	docs, _ := ix.Documents(context.Background(), col, false)
	if len(docs) != 1 || docs[0].Superseded != "" {
		t.Fatalf("documents %+v", docs)
	}
}

func TestANewVersionKeepsTheOldDocumentSupersededAndSearchableByVersion(t *testing.T) {
	ix := newTestIndex(t)
	ctx := context.Background()
	col := newCollection(t, ix, "Product", "1.0")
	old := addDoc(t, ix, col, "guide.md", "Guide", Section{Text: "# Guide\n\nThe lantern setting is called brightness."})
	v2 := "2.0"
	if err := ix.UpdateCollection(ctx, col, CollectionFields{Version: &v2}); err != nil {
		t.Fatal(err)
	}
	cur := addDoc(t, ix, col, "guide.md", "Guide", Section{Text: "# Guide\n\nThe lantern setting is called glow."})
	if cur.ID == old.ID || cur.Change != Updated {
		t.Fatalf("old %+v new %+v", old, cur)
	}
	docs, _ := ix.Documents(ctx, col, false)
	if len(docs) != 2 {
		t.Fatalf("documents %+v", docs)
	}
	live, _ := ix.Documents(ctx, col, true)
	if len(live) != 1 || live[0].ID != cur.ID || live[0].Version != "2.0" {
		t.Fatalf("live %+v", live)
	}
	// by default only the current one is found
	ans, _ := ix.Search(ctx, "lantern", Options{})
	if len(ans.Results) != 1 || ans.Results[0].Document.ID != cur.ID {
		t.Fatalf("current: %+v", ans.Results)
	}
	// asking for the old version finds the old text, marked superseded
	ans, _ = ix.Search(ctx, "lantern", Options{Version: "1.0"})
	if len(ans.Results) != 1 || ans.Results[0].Document.ID != old.ID || !ans.Results[0].Document.Superseded ||
		!strings.Contains(ans.Results[0].Citations[0].Text, "brightness") {
		t.Fatalf("v1: %+v", ans.Results)
	}
}

func TestScopeKeepsASearchToTheWikiOrToCollections(t *testing.T) {
	ix := newTestIndex(t)
	ctx := context.Background()
	a, b := newCollection(t, ix, "A", ""), newCollection(t, ix, "B", "")
	addDoc(t, ix, a, "a.md", "Alpha", Section{Text: "the zeppelin is in collection a"})
	addDoc(t, ix, b, "b.md", "Beta", Section{Text: "the zeppelin is in collection b"})
	ix.page(t, "zeppelin.md", "Zeppelin", "the zeppelin is a page")
	kinds := func(o Options) []string {
		ans, err := ix.Search(ctx, "zeppelin", o)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range ans.Results {
			if r.Document != nil {
				out = append(out, r.Document.Collection)
			} else {
				out = append(out, "page")
			}
		}
		slices.Sort(out)
		return out
	}
	if got := kinds(Options{}); !slices.Equal(got, []string{"A", "B", "page"}) {
		t.Fatalf("all: %v", got)
	}
	if got := kinds(Options{Scope: Scope{Kind: ScopeWiki}}); !slices.Equal(got, []string{"page"}) {
		t.Fatalf("wiki: %v", got)
	}
	if got := kinds(Options{Scope: Scope{Kind: ScopeCollections, IDs: []string{b}}}); !slices.Equal(got, []string{"B"}) {
		t.Fatalf("b: %v", got)
	}
}

func TestDocumentChunksAreEmbeddedAndFoundByMeaning(t *testing.T) {
	ix := newTestIndex(t)
	ctx := context.Background()
	e := newFake("m")
	col := newCollection(t, ix, "Garage", "")
	addDoc(t, ix, col, "cars.md", "Cars", Section{Text: "# Cars\n\nthe vehicle is parked"})
	ix.embedAll(t, e, 10)
	ans, err := ix.Search(ctx, "automobile", Options{Embedder: e})
	if err != nil {
		t.Fatal(err)
	}
	if !ans.Meaning || len(ans.Results) != 1 || ans.Results[0].Document == nil || !ans.Results[0].Meaning {
		t.Fatalf("got %+v", ans)
	}
}

func TestRemoveDocumentAndCollectionTakeTheirChunksAndFiles(t *testing.T) {
	ix := newTestIndex(t)
	ctx := context.Background()
	col := newCollection(t, ix, "Docs", "")
	file := &Original{OriginalInfo: OriginalInfo{Name: "a.md", Type: "text/markdown", Size: 3}, Bytes: []byte("abc")}
	r, err := ix.AddDocument(ctx, NewDocument{Collection: col, Kind: KindMarkdown, Title: "A", Origin: "a.md",
		Sections: []Section{{Text: "# A\n\nbody"}}, Original: file})
	if err != nil {
		t.Fatal(err)
	}
	o, _ := ix.ReadOriginal(ctx, r.ID)
	if o == nil || string(o.Bytes) != "abc" || o.Type != "text/markdown" {
		t.Fatalf("original %+v", o)
	}
	docs, _ := ix.Documents(ctx, col, false)
	if len(docs) != 1 || docs[0].Original == nil || docs[0].Original.Size != 3 {
		t.Fatalf("documents %+v", docs)
	}
	if err := ix.RemoveDocument(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if n := ix.count(t, "search_chunks"); n != 0 {
		t.Fatalf("%d chunks left", n)
	}
	if n := ix.count(t, "search_document_originals"); n != 0 {
		t.Fatalf("%d originals left", n)
	}
	addDoc(t, ix, col, "b.md", "B", Section{Text: "# B\n\nbody"})
	if err := ix.RemoveCollection(ctx, col); err != nil {
		t.Fatal(err)
	}
	if n := ix.count(t, "search_chunks") + ix.count(t, "search_documents") + ix.count(t, "search_collections"); n != 0 {
		t.Fatalf("%d rows left", n)
	}
}

func TestCollectionNamesAreUniqueAndUpdatesNeedACollection(t *testing.T) {
	ix := newTestIndex(t)
	ctx := context.Background()
	newCollection(t, ix, "Docs", "")
	if _, err := ix.CreateCollection(ctx, "Docs", CollectionFields{}); err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Fatalf("got %v", err)
	}
	name := "x"
	if err := ix.UpdateCollection(ctx, "nope", CollectionFields{Name: &name}); err != ErrNoCollection {
		t.Fatalf("got %v", err)
	}
	if _, err := ix.AddDocument(ctx, NewDocument{Collection: "nope", Origin: "a", Title: "a"}); err != ErrNoCollection {
		t.Fatalf("got %v", err)
	}
}
