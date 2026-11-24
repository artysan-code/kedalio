package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"kedalio/core/db"
	"kedalio/core/memory"
	"kedalio/core/memory/search"
	"kedalio/protocol"
)

// asMap is an answer as the tool sends it.
func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func save(t *testing.T, b *Book, s protocol.TasksSave) map[string]any {
	t.Helper()
	a, err := b.Save(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	return asMap(t, a)
}

func TestSaveAddsAndUpdatesWithTheRelationsAndGetListsThemBlocksAndGroups(t *testing.T) {
	b := newBook(t)
	write(t, b, "progetti/kedalio.md", page("Kedalio Server"))
	parent := save(t, b, protocol.TasksSave{Title: "Release", Project: S("progetti/kedalio")})
	eq(t, parent["project"], any("kedalio"))
	tk := save(t, b, protocol.TasksSave{
		Title: "Tests", About: L("kedalio"), BlockedBy: L(parent["id"].(string)), For: S("bob"), WorkedBy: S("run:r-3"),
		WaitingOn: S("cora"), Parent: S(parent["id"].(string)),
	})
	eq(t, asMap(t, must(b.GetOf(ctx, protocol.TasksGet{ID: tk["id"].(string)})))["task"].(map[string]any)["parent"], parent["id"])
	got := asMap(t, must(b.GetOf(ctx, protocol.TasksGet{ID: tk["id"].(string)})))
	rels := got["relations"].(map[string]any)
	eq(t, rels["about"].([]any)[0], any("[[kedalio]]"))
	eq(t, rels["worked_by"].([]any)[0], any("run:r-3"))
	eq(t, rels["part_of"].([]any)[0], any("[["+parent["id"].(string)+"]]"))
	eq(t, asMap(t, must(b.GetOf(ctx, protocol.TasksGet{ID: parent["id"].(string)})))["blocks"].([]any)[0], tk["id"])

	list := func(q protocol.TasksList) map[string]any { return asMap(t, must(b.Search(ctx, q))) }
	first := func(m map[string]any) map[string]any { return m["tasks"].([]any)[0].(map[string]any) }
	eq(t, first(list(protocol.TasksList{About: "kedalio"}))["id"], tk["id"])
	eq(t, first(list(protocol.TasksList{For: "bob"}))["for"], any("bob"))
	eq(t, first(list(protocol.TasksList{WorkedBy: "run:r-3"}))["worked_by"], any("run:r-3"))
	eq(t, first(list(protocol.TasksList{Blocks: tk["id"].(string)}))["id"], parent["id"])
	eq(t, first(list(protocol.TasksList{BlockedBy: parent["id"].(string)}))["id"], tk["id"])
	eq(t, list(protocol.TasksList{BlockedBy: tk["id"].(string)})["count"], any(float64(0)))
	eq(t, list(protocol.TasksList{Group: "closed"})["count"], any(float64(0)))
	eq(t, first(list(protocol.TasksList{Project: "kedalio"}))["id"], parent["id"])
	// a task a filter names by ref is looked for among all
	save(t, b, protocol.TasksSave{ID: parent["id"].(string), Ref: S("K-1")})
	eq(t, first(list(protocol.TasksList{Parent: "K-1"}))["id"], tk["id"])

	// the relations are refused when they do not fit
	_, err := b.Save(ctx, protocol.TasksSave{ID: parent["id"].(string), AssignedTo: S("run:r-1")})
	wantErr(t, err, "worked_by")
	_, err = b.Save(ctx, protocol.TasksSave{ID: parent["id"].(string), Parent: S(tk["id"].(string))})
	wantErr(t, err, "circle")

	// the debrief: the task for bob is apart, the one run works on is the agents'
	d := must(b.BriefOf(ctx))
	eq(t, len(d.ForOthers), 1)
	eq(t, len(d.InProgressByAgents), 0)
	save(t, b, protocol.TasksSave{ID: parent["id"].(string), WorkedBy: S("run:r-9"), Status: "doing"})
	eq(t, len(must(b.BriefOf(ctx)).InProgressByAgents), 1)
}

func TestSaveWithoutATitleOrAnythingToDoSaysSo(t *testing.T) {
	b := newBook(t)
	_, err := b.Save(ctx, protocol.TasksSave{Due: S("2026-10-26")})
	wantErr(t, err, "title")
	tk := mustAdd(t, b, Input{Title: "x"})
	_, err = b.Save(ctx, protocol.TasksSave{ID: tk.ID})
	wantErr(t, err, "nothing to do")
	// `project` alone only says where to look for a ref: it is not a change
	_, err = b.Save(ctx, protocol.TasksSave{ID: tk.ID, Project: S("p")})
	wantErr(t, err, "nothing to do")
}

func TestSaveAboutLinksThePageAndNothingIsLinkedBehindTheCallersBack(t *testing.T) {
	b := newBook(t)
	write(t, b, "persone/nina-petrova.md", page("Nina Petrova"))
	r := save(t, b, protocol.TasksSave{Title: "Sentire Nina Petrova sul preventivo"})
	_, hasRels := r["rels"]
	eq(t, hasRels, false) // the name in the title is not linked on its own
	save(t, b, protocol.TasksSave{ID: r["id"].(string), About: L("persone/nina-petrova")})
	eq(t, asMap(t, must(b.Search(ctx, protocol.TasksList{About: "nina-petrova"})))["count"], any(float64(1)))
}
func TestSaveReportsUnresolvedPeopleAndAdoesNotMakeAPage(t *testing.T) {
	b := newBook(t)
	r := save(t, b, protocol.TasksSave{Title: "Per il lead", AssignedTo: S("lead")})
	eq(t, r["unresolved_people"].([]any)[0], any("lead"))
	eq(t, len(must(b.Store.List(ctx, "persone/", memory.ListOpts{}))), 0)
	_, none := save(t, b, protocol.TasksSave{Title: "Mio"})["unresolved_people"]
	eq(t, none, false)
}

func TestSaveFollowUpsLogDecisionEditStepsAttachDone(t *testing.T) {
	b := newBook(t)
	blocker := mustAdd(t, b, Input{Title: "Before"})
	r := save(t, b, protocol.TasksSave{Title: "Main", BlockedBy: L(blocker.ID), Parent: S(""),
		StepsAdd: []string{"uno", "due"}, Notes: S("Testo originale.")})
	id := r["id"].(string)
	eq(t, r["steps"].([]any)[1].(map[string]any)["text"], any("due"))

	r = save(t, b, protocol.TasksSave{ID: id, Log: "provato", Decision: "si fa cosi", StepsCheck: []int{0}})
	eq(t, r["log"], any(LogPath(id)))
	got := mustGet(t, b, id)
	eq(t, strings.Contains(got.Notes, "## Decisions"), true)
	eq(t, strings.Contains(got.Notes, "- [x] uno"), true)
	lv := must(b.LogOf(ctx, got))
	eq(t, lv.Lines[0].Text, "provato")
	eq(t, lv.Lines[0].Day, "2026-10-26")

	save(t, b, protocol.TasksSave{ID: id, Find: "originale", Replace: "rivisto"})
	eq(t, strings.Contains(mustGet(t, b, id).Notes, "Testo rivisto."), true)
	_, err := b.Save(ctx, protocol.TasksSave{ID: id, Replace: "x"})
	wantErr(t, err, "find")
	r = save(t, b, protocol.TasksSave{ID: id, Attach: "https://example.com/spec", AttachLabel: "spec"})
	eq(t, r["attachment"].(map[string]any)["label"], any("spec"))
	_, many := r["attachments"]
	eq(t, many, false)

	// done: an open step stops it, then the blocker's closing unblocks it
	_, err = b.Save(ctx, protocol.TasksSave{ID: id, Status: "done"})
	wantErr(t, err, "still open")
	r = save(t, b, protocol.TasksSave{ID: blocker.ID, Status: "done"})
	eq(t, r["unblocked"].([]any)[0].(string) != "", true)
	eq(t, strings.Contains(r["unblocked"].([]any)[0].(string), "#"+id), true)
}

func TestSaveDoneOnTheLastOpenPartOfALargerTaskSaysSo(t *testing.T) {
	b := newBook(t)
	parent := mustAdd(t, b, Input{Title: "Parent"})
	part := mustAdd(t, b, Input{Title: "Part", Parent: S(parent.ID)})
	r := save(t, b, protocol.TasksSave{ID: part.ID, Status: "done"})
	eq(t, strings.Contains(r["all_parts_done"].(string), "#"+parent.ID), true)
}

func TestSaveDoneOnARepeatingTaskSaysItWasRearmed(t *testing.T) {
	b := newBook(t)
	tk := mustAdd(t, b, Input{Title: "Backup", Due: S("2026-10-26"), Repeat: S("weekly")})
	r := save(t, b, protocol.TasksSave{ID: tk.ID, Status: "done"})
	eq(t, r["rearmed"], any(true))
	eq(t, r["next_due"], any("2026-11-02"))
}

func TestListShortByDefaultNotesCutFullWholeTruncatedSummaryAndTheStalledFilter(t *testing.T) {
	b := newBook(t)
	long := strings.Repeat("x", 400)
	a := mustAdd(t, b, Input{Title: "A", Notes: S(long), Project: S("p"), Due: S("2020-01-01")})
	mustAdd(t, b, Input{Title: "B", Project: S("p")})
	mustAdd(t, b, Input{Title: "C"})
	short := asMap(t, must(b.Search(ctx, protocol.TasksList{Notes: true, Limit: 1})))
	eq(t, short["truncated"], any(true))
	eq(t, short["count"], any(float64(3)))
	eq(t, short["summary"].(map[string]any)["overdue"], any(float64(1)))
	byProject := short["summary"].(map[string]any)["by_project"].(map[string]any)
	eq(t, byProject["p"], any(float64(2)))
	first := short["tasks"].([]any)[0].(map[string]any)
	eq(t, first["id"], any(a.ID))
	n := first["notes"].(string)
	eq(t, strings.HasSuffix(n, "(cut, 400 characters in all: tasks_get has the rest)"), true)
	for _, k := range []string{"created", "updated", "rels"} {
		if _, ok := first[k]; ok {
			t.Errorf("a short task has no %s", k)
		}
	}
	// a full task has all its fields, its notes cut like the short form's; max_chars 0 gives them whole
	full := asMap(t, must(b.Search(ctx, protocol.TasksList{Full: true, Limit: 1})))
	eq(t, strings.HasSuffix(full["tasks"].([]any)[0].(map[string]any)["notes"].(string), "tasks_get has the rest)"), true)
	eq(t, full["tasks"].([]any)[0].(map[string]any)["created"] != nil, true)
	whole := asMap(t, must(b.Search(ctx, protocol.TasksList{Full: true, Limit: 1, MaxChars: N(0)})))
	eq(t, len(whole["tasks"].([]any)[0].(map[string]any)["notes"].(string)), 400)

	// stalled through the plain list, with the reason of each; a snooze takes it out, until it is written to
	st := asMap(t, must(b.Search(ctx, protocol.TasksList{Stalled: true})))
	eq(t, st["count"], any(float64(1)))
	eq(t, strings.Contains(st["tasks"].([]any)[0].(map[string]any)["stalled"].([]any)[0].(string), "days past"), true)
	save(t, b, protocol.TasksSave{ID: a.ID, Snooze: S("2999-01-01")})
	eq(t, asMap(t, must(b.Search(ctx, protocol.TasksList{Stalled: true})))["count"], any(float64(0)))
	_, err := b.Save(ctx, protocol.TasksSave{ID: a.ID, Snooze: S("tomorrow")})
	wantErr(t, err, "snooze is a day")
	save(t, b, protocol.TasksSave{ID: a.ID, Priority: N(1)})
	eq(t, asMap(t, must(b.Search(ctx, protocol.TasksList{Stalled: true})))["count"], any(float64(1)))
	eq(t, asMap(t, must(b.Search(ctx, protocol.TasksList{Text: "no such words"})))["count"], any(float64(0)))
}

func TestListStopsAtItsBudgetAndSaysHowToSeeTheRest(t *testing.T) {
	b := newBook(t)
	notes := strings.Repeat("n", 280)
	for i := range 80 {
		mustAdd(t, b, Input{Title: fmt.Sprintf("Task %02d", i), Notes: S(notes), Project: S("p")})
	}
	// short tasks without notes fit
	plain := asMap(t, must(b.Search(ctx, protocol.TasksList{Limit: 30})))
	eq(t, len(plain["tasks"].([]any)), 30)
	eq(t, plain["hint"], nil)
	// eighty tasks with their notes do not: the answer stops under the budget and says so
	for _, q := range []protocol.TasksList{{Notes: true, Limit: 80}, {Full: true, Limit: 80}} {
		raw, err := json.Marshal(must(b.Search(ctx, q)))
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > listBudget+2000 {
			t.Errorf("%+v: answer of %d characters, budget %d", q, len(raw), listBudget)
		}
		a := asMap(t, must(b.Search(ctx, q)))
		shown := len(a["tasks"].([]any))
		if shown == 0 || shown >= 80 {
			t.Fatalf("%+v: %d tasks shown", q, shown)
		}
		eq(t, a["shown"], any(float64(shown)))
		eq(t, a["count"], any(float64(80)))
		eq(t, a["truncated"], any(true))
		eq(t, a["summary"].(map[string]any)["by_project"].(map[string]any)["p"], any(float64(80)))
		eq(t, strings.Contains(a["hint"].(string), "tasks_get"), true)
		eq(t, strings.Contains(a["hint"].(string), fmt.Sprintf("%d missing", 80-shown)), true)
		eq(t, strings.Contains(a["hint"].(string), "max_chars 0"), true)
	}
}

func TestListDefaultsToAbout6000CharactersAndMaxCharsChangesIt(t *testing.T) {
	b := newBook(t)
	for i := range 80 {
		mustAdd(t, b, Input{Title: fmt.Sprintf("Task %02d", i), Notes: S(strings.Repeat("n", 280)), Project: S("p")})
	}
	rendered := func(q protocol.TasksList) (string, map[string]any) {
		raw, err := json.MarshalIndent(must(b.Search(ctx, q)), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		return string(raw), asMap(t, must(b.Search(ctx, q)))
	}
	text, a := rendered(protocol.TasksList{Notes: true, Limit: 80})
	if len(text) > 7500 || a["shown"] == nil {
		t.Errorf("default answer is %d characters, shown = %v: want about 6000", len(text), a["shown"])
	}
	// a smaller cap shows fewer, a larger one more, 0 all of them and no hint
	_, small := rendered(protocol.TasksList{Notes: true, Limit: 80, MaxChars: N(2000)})
	_, large := rendered(protocol.TasksList{Notes: true, Limit: 80, MaxChars: N(20000)})
	_, all := rendered(protocol.TasksList{Notes: true, Limit: 80, MaxChars: N(0)})
	ns, nl, na := len(small["tasks"].([]any)), len(large["tasks"].([]any)), len(all["tasks"].([]any))
	if !(ns < len(a["tasks"].([]any)) && len(a["tasks"].([]any)) < nl && nl < na) || na != 80 {
		t.Errorf("tasks shown: 2000 -> %d, default -> %d, 20000 -> %d, 0 -> %d", ns, len(a["tasks"].([]any)), nl, na)
	}
	eq(t, all["hint"], nil)
}

func TestGetCutsLongNotesAndReadsTheRestByOffsetOrSection(t *testing.T) {
	b := newBook(t)
	var notes strings.Builder
	notes.WriteString("Intro of the task.\n\n## Decisions\n\n- chosen: the small one\n\n## Background\n\n")
	for i := range 400 {
		fmt.Fprintf(&notes, "line %03d of a very long background text\n", i)
	}
	tk := mustAdd(t, b, Input{Title: "Long", Notes: S(notes.String())})
	size := func(a *GetAnswer) int { raw, _ := json.Marshal(a); return len(raw) }

	a := must(b.GetOf(ctx, protocol.TasksGet{ID: tk.ID}))
	if n := size(a); n > 8000 {
		t.Errorf("default answer is %d characters, want at most 8000", n)
	}
	if a.NotesCut == nil || !strings.HasPrefix(a.NotesCut.Note, "truncated, ") || a.NotesCut.Total < 15000 || a.NotesCut.Next == 0 {
		t.Fatalf("notes_cut = %+v", a.NotesCut)
	}
	if len(a.NotesCut.Sections) != 2 || a.NotesCut.Sections[1].Title != "Background" {
		t.Errorf("sections = %+v", a.NotesCut.Sections)
	}
	// the pieces, followed by their next offset, give the notes back whole
	got, next := a.Task.Notes, a.NotesCut.Next
	for next > 0 {
		p := must(b.GetOf(ctx, protocol.TasksGet{ID: tk.ID, Offset: next, MaxChars: N(3000)}))
		if n := len(p.Task.Notes); n > 3000 {
			t.Errorf("piece at %d has %d characters of notes, cap 3000", next, n)
		}
		got += p.Task.Notes
		next = p.NotesCut.Next
	}
	eq(t, got, a.Task.Notes+got[len(a.Task.Notes):])
	eq(t, got, mustGet(t, b, tk.ID).Notes)

	// one section by its heading
	d := must(b.GetOf(ctx, protocol.TasksGet{ID: tk.ID, Section: "decisions"}))
	eq(t, d.Task.Notes, "## Decisions\n\n- chosen: the small one\n\n")
	eq(t, d.NotesCut.Section, "decisions")
	_, err := b.GetOf(ctx, protocol.TasksGet{ID: tk.ID, Section: "nope"})
	wantErr(t, err, `its sections are "Decisions", "Background"`)

	// max_chars 0 is everything, as before
	w := must(b.GetOf(ctx, protocol.TasksGet{ID: tk.ID, MaxChars: N(0)}))
	eq(t, w.Task.Notes, mustGet(t, b, tk.ID).Notes)
	eq(t, w.NotesCut == nil, true)

	// short notes are untouched
	s := mustAdd(t, b, Input{Title: "Short", Notes: S("a few words")})
	g := must(b.GetOf(ctx, protocol.TasksGet{ID: s.ID}))
	eq(t, g.Task.Notes, "a few words")
	eq(t, g.NotesCut == nil, true)
}

func TestGetAnswersEachFactOnce(t *testing.T) {
	b := newBook(t)
	write(t, b, "progetti/kedalio.md", page("Kedalio Server"))
	tk := mustAdd(t, b, Input{Title: "Once", Notes: S("Intro.\n\n## Steps\n\n- [ ] first\n- [x] second\n\n## Decisions\n\n- chosen: this\n")})
	save(t, b, protocol.TasksSave{ID: tk.ID, About: L("kedalio")})
	raw, err := json.Marshal(must(b.GetOf(ctx, protocol.TasksGet{ID: tk.ID})))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, must(b.GetOf(ctx, protocol.TasksGet{ID: tk.ID})))
	task := m["task"].(map[string]any)
	for _, k := range []string{"rels", "relExtra"} {
		if _, ok := task[k]; ok {
			t.Errorf("task carries %q next to relations", k)
		}
	}
	eq(t, m["relations"].(map[string]any)["about"].([]any)[0], any("[[kedalio]]"))
	notes := task["notes"].(string)
	if strings.Contains(notes, "[ ] first") || strings.Contains(notes, "## Steps") {
		t.Errorf("notes repeat the steps: %q", notes)
	}
	eq(t, strings.Contains(notes, "## Decisions"), true)
	steps := m["steps"].([]any)
	eq(t, len(steps), 2)
	eq(t, steps[1].(map[string]any)["index"], any(float64(1)))
	eq(t, strings.Contains(string(raw), `"progress":null`), false)
	// nothing empty: no attachments, no null progress on a task with no steps
	plain := asMap(t, must(b.GetOf(ctx, protocol.TasksGet{ID: mustAdd(t, b, Input{Title: "Plain"}).ID})))
	for _, k := range []string{"progress", "attachments", "steps", "notes_cut"} {
		if v, ok := plain[k]; ok {
			t.Errorf("%s = %v on a plain task, want it omitted", k, v)
		}
	}
}

func TestGetShowsTheLastLogLinesOnlyUnlessAskedForMore(t *testing.T) {
	b := newBook(t)
	tk := mustAdd(t, b, Input{Title: "Chatty"})
	for i := range 20 {
		save(t, b, protocol.TasksSave{ID: tk.ID, Log: fmt.Sprintf("event %02d", i)})
	}
	a := must(b.GetOf(ctx, protocol.TasksGet{ID: tk.ID}))
	eq(t, len(a.Log.Last), logShown)
	eq(t, a.Log.Total >= 20, true)
	eq(t, strings.Contains(a.Log.Last[len(a.Log.Last)-1].Text, "event 19"), true)
	eq(t, len(must(b.GetOf(ctx, protocol.TasksGet{ID: tk.ID, Log: 3})).Log.Last), 3)
	eq(t, len(must(b.GetOf(ctx, protocol.TasksGet{ID: tk.ID, Log: 500})).Log.Last), a.Log.Total)
}

func TestListViewRunsASavedViewAndAnswersItsGroupsAndViewsListsThem(t *testing.T) {
	f := fixture(t)
	v := must(f.b.Views(ctx))
	eq(t, strings.Join(viewNames(v.Views), ","), "board,days,per-altri,stalled,today")
	board := asMap(t, must(f.b.ListOf(ctx, protocol.TasksList{View: "board"})))
	if !reflect.DeepEqual(board["view"], map[string]any{"name": "board", "title": "Board", "layout": "board", "group_by": "status"}) {
		t.Errorf("%v", board["view"])
	}
	groups := board["groups"].([]any)
	var names []string
	for _, g := range groups {
		names = append(names, g.(map[string]any)["group"].(string))
	}
	eq(t, strings.Join(names, ","), "todo,doing,waiting")
	eq(t, groups[0].(map[string]any)["tasks"].([]any)[0].(map[string]any)["title"], any("Due today"))
	_, err := f.b.ListOf(ctx, protocol.TasksList{View: "nope"})
	wantErr(t, err, "there are: board, days, per-altri, stalled, today")
	list := must(f.b.ListOf(ctx, protocol.TasksList{}))
	_, ok := list.(*ListAnswer)
	eq(t, ok, true)
}

func TestBriefTheDebriefHasLinesEndingInIdsAndTheStalledOnes(t *testing.T) {
	f := fixture(t)
	d := asMap(t, must(f.b.BriefOf(ctx)))
	eq(t, d["today"], any("2026-10-26"))
	eq(t, d["weekday"], any("Monday"))
	eq(t, len(d["overdue"].([]any)), 1)
	eq(t, strings.Contains(d["overdue"].([]any)[0].(string), "Late one"), true)
	eq(t, len(d["for_others"].([]any)), 1)
	eq(t, strings.HasSuffix(d["overdue"].([]any)[0].(string), "#"+f.a.ID), true)
	_, hasIDs := d["ids"]
	eq(t, hasIDs, false)
	// what is empty or zero is left out
	for _, k := range []string{"done_today", "stalled", "stalled_total", "waiting_total", "missed"} {
		_, has := d[k]
		if has {
			t.Errorf("%q is in the debrief", k)
		}
	}
}

func TestBriefLineKeepsWhenTitleProjectAndIdOnly(t *testing.T) {
	b := newBook(t)
	long := strings.Repeat("a long title ", 5)
	tk := mustAdd(t, b, Input{Title: long, Due: S("2026-10-30"), Time: S("09:30"), Project: S("work/acme"), Ref: S("K-1"),
		Stage: S("spec"), For: S("bob"), Notes: S("## Steps\n\n- [x] a\n- [ ] b")})
	got := b.briefLine(tk)
	eq(t, got, "2026-10-30 · 09:30 · "+cutTitle(long)+" · [acme] · for bob · #"+tk.ID)
	eq(t, len([]rune(cutTitle(long))) <= briefTitle, true)
	eq(t, strings.Contains(got, "K-1") || strings.Contains(got, "spec") || strings.Contains(got, "1/2"), false)
}

func TestOnlyTheBriefCarriesTheClock(t *testing.T) {
	f := fixture(t)
	for name, v := range map[string]any{
		"list": must(f.b.ListOf(ctx, protocol.TasksList{})),
		"get":  must(f.b.GetOf(ctx, protocol.TasksGet{ID: f.a.ID})),
		"save": must(f.b.Save(ctx, protocol.TasksSave{Title: "New"})),
	} {
		m := asMap(t, v)
		for _, k := range []string{"today", "now", "weekday"} {
			if _, has := m[k]; has {
				t.Errorf("%s answers %q", name, k)
			}
		}
	}
}

func TestAFullListDoesNotEmbedTheRelations(t *testing.T) {
	b := newBook(t)
	mustAdd(t, b, Input{Title: "T", For: S("bob"), About: L("progetti/kedalio")})
	a := asMap(t, must(b.ListOf(ctx, protocol.TasksList{Full: true})))
	tk := a["tasks"].([]any)[0].(map[string]any)
	_, rels := tk["rels"]
	eq(t, rels, false)
	eq(t, tk["title"], any("T"))
}

func TestGetOneTaskWithStepsProgressLogAttachmentsAndHowItStandsWithTheOthers(t *testing.T) {
	b := newBook(t)
	parent := mustAdd(t, b, Input{Title: "Parent"})
	part := mustAdd(t, b, Input{Title: "Part", Parent: S(parent.ID), Notes: S("## Steps\n\n- [x] a\n- [ ] b")})
	waits := mustAdd(t, b, Input{Title: "Waits", BlockedBy: L(part.ID)})
	for i := 0; i < 10; i++ {
		must(b.Log(ctx, part.ID, "riga", "2026-10-26"))
	}
	a := asMap(t, must(b.GetOf(ctx, protocol.TasksGet{ID: part.ID})))
	eq(t, a["progress"].(map[string]any)["pct"], any(float64(50)))
	eq(t, len(a["steps"].([]any)), 2)
	eq(t, a["steps"].([]any)[1].(map[string]any)["index"], any(float64(1)))
	lg := a["log"].(map[string]any)
	eq(t, lg["total"], any(float64(10)))
	eq(t, len(lg["last"].([]any)), 3)
	eq(t, strings.Contains(a["part_of"].(string), "Parent"), true)
	eq(t, a["blocks"].([]any)[0], any(waits.ID))
	eq(t, strings.Contains(a["blocking"].([]any)[0].(string), "Waits"), true)
	p := asMap(t, must(b.GetOf(ctx, protocol.TasksGet{ID: parent.ID})))
	eq(t, p["parts_done"], any("0/1"))
	eq(t, strings.HasPrefix(p["parts"].([]any)[0].(string), "todo · "), true)
	w := asMap(t, must(b.GetOf(ctx, protocol.TasksGet{ID: waits.ID})))
	eq(t, strings.Contains(w["waiting_for"].([]any)[0].(string), "Part"), true)
	if _, ok := w["attachments"]; ok {
		t.Error("empty attachments are left out")
	}
}

func TestLineIsEasyToReadBack(t *testing.T) {
	b := newBook(t)
	tk := mustAdd(t, b, Input{Title: "Scrivere", Due: S("2026-10-30"), Time: S("09:30"), Project: S("p"), Ref: S("K-1"),
		Stage: S("spec"), Priority: N(1), For: S("bob"), Notes: S("## Steps\n\n- [x] a\n- [ ] b")})
	eq(t, b.Line(tk, nil), "2026-10-30 · 09:30 · K-1 · Scrivere · [p] · for bob · spec · 1/2 · ! · #"+tk.ID)
	today := mustAdd(t, b, Input{Title: "Oggi", Due: S("2026-10-26"), Status: Doing})
	eq(t, b.Line(today, nil), "Oggi · in progress · #"+today.ID)
}

func TestSummaryOfCountsByProjectAndStatusMostFirst(t *testing.T) {
	s := SummaryOf([]Task{
		task(func(t *Task) { t.Project, t.Status = "b", Todo }),
		task(func(t *Task) { t.Project = "a" }),
		task(func(t *Task) { t.Project = "a" }),
		task(func(t *Task) { t.Status, t.Due = Done, "2000-01-01" }),
	}, "2026-10-26")
	raw, _ := json.Marshal(s)
	eq(t, string(raw), `{"by_project":{"a":2,"b":1,"-":1},"by_status":{"todo":3,"done":1},"overdue":0}`)
}

func recallBook(t *testing.T) (*Book, *search.Index) {
	t.Helper()
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqldb.Close() })
	for _, m := range []func(context.Context, *sql.DB) error{memory.Migrate, Migrate} {
		if err := m(ctx, sqldb); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := search.New(ctx, sqldb)
	if err != nil {
		t.Fatal(err)
	}
	b := NewBook(memory.New(sqldb, ix), sqldb, "alice", time.UTC, "test")
	b.Now = func() time.Time { return at("2026-10-26T10:00:00") }
	return b, ix
}

