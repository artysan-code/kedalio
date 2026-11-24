package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kedalio/core/db"
	"kedalio/core/memory"
	"kedalio/core/memory/search"
	"kedalio/core/tasks"
)

const testToken = "test-token-not-a-secret"

var ctx = context.Background()

type rig struct {
	srv   *Server
	http  *httptest.Server
	store *memory.Store
}

// newRig is a server over a fresh in-memory database, behind an httptest server.
func newRig(t *testing.T, mask func(string) string) *rig { return newRigWith(t, mask, nil) }

// newRigWith lets a test add to the server's configuration.
func newRigWith(t *testing.T, mask func(string) string, tune func(*Config)) *rig {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
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
	store := memory.New(d, ix)
	store.Areas = memory.AreasEN
	book := tasks.NewBook(store, d, "owner", time.UTC, "owner")
	cfg := Config{Memory: store, Search: ix, Tasks: book,
		Token: testToken, Mask: mask, Zone: time.UTC, Owner: "Alice", Language: "English"}
	if tune != nil {
		tune(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(srv.Handler())
	t.Cleanup(h.Close)
	return &rig{srv, h, store}
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func (r *rig) connect(t *testing.T, token string) (*sdk.ClientSession, error) {
	t.Helper()
	c := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	return c.Connect(ctx, &sdk.StreamableClientTransport{
		Endpoint: r.http.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{token}}, DisableStandaloneSSE: true,
	}, nil)
}

func (r *rig) session(t *testing.T) *sdk.ClientSession {
	t.Helper()
	s, err := r.connect(t, testToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// call runs a tool and returns its text answer, decoded when it is JSON; failed says whether the call was refused.
func call(t *testing.T, s *sdk.ClientSession, name string, args map[string]any) (out map[string]any, failed bool) {
	t.Helper()
	res, err := s.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	text := res.Content[0].(*sdk.TextContent).Text
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s answered %q: %v", name, text, err)
	}
	return out, res.IsError
}

func mustCall(t *testing.T, s *sdk.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	out, failed := call(t, s, name, args)
	if failed {
		t.Fatalf("%s refused: %v", name, out)
	}
	return out
}

func TestToolsListNamesAndRequiredArguments(t *testing.T) {
	s := newRig(t, nil).session(t)
	res, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	required := map[string][]string{}
	for _, tl := range res.Tools {
		m := tl.InputSchema.(map[string]any)
		var req []string
		rs, _ := m["required"].([]any)
		for _, r := range rs {
			req = append(req, r.(string))
		}
		required[tl.Name] = req
		if m["type"] != "object" {
			t.Errorf("%s: schema type %v", tl.Name, m["type"])
		}
	}
	want := map[string][]string{
		"memory_recall": nil, "memory_read": nil, "memory_list": nil, "memory_write": {"op"}, "memory_delete": {"what"},
		"tasks_brief": nil, "tasks_list": nil, "tasks_get": {"id"}, "tasks_save": nil,
		"memory_sources": {"op"}, "memory_upkeep": {"op"},
	}
	if len(required) != len(want) {
		t.Errorf("tools = %v", required)
	}
	for name, req := range want {
		got, ok := required[name]
		if !ok || !slices.Equal(got, req) {
			t.Errorf("%s: required %v (listed %v), want %v", name, got, ok, req)
		}
	}
}

func TestInitializeTellsTheInstructions(t *testing.T) {
	s := newRig(t, nil).session(t)
	ins := s.InitializeResult().Instructions
	if !strings.Contains(ins, "Alice's memory") || !strings.Contains(ins, "Answer in English") || len(ins) > 700 {
		t.Errorf("instructions = %q", ins)
	}
}

func TestInitializeSaysWhichKedalioItIs(t *testing.T) {
	plain := newRig(t, nil).session(t).InitializeResult()
	if !strings.HasPrefix(plain.Instructions, "This is the Go core of Kedalio. ") || len(plain.Instructions) > 512 {
		t.Errorf("instructions = %q (%d)", plain.Instructions, len(plain.Instructions))
	}
	named := newRigWith(t, nil, func(c *Config) { c.PublicURL = "https://go.example.com/"; c.Label = "trial" }).session(t).InitializeResult()
	want := "the Go core of Kedalio at go.example.com (trial)"
	if !strings.HasPrefix(named.Instructions, "This is "+want+". ") || !strings.Contains(named.ServerInfo.Title, want) {
		t.Errorf("instructions = %q, title = %q", named.Instructions, named.ServerInfo.Title)
	}
	if named.ServerInfo.Name == "kedalio" {
		t.Errorf("the server's name is the Deno connector's: %q", named.ServerInfo.Name)
	}
}

func TestMemoryWriteThenReadAndRecall(t *testing.T) {
	s := newRig(t, nil).session(t)
	body := "# Espresso\n\nHow the owner takes coffee.\n\nStrong espresso, no sugar, in the morning.\n"
	w := mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "Notes/Espresso.md", "body": body, "base_rev": 0})
	if w["written"] != "notes/espresso.md" || w["rev"] != float64(1) {
		t.Fatalf("write = %v", w)
	}
	r := mustCall(t, s, "memory_read", map[string]any{"path": "notes/espresso.md"})
	if !strings.HasSuffix(r["body"].(string), body) || r["rev"] != float64(1) || r["title"] != "Espresso" {
		t.Fatalf("read = %v", r)
	}
	if k, _ := r["kind"].(string); k != "concept" {
		t.Errorf("kind = %v", r["kind"])
	}
	rec := mustCall(t, s, "memory_recall", map[string]any{"query": "espresso"})
	pages := rec["pages"].([]any)
	if len(pages) == 0 || pages[0].(map[string]any)["path"] != "notes/espresso.md" {
		t.Fatalf("recall = %v", rec)
	}
	found := mustCall(t, s, "memory_recall", map[string]any{"query": "espresso", "mode": "search"})
	if res := found["results"].([]any); len(res) != 1 || res[0].(map[string]any)["path"] != "notes/espresso.md" {
		t.Fatalf("search = %v", found)
	}

	// a stale base is an error the writer reads, the rules' refusals are an answer
	if out, failed := call(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/espresso.md", "body": body, "base_rev": 0}); !failed ||
		!strings.Contains(out["errors"].([]any)[0].(map[string]any)["problem"].(string), "changed meanwhile") {
		t.Errorf("stale write = %v, %v", out, failed)
	}
	ref := mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/bad.md", "body": "no title here"})
	if ref["refused"] != true || len(ref["errors"].([]any)) == 0 {
		t.Errorf("rules = %v", ref)
	}
	if ref := mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "tasks/t-1.md", "body": body}); ref["refused"] != true {
		t.Errorf("tasks/ page = %v", ref)
	}

	e := mustCall(t, s, "memory_write", map[string]any{"op": "edit", "path": "notes/espresso.md", "find": "no sugar", "replace": "one sugar"})
	if e["edited"] != "notes/espresso.md" || e["rev"] != float64(2) {
		t.Errorf("edit = %v", e)
	}
	list := mustCall(t, s, "memory_list", map[string]any{"folder": "notes"})
	if docs := list["documents"].([]any); len(docs) != 1 || list["folder"] != "notes/" {
		t.Errorf("list = %v", list)
	}
	h := mustCall(t, s, "memory_read", map[string]any{"path": "notes/espresso.md", "history": true})
	if len(h["versions"].([]any)) != 2 {
		t.Errorf("history = %v", h)
	}
	mustCall(t, s, "memory_write", map[string]any{"op": "restore", "path": "notes/espresso.md", "rev": 1})
	if r := mustCall(t, s, "memory_read", map[string]any{"path": "notes/espresso.md"}); !strings.HasSuffix(r["body"].(string), body) || r["rev"] != float64(3) {
		t.Errorf("restored = %v", r)
	}
	mustCall(t, s, "memory_delete", map[string]any{"what": "page", "path": "notes/espresso.md"})
	if _, failed := call(t, s, "memory_read", map[string]any{"path": "notes/espresso.md"}); !failed {
		t.Error("a removed page is read")
	}
	if out, failed := call(t, s, "memory_delete", map[string]any{"what": "collection", "id": "x"}); failed || out["refused"] != true {
		t.Errorf("a collection that is not there was deleted: %v", out)
	}
}

