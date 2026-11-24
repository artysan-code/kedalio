package search

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSearchNoteNamesNoAddressAndASlowModelDoesNotCostTheNextAsk(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	slow := funcEmbedder{"fake", func(ctx context.Context, in []string) ([][]float32, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		select {
		case <-release:
			return newFake("fake").Embed(ctx, in)
		case <-ctx.Done():
			return nil, &url.Error{Op: "Post", URL: "http://embed:8080/v1/embeddings", Err: ctx.Err()}
		}
	}}
	ix := brain(t)
	a := search(t, ix, "tyres", Options{Embedder: slow, Timeout: 50 * time.Millisecond})
	if a.Meaning || len(a.Notes) != 1 || strings.Contains(a.Notes[0], "http") || strings.Contains(a.Notes[0], "embed:8080") {
		t.Fatalf("got %+v", a)
	}
	// the query's embedding went on apart from that search: once the model answers, the next ask has it without a new call
	close(release)
	time.Sleep(100 * time.Millisecond)
	b := search(t, ix, "tyres", Options{Embedder: slow, Timeout: 50 * time.Millisecond})
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 || len(b.Notes) != 0 {
		t.Fatalf("calls=%d notes=%v", calls, b.Notes)
	}
	// a plain network error says the model could not be reached, still without the address
	if n := noteFor("meaning", &url.Error{Op: "Post", URL: "http://embed:8080/x", Err: errors.New("refused")}); strings.Contains(n, "embed") {
		t.Fatal(n)
	}
}