func TestRecallTasksAreNodesOfKindTaskReachedThroughWhatTheyAreAboutClosedOnesRankDown(t *testing.T) {
	b, ix := recallBook(t)
	write(t, b, "progetti/kedalio.md", "# Kedalio\n\nIl server personale.\n")
	open := mustAdd(t, b, Input{Title: "Scrivere la guida", About: L("progetti/kedalio")})
	old := mustAdd(t, b, Input{Title: "Vecchia guida", About: L("progetti/kedalio")})
	mustUpdate(t, b, old.ID, Input{Status: Done})

	groups := must(b.TaskGroups(ctx))
	if !reflect.DeepEqual(groups, map[string]string{"tasks/" + open.ID + ".md": "open", "tasks/" + old.ID + ".md": "closed", LogPath(old.ID): "closed"}) {
		t.Errorf("%v", groups)
	}
	a, err := b.Store.Recall(ctx, "Kedalio", memory.RecallOptions{Index: ix, Budget: 4000, TaskGroups: b.TaskGroups, Now: at("2026-10-26T10:00:00")})
	if err != nil {
		t.Fatal(err)
	}
	var tasks []string
	for _, p := range a.Pages {
		if p.Kind == memory.KindTask {
			tasks = append(tasks, p.Path)
		}
	}
	for _, r := range a.Related {
		if r.Kind == memory.KindTask {
			tasks = append(tasks, r.Path)
		}
	}
	eq(t, strings.Join(tasks, ","), "tasks/"+open.ID+".md,tasks/"+old.ID+".md")
	eq(t, a.Pages[0].Path, "progetti/kedalio.md")
}

