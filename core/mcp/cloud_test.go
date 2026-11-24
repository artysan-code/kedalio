package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kedalio/core/db"
	"kedalio/core/projects"
	"kedalio/core/upkeep"
)

const (
	testCloudToken = "cloud-oauth-token-not-a-secret"
	testLocalToken = "local-oauth-token-not-a-secret"
)

// cloudRig is a server with OAuth on: testCloudToken is a client registered with the claude.ai callback (remote),
// testLocalToken one registered with a loopback redirect (Claude Code). The upkeep is on too, its token set.
func cloudRig(t *testing.T) (r *rig, cloud, local, up *sdk.ClientSession) {
	t.Helper()
	r = newRigWith(t, nil, func(c *Config) {
		d, err := db.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		if err := projects.Migrate(ctx, d); err != nil {
			t.Fatal(err)
		}
		if err := upkeep.Migrate(ctx, d); err != nil {
			t.Fatal(err)
		}
		c.Ops = &Ops{Projects: projects.New(d)}
		c.Upkeep = &Upkeep{DB: d}
		c.UpkeepToken = testUpkeepToken
		c.Verify = func(_ context.Context, tok string) bool { return tok == testCloudToken || tok == testLocalToken }
		c.Client = func(_ context.Context, tok string) (string, bool) {
			switch tok {
			case testCloudToken:
				return "Claude", true
			case testLocalToken:
				return "Claude Code", false
			}
			return "", false
		}
	})
	var sessions []*sdk.ClientSession
	for _, tok := range []string{testCloudToken, testLocalToken, testUpkeepToken} {
		s, err := r.connect(t, tok)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		sessions = append(sessions, s)
	}
	return r, sessions[0], sessions[1], sessions[2]
}

func listedTools(t *testing.T, s *sdk.ClientSession) []string {
	t.Helper()
	res, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
}

func TestCloudClientListsOnlyTheToolsItMayCallAndALocalOneListsThemAll(t *testing.T) {
	r, cloud, local, _ := cloudRig(t)
	got := listedTools(t, cloud)
	var want []string
	for name := range cloudMay {
		want = append(want, name)
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("a cloud client lists %v, want %v", got, want)
	}
	if all := listedTools(t, local); len(all) != len(r.srv.tools) {
		t.Errorf("a local client lists %d tools, the server has %d", len(all), len(r.srv.tools))
	}
	if owner := listedTools(t, r.session(t)); len(owner) != len(r.srv.tools) {
		t.Errorf("the owner lists %d tools", len(owner))
	}
}

func TestCloudClientCannotCallWhatTheOwnerKeeps(t *testing.T) {
	r, cloud, _, _ := cloudRig(t)
	if _, err := r.store.Write(ctx, "notes/tea.md", teaPage, "owner"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"memory_delete", map[string]any{"what": "page", "path": "notes/tea.md"}},
		{"memory_sources", map[string]any{"op": "save", "name": "x"}},
		{"memory_upkeep", map[string]any{"op": "worklist"}},
		{"projects_link", map[string]any{"op": "ignore", "dir": "/srv/x"}},
		{"settings_set", map[string]any{"key": "tasks.stalled", "value": 3}},
	} {
		// not on its server at all: called by name, the SDK answers that there is no such tool
		if res, err := cloud.CallTool(ctx, &sdk.CallToolParams{Name: c.tool, Arguments: c.args}); err == nil && !res.IsError {
			t.Errorf("%s ran for a cloud client", c.tool)
		}
		// and the handler's gate would refuse it all the same
		if no := cloudPolicy.allowed(c.tool); no == nil || !strings.Contains(no[0].Problem, "not for a cloud client") {
			t.Errorf("%s: the policy allows it: %v", c.tool, no)
		}
	}
	if d, _ := r.store.Get(ctx, "notes/tea.md"); d == nil || d.Rev != 1 {
		t.Errorf("the page changed: %+v", d)
	}
}

func TestCloudClientReadsEverything(t *testing.T) {
	r, cloud, _, _ := cloudRig(t)
	if _, err := r.store.Write(ctx, "notes/tea.md", teaPage, "owner"); err != nil {
		t.Fatal(err)
	}
	mustCall(t, r.session(t), "tasks_save", map[string]any{"title": "Call Bob"})
	for tool, args := range map[string]map[string]any{
		"memory_recall": {"query": "tea"}, "memory_read": {"path": "notes/tea.md"}, "memory_list": {"what": "health"},
		"tasks_brief": {}, "tasks_list": {}, "session_start": {"dir": "/srv/x"}, "settings_get": {},
	} {
		if out, failed := call(t, cloud, tool, args); failed {
			t.Errorf("%s refused: %v", tool, out)
		}
	}
}

