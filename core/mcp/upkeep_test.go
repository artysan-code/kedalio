package mcp

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kedalio/core/db"
	"kedalio/core/memory"
	"kedalio/core/upkeep"
)

const testUpkeepToken = "upkeep-token-not-a-secret-but-long-enough"

// upkeepRig is a server with the upkeep on, its second token set; markWeeks is the switch.
func upkeepRig(t *testing.T, markWeeks bool) (*rig, *sql.DB) {
	t.Helper()
	return upkeepRigIn(t, markWeeks, memory.AreasEN)
}

// upkeepRigIn is upkeepRig on an instance whose areas are the given ones (the Italian names, say).
func upkeepRigIn(t *testing.T, markWeeks bool, areas memory.Areas) (*rig, *sql.DB) {
	return upkeepRigAt(t, markWeeks, areas, time.Time{})
}

// upkeepRigAt pins the clock the weeks job reads (zero: the current time).
func upkeepRigAt(t *testing.T, markWeeks bool, areas memory.Areas, now time.Time) (*rig, *sql.DB) {
	t.Helper()
	var d *sql.DB
	r := newRigWith(t, nil, func(c *Config) {
		c.Now = now
		c.Memory.Areas = areas
		var err error
		if d, err = db.Open(":memory:"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		if err := upkeep.Migrate(ctx, d); err != nil {
			t.Fatal(err)
		}
		c.Upkeep = &Upkeep{DB: d, MarkWeeks: markWeeks}
		c.UpkeepToken = testUpkeepToken
	})
	return r, d
}

func TestUpkeepWorklistCountsAndItems(t *testing.T) {
	r, _ := upkeepRig(t, false)
	s := r.session(t)
	mustCall(t, s, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "Alice drinks green tea every morning"})
	mustCall(t, s, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "the printer is on the second floor"})

	w := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist"})
	counts := w["counts"].(map[string]any)
	if counts["inbox"] != float64(2) || counts["weeks"] != float64(0) || w["next"] != "inbox" {
		t.Fatalf("worklist = %v", w)
	}
	for _, j := range jobs {
		if _, ok := counts[j]; !ok {
			t.Errorf("no count for %s", j)
		}
	}
	items := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist", "job": "inbox"})["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v", items)
	}
	first := items[0].(map[string]any)
	if first["key"] == "" || first["text"] != "Alice drinks green tea every morning" {
		t.Errorf("first item = %v", first)
	}
}

func TestUpkeepStartRefusesWhileOpenAndEndMasksAndCaps(t *testing.T) {
	mask := func(s string) string { return strings.ReplaceAll(s, "hunter2hunter2", "[secret]") }
	r := newRigWith(t, mask, func(c *Config) {
		d, err := db.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		if err := upkeep.Migrate(ctx, d); err != nil {
			t.Fatal(err)
		}
		c.Upkeep = &Upkeep{DB: d}
	})
	s := r.session(t)
	st := mustCall(t, s, "memory_upkeep", map[string]any{"op": "start", "job": "hubs"})
	id, _ := st["run"].(string)
	if id == "" || st["sweep"] == nil || st["items"] == nil {
		t.Fatalf("start = %v", st)
	}
	if out, failed := call(t, s, "memory_upkeep", map[string]any{"op": "start", "job": "logs"}); !failed || !strings.Contains(out["errors"].([]any)[0].(map[string]any)["problem"].(string), id) {
		t.Errorf("second start = %v, failed %v", out, failed)
	}
	if _, failed := call(t, s, "memory_upkeep", map[string]any{"op": "end", "run": id, "status": "ok", "report": strings.Repeat("x", 2001)}); !failed {
		t.Error("a report over 2000 characters was taken")
	}
	mustCall(t, s, "memory_upkeep", map[string]any{"op": "end", "run": id, "status": "partial", "report": "key hunter2hunter2 seen"})
	last, err := upkeep.Last(ctx, r.srv.cfg.Upkeep.DB)
	if err != nil || last.Status != "partial" || last.Report != "key [secret] seen" {
		t.Errorf("last = %+v, %v", last, err)
	}
	mustCall(t, s, "memory_upkeep", map[string]any{"op": "start", "job": "logs"}) // the first one has ended
	if _, failed := call(t, s, "memory_upkeep", map[string]any{"op": "end", "run": "u-nope", "status": "ok"}); !failed {
		t.Error("ended a run that does not exist")
	}
}

func TestUpkeepDismissHidesDuplicatesAndConflicts(t *testing.T) {
	r, d := upkeepRig(t, false)
	s := r.session(t)
	mustCall(t, s, "memory_upkeep", map[string]any{"op": "dismiss", "job": "duplicates", "items": []string{"a.md|b.md"}})
	got, err := upkeep.Dismissed(ctx, d, "duplicates")
	if err != nil || !got["a.md|b.md"] {
		t.Errorf("dismissed = %v, %v", got, err)
	}
	if _, failed := call(t, s, "memory_upkeep", map[string]any{"op": "dismiss", "job": "hubs", "items": []string{"x"}}); !failed {
		t.Error("hubs were dismissed")
	}
}

func TestUpkeepHubsAreLongPagesLongestFirst(t *testing.T) {
	r, _ := upkeepRig(t, false)
	s := r.session(t)
	long := func(n int) string { return "# Hub\n\nA hub page.\n\n" + strings.Repeat("word ", n) + "\n" }
	for path, n := range map[string]int{"notes/short.md": 50, "notes/mid.md": 450, "notes/big.md": 700} {
		if _, err := r.store.Write(ctx, path, long(n), "owner"); err != nil {
			t.Fatal(err)
		}
	}
	items := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist", "job": "hubs"})["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["key"] != "notes/big.md" {
		t.Errorf("hubs = %v", items)
	}
}

