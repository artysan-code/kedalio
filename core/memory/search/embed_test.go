package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmbedModelThatDoesNotAnswerIsGivenUpOnAtTheDeadline(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-done }))
	defer srv.Close()
	defer close(done)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	if _, err := NewOpenAIEmbedder(EmbedConfig{URL: srv.URL, Model: "m"}).Embed(ctx, []string{"q"}); err == nil {
		t.Fatal("no error")
	}
	if time.Since(t0) > 2*time.Second {
		t.Fatal("gave up after seconds, not near the deadline")
	}
}

func TestEmbedURLWithTrailingSlashDoesNotDoubleIt(t *testing.T) {
	var path atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path.Store(r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float32{1}}}})
	}))
	defer srv.Close()
	if _, err := NewOpenAIEmbedder(EmbedConfig{URL: srv.URL + "/", Model: "m"}).Embed(context.Background(), []string{"q"}); err != nil {
		t.Fatal(err)
	}
	if got := path.Load(); got != "/v1/embeddings" {
		t.Errorf("path = %v, want /v1/embeddings", got)
	}
}

func TestEmbedOpenAIOneRequestPutBackInOrder(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path %s", r.URL.Path)
		}
		var in struct {
			Input []string
			Model string
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.Model != "m" {
			t.Errorf("model %q", in.Model)
		}
		// llama.cpp may answer out of order: index says whose vector it is
		var data []map[string]any
		for i := len(in.Input) - 1; i >= 0; i-- {
			data = append(data, map[string]any{"index": i, "embedding": []int{len(in.Input[i])}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	got, err := NewOpenAIEmbedder(EmbedConfig{URL: srv.URL, Model: "m"}).Embed(context.Background(), []string{"a", "bbb"})
	if err != nil || !reflect.DeepEqual(got, [][]float32{{1}, {3}}) || calls.Load() != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestEmbedOpenAIWrongLengthOrStatusIsAnError(t *testing.T) {
	short := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"index":0,"embedding":[1]}]}`))
	}))
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "loading model", http.StatusServiceUnavailable)
	}))
	defer short.Close()
	defer down.Close()
	if _, err := NewOpenAIEmbedder(EmbedConfig{URL: short.URL, Model: "m"}).Embed(context.Background(), []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("got %v", err)
	}
	if _, err := NewOpenAIEmbedder(EmbedConfig{URL: down.URL, Model: "m"}).Embed(context.Background(), []string{"a"}); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("got %v", err)
	}
}

// A vector that is empty, of another length, or an index that is out of range or repeated is an unusable answer, not
// the meaning of some text.
func TestEmbedOpenAIMalformedVectorsAreAnError(t *testing.T) {
	for name, body := range map[string]string{
		"empty":     `{"data":[{"index":0,"embedding":[]},{"index":1,"embedding":[1,2]}]}`,
		"length":    `{"data":[{"index":0,"embedding":[1,2]},{"index":1,"embedding":[1]}]}`,
		"duplicate": `{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[2]}]}`,
		"range":     `{"data":[{"index":0,"embedding":[1]},{"index":5,"embedding":[2]}]}`,
		"negative":  `{"data":[{"index":-1,"embedding":[1]},{"index":0,"embedding":[2]}]}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		_, err := NewOpenAIEmbedder(EmbedConfig{URL: srv.URL, Model: "m"}).Embed(context.Background(), []string{"a", "b"})
		srv.Close()
		if !errors.Is(err, errBadAnswer) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	// out of order is fine: index says where each vector goes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"index":1,"embedding":[2,2]},{"index":0,"embedding":[1,1]}]}`))
	}))
	defer srv.Close()
	got, err := NewOpenAIEmbedder(EmbedConfig{URL: srv.URL, Model: "m"}).Embed(context.Background(), []string{"a", "b"})
	if err != nil || got[0][0] != 1 || got[1][0] != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
}

// The background sends one text at a time and none while a query is in flight.
func TestLanesBackgroundYieldsToAQuery(t *testing.T) {
	type call struct {
		text string
		gate chan struct{}
	}
	calls := make(chan call, 10)
	model := funcEmbedder{model: "m", fn: func(_ context.Context, in []string) ([][]float32, error) {
		c := call{in[0], make(chan struct{})}
		calls <- c
		<-c.gate
		out := make([][]float32, len(in))
		for i, s := range in {
			out[i] = []float32{float32(len(s))}
		}
		return out, nil
	}}
	query, background := Lanes(model)
	next := func() call {
		select {
		case c := <-calls:
			return c
		case <-time.After(2 * time.Second):
			t.Fatal("no call arrived")
			return call{}
		}
	}
	noCall := func() {
		select {
		case c := <-calls:
			t.Fatalf("unexpected call %q", c.text)
		case <-time.After(50 * time.Millisecond):
		}
	}
	type answer struct {
		v   [][]float32
		err error
	}
	pagesDone, queryDone := make(chan answer, 1), make(chan answer, 1)
	go func() {
		v, err := background.Embed(context.Background(), []string{"a", "bb", "ccc"})
		pagesDone <- answer{v, err}
	}()
	a := next()
	if a.text != "a" {
		t.Fatalf("first %q", a.text)
	}
	go func() {
		v, err := query.Embed(context.Background(), []string{"question"})
		queryDone <- answer{v, err}
	}()
	q := next()
	if q.text != "question" {
		t.Fatalf("second %q", q.text)
	}
	close(a.gate)
	// the query was sent; the background's next text waits for it
	noCall()
	close(q.gate)
	if got := <-queryDone; got.err != nil || !reflect.DeepEqual(got.v, [][]float32{{8}}) {
		t.Fatalf("query: %v", got)
	}
	bb := next()
	if bb.text != "bb" {
		t.Fatalf("third %q", bb.text)
	}
	close(bb.gate)
	ccc := next()
	close(ccc.gate)
	if got := <-pagesDone; got.err != nil || !reflect.DeepEqual(got.v, [][]float32{{1}, {2}, {3}}) {
		t.Fatalf("pages: %v", got)
	}
	if query.Model() != "m" || background.Model() != "m" {
		t.Fatal("model")
	}
}