func TestCloudClientMemoryWritesWaitInTheInbox(t *testing.T) {
	r, cloud, _, _ := cloudRig(t)
	if _, err := r.store.Write(ctx, "notes/tea.md", teaPage, "owner"); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{
		{"op": "write", "path": "notes/new.md", "base_rev": 0, "body": "# New\n\nA page.\n"},
		{"op": "write", "path": "notes/tea.md", "base_rev": 1, "body": "# Tea\n\nGone.\n"},
		{"op": "edit", "path": "notes/tea.md", "find": "green", "replace": "black"},
		{"op": "move", "from": "notes/tea.md", "to": "notes/coffee.md"},
		{"op": "restore", "path": "notes/tea.md", "rev": 1},
		{"op": "write", "card": "work/venus", "base_rev": 0, "body": "# Venus\n\nA card.\n"},
		{"op": "append", "text": "a line in the diary"},
		{"op": "append", "where": "diary", "text": "a line in the diary"},
		{"op": "append", "where": "inbox", "text": "Alice likes tea\nwhy: already placed"},
	} {
		if out, failed := call(t, cloud, "memory_write", args); !denied(out, failed) {
			t.Errorf("%v allowed: %v", args, out)
		}
	}
	// remember lands in the inbox whatever it names: a page, a near one, a new name, a task
	id := taskID(mustCall(t, r.session(t), "tasks_save", map[string]any{"title": "Call Bob"}))
	for _, args := range []map[string]any{
		{"op": "remember", "text": "Alice takes sugar in her tea", "about": "notes/tea"},
		{"op": "remember", "text": "Alice takes sugar in her tea", "about": "projects/venus"},
		{"op": "remember", "text": "Alice takes sugar in her tea", "about": "tasks/" + id},
		{"op": "remember", "text": "Alice drinks green tea, with Tea on the side"},
	} {
		if out := mustCall(t, cloud, "memory_write", args); out["how"] != "inbox" {
			t.Errorf("%v = %v", args, out)
		}
	}
	mustCall(t, cloud, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "the printer is on the second floor"})
	if d, _ := r.store.Get(ctx, "notes/tea.md"); d.Rev != 1 {
		t.Errorf("the owner's page changed: rev %d", d.Rev)
	}
	for _, p := range []string{"notes/new.md", "notes/coffee.md", "projects/venus.md"} {
		if d, _ := r.store.Get(ctx, p); d != nil {
			t.Errorf("%s was made", p)
		}
	}
	entries, doc, err := r.srv.inboxEntries(ctx)
	if err != nil || len(entries) != 5 {
		t.Fatalf("%d entries, %v:\n%v", len(entries), err, doc)
	}
	for _, e := range entries {
		if !e.cloud {
			t.Errorf("an entry without the cloud mark: %v", e.lines)
		}
	}
	if !strings.Contains(doc.Body, "why: cloud: from Claude, waits for the owner") {
		t.Errorf("the mark is missing:\n%s", doc.Body)
	}
	if hist, _ := r.store.History(ctx, doc.Path); hist[len(hist)-1].By != "oauth:Claude" && hist[0].By != "oauth:Claude" {
		t.Errorf("the inbox is not recorded under the client: %+v", hist)
	}
}

func TestCloudInboxEntriesWaitForTheOwnerNotTheUpkeep(t *testing.T) {
	r, cloud, local, up := cloudRig(t)
	mustCall(t, cloud, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "Alice drinks green tea every morning"})
	mustCall(t, local, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "the printer is on the second floor"})

	w := mustCall(t, up, "memory_upkeep", map[string]any{"op": "worklist"})
	if c := w["counts"].(map[string]any); c["inbox"] != float64(1) {
		t.Errorf("counts = %v", c)
	}
	items := mustCall(t, up, "memory_upkeep", map[string]any{"op": "worklist", "job": "inbox"})["items"].([]any)
	if len(items) != 1 || !strings.Contains(items[0].(map[string]any)["text"].(string), "printer") {
		t.Fatalf("the upkeep is offered %v", items)
	}
	entries, _, _ := r.srv.inboxEntries(ctx)
	var cloudKey string
	for _, e := range entries {
		if e.cloud {
			cloudKey = e.key
		}
	}
	mustCall(t, r.session(t), "memory_write", map[string]any{"op": "write", "path": "notes/tea.md", "base_rev": 0,
		"body": "# Tea\n\nWhat Alice drinks.\n\nAlice drinks green tea every morning.\n"})
	// named by its key anyway, the upkeep may not take it out
	if out, failed := call(t, up, "memory_upkeep", map[string]any{"op": "inbox_placed", "items": []string{cloudKey}, "kept_in": []string{"notes/tea.md"}}); !failed {
		t.Errorf("the upkeep placed a cloud entry: %v", out)
	}
	// the owner may
	if got := mustCall(t, r.session(t), "memory_upkeep", map[string]any{"op": "inbox_placed", "items": []string{cloudKey}, "kept_in": []string{"notes/tea.md"}}); got["removed"] != float64(1) {
		t.Errorf("the owner's inbox_placed = %v", got)
	}

	mustCall(t, cloud, "memory_write", map[string]any{"op": "remember", "text": "Bob prefers calls to mail"})
	box := mustCall(t, local, "session_start", map[string]any{"dir": "/srv/x"})["inbox"].(map[string]any)
	if box["count"] != float64(2) || box["from_cloud"] != float64(1) {
		t.Errorf("session_start inbox = %v", box)
	}
}