func TestUpkeepInboxPlacedRemovesOnlyWhatIsKept(t *testing.T) {
	r, _ := upkeepRig(t, false)
	s := r.session(t)
	mustCall(t, s, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "Alice drinks green tea every morning"})
	mustCall(t, s, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "the printer is on the second floor"})
	items := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist", "job": "inbox"})["items"].([]any)
	tea, printer := items[0].(map[string]any)["key"].(string), items[1].(map[string]any)["key"].(string)
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/tea.md", "base_rev": 0,
		"body": "# Tea\n\nWhat Alice drinks.\n\nAlice drinks green tea every morning.\n"})

	// the printer sentence is on no page: refused, and nothing is removed
	out, failed := call(t, s, "memory_upkeep", map[string]any{"op": "inbox_placed", "items": []string{tea, printer}, "kept_in": []string{"notes/tea.md"}})
	if !failed || !strings.Contains(out["errors"].([]any)[0].(map[string]any)["problem"].(string), "printer") {
		t.Fatalf("inbox_placed = %v, failed %v", out, failed)
	}
	if n, _ := r.srv.inboxCount(ctx); n != 2 {
		t.Fatalf("inbox holds %d entries after a refusal", n)
	}
	got := mustCall(t, s, "memory_upkeep", map[string]any{"op": "inbox_placed", "items": []string{tea}, "kept_in": []string{"notes/tea.md"}})
	if got["removed"] != float64(1) {
		t.Fatalf("inbox_placed = %v", got)
	}
	left := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist", "job": "inbox"})["items"].([]any)
	if len(left) != 1 || left[0].(map[string]any)["key"] != printer {
		t.Errorf("left = %v", left)
	}
	if _, failed := call(t, s, "memory_upkeep", map[string]any{"op": "inbox_placed", "items": []string{printer}, "kept_in": []string{"notes/none.md"}}); !failed {
		t.Error("a page that does not exist was a destination")
	}
}

func TestUpkeepWithoutConfigAnswersAnErrorAndNothingElseChanges(t *testing.T) {
	r := newRig(t, nil) // Upkeep nil, like the recall evaluation
	s := r.session(t)
	out, failed := call(t, s, "memory_upkeep", map[string]any{"op": "worklist"})
	if !failed || !strings.Contains(out["errors"].([]any)[0].(map[string]any)["problem"].(string), "not set up") {
		t.Errorf("worklist = %v, failed %v", out, failed)
	}
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/x.md", "base_rev": 0, "body": "# X\n\nA page about x.\n\nIt is a page.\n"})
}

func TestUpkeepTokenWritesAsUpkeepAndEmptyOrWrongTokensAreRefused(t *testing.T) {
	r, _ := upkeepRig(t, false)
	s, err := r.connect(t, testUpkeepToken)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/x.md", "base_rev": 0, "body": "# X\n\nA page about x.\n\nIt is a page.\n"})
	if d, _ := r.store.Get(ctx, "notes/x.md"); d == nil || d.By != "upkeep" {
		t.Errorf("written by %+v, want upkeep", d)
	}
	mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist"}) // the upkeep token may call it
	mustCall(t, r.session(t), "memory_write", map[string]any{"op": "write", "path": "notes/y.md", "base_rev": 0, "body": "# Y\n\nA page about y.\n\nIt is a page.\n"})
	if d, _ := r.store.Get(ctx, "notes/y.md"); d == nil || d.By != "owner" {
		t.Errorf("owner's write recorded as %+v", d)
	}
	mustCall(t, r.session(t), "memory_upkeep", map[string]any{"op": "worklist"}) // and the owner's
	for _, bad := range []string{"", testUpkeepToken + "x", testUpkeepToken[:len(testUpkeepToken)-1], "other"} {
		if s, err := r.connect(t, bad); err == nil {
			s.Close()
			t.Errorf("token %q was accepted", bad)
		}
	}
}

func TestNoUpkeepTokenSetMeansAnEmptyBearerIsStillRefused(t *testing.T) {
	r := newRig(t, nil)
	if s, err := r.connect(t, ""); err == nil {
		s.Close()
		t.Error("an empty bearer was accepted with no upkeep token set")
	}
	if got := r.srv.by(ctx); got != "owner" {
		t.Errorf("author = %q", got)
	}
}

func TestSessionStartTellsTheLastUpkeep(t *testing.T) {
	r := newOpsRig(t)
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := upkeep.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	s := r.session(t)
	if got := mustCall(t, s, "session_start", map[string]any{}); got["upkeep"] != nil {
		t.Errorf("upkeep without the upkeep set up = %v", got["upkeep"])
	}
	r.srv.cfg.Upkeep = &Upkeep{DB: d}
	if got := mustCall(t, s, "session_start", map[string]any{}); got["upkeep"] != nil {
		t.Errorf("upkeep before any run = %v", got["upkeep"])
	}
	id := mustCall(t, s, "memory_upkeep", map[string]any{"op": "start", "job": "weeks"})["run"].(string)
	open := mustCall(t, s, "session_start", map[string]any{})["upkeep"].(map[string]any)
	if open["status"] != "open" || open["job"] != "weeks" {
		t.Errorf("open run = %v", open)
	}
	mustCall(t, s, "memory_upkeep", map[string]any{"op": "end", "run": id, "status": "ok", "report": "done"})
	done := mustCall(t, s, "session_start", map[string]any{})["upkeep"].(map[string]any)
	if done["status"] != "ok" || done["job"] != "weeks" || done["last"] == "" {
		t.Errorf("ended run = %v", done)
	}
}

