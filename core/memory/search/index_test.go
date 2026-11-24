package search

import (
	"context"
	"errors"
	"testing"
)

func TestIndexPageChunksReplaceTheOldOnesAndGoWithThePage(t *testing.T) {
	ix := newTestIndex(t)
	ctx := context.Background()
	ix.page(t, "a.md", "A", "# One\n\nfirst\n\n# Two\n\nsecond")
	if n := ix.count(t, "search_chunks"); n != 2 {
		t.Fatalf("chunks = %d", n)
	}
	ix.page(t, "a.md", "A", "only")
	if n := ix.count(t, "search_chunks"); n != 1 {
		t.Fatalf("chunks = %d", n)
	}
	tx, _ := ix.db.BeginTx(ctx, nil)
	if err := ix.RemovePage(ctx, tx, "a.md"); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	if n := ix.count(t, "search_chunks"); n != 0 {
		t.Fatalf("chunks = %d", n)
	}
	// the FTS follows
	if n := ix.count(t, "search_chunks_fts where search_chunks_fts match 'only'"); n != 0 {
		t.Fatalf("fts rows = %d", n)
	}
}

func TestIndexPageInsideATransactionGoesBackWithIt(t *testing.T) {
	ix := newTestIndex(t)
	ctx := context.Background()
	tx, _ := ix.db.BeginTx(ctx, nil)
	if err := ix.IndexPage(ctx, tx, "a.md", "A", "text"); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if n := ix.count(t, "search_chunks"); n != 0 {
		t.Fatalf("chunks = %d", n)
	}
}

func pending(t *testing.T, ix *Index, model string) int {
	t.Helper()
	n, err := ix.Pending(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEmbedPendingFillsBatchesThenNothingPendingAnotherModelMakesAllPending(t *testing.T) {
	ix, e, ctx := newTestIndex(t), newFake("fake"), context.Background()
	ix.page(t, "a.md", "A", "one\n\n# H\n\ntwo\n\n# K\n\nthree")
	if n := pending(t, ix, "fake"); n != 3 {
		t.Fatalf("pending = %d", n)
	}
	for _, want := range []int{2, 1, 0} {
		if n, err := ix.EmbedPending(ctx, e, 2); err != nil || n != want {
			t.Fatalf("embedded %d, %v; want %d", n, err, want)
		}
	}
	if n := pending(t, ix, "fake"); n != 0 {
		t.Fatalf("pending = %d", n)
	}
	if e.calls[0][0] != "A\none" {
		t.Fatalf("the title goes to the model with the text: %q", e.calls[0][0])
	}
	if n := pending(t, ix, "other"); n != 3 {
		t.Fatalf("pending = %d", n)
	}
}

func TestEditingOneParagraphReEmbedsThatChunkOnly(t *testing.T) {
	ix := newTestIndex(t)
	ix.page(t, "a.md", "A", "# One\n\nfirst\n\n# Two\n\nsecond")
	ix.embedAll(t, newFake("fake"), 32)
	ix.page(t, "a.md", "A", "# One\n\nfirst\n\n# Two\n\nchanged")
	if n := pending(t, ix, "fake"); n != 1 {
		t.Fatalf("pending = %d", n)
	}
}

func TestChunkRewrittenWhileTheModelWorkedDoesNotGetTheOldTextsVector(t *testing.T) {
	ix, inner := newTestIndex(t), newFake("fake")
	ix.page(t, "a.md", "A", "before")
	racing := funcEmbedder{"fake", func(ctx context.Context, in []string) ([][]float32, error) {
		ix.page(t, "a.md", "A", "after")
		return inner.Embed(ctx, in)
	}}
	if n, err := ix.EmbedPending(context.Background(), racing, 32); err != nil || n != 1 {
		t.Fatalf("embedded %d, %v", n, err)
	}
	if n := pending(t, ix, "fake"); n != 1 {
		t.Fatalf("the new text still waits for its own vector: pending = %d", n)
	}
}

func TestEmbedPendingModelThatFailsLeavesTheChunksPending(t *testing.T) {
	ix := newTestIndex(t)
	ix.page(t, "a.md", "A", "text")
	down := funcEmbedder{"fake", func(context.Context, []string) ([][]float32, error) { return nil, errors.New("down") }}
	if _, err := ix.EmbedPending(context.Background(), down, 32); err == nil || err.Error() != "down" {
		t.Fatalf("got %v", err)
	}
	if n := pending(t, ix, "fake"); n != 1 {
		t.Fatalf("pending = %d", n)
	}
}

func TestNewIsIdempotent(t *testing.T) {
	ix := newTestIndex(t)
	if _, err := New(context.Background(), ix.db); err != nil {
		t.Fatal(err)
	}
}
