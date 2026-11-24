package search

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"

	"kedalio/core/db"
)

// newTestIndex is an index over a fresh in-memory database.
func newTestIndex(t *testing.T) *Index {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ix, err := New(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// page indexes a page in a transaction of its own, as the page store does in its write.
func (ix *Index) page(t *testing.T, path, title, body string) {
	t.Helper()
	ctx := context.Background()
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ix.IndexPage(ctx, tx, path, title, body); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func (ix *Index) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := ix.db.QueryRow("select count(*) from " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (ix *Index) embedAll(t *testing.T, e Embedder, batch int) {
	t.Helper()
	for {
		n, err := ix.EmbedPending(context.Background(), e, batch)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

// Words that mean the same thing share a dimension, so meaning finds what words miss; any other word lands on a
// dimension of its own by its hash.
var concepts = map[string]int{"car": 0, "automobile": 0, "vehicle": 0, "dog": 1, "hound": 1, "puppy": 1}

const fakeDim = 64

var letters = regexp.MustCompile(`\p{L}+`)

type fakeEmbedder struct {
	model string
	mu    sync.Mutex
	calls [][]string
}

func newFake(model string) *fakeEmbedder { return &fakeEmbedder{model: model} }

func (f *fakeEmbedder) Model() string { return f.model }

func (f *fakeEmbedder) Embed(_ context.Context, input []string) ([][]float32, error) {
	f.mu.Lock()
	f.calls = append(f.calls, input)
	f.mu.Unlock()
	out := make([][]float32, len(input))
	for i, t := range input {
		v := make([]float32, fakeDim)
		for _, w := range letters.FindAllString(strings.ToLower(t), -1) {
			h := uint32(7)
			for _, c := range w {
				h = h*31 + uint32(c)
			}
			d, ok := concepts[w]
			if !ok {
				d = 8 + int(h%(fakeDim-8))
			}
			v[d]++
		}
		out[i] = v
	}
	return out, nil
}

// funcEmbedder is an Embedder written inline.
type funcEmbedder struct {
	model string
	fn    func(context.Context, []string) ([][]float32, error)
}

func (f funcEmbedder) Model() string { return f.model }
func (f funcEmbedder) Embed(ctx context.Context, in []string) ([][]float32, error) {
	return f.fn(ctx, in)
}