// the week marking: a week page with three list items marks the days of its week; off, nothing is marked.
func TestWeekMarkingIsBehindTheSwitch(t *testing.T) {
	for _, on := range []bool{true, false} {
		r, _ := upkeepRig(t, on)
		s := r.session(t)
		for _, day := range []string{"2026-10-05", "2026-10-07", "2026-10-14"} {
			if _, err := r.store.Write(ctx, "diary/"+day+".md", "# "+day+"\n\nWhat happened.\n\n- 10:00 something happened\n", "owner"); err != nil {
				t.Fatal(err)
			}
		}
		due := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist", "job": "weeks"})["items"].([]any)
		if len(due) != 2 || due[0].(map[string]any)["page"] != "diary/weeks/2026-w41.md" {
			t.Fatalf("due weeks = %v", due)
		}
		week := "# 2026-W41\n\nThe week.\n\n- One thing.\n- Another thing.\n"
		mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "diary/weeks/2026-w41.md", "base_rev": 0, "body": week})
		marked := func(day string) string {
			d, _ := r.store.Get(ctx, "diary/"+day+".md")
			return frontOf(d.Body)
		}
		if marked("2026-10-05") != "" {
			t.Errorf("on=%v: marked with two items", on)
		}
		week += "- A third thing.\n"
		d, _ := r.store.Get(ctx, "diary/weeks/2026-w41.md")
		mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "diary/weeks/2026-w41.md", "base_rev": d.Rev, "body": week})
		want := ""
		if on {
			want = "diary/weeks/2026-w41"
		}
		if got := marked("2026-10-05"); got != want || marked("2026-10-07") != want {
			t.Errorf("on=%v: days marked %q, %q, want %q", on, got, marked("2026-10-07"), want)
		}
		if marked("2026-10-14") != "" {
			t.Errorf("on=%v: a day of another week was marked", on)
		}
		left := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist", "job": "weeks"})["items"].([]any)
		// a week page of three items folds its days with the marking off too: the page is newer than they are
		if len(left) != 1 {
			t.Errorf("on=%v: %d weeks left, want 1", on, len(left))
		}
	}
}

// frontOf is the `summarised` value of a page's front matter.
func frontOf(body string) string { return memory.FrontMatter(body)["summarised"] }

// --- the no-lost-sentence check on the upkeep's writes ---

const cooking = "# Cooking\n\nWhat Alice cooks at home.\n\nAlice bakes sourdough bread on Sunday mornings.\n\nShe keeps the starter in the fridge.\n"