func TestMemoryAppendRememberMoveAndCard(t *testing.T) {
	s := newRig(t, nil).session(t)
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "people/bobby.md", "base_rev": 0,
		"body": "# Bobby\n\nA friend of the owner.\n"})
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/trip.md", "base_rev": 0,
		"body": "# Trip\n\nA trip with [[people/bobby]].\n"})

	a := mustCall(t, s, "memory_write", map[string]any{"op": "append", "text": "Planned the trip with [[people/bobby]]"})
	day := time.Now().UTC().Format("2006-01-02")
	if a["added"] != "diary/"+day+".md" {
		t.Fatalf("append = %v", a)
	}
	d := mustCall(t, s, "memory_read", map[string]any{"path": "diary/" + day + ".md"})
	if body := d["body"].(string); !strings.Contains(body, "# "+day+"\n\nWhat happened on ") || !strings.Contains(body, " Planned the trip") {
		t.Errorf("diary = %q", body)
	}
	long := strings.Repeat("word ", 45)
	if r := mustCall(t, s, "memory_write", map[string]any{"op": "append", "text": long}); r["refused"] != true {
		t.Errorf("a long diary line = %v", r)
	}
	if in := mustCall(t, s, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "ask about the visa"}); in["added"] != "inbox/inbox.md" {
		t.Errorf("inbox = %v", in)
	}

	rem := mustCall(t, s, "memory_write", map[string]any{"op": "remember", "text": "Bobby likes tea"})
	if rem["how"] != "named" || rem["remembered"] != "people/bobby.md" {
		t.Errorf("remember named = %v", rem)
	}
	if rem := mustCall(t, s, "memory_write", map[string]any{"op": "remember", "text": "something nobody names"}); rem["how"] != "inbox" {
		t.Errorf("remember inbox = %v", rem)
	}
	if rem := mustCall(t, s, "memory_write", map[string]any{"op": "remember", "text": "Carol moved to Turin", "about": "Carol"}); rem["how"] != "new" || rem["remembered"] != "carol.md" {
		t.Errorf("remember new = %v", rem)
	}

	mv := mustCall(t, s, "memory_write", map[string]any{"op": "move", "from": "people/bobby.md", "to": "people/roberto.md"})
	if mv["to"] != "people/roberto.md" || len(mv["links_updated"].([]any)) != 2 /* the trip and the diary */ {
		t.Fatalf("move = %v", mv)
	}
	if r := mustCall(t, s, "memory_read", map[string]any{"path": "notes/trip.md"}); !strings.Contains(r["body"].(string), "[[people/roberto]]") {
		t.Errorf("link not followed: %v", r["body"])
	}

	// a project's card: created with base_rev 0, replaced with the rev read
	if out, failed := call(t, s, "memory_write", map[string]any{"op": "write", "card": "work/site", "body": "# Site\n\nCard.\n"}); !failed {
		t.Errorf("a card without base_rev = %v", out)
	}
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "card": "work/site", "body": "# Site\n\nWhere we were.\n", "base_rev": 0})
	if c := mustCall(t, s, "memory_read", map[string]any{"card": "work/site"}); c["path"] != "projects/work/site/card.md" ||
		!strings.Contains(c["body"].(string), "is: card") {
		t.Errorf("card = %v", c)
	}
	// a project with no card never gets another project's, though a page named card exists
	if out, failed := call(t, s, "memory_read", map[string]any{"card": "work/other"}); !failed {
		t.Errorf("the card of a project with none = %v", out)
	}
}