func TestSaveUpdateResolvesARefWithinTheGivenProject(t *testing.T) {
	b := newBook(t)
	mustAdd(t, b, Input{Title: "In p", Project: S("p"), Ref: S("K-1")})
	other := mustAdd(t, b, Input{Title: "In q", Project: S("q"), Ref: S("K-1")})
	// the same ref in two projects: without the project the update could not tell them apart
	save(t, b, protocol.TasksSave{ID: "K-1", Project: S("q"), Title: "In q, renamed"})
	got, err := b.Find(ctx, other.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, got.Title, "In q, renamed")
}

func TestSearchRefusesAStatusOrGroupItDoesNotKnow(t *testing.T) {
	b := newBook(t)
	mustAdd(t, b, Input{Title: "Open one"})
	_, err := b.Search(ctx, protocol.TasksList{Status: "in_progress"})
	wantErr(t, err, "status is one of")
	_, err = b.Search(ctx, protocol.TasksList{Group: "active-ish"})
	wantErr(t, err, "group is one of")
	if _, err := b.Search(ctx, protocol.TasksList{Status: "all"}); err != nil {
		t.Error(err)
	}
}

func TestGroupsByPathAClosedTasksLogIsInTheGroupsTooAnOpenOnesIsNot(t *testing.T) {
	b, _ := recallBook(t)
	done := mustAdd(t, b, Input{Title: "Chiuso"})
	open := mustAdd(t, b, Input{Title: "Aperto"})
	for _, id := range []string{done.ID, open.ID} {
		if _, err := b.Log(ctx, id, "avanzamento", ""); err != nil {
			t.Fatal(err)
		}
	}
	mustUpdate(t, b, done.ID, Input{Status: Done})
	got := must(GroupsByPath(ctx, b.DB))
	// an open task's log stays out: the core seeds the active tasks' pages from this map, and a log is no work in hand
	want := map[string]string{
		"tasks/" + done.ID + ".md": "closed", LogPath(done.ID): "closed", "tasks/" + open.ID + ".md": "open",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("groups = %v, want %v", got, want)
	}
}