func (r *rig) upkeepSession(t *testing.T) *sdk.ClientSession {
	t.Helper()
	s, err := r.connect(t, testUpkeepToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// denied is whether a call was turned away, as an error or as the memory's refusal.
func denied(out map[string]any, failed bool) bool { return failed || out["refused"] == true }

func problemOf(out map[string]any) string {
	switch e := out["errors"].([]any)[0].(type) {
	case string: // the memory's rules answer in sentences
		return e
	default:
		return e.(map[string]any)["problem"].(string)
	}
}

func TestUpkeepWriteThatLosesASentenceIsRefusedUntilItIsKeptElsewhere(t *testing.T) {
	r, _ := upkeepRig(t, false)
	if _, err := r.store.Write(ctx, "notes/cooking.md", cooking, "owner"); err != nil {
		t.Fatal(err)
	}
	up := r.upkeepSession(t)
	slim := "# Cooking\n\nWhat Alice cooks at home.\n\nAlice bakes sourdough bread on Sunday mornings.\n"
	args := map[string]any{"op": "write", "path": "notes/cooking.md", "base_rev": 1, "body": slim}

	out, failed := call(t, up, "memory_write", args)
	if !failed || !strings.Contains(problemOf(out), "starter in the fridge") ||
		!strings.Contains(out["errors"].([]any)[0].(map[string]any)["fix"].(string), "kept_in") {
		t.Fatalf("write = %v, failed %v", out, failed)
	}
	if d, _ := r.store.Get(ctx, "notes/cooking.md"); d.Rev != 1 {
		t.Fatalf("a refused write changed the page: rev %d", d.Rev)
	}
	// the destination does not exist yet: that is refused as well, with the same advice
	args["kept_in"] = []string{"notes/starter.md"}
	if out, failed := call(t, up, "memory_write", args); !failed || !strings.Contains(problemOf(out), "does not exist") {
		t.Fatalf("missing destination = %v", out)
	}
	mustCall(t, up, "memory_write", map[string]any{"op": "write", "path": "notes/starter.md", "base_rev": 0,
		"body": "# Starter\n\nThe sourdough starter.\n\nShe keeps the starter in the fridge.\n"})
	mustCall(t, up, "memory_write", args)
	if d, _ := r.store.Get(ctx, "notes/cooking.md"); d.Rev != 2 || d.By != "upkeep" {
		t.Errorf("page after the kept write: %+v", d)
	}
}

func TestOwnerWritesAreNeverChecked(t *testing.T) {
	r, _ := upkeepRig(t, false)
	if _, err := r.store.Write(ctx, "notes/cooking.md", cooking, "owner"); err != nil {
		t.Fatal(err)
	}
	mustCall(t, r.session(t), "memory_write", map[string]any{"op": "write", "path": "notes/cooking.md", "base_rev": 1,
		"body": "# Cooking\n\nWhat Alice cooks at home.\n\nNothing else is kept here.\n"})
}

func TestUpkeepEditAndDeleteAreCheckedToo(t *testing.T) {
	r, _ := upkeepRig(t, false)
	if _, err := r.store.Write(ctx, "notes/cooking.md", cooking, "owner"); err != nil {
		t.Fatal(err)
	}
	up := r.upkeepSession(t)
	edit := map[string]any{"op": "edit", "path": "notes/cooking.md", "find": "She keeps the starter in the fridge.\n", "replace": ""}
	if out, failed := call(t, up, "memory_write", edit); !failed || !strings.Contains(problemOf(out), "starter") {
		t.Fatalf("edit = %v", out)
	}
	if out, failed := call(t, up, "memory_delete", map[string]any{"what": "page", "path": "notes/cooking.md"}); !failed || !strings.Contains(problemOf(out), "sourdough") {
		t.Fatalf("delete = %v", out)
	}
	if d, _ := r.store.Get(ctx, "notes/cooking.md"); d == nil {
		t.Fatal("a refused delete removed the page")
	}
	mustCall(t, up, "memory_write", map[string]any{"op": "write", "path": "notes/bread.md", "base_rev": 0, "body": cooking[:0] +
		"# Bread\n\nWhat Alice cooks at home.\n\nAlice bakes sourdough bread on Sunday mornings.\n\nShe keeps the starter in the fridge.\n"})
	edit["kept_in"] = []string{"notes/bread.md"}
	mustCall(t, up, "memory_write", edit)
	mustCall(t, up, "memory_delete", map[string]any{"what": "page", "path": "notes/cooking.md", "kept_in": []string{"notes/bread.md"}})
	if d, _ := r.store.Get(ctx, "notes/cooking.md"); d != nil {
		t.Error("the page is still there")
	}
}

func TestUpkeepChecksExemptDiaryDaysAndInboxOnly(t *testing.T) {
	r, _ := upkeepRig(t, false)
	up := context.WithValue(ctx, authorKey{}, upkeepAuthor)
	before := "# X\n\nAlice bakes sourdough bread on Sunday mornings.\n\n- a list item here\n"
	for path, exempt := range map[string]bool{
		"diary/2026-10-05.md": true, "inbox/inbox.md": true, "tasks/t-a1/log.md": false,
		"diary/weeks/2026-w41.md": false, "notes/x.md": false,
	} {
		err := r.srv.survives(up, path, before, "# X\n", nil)
		if exempt != (err == nil) {
			t.Errorf("%s: exempt %v, error %v", path, exempt, err)
		}
		if err := r.srv.survives(ctx, path, before, "# X\n", nil); err != nil {
			t.Errorf("%s: the owner was checked: %v", path, err)
		}
	}
	// a move keeps the body whole, so it needs no check; a restore does not and is the owner's
	if _, err := r.store.Write(ctx, "notes/x.md", before, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.Write(ctx, "notes/x.md", "# X\n\nShort.\n", "owner"); err != nil {
		t.Fatal(err)
	}
	if out, failed := call(t, r.upkeepSession(t), "memory_write", map[string]any{"op": "restore", "path": "notes/x.md", "rev": 1}); !denied(out, failed) || !strings.Contains(problemOf(out), "restore") {
		t.Errorf("a restore by the upkeep = %v", out)
	}
	mustCall(t, r.session(t), "memory_write", map[string]any{"op": "restore", "path": "notes/x.md", "rev": 1})
	mustCall(t, r.upkeepSession(t), "memory_write", map[string]any{"op": "move", "from": "notes/x.md", "to": "notes/y.md"})
}

// --- review fixes ---

func (r *rig) seedCooking(t *testing.T) {
	t.Helper()
	if _, err := r.store.Write(ctx, "notes/cooking.md", cooking, "owner"); err != nil {
		t.Fatal(err)
	}
}

func TestUpkeepKeptInCannotNameThePageItself(t *testing.T) {
	r, _ := upkeepRig(t, false)
	r.seedCooking(t)
	up := r.upkeepSession(t)
	self := []string{"notes/cooking.md"}
	slim := "# Cooking\n\nWhat Alice cooks at home.\n"
	for name, args := range map[string]map[string]any{
		"write":  {"op": "write", "path": "notes/cooking.md", "base_rev": 1, "body": slim, "kept_in": self},
		"edit":   {"op": "edit", "path": "notes/cooking.md", "find": "She keeps the starter in the fridge.\n", "replace": "", "kept_in": self},
		"slug":   {"op": "write", "path": "Notes/Cooking.md", "base_rev": 1, "body": slim, "kept_in": self},
		"editor": {"op": "edit", "path": "notes/cooking.md", "find": "She keeps the starter in the fridge.\n", "replace": "", "kept_in": []string{"Notes/Cooking"}},
	} {
		if out, failed := call(t, up, "memory_write", args); !failed || !strings.Contains(problemOf(out), "page being changed") {
			t.Errorf("%s with kept_in itself = %v, failed %v", name, out, failed)
		}
	}
	if out, failed := call(t, up, "memory_delete", map[string]any{"what": "page", "path": "notes/cooking.md", "kept_in": self}); !failed || !strings.Contains(problemOf(out), "page being changed") {
		t.Errorf("delete with kept_in itself = %v", out)
	}
	if d, _ := r.store.Get(ctx, "notes/cooking.md"); d == nil || d.Rev != 1 {
		t.Fatalf("the page changed: %+v", d)
	}
}

func TestUpkeepKeptInCannotBeTheInboxOrADiaryDay(t *testing.T) {
	r, _ := upkeepRig(t, false)
	r.seedCooking(t)
	own := r.session(t)
	mustCall(t, own, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "She keeps the starter in the fridge."})
	if _, err := r.store.Write(ctx, "diary/2026-10-05.md", "# 2026-10-05\n\n- 09:00 She keeps the starter in the fridge.\n", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.Write(ctx, "diary/weeks/2026-w41.md", "# Week\n\nShe keeps the starter in the fridge.\n", "owner"); err != nil {
		t.Fatal(err)
	}
	up := r.upkeepSession(t)
	edit := map[string]any{"op": "edit", "path": "notes/cooking.md", "find": "She keeps the starter in the fridge.\n", "replace": ""}
	for _, p := range []string{"inbox/inbox.md", "diary/2026-10-05.md"} {
		edit["kept_in"] = []string{p}
		if out, failed := call(t, up, "memory_write", edit); !failed || !strings.Contains(problemOf(out), "only added to") {
			t.Errorf("kept_in %s = %v", p, out)
		}
	}
	edit["kept_in"] = []string{"diary/weeks/2026-w41.md"} // a week page is an ordinary page
	mustCall(t, up, "memory_write", edit)
}

func TestUpkeepInboxPlacedCannotNameTheInbox(t *testing.T) {
	r, _ := upkeepRig(t, false)
	s := r.session(t)
	mustCall(t, s, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "Alice drinks green tea every morning"})
	key := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist", "job": "inbox"})["items"].([]any)[0].(map[string]any)["key"].(string)
	if out, failed := call(t, s, "memory_upkeep", map[string]any{"op": "inbox_placed", "items": []string{key}, "kept_in": []string{"inbox/inbox.md"}}); !failed {
		t.Fatalf("inbox_placed with the inbox as destination = %v", out)
	}
	if n, _ := r.srv.inboxCount(ctx); n != 1 {
		t.Errorf("the entry is gone: %d", n)
	}
}

func TestUpkeepCannotDeleteTheInboxOrADiaryDayNorNonPages(t *testing.T) {
	r, _ := upkeepRig(t, false)
	mustCall(t, r.session(t), "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "Alice drinks green tea every morning"})
	if _, err := r.store.Write(ctx, "diary/2026-10-05.md", "# 2026-10-05\n\n- 09:00 Alice drinks green tea every morning.\n", "owner"); err != nil {
		t.Fatal(err)
	}
	up := r.upkeepSession(t)
	for _, p := range []string{"inbox/inbox.md", "diary/2026-10-05.md"} {
		if out, failed := call(t, up, "memory_delete", map[string]any{"what": "page", "path": p}); !denied(out, failed) || !strings.Contains(problemOf(out), "log") {
			t.Errorf("delete %s = %v", p, out)
		}
		if d, _ := r.store.Get(ctx, p); d == nil {
			t.Errorf("%s was deleted", p)
		}
	}
	for _, what := range []string{"document", "collection"} {
		if _, failed := call(t, up, "memory_delete", map[string]any{"what": what, "id": "x", "collection": "x"}); !failed {
			t.Errorf("the upkeep deleted a %s", what)
		}
	}
	mustCall(t, r.session(t), "memory_delete", map[string]any{"what": "page", "path": "diary/2026-10-05.md"}) // the owner may
}

func TestUpkeepTokenReachesOnlyItsTools(t *testing.T) {
	r, _ := upkeepRig(t, false)
	up := r.upkeepSession(t)
	for _, tool := range []string{"tasks_save", "tasks_brief", "memory_sources"} {
		out, failed := call(t, up, tool, map[string]any{})
		if !failed || !strings.Contains(problemOf(out), "not for the upkeep token") {
			t.Errorf("%s = %v, failed %v", tool, out, failed)
		}
	}
	for tool, args := range map[string]map[string]any{
		"memory_upkeep": {"op": "worklist"}, "memory_recall": {}, "memory_list": {}, "tasks_list": {},
	} {
		if out, failed := call(t, up, tool, args); failed {
			t.Errorf("%s was refused: %v", tool, out)
		}
	}
	if out, failed := call(t, r.session(t), "tasks_brief", map[string]any{}); failed {
		t.Errorf("the owner was refused tasks_brief: %v", out)
	}
}

func TestUpkeepCannotWriteTaskPagesButSumsUpALog(t *testing.T) {
	r, _ := upkeepRig(t, false)
	own := r.session(t)
	task := mustCall(t, own, "tasks_save", map[string]any{"title": "Plan the harvest"})
	id, _ := task["id"].(string)
	if id == "" {
		t.Fatalf("tasks_save = %v", task)
	}
	log := "tasks/" + id + "/log.md"
	body := "# Plan the harvest\n\n## Log\n- 10:00 Counted the crates in the barn\n- 11:00 Called the neighbour about the truck\n"
	if _, err := r.store.Write(ctx, log, body, "owner"); err != nil {
		t.Fatal(err)
	}
	up := r.upkeepSession(t)
	if out, failed := call(t, up, "memory_write", map[string]any{"op": "write", "path": "tasks/" + id + ".md", "body": "# x\n\nA body of words.\n"}); !denied(out, failed) || !strings.Contains(problemOf(out), "tasks") {
		t.Errorf("a task page by the upkeep = %v", out)
	}
	// cutting entries out of the log is checked
	cut := "# Plan the harvest\n\n## Log\n- 10:00 Counted the crates in the barn\n"
	if out, failed := call(t, up, "memory_write", map[string]any{"op": "write", "path": log, "body": cut}); !failed || !strings.Contains(problemOf(out), "neighbour") {
		t.Fatalf("a log cut by the upkeep = %v", out)
	}
	// a summary above the entries, which stay, is how a long log is summed up
	sum := "# Plan the harvest\n\n## Summary\nThe crates are counted and the truck is arranged.\n\n## Log\n- 10:00 Counted the crates in the barn\n- 11:00 Called the neighbour about the truck\n"
	mustCall(t, up, "memory_write", map[string]any{"op": "write", "path": log, "body": sum})
	if d, _ := r.store.Get(ctx, log); d == nil || !strings.Contains(d.Body, "## Summary") || d.By != "upkeep" {
		t.Errorf("the log = %+v", d)
	}
}

func TestUpkeepDeleteRemovesThePageItChecked(t *testing.T) {
	r, _ := upkeepRig(t, false)
	if _, err := r.store.Write(ctx, "notes/bread.md", "# Bread\n\nAlice bakes sourdough bread on Sunday mornings.\n", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.Write(ctx, "notes/loaf.md", "# Loaf\n\nAlice bakes sourdough bread on Sunday mornings.\n", "owner"); err != nil {
		t.Fatal(err)
	}
	// a path spelled another way finds the page, and the removal is of the page found
	got := mustCall(t, r.upkeepSession(t), "memory_delete", map[string]any{"what": "page", "path": "Notes/Bread", "kept_in": []string{"notes/loaf.md"}})
	if got["removed"] != "notes/bread.md" {
		t.Errorf("deleted = %v", got)
	}
	if d, _ := r.store.Get(ctx, "notes/bread.md"); d != nil {
		t.Error("the page is still there")
	}
}

func TestUpkeepEndOfAnEndedRunAndTheCaps(t *testing.T) {
	r, _ := upkeepRig(t, false)
	s := r.session(t)
	id := mustCall(t, s, "memory_upkeep", map[string]any{"op": "start", "job": "hubs"})["run"].(string)
	mustCall(t, s, "memory_upkeep", map[string]any{"op": "end", "run": id, "status": "ok", "report": "first"})
	if out, failed := call(t, s, "memory_upkeep", map[string]any{"op": "end", "run": id, "status": "failed", "report": "second"}); !failed || !strings.Contains(problemOf(out), "ended already") {
		t.Errorf("second end = %v", out)
	}
	if last, _ := upkeep.Last(ctx, r.srv.cfg.Upkeep.DB); last.Status != "ok" || last.Report != "first" {
		t.Errorf("the ended run changed: %+v", last)
	}
	many := make([]string, 51)
	for i := range many {
		many[i] = "k"
	}
	if _, failed := call(t, s, "memory_upkeep", map[string]any{"op": "dismiss", "job": "duplicates", "items": many}); !failed {
		t.Error("51 keys were taken")
	}
	if _, failed := call(t, s, "memory_upkeep", map[string]any{"op": "dismiss", "job": "duplicates", "items": []string{strings.Repeat("k", 201)}}); !failed {
		t.Error("a key of 201 bytes was taken")
	}
	mustCall(t, s, "memory_upkeep", map[string]any{"op": "dismiss", "job": "duplicates", "items": many[:50]})
}

func TestShortUpkeepTokenRefusesToStart(t *testing.T) {
	cfg := newRig(t, nil).srv.cfg
	cfg.UpkeepToken = "short-secret-value"
	_, err := New(cfg)
	if err == nil || !strings.Contains(err.Error(), "at least 32") || strings.Contains(err.Error(), cfg.UpkeepToken) {
		t.Errorf("New with a short upkeep token = %v", err)
	}
}

func TestUpkeepStartWithAnUnknownJobOpensNoRunAndAFailedOneIsClosed(t *testing.T) {
	r, _ := upkeepRig(t, false)
	s := r.session(t)
	if _, failed := call(t, s, "memory_upkeep", map[string]any{"op": "start", "job": "nope"}); !failed {
		t.Fatal("an unknown job started")
	}
	if last, _ := upkeep.Last(ctx, r.srv.cfg.Upkeep.DB); last != nil {
		t.Fatalf("a run was opened for an unknown job: %+v", last)
	}
	// the sweep fails when the memory's database is gone: the run is closed as failed, not left open
	r.srv.cfg.Tasks.DB.Close()
	if _, failed := call(t, s, "memory_upkeep", map[string]any{"op": "start", "job": "hubs"}); !failed {
		t.Fatal("start went through with the tasks database closed")
	}
	last, _ := upkeep.Last(ctx, r.srv.cfg.Upkeep.DB)
	if last == nil || last.Status != "failed" || last.Ended == "" {
		t.Fatalf("the run after a failed start: %+v", last)
	}
}

func TestUpkeepInboxPlacedShortEntryNeedsItsTextOnThePage(t *testing.T) {
	r, _ := upkeepRig(t, false)
	s := r.session(t)
	mustCall(t, s, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "Alice likes tea"})
	key := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist", "job": "inbox"})["items"].([]any)[0].(map[string]any)["key"].(string)
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/other.md", "base_rev": 0, "body": "# Other\n\nA page about something else entirely.\n"})
	if out, failed := call(t, s, "memory_upkeep", map[string]any{"op": "inbox_placed", "items": []string{key}, "kept_in": []string{"notes/other.md"}}); !failed || !strings.Contains(problemOf(out), "Alice likes tea") {
		t.Fatalf("a short entry on no page = %v", out)
	}
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/tea.md", "base_rev": 0, "body": "# Tea\n\nNotes. Alice likes tea\n"})
	if got := mustCall(t, s, "memory_upkeep", map[string]any{"op": "inbox_placed", "items": []string{key}, "kept_in": []string{"notes/tea.md"}}); got["removed"] != float64(1) {
		t.Errorf("inbox_placed = %v", got)
	}
}

func TestUpkeepInboxPlacedKeepsOtherEntriesWhenOneHasABlankLine(t *testing.T) {
	r, _ := upkeepRig(t, false)
	s := r.session(t)
	page := "# Inbox\n\n- 2026-11-01 10:00 Alice drinks green tea every morning\n\n  and the mug is blue and chipped\n- 2026-11-01 10:05 the printer is on the second floor\n"
	if _, err := r.store.Write(ctx, "inbox/inbox.md", page, "owner"); err != nil {
		t.Fatal(err)
	}
	items := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist", "job": "inbox"})["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v", items)
	}
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/tea.md", "base_rev": 0,
		"body": "# Tea\n\nAlice drinks green tea every morning and the mug is blue and chipped.\n"})
	key := items[0].(map[string]any)["key"].(string)
	mustCall(t, s, "memory_upkeep", map[string]any{"op": "inbox_placed", "items": []string{key}, "kept_in": []string{"notes/tea.md"}})
	d, _ := r.store.Get(ctx, "inbox/inbox.md")
	if !strings.Contains(d.Body, "printer is on the second floor") || strings.Contains(d.Body, "mug") {
		t.Errorf("inbox after = %q", d.Body)
	}
}

