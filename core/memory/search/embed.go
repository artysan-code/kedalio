package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The model that turns text into vectors sits behind an interface so the core can point it at an OpenAI-compatible
// API (llama.cpp's server, for one) and tests can use a fake. There is no default model: the owner names it. The model is a
// separate process reached over HTTP (docs/architecture.md).

const defaultEmbedTimeout = 120 * time.Second

// Embedder turns texts into vectors.
type Embedder interface {
	// Model is stored with every vector: chunks embedded by another model count as not embedded.
	Model() string
	// Embed returns the vectors of input, in order. The caller bounds the time with ctx.
	Embed(ctx context.Context, input []string) ([][]float32, error)
}

// withDefaultTimeout bounds ctx by d unless the caller already set a deadline.
func withDefaultTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// EmbedConfig points at an embedding server.
type EmbedConfig struct {
	URL   string
	Model string
}

// errBadAnswer is a model that answered, but not with something usable: a status other than 2xx, a body that is not
// the expected JSON, or the wrong number of vectors. Callers tell it from a model that was not reached with errors.Is.
var errBadAnswer = errors.New("embedding model: unexpected answer")

type openAI struct{ cfg EmbedConfig }

// NewOpenAIEmbedder is an Embedder over an OpenAI-compatible /v1/embeddings (llama.cpp's server, Infinity, vLLM):
// every input in one request.
func NewOpenAIEmbedder(cfg EmbedConfig) Embedder {
	// a base URL written with its trailing slash would otherwise ask for //v1/embeddings
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	return openAI{cfg}
}

func (o openAI) Model() string { return o.cfg.Model }

func (o openAI) Embed(ctx context.Context, input []string) ([][]float32, error) {
	ctx, cancel := withDefaultTimeout(ctx, defaultEmbedTimeout)
	defer cancel()
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := postJSON(ctx, o.cfg.URL+"/v1/embeddings", map[string]any{"model": o.cfg.Model, "input": input}, &out, "embedding model"); err != nil {
		return nil, err
	}
	if len(out.Data) != len(input) {
		return nil, errBadAnswer
	}
	// the order of data is the server's: index says which input each vector belongs to. A vector that is empty, one
	// of another length, or an index outside the inputs or repeated would be stored as the meaning of the wrong text.
	vecs := make([][]float32, len(out.Data))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) || vecs[d.Index] != nil || len(d.Embedding) == 0 {
			return nil, errBadAnswer
		}
		vecs[d.Index] = d.Embedding
	}
	for _, v := range vecs {
		if len(v) != len(vecs[0]) {
			return nil, errBadAnswer
		}
	}
	return vecs, nil
}

// Lanes is one model asked by two callers: a search waits on its query, the background embeds the memory. The model
// answers requests in the order they arrive, so a query sent behind a batch of pages waits for the whole batch (seconds
// on a small server). The background lane sends one text per request, and none while a query is in flight: a query
// waits at most for the one text already being embedded. One text per request costs the background nothing, since the
// model embeds every input on its own anyway.
func Lanes(e Embedder) (query, background Embedder) {
	l := &lanes{e: e, idle: make(chan struct{})}
	close(l.idle)
	return laneQuery{l}, laneBackground{l}
}

type lanes struct {
	e       Embedder
	mu      sync.Mutex
	queries int
	// idle is closed while no query is in flight
	idle chan struct{}
}

type laneQuery struct{ l *lanes }

func (q laneQuery) Model() string { return q.l.e.Model() }

func (q laneQuery) Embed(ctx context.Context, input []string) ([][]float32, error) {
	l := q.l
	l.mu.Lock()
	if l.queries == 0 {
		l.idle = make(chan struct{})
	}
	l.queries++
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		if l.queries--; l.queries == 0 {
			close(l.idle)
		}
		l.mu.Unlock()
	}()
	return l.e.Embed(ctx, input)
}

type laneBackground struct{ l *lanes }

func (b laneBackground) Model() string { return b.l.e.Model() }

func (b laneBackground) Embed(ctx context.Context, input []string) ([][]float32, error) {
	l := b.l
	out := make([][]float32, 0, len(input))
	for _, text := range input {
		for {
			l.mu.Lock()
			busy, idle := l.queries > 0, l.idle
			l.mu.Unlock()
			if !busy {
				break
			}
			select {
			case <-idle:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		v, err := l.e.Embed(ctx, []string{text})
		if err != nil {
			return nil, err
		}
		if len(v) != 1 {
			return nil, errBadAnswer
		}
		out = append(out, v...)
	}
	return out, nil
}

// postJSON posts body as JSON and decodes the answer into out. A status other than 2xx is an error named after what.
func postJSON(ctx context.Context, url string, body, out any, what string) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("%s: HTTP %d: %w", what, res.StatusCode, errBadAnswer)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		// the decoder's text names the fault, never the address
		return fmt.Errorf("%s: %w: %v", what, errBadAnswer, err)
	}
	return nil
}