func TestBriefCutsTheLongListsAtTenAndSaysHowMany(t *testing.T) {
	b := newBook(t)
	for i := 0; i < 14; i++ {
		mustAdd(t, b, Input{Title: fmt.Sprintf("Waits %d", i), Status: "waiting", WaitingOn: S("alice")})
		mustAdd(t, b, Input{Title: fmt.Sprintf("Someday %d", i)})
	}
	d := must(b.BriefOf(ctx))
	eq(t, len(d.WaitingOnOthers), briefCap)
	eq(t, d.WaitingTotal, 14)
	eq(t, len(d.NoDate), briefCap)
	eq(t, d.NoDateTotal, 14)
	eq(t, strings.Contains(d.More, "tasks_list"), true)
}

func TestSaveAnswersWhoTheTaskIsAndNeverEchoesTheNotes(t *testing.T) {
	b := newBook(t)
	r := save(t, b, protocol.TasksSave{Title: "Main", Ref: S("K-7"), Notes: S("Secret plan, long text."),
		StepsAdd: []string{"uno", "due", "tre"}})
	for _, k := range []string{"id", "ref", "title", "status", "updated_at", "added"} {
		if _, ok := r[k]; !ok {
			t.Errorf("add answer lacks %q: %v", k, r)
		}
	}
	eq(t, r["ref"], any("K-7"))
	eq(t, r["progress"], any("0/3"))
	for _, k := range []string{"task", "notes", "attachments"} {
		if _, ok := r[k]; ok {
			t.Errorf("add answer echoes %q", k)
		}
	}
	raw, _ := json.Marshal(r)
	eq(t, strings.Contains(string(raw), "Secret plan"), false)

	id := r["id"].(string)
	r = save(t, b, protocol.TasksSave{ID: id, StepsCheck: []int{1}})
	eq(t, len(r["steps"].([]any)), 1)
	st := r["steps"].([]any)[0].(map[string]any)
	eq(t, st["index"], any(float64(1)))
	eq(t, st["text"], any("due"))
	eq(t, st["done"], any(true))
	eq(t, r["progress"], any("1/3"))
	eq(t, r["id"], any(id))
	eq(t, r["title"], any("Main"))
	if _, ok := r["task"]; ok {
		t.Error("update answer echoes the task")
	}
}