// --- second round ---

func TestTaskPagesAreGuardedUnderEverySpellingOfTheirPath(t *testing.T) {
	r, _ := upkeepRig(t, false)
	task := mustCall(t, r.session(t), "tasks_save", map[string]any{"title": "Plan the harvest"})
	id := task["id"].(string)
	page := "tasks/" + id + ".md"
	before, _ := r.store.Get(ctx, page)
	if before == nil {
		t.Fatal("no task page")
	}
	if _, err := r.store.Write(ctx, "notes/loaf.md", "# Loaf\n\nA page of bread.\n", "owner"); err != nil {
		t.Fatal(err)
	}
	spellings := []string{"tásks/" + id + ".md", "Tasks/" + id, "TASKS/" + id + ".MD", "tasks /" + id, " /tásks/" + id}
	for who, s := range map[string]*sdk.ClientSession{"upkeep": r.upkeepSession(t), "owner": r.session(t)} {
		for _, sp := range spellings {
			calls := []struct {
				tool string
				args map[string]any
			}{
				{"memory_write", map[string]any{"op": "write", "path": sp, "body": "# x\n\nOverwritten words here.\n"}},
				{"memory_write", map[string]any{"op": "edit", "path": sp, "find": "Plan the harvest", "replace": "Gone"}},
				{"memory_write", map[string]any{"op": "move", "from": sp, "to": "notes/x.md"}},
				{"memory_write", map[string]any{"op": "move", "from": "notes/loaf.md", "to": sp}},
			}
			if who == "upkeep" {
				calls = append(calls, struct {
					tool string
					args map[string]any
				}{"memory_delete", map[string]any{"what": "page", "path": sp, "kept_in": []string{"notes/loaf.md"}}})
			}
			for _, c := range calls {
				if out, failed := call(t, s, c.tool, c.args); !denied(out, failed) {
					t.Errorf("%s: %s %v on %q went through: %v", who, c.tool, c.args["op"], sp, out)
				}
			}
			if d, _ := r.store.Get(ctx, page); d == nil || d.Body != before.Body || d.Rev != before.Rev {
				t.Fatalf("%s: the task page changed after %q: %+v", who, sp, d)
			}
			if d, _ := r.store.Get(ctx, "notes/x.md"); d != nil {
				t.Fatalf("%s: the task page was moved out", who)
			}
		}
	}
}

