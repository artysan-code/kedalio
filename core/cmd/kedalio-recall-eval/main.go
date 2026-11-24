// Command kedalio-recall-eval asks a copy of the memory the questions of a file and scores what memory_recall answers
// (docs/recall-eval.md). It reads a copy of the data folder, never the folder itself: opening a database writes (the
// write-ahead log, the migrations), so core.db and kek are copied into a temporary folder that is deleted at the end.
//
// The questions go through the memory_recall tool of the connector, called in-process on its HTTP handler (no port is
// opened), so an answer is what production gives for the same query and mode, the fallback that mcp adds when recall
// finds nothing relevant included. No background work runs: no embedding lane, no neighbour refresh, no backup. The
// meaning of a query comes only from a file of stored vectors. Exit 1 only on an error, never on a miss.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"kedalio/core/db"
	"kedalio/core/instance"
	"kedalio/core/mcp"
	"kedalio/core/memory"
	"kedalio/core/memory/search"
	"kedalio/core/projects"
	"kedalio/core/recalleval"
	"kedalio/core/tasks"
	"kedalio/core/vault"
	"kedalio/protocol"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "kedalio-recall-eval:", err)
		os.Exit(1)
	}
}

type config struct {
	data, questions, vectors, mode, now, jsonOut string
	embedURL, embedModel, zone, language         string
	budget, limit                                int
	makeVectors                                  bool
}