func TestTasksAddGetListBrief(t *testing.T) {
	s := newRig(t, nil).session(t)
	today := time.Now().UTC().Format("2006-01-02")
	add := mustCall(t, s, "tasks_save", map[string]any{"title": "Buy beans", "due": today, "notes": "The dark roast.", "project": "home/errands"})
	id := add["id"].(string)
	if !strings.HasPrefix(id, "t-") || add["added"] == "" || add["task"] != nil || add["notes"] != nil || add["title"] != "Buy beans" {
		t.Fatalf("add = %v", add)
	}
	got := mustCall(t, s, "tasks_get", map[string]any{"id": id})
	if got["task"].(map[string]any)["title"] != "Buy beans" {
		t.Errorf("get = %v", got)
	}
	list := mustCall(t, s, "tasks_list", map[string]any{"text": "beans"})
	if list["count"] != float64(1) {
		t.Errorf("list = %v", list)
	}
	brief := mustCall(t, s, "tasks_brief", nil)
	if b, _ := json.Marshal(brief); !strings.Contains(string(b), "#"+id) {
		t.Errorf("brief does not hold %s: %v", id, brief)
	}
	if v := mustCall(t, s, "tasks_list", map[string]any{"views": true}); v["views"] == nil {
		t.Errorf("views = %v", v)
	}

	// done closes it; null clears a field
	mustCall(t, s, "tasks_save", map[string]any{"id": id, "due": nil, "log": "bought"})
	if g := mustCall(t, s, "tasks_get", map[string]any{"id": id})["task"].(map[string]any); g["due"] != nil {
		t.Errorf("due not cleared: %v", g["due"])
	}
	mustCall(t, s, "tasks_save", map[string]any{"id": id, "status": "done"})
	if l := mustCall(t, s, "tasks_list", map[string]any{}); l["count"] != float64(0) {
		t.Errorf("open tasks after done = %v", l)
	}
	if out, failed := call(t, s, "tasks_save", map[string]any{}); !failed {
		t.Errorf("an empty save = %v", out)
	}
	// closing a task leaves its line in the diary
	day := mustCall(t, s, "memory_read", map[string]any{"path": "diary/" + today + ".md"})
	if !strings.Contains(day["body"].(string), id) {
		t.Errorf("diary has no line for the closed task: %v", day["body"])
	}
}