func TestOwnerDeleteOfAnotherSpellingBehavesAsEver(t *testing.T) {
	r, _ := upkeepRig(t, false)
	if _, err := r.store.Write(ctx, "notes/bread.md", "# Bread\n\nA page of bread.\n", "owner"); err != nil {
		t.Fatal(err)
	}
	if out, failed := call(t, r.session(t), "memory_delete", map[string]any{"what": "page", "path": "Notes/Bread"}); !failed {
		t.Errorf("the owner's delete of a spelling that is no page went through: %v", out)
	}
	if d, _ := r.store.Get(ctx, "notes/bread.md"); d == nil {
		t.Error("the page is gone")
	}
	got := mustCall(t, r.session(t), "memory_delete", map[string]any{"what": "page", "path": "notes/bread.md"})
	if got["removed"] != "notes/bread.md" {
		t.Errorf("removed = %v", got)
	}
}

func TestKeptInIsCappedOnWriteEditAndDeleteToo(t *testing.T) {
	r, _ := upkeepRig(t, false)
	r.seedCooking(t)
	up := r.upkeepSession(t)
	many := make([]string, 51)
	for i := range many {
		many[i] = "notes/cooking.md"
	}
	long := []string{strings.Repeat("k", 201)}
	for _, k := range [][]string{many, long} {
		for _, c := range []struct {
			tool string
			args map[string]any
		}{
			{"memory_write", map[string]any{"op": "write", "path": "notes/cooking.md", "base_rev": 1, "body": "# C\n\nShort.\n", "kept_in": k}},
			{"memory_write", map[string]any{"op": "edit", "path": "notes/cooking.md", "find": "Alice", "replace": "Bob", "kept_in": k}},
			{"memory_delete", map[string]any{"what": "page", "path": "notes/cooking.md", "kept_in": k}},
		} {
			if out, failed := call(t, up, c.tool, c.args); !failed || !strings.Contains(problemOf(out), "at most") {
				t.Errorf("%s with %d kept_in: %v", c.tool, len(k), out)
			}
		}
	}
}