func run(args []string, out io.Writer) error {
	var c config
	fs := flag.NewFlagSet("kedalio-recall-eval", flag.ContinueOnError)
	fs.StringVar(&c.data, "data", "", "a folder with core.db and kek (a restored copy, never the live folder: it is copied, not opened)")
	fs.StringVar(&c.questions, "questions", "", "questions file: {questions:[{q, expect, not?, mode?}]}")
	fs.StringVar(&c.vectors, "vectors", "", "JSON map query -> vector, the only source of meaning; none: words only")
	fs.StringVar(&c.mode, "mode", "both", "recall, search or both")
	fs.StringVar(&c.now, "now", "", "pin memory_recall's clock (RFC 3339); none: the current time")
	fs.IntVar(&c.budget, "budget", 2000, "recall budget in tokens")
	fs.IntVar(&c.limit, "limit", 10, "search limit")
	fs.StringVar(&c.jsonOut, "json", "", "write the full result as JSON to this file")
	fs.BoolVar(&c.makeVectors, "make-vectors", false, "ask the embedding server for each question's vector and write them to -vectors, then stop")
	fs.StringVar(&c.embedURL, "embed-url", "", "make-vectors: base URL of an OpenAI-compatible embeddings server")
	fs.StringVar(&c.embedModel, "embed-model", "", "the embedding model: the one the stored chunks were embedded with (required with -vectors)")
	fs.StringVar(&c.zone, "zone", "", "the time zone (IANA name); the database's own when empty")
	fs.StringVar(&c.language, "language", "", "the language (English, Italian); the database's own when empty")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	if c.questions == "" {
		return errors.New("usage: kedalio-recall-eval -data <folder> -questions <file> [-vectors <file>] [-mode recall|search|both] [-now <time>] [-json <file>]\n" +
			"       kedalio-recall-eval -make-vectors -questions <file> -vectors <file> -embed-url <url> [-embed-model <name>]")
	}
	raw, err := os.ReadFile(c.questions)
	if err != nil {
		return err
	}
	qs, err := recalleval.ParseQuestions(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", c.questions, err)
	}
	if c.makeVectors {
		return makeVectors(ctx, c, qs, out)
	}
	if !slices.Contains([]string{"recall", "search", "both"}, c.mode) {
		return fmt.Errorf("-mode is recall, search or both, not %q", c.mode)
	}
	if c.data == "" {
		return errors.New("-data is the folder with core.db and kek")
	}
	var now time.Time
	if c.now != "" {
		if now, err = time.Parse(time.RFC3339, c.now); err != nil {
			return fmt.Errorf("-now: %w", err)
		}
	}
	var vecs *vectorSet
	if c.vectors != "" {
		// a model name that differs from the one the stored chunks carry would silently switch the vector side off
		if c.embedModel == "" {
			return errors.New("-vectors needs -embed-model: the model the stored chunks were embedded with")
		}
		if vecs, err = readVectors(c.vectors, c.embedModel); err != nil {
			return err
		}
	}
	tmp, err := os.MkdirTemp("", "kedalio-recall-eval-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	h, closeDB, err := openCopy(ctx, c, tmp, now, vecs)
	if err != nil {
		return err
	}
	defer closeDB()

	res := Result{Vectors: "none: words only, no meaning search", Budget: c.budget, Limit: c.limit, NowPinned: !now.IsZero(),
		Totals: map[string]recalleval.Totals{}}
	if vecs != nil {
		res.Vectors = fmt.Sprintf("%s: %d vectors, model %s", filepath.Base(c.vectors), len(vecs.byText), c.embedModel)
	}
	if res.NowPinned {
		res.Now = now.UTC().Format(time.RFC3339)
	}
	var outcomes []recalleval.Outcome
	for _, q := range qs {
		for _, mode := range recalleval.Modes(q, c.mode) {
			r := ask(h, q, mode, c)
			res.Questions = append(res.Questions, r)
			outcomes = append(outcomes, r.Outcome)
		}
	}
	res.Totals = recalleval.ByMode(outcomes)
	if vecs != nil {
		res.MissingVectors = vecs.missing()
	}
	if c.jsonOut != "" {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(res); err != nil {
			return err
		}
		if err := os.WriteFile(c.jsonOut, buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	res.summary(out)
	return nil
}

// Result is the JSON the run writes: nothing in it depends on the clock or the machine, so two runs over the same
// copy, questions and vectors are byte for byte the same.
type Result struct {
	// Vectors says where the meaning of the queries came from.
	Vectors string `json:"vectors"`
	// NowPinned says whether recall's clock was pinned to Now; if not, questions of time follow the real clock.
	NowPinned bool   `json:"now_pinned"`
	Now       string `json:"now,omitempty"`
	Budget    int    `json:"budget"`
	Limit     int    `json:"limit"`
	// Totals are per mode.
	Totals map[string]recalleval.Totals `json:"totals"`
	// MissingVectors are the query texts the search asked the vectors file for and it lacked (a question of time is
	// searched without its time words): meaning was left out for them.
	MissingVectors []string `json:"missing_vectors,omitempty"`
	Questions      []Asked  `json:"questions"`
}

// Asked is one question in one mode: its ranks, and what the answer said about its own meaning lane.
type Asked struct {
	recalleval.Outcome
	Meaning bool     `json:"meaning"`
	Notes   []string `json:"notes,omitempty"`
}

func (r Result) summary(w io.Writer) {
	fmt.Fprintf(w, "vectors: %s\n", r.Vectors)
	if r.NowPinned {
		fmt.Fprintf(w, "now: pinned to %s\n", r.Now)
	} else {
		fmt.Fprintln(w, "now: not pinned, the real clock")
	}
	modes := make([]string, 0, len(r.Totals))
	for m := range r.Totals {
		modes = append(modes, m)
	}
	sort.Strings(modes)
	for _, m := range modes {
		t := r.Totals[m]
		fmt.Fprintf(w, "%s: %d questions, %d errors, hit rate %.1f%%, MRR %.3f, rank-1 %.1f%%, noise %d\n",
			m, t.Count, t.Errors, t.HitRate*100, t.MRR, t.Rank1Share*100, t.Noise)
	}
	if len(r.MissingVectors) > 0 {
		fmt.Fprintf(w, "no vector for %d asked text(s), meaning left out for them: %s\n",
			len(r.MissingVectors), strings.Join(r.MissingVectors, " | "))
	}
}

// openCopy copies the data folder's database and key into tmp and opens that copy the way core/instance does, without
// its background work: no embedding lane, no neighbour refresh, no backups, no OAuth. The key and the recovery code
// are never printed. The returned handler is the connector's.
func openCopy(ctx context.Context, c config, tmp string, now time.Time, vecs *vectorSet) (http.Handler, func(), error) {
	for _, name := range []string{"core.db", "core.db-wal", "core.db-shm", "kek"} {
		err := copyFile(filepath.Join(c.data, name), filepath.Join(tmp, name))
		if err != nil && !(errors.Is(err, os.ErrNotExist) && strings.HasPrefix(name, "core.db-")) {
			return nil, nil, fmt.Errorf("copying %s: %w", name, err)
		}
	}
	raw, err := vault.HostKey(tmp, false)
	if err != nil {
		return nil, nil, err
	}
	if raw == nil {
		return nil, nil, errors.New("the data folder has no kek: copy the instance key beside core.db")
	}
	dbKey, err := vault.DatabaseKey(raw)
	if err != nil {
		return nil, nil, err
	}
	sqldb, err := db.OpenEncrypted(filepath.Join(tmp, "core.db"), dbKey)
	if errors.Is(err, db.ErrBadKey) {
		return nil, nil, errors.New("the kek does not open core.db: it must be the key of the same instance")
	} else if err != nil {
		return nil, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			sqldb.Close()
		}
	}()
	if err := vault.Migrate(ctx, sqldb); err != nil {
		return nil, nil, err
	}
	v, err := vault.Open(ctx, sqldb, tmp)
	if err != nil {
		return nil, nil, err
	}
	if v.Locked() {
		return nil, nil, errors.New("the instance is locked: it needs its recovery code, and this tool never asks for one")
	}
	// the same order as the instance: memory's tables first, then search and tasks, which keep derived tables beside them
	if err := memory.Migrate(ctx, sqldb); err != nil {
		return nil, nil, err
	}
	index, err := search.New(ctx, sqldb)
	if err != nil {
		return nil, nil, err
	}
	if err := tasks.Migrate(ctx, sqldb); err != nil {
		return nil, nil, err
	}
	// the copy's own language and zone, as the core would have them; the flags take their place when given
	language, zone, err := instance.StoredLocale(ctx, sqldb, c.language, c.zone)
	if err != nil {
		return nil, nil, err
	}
	loc, err := tasks.LoadZone(zone)
	if err != nil {
		return nil, nil, err
	}
	store := memory.New(sqldb, index)
	if language == "Italian" {
		store.Areas = memory.AreasIT
	}
	book := tasks.NewBook(store, sqldb, "owner", loc, "owner")
	if err := projects.Migrate(ctx, sqldb); err != nil {
		return nil, nil, err
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, nil, err
	}
	cfg := mcp.Config{
		Memory: store, Search: index, Tasks: book, Ops: &mcp.Ops{Projects: projects.New(sqldb)},
		Mask: v.Mask, Token: hex.EncodeToString(token), Language: language, Zone: loc, Now: now,
	}
	if vecs != nil {
		cfg.Embedder = vecs
	}
	srv, err := mcp.New(cfg)
	if err != nil {
		return nil, nil, err
	}
	ok = true
	handler := srv.Handler()
	// the request carries the in-process token; the caller never sees it
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+cfg.Token)
		handler.ServeHTTP(w, r)
	}), func() { sqldb.Close() }, nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ask puts one question to the memory_recall tool, as a client's tools/call, and scores the answer. The tool is what