func TestSaveWholeNotesWithoutStepsKeepTheStoredChecklist(t *testing.T) {
	b := newBook(t)
	task := mustAdd(t, b, Input{Title: "Keep", Notes: S("Intro.\n\n## Steps\n\n- [x] a\n- [ ] b\n- [ ] c\n\n## Decisions\n\nd1")})
	// what tasks_get shows: the notes without the checklist, sent back whole
	got := asMap(t, must(b.GetOf(ctx, protocol.TasksGet{ID: task.ID})))
	notes := mustGetNotes(got)
	eq(t, strings.Contains(notes, "[x] a"), false)
	save(t, b, protocol.TasksSave{ID: task.ID, Notes: S(notes + "\n\nEdited.")})
	cur := mustGet(t, b, task.ID)
	eq(t, strings.Join(stepTexts(cur.Notes), ","), "a,b,c")
	st := Steps(cur.Notes, nil)
	eq(t, st[0].Done, true)
	eq(t, st[1].Done, false)
	eq(t, strings.Contains(cur.Notes, "Edited."), true)
	eq(t, strings.Contains(cur.Notes, "d1"), true)
	// a body that brings its own steps replaces them
	save(t, b, protocol.TasksSave{ID: task.ID, Notes: S("New.\n\n## Steps\n\n- [ ] only")})
	eq(t, strings.Join(stepTexts(mustGet(t, b, task.ID).Notes), ","), "only")
}

func mustGetNotes(a map[string]any) string {
	n, _ := a["task"].(map[string]any)["notes"].(string)
	return n
}

func TestGetSectionStepsAnswersWithTheStepsList(t *testing.T) {
	b := newBook(t)
	task := mustAdd(t, b, Input{Title: "S", Notes: S("Intro.\n\n## Steps\n\n- [x] a\n- [ ] b\n\n## Decisions\n\nd1")})
	for _, sec := range []string{"Steps", "steps", "Passi"} {
		a := asMap(t, must(b.GetOf(ctx, protocol.TasksGet{ID: task.ID, Section: sec})))
		eq(t, len(a["steps"].([]any)), 2)
		eq(t, mustGetNotes(a), "")
	}
}