func TestInboxPlacedNeedsTheShortFactOfAnEntryToo(t *testing.T) {
	r, _ := upkeepRig(t, false)
	s := r.session(t)
	mustCall(t, s, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "Alice keeps the spare key under the blue flowerpot. Her pin: 4821."})
	key := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist", "job": "inbox"})["items"].([]any)[0].(map[string]any)["key"].(string)
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/key.md", "base_rev": 0,
		"body": "# Key\n\nAlice keeps the spare key under the blue flowerpot.\n"})
	if out, failed := call(t, s, "memory_upkeep", map[string]any{"op": "inbox_placed", "items": []string{key}, "kept_in": []string{"notes/key.md"}}); !failed || !strings.Contains(problemOf(out), "4821") {
		t.Fatalf("placed without the pin: %v", out)
	}
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/key.md", "base_rev": 1,
		"body": "# Key\n\nAlice keeps the spare key under the blue flowerpot. Her pin: 4821.\n"})
	if got := mustCall(t, s, "memory_upkeep", map[string]any{"op": "inbox_placed", "items": []string{key}, "kept_in": []string{"notes/key.md"}}); got["removed"] != float64(1) {
		t.Errorf("inbox_placed = %v", got)
	}
}

func TestUpkeepHubsLeaveOutRecordsAndTasks(t *testing.T) {
	for _, tc := range []struct {
		areas memory.Areas
		week  string
	}{{memory.AreasEN, "weeks"}, {memory.AreasIT, "settimane"}} {
		r, _ := upkeepRigIn(t, false, tc.areas)
		s := r.session(t)
		long := "# Page\n\nA page.\n\n" + strings.Repeat("word ", 700) + "\n"
		d := tc.areas.Diary
		for _, p := range []string{d + "/2026-10-16.md", d + "/" + tc.week + "/2026-w41.md", tc.areas.Inbox + "/inbox.md", tc.areas.Notes + "/big.md"} {
			if _, err := r.store.Write(ctx, p, long, "owner"); err != nil {
				t.Fatal(p, err)
			}
		}
		for _, k := range []string{"episode", "source"} {
			if _, err := r.store.Write(ctx, tc.areas.Notes+"/"+k+".md", "---\nkind: "+k+"\n---\n"+long, "owner"); err != nil {
				t.Fatal(k, err)
			}
		}
		items := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist", "job": "hubs"})["items"].([]any)
		var got []string
		for _, it := range items {
			got = append(got, it.(map[string]any)["path"].(string))
		}
		// days, week pages, the inbox and the records of kind episode or source are not pages to rewrite
		want := []string{tc.areas.Notes + "/big.md"}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: hubs = %v, want %v", d, got, want)
		}
	}
}

