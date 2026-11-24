package mcp

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kedalio/core/db"
	"kedalio/core/memory"
	"kedalio/core/memory/search"
	"kedalio/core/projects"
	"kedalio/core/tasks"
	"kedalio/core/vault"
)

// newOpsRig is newRig with the session and account tools on.
func newOpsRig(t *testing.T) *rig {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for _, m := range []func() error{
		func() error { return memory.Migrate(ctx, d) },
		func() error { return tasks.Migrate(ctx, d) },
		func() error { return projects.Migrate(ctx, d) },
		func() error { return vault.Migrate(ctx, d) },
	} {
		if err := m(); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := search.New(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	store := memory.New(d, ix)
	store.Areas = memory.AreasEN
	book := tasks.NewBook(store, d, "owner", time.UTC, "owner")
	srv, err := New(Config{Memory: store, Search: ix, Tasks: book,
		Token: testToken, Zone: time.UTC, Owner: "Alice", Language: "English",
		Ops: &Ops{Projects: projects.New(d)}})
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(srv.Handler())
	t.Cleanup(h.Close)
	return &rig{srv, h, store}
}

func TestOpsToolsAreOfferedOnlyWithOps(t *testing.T) {
	s := newOpsRig(t).session(t)
	res, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range res.Tools {
		have[tl.Name] = true
	}
	for _, n := range []string{"session_start", "projects_link", "settings_get", "settings_set"} {
		if !have[n] {
			t.Errorf("%s is not offered", n)
		}
	}
	if len(res.Tools) != 15 {
		t.Errorf("%d tools", len(res.Tools))
	}
}

func TestSessionStartAsksThenKnowsTheFolder(t *testing.T) {
	r := newOpsRig(t)
	s := r.session(t)
	dir := "/home/alice/work/acme/site"
	out := mustCall(t, s, "session_start", map[string]any{"dir": dir, "remote": "git@git.example.com:acme/site.git"})
	ask := out["folder"].(map[string]any)["ask"].(map[string]any)
	if p := ask["proposal"].(map[string]any); p["path"] != "work/acme/site" || p["exists"] != false || p["why"] != "path" {
		t.Fatalf("proposal %v", p)
	}
	if out["project"] != nil || out["card"] != nil {
		t.Errorf("a project with no folder: %v", out)
	}

	link := mustCall(t, s, "projects_link", map[string]any{"op": "create", "dir": dir, "name": "work/acme/site",
		"repo": "git@git.example.com:acme/site.git", "branch": "main"})
	if link["project"] != "work/acme/site" || link["created"] != true || link["repo"] != "added" ||
		link["needs"].([]any)[0] != "environments" {
		t.Fatalf("link %v", link)
	}
	// the same answer again is a no-op that still answers
	if again := mustCall(t, s, "projects_link", map[string]any{"op": "link", "dir": dir, "project": "work/acme/site"}); again["created"] != false {
		t.Errorf("again %v", again)
	}

	// the card, its tasks and its prefs come with the start
	body := "# work/acme/site\n\nThe shop's site.\n\n## Last time\n\nFixed the cart.\n\n## Next\n\nSITE-2.\n"
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "card": "work/acme/site", "body": body, "base_rev": 0})
	mustCall(t, s, "tasks_save", map[string]any{"title": "Pay the invoice", "project": "work/acme/site", "due": "2020-01-01"})
	mustCall(t, s, "tasks_save", map[string]any{"title": "Renew the domain", "project": "work/other", "due": "2020-01-01", "priority": 1})
	mustCall(t, s, "settings_set", map[string]any{"key": "project.work/acme/site.session", "value": map[string]any{"mode": "work", "start": []any{"check CI"}}})

	for _, args := range []map[string]any{
		{"dir": dir + "/src"},
		{"remote": "https://git.example.com/acme/site"},
	} {
		got := mustCall(t, s, "session_start", args)
		if got["project"] != "work/acme/site" || got["folder"] != "known" {
			t.Fatalf("%v: %v", args, got)
		}
		if c := got["card"].(map[string]any); !strings.Contains(c["text"].(string), "Fixed the cart") || c["rev"].(float64) != 1 {
			t.Errorf("card %v", c)
		}
		if o := got["own"].(map[string]any)["overdue"].([]any); len(o) != 1 || !strings.Contains(o[0].(string), "Pay the invoice") {
			t.Errorf("own %v", got["own"])
		}
		if u := got["urgent"].([]any); len(u) != 1 || !strings.HasPrefix(u[0].(string), "work/other: ") {
			t.Errorf("urgent %v", u)
		}
		if p := got["prefs"].(map[string]any); p["mode"] != "work" {
			t.Errorf("prefs %v", p)
		}
		if got["pending_onboarding"].([]any)[0] != "environments" {
			t.Errorf("pending %v", got["pending_onboarding"])
		}
	}
}