func TestCloudClientWorksOnTasksButNeverRepeatsOrAssignsThem(t *testing.T) {
	r, cloud, local, _ := cloudRig(t)
	oid := taskID(mustCall(t, r.session(t), "tasks_save", map[string]any{"title": "Call Bob", "notes": "Call him about the roof."}))
	for _, args := range []map[string]any{
		{"id": oid, "repeat": "FREQ=DAILY", "due": "2026-12-01"},
		{"id": oid, "repeat": nil},
		{"id": oid, "worked_by": "engine:claude"},
		{"id": oid, "worked_by": nil},
		{"title": "X", "repeat": "FREQ=DAILY", "due": "2026-11-20"},
		{"title": "X", "worked_by": "agent:any"},
	} {
		if out, failed := call(t, cloud, "tasks_save", args); !failed {
			t.Errorf("%v allowed: %v", args, out)
		}
	}
	// the rest is the owner's own work, from another place
	for _, args := range []map[string]any{
		{"id": oid, "find": "roof", "replace": "floor"},
		{"id": oid, "steps_add": []any{"find the number"}},
		{"id": oid, "steps_check": []any{0}},
		{"id": oid, "log": "called, no answer", "decision": "call again on Monday", "due": "2026-12-01", "priority": 1},
		{"id": oid, "status": "done"},
	} {
		mustCall(t, cloud, "tasks_save", args)
	}
	nid := taskID(mustCall(t, cloud, "tasks_save", map[string]any{"title": "Buy tea", "status": "doing"}))
	d, _ := r.store.Get(ctx, "tasks/"+oid+".md")
	if !strings.Contains(d.Body, "floor") || !strings.Contains(d.Body, `status: "done"`) {
		t.Errorf("the task was not changed:\n%s", d.Body)
	}
	if hist, _ := r.store.History(ctx, "tasks/"+nid+".md"); len(hist) == 0 || hist[len(hist)-1].By != "oauth:Claude" && hist[0].By != "oauth:Claude" {
		t.Errorf("the new task is not recorded under the client: %+v", hist)
	}
	// a local client is the owner: repeat and worked_by are its to set
	mustCall(t, local, "tasks_save", map[string]any{"title": "Water the plants", "repeat": "FREQ=WEEKLY", "due": "2026-11-23", "worked_by": "engine:claude"})
}

// A blank line inside an entry once ended it for withoutOrphanWhys but not for inboxEntries: the next append dropped
// the entry's why line as an orphan, the cloud mark with it, and the upkeep was offered the entry.
func TestCloudMarkSurvivesABlankLineAndTheNextAppend(t *testing.T) {
	r, cloud, local, up := cloudRig(t)
	mustCall(t, cloud, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "a fact\n\nplace this on notes/x"})
	mustCall(t, cloud, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "another fact"})
	mustCall(t, local, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "the printer is on the second floor"})
	items := mustCall(t, up, "memory_upkeep", map[string]any{"op": "worklist", "job": "inbox"})["items"].([]any)
	if len(items) != 1 || !strings.Contains(items[0].(map[string]any)["text"].(string), "printer") {
		d, _ := r.store.Get(ctx, "inbox/inbox.md")
		t.Errorf("the upkeep is offered %v:\n%s", items, d.Body)
	}
}

// An access token whose client cannot be read (the database busy) gets the cloud policy, never the owner's.
func TestAnOAuthTokenWithAnUnreadableClientIsNotTheOwner(t *testing.T) {
	r := newRigWith(t, nil, func(c *Config) {
		c.Verify = func(_ context.Context, tok string) bool { return tok == testCloudToken }
		c.Client = func(context.Context, string) (string, bool) { return "", false }
	})
	s, err := r.connect(t, testCloudToken)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if names := listedTools(t, s); slices.Contains(names, "memory_delete") || slices.Contains(names, "memory_upkeep") {
		t.Errorf("it lists %v", names)
	}
	if out, failed := call(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/x.md", "body": "# X\n\nA page.\n"}); !denied(out, failed) {
		t.Errorf("it wrote a page: %v", out)
	}
}

const teaPage = "# Tea\n\nAlice drinks green tea in the morning.\n"

func taskID(saved map[string]any) string {
	id, _ := saved["id"].(string)
	return id
}