func TestUpkeepWeeksFollowTheInstancesAreas(t *testing.T) {
	r, _ := upkeepRigAt(t, false, memory.AreasIT, time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC))
	s := r.session(t)
	for _, day := range []string{"2026-10-26", "2026-10-28", "2026-11-03"} {
		if _, err := r.store.Write(ctx, "diario/"+day+".md", "# "+day+"\n\nCosa e successo.\n\n- 10:00 qualcosa\n", "owner"); err != nil {
			t.Fatal(err)
		}
	}
	due := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist", "job": "weeks"})["items"].([]any)
	// W45 ended three days ago: not yet a week past
	if len(due) != 1 || due[0].(map[string]any)["page"] != "diario/settimane/2026-w44.md" {
		t.Fatalf("due weeks = %v", due)
	}
}

func TestNextJobTakesTheDueJobRunLongestAgo(t *testing.T) {
	due := map[string]int{"inbox": 3, "hubs": 40, "logs": 20}
	if got := nextJob(due, nil); got != "inbox" {
		t.Errorf("nothing ran yet: %q, want the first due job", got)
	}
	ran := map[string]string{"inbox": "2026-11-13T02:30:00Z", "hubs": "2026-11-10T02:30:00Z"}
	if got := nextJob(due, ran); got != "logs" {
		t.Errorf("logs never ran: %q", got)
	}
	ran["logs"] = "2026-11-12T02:30:00Z"
	if got := nextJob(due, ran); got != "hubs" {
		t.Errorf("hubs ran longest ago: %q", got)
	}
	if got := nextJob(map[string]int{}, ran); got != "" {
		t.Errorf("nothing due: %q", got)
	}
}

// The inbox gets entries every day: a night of inbox must not keep the other jobs waiting.
func TestUpkeepWorklistTurnsToAnotherJobAfterARun(t *testing.T) {
	r, _ := upkeepRig(t, false)
	s := r.session(t)
	mustCall(t, s, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "the printer is on the second floor"})
	if _, err := r.store.Write(ctx, "notes/big.md", "# Hub\n\nA hub page.\n\n"+strings.Repeat("word ", 600)+"\n", "owner"); err != nil {
		t.Fatal(err)
	}
	if w := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist"}); w["next"] != "inbox" {
		t.Fatalf("first night: %v", w)
	}
	run := mustCall(t, s, "memory_upkeep", map[string]any{"op": "start", "job": "inbox"})["run"].(string)
	mustCall(t, s, "memory_upkeep", map[string]any{"op": "end", "run": run, "status": "ok", "report": "nothing placed"})
	mustCall(t, s, "memory_write", map[string]any{"op": "append", "where": "inbox", "text": "the plant needs water on Mondays"})
	if w := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist"}); w["next"] != "hubs" {
		t.Fatalf("after a night of inbox: %v", w)
	}
}

func TestUpkeepRunsAnswersTheLastRunsWithTheirReports(t *testing.T) {
	r, _ := upkeepRig(t, false)
	s := r.session(t)
	if got := mustCall(t, s, "memory_upkeep", map[string]any{"op": "runs"})["runs"].([]any); len(got) != 0 {
		t.Fatalf("runs before any = %v", got)
	}
	var last string
	for i := 0; i < recentRuns+2; i++ {
		id := mustCall(t, s, "memory_upkeep", map[string]any{"op": "start", "job": "hubs"})["run"].(string)
		mustCall(t, s, "memory_upkeep", map[string]any{"op": "end", "run": id, "status": "ok", "report": "two hubs split"})
		last = id
	}
	open := mustCall(t, s, "memory_upkeep", map[string]any{"op": "start", "job": "logs"})["run"].(string)
	got := mustCall(t, s, "memory_upkeep", map[string]any{"op": "runs"})["runs"].([]any)
	if len(got) != recentRuns {
		t.Fatalf("%d runs, want %d", len(got), recentRuns)
	}
	first, second := got[0].(map[string]any), got[1].(map[string]any)
	if first["run"] != open || first["status"] != "open" || first["job"] != "logs" {
		t.Errorf("first = %v, want the open logs run", first)
	}
	if _, has := first["report"]; has {
		t.Errorf("an open run carries a report: %v", first)
	}
	if second["run"] != last || second["status"] != "ok" || second["report"] != "two hubs split" || second["ended"] == nil {
		t.Errorf("second = %v", second)
	}
}

func TestUpkeepHubsAnswerAtMostHubItems(t *testing.T) {
	r, _ := upkeepRig(t, false)
	s := r.session(t)
	body := "# Hub\n\nA hub page.\n\n" + strings.Repeat("word ", 450) + "\n"
	for i := 0; i < hubItems+2; i++ {
		if _, err := r.store.Write(ctx, "notes/hub-"+string(rune('a'+i))+".md", body, "owner"); err != nil {
			t.Fatal(err)
		}
	}
	w := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist"})
	if w["counts"].(map[string]any)["hubs"] != float64(hubItems+2) {
		t.Fatalf("counts = %v", w["counts"])
	}
	if items := mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist", "job": "hubs"})["items"].([]any); len(items) != hubItems {
		t.Errorf("%d hubs in one answer, want %d", len(items), hubItems)
	}
}
