package memory

import (
	"context"
	"slices"
	"testing"
)

func TestRecallGraphIsReusedUntilAWriteChangesIt(t *testing.T) {
	s, ix := brain(t)
	ctx := context.Background()
	today := "2026-01-01"
	g1, err := s.recaller(ctx).graph(DefaultRecallWeights, today)
	if err != nil {
		t.Fatal(err)
	}
	if g2, _ := s.recaller(ctx).graph(DefaultRecallWeights, today); g2 != g1 {
		t.Error("nothing was written: the graph is read again")
	}
	if g3, _ := s.recaller(ctx).graph(DefaultRecallWeights, "2026-01-02"); g3 == g1 {
		t.Error("a new day reuses a graph whose ended relations were judged on another day")
	}

	res, _ := s.Resolver(ctx)
	if res("nuova") != "" {
		t.Fatal("the page exists before it is written")
	}
	write(t, s, "note/nuova.md", pageOf("Nuova", "Parla del tabletop, vedi [[giochi/tabletop]]."))
	if res, _ = s.Resolver(ctx); res("nuova") != "note/nuova.md" {
		t.Error("a cached resolver does not know a page written after it")
	}
	g4, _ := s.recaller(ctx).graph(DefaultRecallWeights, today)
	if g4 == g1 || !slices.Contains(g4.nodes, "note/nuova.md") {
		t.Error("a page written after the graph was cached is not in it")
	}
	if _, titles, err := s.recaller(ctx).nodeKindsOf(g4); err != nil || titles["note/nuova.md"] != "Nuova" {
		t.Errorf("the titles kept with the graph miss the new page: %v", err)
	}
	if got := shown(recall(t, s, ix, "tabletop", RecallOptions{})); !slices.Contains(got, "note/nuova.md") {
		t.Errorf("recall does not reach the new page: %v", got)
	}

	if err := s.Remove(ctx, "note/nuova.md", "test"); err != nil {
		t.Fatal(err)
	}
	if g5, _ := s.recaller(ctx).graph(DefaultRecallWeights, today); slices.Contains(g5.nodes, "note/nuova.md") {
		t.Error("a removed page is still in the cached graph")
	}

	if err := s.setSimilar(ctx, "persone/alice.md", []near{{path: "note/fatture.md", w: 0.9}}); err != nil {
		t.Fatal(err)
	}
	g6, _ := s.recaller(ctx).graph(DefaultRecallWeights, today)
	a, b := g6.index["persone/alice.md"], g6.index["note/fatture.md"]
	if !slices.ContainsFunc(g6.adj[a], func(e edge) bool { return e.to == b && e.meaning }) {
		t.Error("a similarity written after the graph was cached is not in it")
	}
}