func TestArgumentsAreCheckedInTheToolsOwnWords(t *testing.T) {
	s := newRig(t, nil).session(t)
	cases := []struct {
		tool  string
		args  map[string]any
		field string
	}{
		{"memory_recall", map[string]any{"budget": 5}, "budget"},
		{"memory_recall", map[string]any{"mode": "dream"}, "mode"},
		{"memory_write", map[string]any{}, "op"},
		{"tasks_get", map[string]any{"id": 4}, "id"},
		{"memory_write", map[string]any{"op": "write", "body": "# X\n\nx.\n"}, "path or card"},
		{"memory_recall", map[string]any{"mode": "search"}, "query"},
	}
	for _, c := range cases {
		out, failed := call(t, s, c.tool, c.args)
		if !failed {
			t.Errorf("%s %v was not refused", c.tool, c.args)
			continue
		}
		is := out["errors"].([]any)[0].(map[string]any)
		if is["field"] != c.field || is["problem"] == "" || is["fix"] == "" {
			t.Errorf("%s %v: %v", c.tool, c.args, is)
		}
	}
}

// failures is a stand-in limiter: three wrong tokens, then 429 whoever asks from that address.
type failures struct{ n map[string]int }

func (f *failures) Blocked(k string, _ time.Time) int {
	if f.n[k] >= 3 {
		return 30
	}
	return 0
}
func (f *failures) Take(k string, now time.Time) int { f.n[k]++; return f.Blocked(k, now) }