// answers, so its fallback (the search's nearest three when recall finds nothing relevant) is in the answer, and
// scored as «nearest» after the pages.
func ask(h http.Handler, q recalleval.Question, mode string, c config) Asked {
	args := protocol.MemoryRecall{Query: q.Q, Mode: mode, Budget: c.budget}
	if mode == recalleval.ModeSearch {
		args.Limit = c.limit
	}
	text, err := callTool(h, protocol.ToolMemoryRecall, args)
	if err != nil {
		return Asked{Outcome: recalleval.Outcome{Q: q.Q, Mode: mode, Found: []recalleval.Found{}, Err: err.Error()}}
	}
	var got struct {
		Pages, Related, Nearest, Results []answered
		Meaning                          bool
		Notes                            []string
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		return Asked{Outcome: recalleval.Outcome{Q: q.Q, Mode: mode, Found: []recalleval.Found{}, Err: "unreadable answer: " + err.Error()}}
	}
	a := recalleval.Answer{Pages: slots(got.Pages), Related: slots(got.Related), Nearest: slots(got.Nearest)}
	if mode == recalleval.ModeSearch {
		a = recalleval.Answer{Pages: slots(got.Results)}
	}
	return Asked{Outcome: recalleval.Score(q, mode, a), Meaning: got.Meaning, Notes: got.Notes}
}