func TestSessionStartLeavesOutWhatIsEmptyAndGivesTheDay(t *testing.T) {
	s := newOpsRig(t).session(t)
	got := mustCall(t, s, "session_start", map[string]any{})
	for _, k := range []string{"today", "weekday", "project", "folder", "card", "own", "urgent", "inbox"} {
		if _, ok := got[k]; !ok {
			t.Errorf("%q is missing", k)
		}
	}
	for _, k := range []string{"missed", "memory_stale", "prefs", "pending_onboarding"} {
		if v, ok := got[k]; ok {
			t.Errorf("%q should be left out, got %v", k, v)
		}
	}
}

func TestProjectsLinkIgnoreRefusalsAndInbox(t *testing.T) {
	s := newOpsRig(t).session(t)
	mustCall(t, s, "projects_link", map[string]any{"op": "ignore", "dir": "/home/alice/work/tmp"})
	if got := mustCall(t, s, "session_start", map[string]any{"dir": "/home/alice/work/tmp"}); got["folder"] != "ignored" {
		t.Errorf("folder %v", got["folder"])
	}
	if got := mustCall(t, s, "session_start", map[string]any{"dir": "/home/alice"}); got["folder"] != "none" {
		t.Errorf("a home folder is not asked about: %v", got["folder"])
	}
	mustCall(t, s, "projects_link", map[string]any{"op": "create", "dir": "/h/work/a", "name": "work/a"})
	for name, args := range map[string]map[string]any{
		"another project's folder": {"op": "create", "dir": "/h/work/a/sub", "name": "work/b"},
		"a project that is not":    {"op": "link", "dir": "/h/work/c", "project": "work/nope"},
		"no path":                  {"op": "link", "dir": "/h/work/c"},
		"a bad path":               {"op": "create", "dir": "/h/work/c", "name": "Not A Path!"},
	} {
		if out, failed := call(t, s, "projects_link", args); !failed {
			t.Errorf("%s: %v", name, out)
		}
	}
	mustCall(t, s, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "call the bank"})
	if got := mustCall(t, s, "session_start", map[string]any{}); got["inbox"].(map[string]any)["count"].(float64) != 1 {
		t.Errorf("inbox %v", got["inbox"])
	}
}

func TestSettingsKeysAndRefusals(t *testing.T) {
	s := newOpsRig(t).session(t)
	keys := mustCall(t, s, "settings_get", map[string]any{})["keys"].(map[string]any)
	if _, ok := keys["project.<path>.session"]; !ok || keys["tasks.stalled"] == nil {
		t.Errorf("keys %v", keys)
	}
	got := mustCall(t, s, "settings_set", map[string]any{"key": "tasks.stalled", "value": map[string]any{"doingDays": 3}})
	if got["value"].(map[string]any)["doingDays"].(float64) != 3 {
		t.Errorf("stalled %v", got)
	}
	// a setting is inherited down the path and says where from
	mustCall(t, s, "settings_set", map[string]any{"key": "project.work/acme.session", "value": map[string]any{"end": []any{"update the changelog"}}})
	inh := mustCall(t, s, "settings_get", map[string]any{"key": "project.work/acme/site.session"})
	if inh["origin"] != "work/acme" {
		t.Errorf("inherited %v", inh)
	}
	mustCall(t, s, "settings_set", map[string]any{"key": "project.work/acme.session", "value": nil})
	if back := mustCall(t, s, "settings_get", map[string]any{"key": "project.work/acme/site.session"}); back["origin"] != "default" {
		t.Errorf("cleared %v", back)
	}
	mustCall(t, s, "projects_link", map[string]any{"op": "create", "dir": "/h/work/z", "name": "work/z", "repo": "https://git.example.com/z/z"})
	mustCall(t, s, "settings_set", map[string]any{"key": "project.work/z.base", "value": "develop"})
	if b := mustCall(t, s, "settings_get", map[string]any{"key": "project.work/z.base"}); b["value"] != "develop" {
		t.Errorf("base %v", b)
	}
	for name, args := range map[string]map[string]any{
		"an unknown key":     {"key": "agent.name", "value": "x"},
		"a bad mode":         {"key": "project.work/z.session", "value": map[string]any{"mode": "play"}},
		"an unknown field":   {"key": "project.work/z.session", "value": map[string]any{"colour": "red"}},
		"too many steps":     {"key": "project.work/z.session", "value": map[string]any{"start": []any{"1", "2", "3", "4", "5", "6", "7", "8", "9"}}},
		"a bad environment":  {"key": "project.work/z.environments", "value": []any{map[string]any{"name": "live", "kind": "staging"}}},
		"days out of range":  {"key": "tasks.stalled", "value": map[string]any{"todoDays": 400}},
		"a repo not on http": {"key": "project.work/z.repo", "value": "git@git.example.com:z/z.git"},
	} {
		if out, failed := call(t, s, "settings_set", args); !failed {
			t.Errorf("%s: %v", name, out)
		}
	}
	if _, failed := call(t, s, "settings_set", map[string]any{"key": "project.work/z.session"}); !failed {
		t.Error("a missing value")
	}
}