func TestWrongTokensAreLimitedPerAddressButTheRightTokenAlwaysPasses(t *testing.T) {
	r := newRigWith(t, nil, func(c *Config) {
		c.AuthFailures = &failures{map[string]int{}}
		c.ClientIP = func(r *http.Request) string { return r.Header.Get("X-Test-Ip") }
	})
	post := func(ip, token string) int {
		req, _ := http.NewRequest("POST", r.http.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("X-Test-Ip", ip)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for i := 0; i < 3; i++ {
		if code := post("a", "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d", i, code)
		}
	}
	if code := post("a", "wrong"); code != http.StatusTooManyRequests {
		t.Errorf("a fourth wrong token: %d", code)
	}
	if code := post("a", testToken); code != http.StatusOK {
		t.Errorf("the right token from a blocked address: %d, the owner behind a shared proxy must not be locked out", code)
	}
	if code := post("b", testToken); code != http.StatusOK {
		t.Errorf("another address: %d", code)
	}
}

func TestBearerTokenIsRequired(t *testing.T) {
	r := newRig(t, nil)
	post := func(auth string) int {
		req, _ := http.NewRequest("POST", r.http.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for _, auth := range []string{"", "Bearer wrong", "Bearer ", "Basic " + testToken, testToken, "Bearer " + testToken + "x"} {
		if code := post(auth); code != http.StatusUnauthorized {
			t.Errorf("%q: %d", auth, code)
		}
	}
	if code := post("Bearer " + testToken); code != http.StatusOK {
		t.Errorf("the token: %d", code)
	}
	if _, err := r.connect(t, "wrong"); err == nil {
		t.Error("a client with a wrong token connected")
	}
	if res, err := http.Get(r.http.URL + "/health"); err != nil || res.StatusCode != 200 {
		t.Errorf("health: %v %v", res, err)
	}
	if _, err := New(Config{}); err == nil {
		t.Error("a server without a token")
	}
}

func TestMaskIsAppliedToEveryAnswerAndAGuardsWrites(t *testing.T) {
	mask := func(s string) string { return strings.ReplaceAll(s, "swordfish", "***") }
	r := newRig(t, mask)
	s := r.session(t)
	// a page that got in before the mask knew the word: written to the store directly
	if _, err := r.store.Write(ctx, "notes/pass.md", "# Pass\n\nThe word is swordfish, <b>& more</b>.\n", "test"); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string]map[string]any{
		"memory_read":   {"path": "notes/pass.md"},
		"memory_recall": {"query": "word swordfish"},
		"memory_list":   {"folder": "notes"},
	} {
		res, err := s.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		text := res.Content[0].(*sdk.TextContent).Text
		if strings.Contains(text, "swordfish") {
			t.Errorf("%s leaks: %s", name, text)
		}
		if name == "memory_read" && !strings.Contains(text, "The word is ***, <b>& more</b>.") {
			t.Errorf("%s: %s", name, text)
		}
	}
	// numbers and keys are not touched
	if rd := mustCall(t, s, "memory_read", map[string]any{"path": "notes/pass.md"}); rd["rev"] != float64(1) {
		t.Errorf("rev = %v", rd["rev"])
	}
	// a text the mask would change is a secret: never kept
	if w := mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/new.md", "body": "# New\n\nswordfish.\n"}); w["refused"] != true {
		t.Errorf("secret page = %v", w)
	}
	if w := mustCall(t, s, "memory_write", map[string]any{"op": "remember", "text": "the word is swordfish"}); w["refused"] != true {
		t.Errorf("secret remember = %v", w)
	}
}

func TestSearchTasksExcerptIsAroundTheWordFound(t *testing.T) {
	s := newRig(t, nil).session(t)
	notes := strings.Repeat("filler ", 60) + "needle and the rest"
	mustCall(t, s, "tasks_save", map[string]any{"title": strings.Repeat("T", 80), "ref": strings.Repeat("R", 40), "notes": notes})
	out := mustCall(t, s, "memory_recall", map[string]any{"mode": "search", "query": "needle", "include_tasks": true})
	for _, r := range out["results"].([]any) {
		found := r.(map[string]any)
		if p, _ := found["path"].(string); strings.HasPrefix(p, "tasks/") {
			if !strings.Contains(found["excerpt"].(string), "needle") {
				t.Errorf("excerpt misses the word: %v", found)
			}
			return
		}
	}
	t.Errorf("the task was not found: %v", out)
}

func TestRememberNeverWritesIntoATaskPage(t *testing.T) {
	s := newRig(t, nil).session(t)
	add := mustCall(t, s, "tasks_save", map[string]any{"title": "Replace the espresso grinder"})
	id := add["id"].(string)
	// the text names the task's title, but a task is not a place to remember things
	rem := mustCall(t, s, "memory_write", map[string]any{"op": "remember", "text": "Replace the espresso grinder before March"})
	if rem["how"] != "inbox" {
		t.Errorf("remember on a task title = %v", rem)
	}
	// the task was never a candidate, so the reason is not "the task page would not take it"
	if r, _ := rem["reason"].(string); strings.Contains(r, "tasks/") {
		t.Errorf("reason names the task page: %q", r)
	}
	got := mustCall(t, s, "tasks_get", map[string]any{"id": id})["task"].(map[string]any)
	if n, _ := got["notes"].(string); strings.Contains(n, "March") {
		t.Errorf("the task was written: %v", got)
	}
	// `about` pointing at a task is refused with the way to do it
	if r := mustCall(t, s, "memory_write", map[string]any{"op": "remember", "text": "a fact", "about": "tasks/" + id}); r["refused"] != true {
		t.Errorf("about a task = %v", r)
	}
}

func TestMoveRefusesTheInboxAndDiaryDaysAndMovesToThemToo(t *testing.T) {
	s := newRig(t, nil).session(t)
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/trip.md", "base_rev": 0, "body": "# Trip\n\nA trip.\n"})
	day := time.Now().UTC().Format("2006-01-02")
	mustCall(t, s, "memory_write", map[string]any{"op": "append", "text": "Went out"})
	mustCall(t, s, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "sort me"})
	for _, m := range []map[string]any{
		{"op": "move", "from": "diary/" + day + ".md", "to": "notes/day.md"},
		{"op": "move", "from": "inbox/inbox.md", "to": "notes/inbox.md"},
		{"op": "move", "from": "notes/trip.md", "to": "inbox/trip.md"},
	} {
		if r := mustCall(t, s, "memory_write", m); r["refused"] != true {
			t.Errorf("%v = %v", m, r)
		}
	}
}