// answered is one entry of an answer: a page has a path, a document of a collection only its id.
type answered struct {
	Path     string `json:"path"`
	Document string `json:"document"`
}

// slots names each entry of an answer in its order. A document takes its place as doc:<id>, the name the index gives
// it: search's limit counts it, so leaving it out would rank the pages after it higher than the tool shows them.
func slots(r []answered) []string {
	var p []string
	for _, x := range r {
		switch {
		case x.Path != "":
			p = append(p, x.Path)
		case x.Document != "":
			p = append(p, "doc:"+x.Document)
		}
	}
	return p
}

// callTool is a tools/call on the connector's handler, in process. It returns the text of the tool's answer.
func callTool(h http.Handler, name string, args any) (string, error) {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args}})
	if err != nil {
		return "", err
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return "", fmt.Errorf("the connector answered HTTP %d", rec.Code)
	}
	var rpc struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rpc); err != nil {
		return "", fmt.Errorf("unreadable answer: %w", err)
	}
	if rpc.Error != nil {
		return "", errors.New(rpc.Error.Message)
	}
	if len(rpc.Result.Content) == 0 {
		return "", errors.New("the tool answered nothing")
	}
	if rpc.Result.IsError {
		return "", errors.New(rpc.Result.Content[0].Text)
	}
	return rpc.Result.Content[0].Text, nil
}

// vectorSet is a search.Embedder over stored vectors: the vector of the exact text, an error for any other. A text it
// lacks is remembered, so that the run can say meaning was left out for it.
type vectorSet struct {
	model  string
	byText map[string][]float32

	mu   sync.Mutex
	miss map[string]bool
}

func readVectors(path, model string) (*vectorSet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v := &vectorSet{model: model, miss: map[string]bool{}}
	if err := json.Unmarshal(raw, &v.byText); err != nil {
		return nil, fmt.Errorf("%s: not a {query: [numbers]} map: %w", path, err)
	}
	for q, vec := range v.byText {
		if len(vec) == 0 {
			return nil, fmt.Errorf("%s: the vector of %q is empty", path, q)
		}
	}
	return v, nil
}

func (v *vectorSet) Model() string { return v.model }

func (v *vectorSet) Embed(_ context.Context, input []string) ([][]float32, error) {
	out := make([][]float32, len(input))
	for i, text := range input {
		vec, ok := v.byText[text]
		if !ok {
			v.mu.Lock()
			v.miss[text] = true
			v.mu.Unlock()
			return nil, fmt.Errorf("no stored vector for %q", text)
		}
		out[i] = vec
	}
	return out, nil
}

func (v *vectorSet) missing() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []string
	for t := range v.miss {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// makeVectors asks the embedding server once per question and writes the vectors file the runs read. Nothing else
// ever calls a model.
func makeVectors(ctx context.Context, c config, qs []recalleval.Question, out io.Writer) error {
	if c.vectors == "" || c.embedURL == "" || c.embedModel == "" {
		return errors.New("-make-vectors needs -vectors (the file to write), -embed-url and -embed-model")
	}
	e := search.NewOpenAIEmbedder(search.EmbedConfig{URL: c.embedURL, Model: c.embedModel})
	vecs := map[string][]float32{}
	for _, q := range qs {
		if _, done := vecs[q.Q]; done {
			continue
		}
		got, err := e.Embed(ctx, []string{q.Q})
		if err != nil {
			return fmt.Errorf("embedding %q: %w", q.Q, err)
		}
		vecs[q.Q] = got[0]
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(vecs); err != nil {
		return err
	}
	if err := os.WriteFile(c.vectors, buf.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "%d vectors of model %s written to %s\n", len(vecs), c.embedModel, c.vectors)
	return nil
}
