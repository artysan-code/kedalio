package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kedalio/core/db"
	"kedalio/core/memory"
	"kedalio/core/memory/search"
	"kedalio/core/projects"
	"kedalio/core/tasks"
	"kedalio/core/vault"
)

const testModel = "test-model"

// topics is a made-up embedder: a text's vector says which of three subjects it mentions.
type topics struct{}

func (topics) Model() string { return testModel }
func (topics) Embed(_ context.Context, input []string) ([][]float32, error) {
	out := make([][]float32, len(input))
	for i, t := range input {
		t = strings.ToLower(t)
		switch {
		case strings.Contains(t, "tomatoes") || strings.Contains(t, "greenery"):
			out[i] = []float32{1, 0, 0}
		case strings.Contains(t, "invoices"):
			out[i] = []float32{0, 1, 0}
		default:
			out[i] = []float32{0, 0, 1}
		}
	}
	return out, nil
}

// makeData builds a data folder the way the core does, with three made-up pages and their vectors, and closes it.
func makeData(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	raw, err := vault.HostKey(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	key, err := vault.DatabaseKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.OpenEncrypted(filepath.Join(dir, "core.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := vault.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Open(ctx, d, dir); err != nil {
		t.Fatal(err)
	}
	if err := memory.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	ix, err := search.New(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := tasks.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := projects.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	store := memory.New(d, ix)
	store.Areas = memory.AreasEN
	for path, body := range map[string]string{
		"people/alice.md":  "# Alice\n\nAlice grows tomatoes and roses on her balcony.\n",
		"people/bob.md":    "# Bob\n\nBob keeps the invoices of the example studio.\n",
		"notes/example.md": "# Example\n\nA note about nothing in particular.\n",
	} {
		if _, err := store.Write(ctx, path, body, "owner"); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	for {
		n, err := ix.EmbedPending(ctx, topics{}, 8)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	return dir
}

func write(t *testing.T, path, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), path)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func snapshot(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][32]byte{}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name()] = sha256.Sum256(b)
	}
	return out
}

func evalRun(t *testing.T, args ...string) (stdout string, result Result, raw []byte) {
	t.Helper()
	jsonOut := filepath.Join(t.TempDir(), "out.json")
	var buf bytes.Buffer
	if err := run(append(args, "-json", jsonOut), &buf); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return buf.String(), result, raw
}

func TestRunScoresRecallAndSearchOverACopyAndIsDeterministic(t *testing.T) {
	data := makeData(t)
	before := snapshot(t, data)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	questions := write(t, "q.json", `{"questions":[
		{"q":"greenery","expect":["people/alice.md"],"not":["notes/example.md"]},
		{"q":"invoices","expect":["people/bob.md"]},
		{"q":"greenery","expect":["people/alice.md"],"mode":"search"}]}`)
	vectors := write(t, "v.json", `{"greenery":[1,0,0],"invoices":[0,1,0]}`)
	args := []string{"-data", data, "-questions", questions, "-vectors", vectors, "-embed-model", testModel,
		"-now", "2026-01-02T03:04:05Z"}

	stdout, res, raw1 := evalRun(t, args...)
	_, _, raw2 := evalRun(t, args...)
	if !bytes.Equal(raw1, raw2) {
		t.Errorf("two runs differ:\n%s\n%s", raw1, raw2)
	}
	if !res.NowPinned || res.Now != "2026-01-02T03:04:05Z" || !strings.Contains(stdout, "now: pinned") {
		t.Errorf("now: %+v\n%s", res, stdout)
	}
	if len(res.MissingVectors) != 0 {
		t.Errorf("missing vectors %v", res.MissingVectors)
	}
	// "greenery" is in no page: only the stored vector finds Alice
	if r, s := res.Totals["recall"], res.Totals["search"]; r.Count != 2 || r.HitRate != 1 || s.Count != 3 || s.HitRate != 1 {
		t.Errorf("totals %+v", res.Totals)
	}
	for _, q := range res.Questions {
		if q.Err != "" || !q.Meaning {
			t.Errorf("%s %s: err %q meaning %v", q.Mode, q.Q, q.Err, q.Meaning)
		}
	}

	// without vectors: words only, said so, and a question with no word in common is a miss
	stdout, res, _ = evalRun(t, "-data", data, "-questions", questions, "-mode", "search")
	if !strings.Contains(stdout, "words only") || !strings.Contains(res.Vectors, "words only") || res.NowPinned {
		t.Errorf("words-only run:\n%s", stdout)
	}
	if s := res.Totals["search"]; s.Count != 3 || s.Errors != 0 || s.HitRate != 1.0/3 {
		t.Errorf("words-only search totals %+v", s)
	}

	if !equalSnapshots(before, snapshot(t, data)) {
		t.Error("the data folder was changed")
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("the temporary copy was left behind: %v", left)
	}
}

func equalSnapshots(a, b map[string][32]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestRunReportsAVectorTheFileLacks(t *testing.T) {
	data := makeData(t)
	questions := write(t, "q.json", `{"questions":[{"q":"greenery","expect":["people/alice.md"],"mode":"search"}]}`)
	vectors := write(t, "v.json", `{"something else":[1,0,0]}`)
	_, res, _ := evalRun(t, "-data", data, "-questions", questions, "-vectors", vectors, "-embed-model", testModel)
	if len(res.MissingVectors) != 1 || res.MissingVectors[0] != "greenery" || res.Questions[0].Meaning {
		t.Errorf("%+v", res)
	}
}

func TestRunRefusesWithoutTheKey(t *testing.T) {
	data := makeData(t)
	if err := os.Remove(filepath.Join(data, "kek")); err != nil {
		t.Fatal(err)
	}
	questions := write(t, "q.json", `{"questions":[{"q":"a","expect":["b.md"]}]}`)
	if err := run([]string{"-data", data, "-questions", questions}, &bytes.Buffer{}); err == nil {
		t.Fatal("want an error")
	}
}

func TestMakeVectorsAsksOncePerQuestion(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model string
			Input []string
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		asked = append(asked, in.Input...)
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []item
		for i := range in.Input {
			data = append(data, item{i, []float32{0.5, float32(len(asked))}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	questions := write(t, "q.json", `{"questions":[{"q":"alice","expect":["a.md"]},{"q":"bob","expect":["b.md"]},{"q":"alice","expect":["a.md"],"mode":"search"}]}`)
	out := filepath.Join(t.TempDir(), "v.json")
	if err := run([]string{"-make-vectors", "-questions", questions, "-vectors", out, "-embed-url", srv.URL, "-embed-model", testModel}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 {
		t.Errorf("asked %v, want one call per distinct question", asked)
	}
	v, err := readVectors(out, "m")
	if err != nil || len(v.byText) != 2 || v.byText["bob"][1] != 2 {
		t.Errorf("%v %v", v, err)
	}
}

func TestSlotsKeepADocumentsPlace(t *testing.T) {
	got := slots([]answered{{Path: "a.md"}, {Document: "d1"}, {}, {Path: "b.md"}})
	want := []string{"a.md", "doc:d1", "b.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("slots = %v, want %v", got, want)
	}
}