func TestRecallThatFindsNothingRelevantStillHandsOverTheSearchsNearest(t *testing.T) {
	s := newRig(t, nil).session(t)
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "note/polizza.md",
		"body": "# Polizza RC\n\nLa polizza di responsabilità professionale.\n\nL'assicurazione obbligatoria dello studio.\n"})
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "note/studio.md",
		"body": "# Studio\n\nLo studio ha una [[note/polizza]] per i clienti.\n"})
	rec := mustCall(t, s, "memory_recall", map[string]any{"query": "la mia assicurazione dell'auto"})
	if len(rec["pages"].([]any)) != 0 || !strings.Contains(fmt.Sprint(rec["notes"]), "nothing relevant") {
		t.Fatalf("recall = %v", rec)
	}
	near, _ := rec["nearest"].([]any)
	if len(near) == 0 || near[0].(map[string]any)["path"] != "note/polizza.md" {
		t.Fatalf("no nearest in %v", rec)
	}
}

func TestMemoryReadCapsItsLinkListsAndCountsTheRest(t *testing.T) {
	s := newRig(t, nil).session(t)
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/hub.md", "body": "# Hub\n\nThe hub.\n", "base_rev": 0})
	for i := range readLinks + 5 {
		mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": fmt.Sprintf("notes/n%02d.md", i),
			"body": fmt.Sprintf("# Note %d\n\nSee [[notes/hub]].\n", i), "base_rev": 0, "distinct": true})
	}
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/zeta.md", "body": "# Zeta\n\nSee [[notes/hub]].\n", "base_rev": 0})
	// by default the backlinks are a count: the hub of a project is linked by hundreds of pages
	def := mustCall(t, s, "memory_read", map[string]any{"path": "notes/hub.md"})
	dl := def["links"].(map[string]any)
	if dl["back"] != float64(readLinks+6) || dl["related"] != float64(0) {
		t.Errorf("default links = %v, want back and related as counts", dl)
	}
	if _, ok := dl["out"].([]any); !ok {
		t.Errorf("default links.out = %v, want the list", dl["out"])
	}
	for _, k := range []string{"created", "by"} {
		if _, ok := def[k]; ok {
			t.Errorf("memory_read carries %q", k)
		}
	}
	if def["rev"] == nil || def["updated"] == nil {
		t.Errorf("rev and updated stay: %v", def)
	}
	r := mustCall(t, s, "memory_read", map[string]any{"path": "notes/hub.md", "links": true})
	links := r["links"].(map[string]any)
	back := links["back"].([]any)
	if len(back) != readLinks {
		t.Fatalf("back has %d entries, want %d", len(back), readLinks)
	}
	if more := links["more"].(map[string]any); more["back"] != float64(6) {
		t.Errorf("more = %v", more)
	}
}
