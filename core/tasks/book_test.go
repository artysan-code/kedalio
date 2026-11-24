package tasks

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"kedalio/core/memory"
)

func TestApplyInputChecksWhatItIsGivenAnEmptyValueClears(t *testing.T) {
	now := at("2026-09-30T10:00:00")
	apply := func(base Task, in Input) (Task, error) { return ApplyInput(base, in, now, "alice") }
	_, err := apply(task(nil), Input{Due: S("2026-02-30")})
	wantErr(t, err, "due")
	_, err = apply(task(nil), Input{Title: strings.Repeat("x", MaxTitle+1)})
	wantErr(t, err, "at most 80")
	got, err := apply(task(nil), Input{Title: "  " + strings.Repeat("x", MaxTitle) + "  "})
	if err != nil || len(got.Title) != MaxTitle {
		t.Errorf("%v %v", got.Title, err)
	}
	_, err = apply(task(nil), Input{Time: S("25:00"), Due: S("2026-10-01")})
	wantErr(t, err, "time")
	_, err = apply(task(nil), Input{Time: S("10:00")})
	wantErr(t, err, "needs a day")
	cleared, _ := apply(task(func(t *Task) { t.Due = "2026-10-01" }), Input{Due: S("")})
	eq(t, cleared.Due, "")
	done, _ := apply(task(nil), Input{Status: Done})
	eq(t, done.Done, stamp(now))
	_, err = apply(task(nil), Input{Status: "started"})
	wantErr(t, err, "status is one of")
	_, err = apply(task(nil), Input{Priority: N(4)})
	wantErr(t, err, "priority")
	_, err = apply(task(nil), Input{Remind: N(2000)})
	wantErr(t, err, "remind")
	_, err = apply(task(nil), Input{Labels: L("a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m")})
	wantErr(t, err, "at most 12")
}

func TestApplyInputClearsProjectOwnerAndNotes(t *testing.T) {
	base := task(func(t *Task) { t.Project, t.Owner, t.Notes = "work/acme", "ariel", "testo" })
	got, err := ApplyInput(base, Input{Project: S(""), Notes: S("")}, at("2026-09-30T10:00:00"), "alice")
	if err != nil {
		t.Fatal(err)
	}
	// the owner is derived from the relations, which hold no assigned_to: it follows them
	eq(t, got.Project+got.Notes, "")
}

func TestProjectFieldsRefStageParentBlockedByLabelsDetailAreCheckedCleanedAndKept(t *testing.T) {
	now := at("2026-10-05T10:00:00")
	base := task(func(t *Task) { t.ID = "t-20261005-aaaaaa" })
	tk, err := ApplyInput(base, Input{
		Ref: S(" TASK-495 "), Stage: S("Release  Pending"), Parent: S("#t-20261005-bbbbbb"),
		BlockedBy: L("t-20261005-cccccc", "#t-20261005-cccccc"), Labels: L("Permessi", "permessi", " BE "),
		Detail: S("TASKS.md#task-495"),
	}, now, "alice")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, tk.Ref, "TASK-495")
	eq(t, tk.Stage, "release pending")
	eq(t, tk.Parent, "t-20261005-bbbbbb")
	if !reflect.DeepEqual(tk.BlockedBy, []string{"t-20261005-cccccc"}) || !reflect.DeepEqual(tk.Labels, []string{"permessi", "be"}) {
		t.Errorf("%v %v", tk.BlockedBy, tk.Labels)
	}
	eq(t, tk.Detail, "TASKS.md#task-495")
	if back := FromFile(ToFile(tk), ""); !reflect.DeepEqual(*back, tk) {
		t.Errorf("%+v\nwant %+v", *back, tk)
	}
	cleared, _ := ApplyInput(tk, Input{Ref: S(""), BlockedBy: L(), Labels: L(), Parent: S("")}, now, "alice")
	eq(t, cleared.Ref+cleared.Parent, "")
	eq(t, len(cleared.BlockedBy)+len(cleared.Labels), 0)
	if strings.Contains(ToFile(cleared), "blocked_by") {
		t.Error("nothing left to write")
	}
	_, err = ApplyInput(tk, Input{Parent: S("TASK-1")}, now, "alice")
	wantErr(t, err, "task id")
	_, err = ApplyInput(tk, Input{Parent: S(tk.ID)}, now, "alice")
	wantErr(t, err, "itself")
	_, err = ApplyInput(tk, Input{BlockedBy: L(tk.ID)}, now, "alice")
	wantErr(t, err, "itself")
	_, err = ApplyInput(tk, Input{Ref: S("a\nb")}, now, "alice")
	wantErr(t, err, "ref")
}

func TestBookAddCompleteARepeatingTaskReArmsDropInsteadOfDelete(t *testing.T) {
	b := newBook(t)
	b.clockAt("2026-09-30T10:00:00")
	tk := mustAdd(t, b, Input{Title: "Backup", Due: S("2026-09-30"), Repeat: S("weekly")})
	// for the account and its own, so nothing is written
	eq(t, tk.Owner, "")
	eq(t, len(tk.Rels), 0)
	r := mustUpdate(t, b, tk.ID, Input{Status: Done})
	eq(t, r.Rearmed, true)
	eq(t, r.Task.ID, tk.ID)
	eq(t, r.Task.Status, Todo)
	eq(t, r.Task.Due, "2026-10-07")
	mustUpdate(t, b, tk.ID, Input{Status: Dropped})
	all := must(b.List(ctx))
	if len(all) != 1 || all[0].Status != Dropped {
		t.Errorf("%+v", all)
	}
}

func TestBookAnEditorHoldingAnOlderVersionIsRefusedNotOverwrittenEveryChangeIsARevision(t *testing.T) {
	b := newBook(t)
	b.clockAt("2026-09-30T09:00:00")
	tk := mustAdd(t, b, Input{Title: "concorrenza"})
	b.clockAt("2026-09-30T09:05:00")
	mustUpdate(t, b, tk.ID, Input{Notes: S("dalla chat")})
	b.clockAt("2026-09-30T09:06:00")
	_, err := b.Update(ctx, tk.ID, Input{Notes: S("dalla console")}, tk.Updated)
	var stale *StaleError
	if !errors.As(err, &stale) {
		t.Fatalf("%v", err)
	}
	eq(t, mustGet(t, b, tk.ID).Notes, "dalla chat")
	h := must(b.Store.History(ctx, "tasks/"+tk.ID+".md"))
	eq(t, len(h), 2)
}

func TestBookTwoChangesInTheSameMillisecondAreTwoVersions(t *testing.T) {
	b := newBook(t)
	b.clockAt("2026-09-30T09:00:00")
	tk := mustAdd(t, b, Input{Title: "stesso istante"})
	first := must(b.Update(ctx, tk.ID, Input{Notes: S("uno")}, tk.Updated)).Task
	if first.Updated == tk.Updated {
		t.Fatal("a new version")
	}
	if _, err := b.Update(ctx, tk.ID, Input{Notes: S("due")}, tk.Updated); err == nil {
		t.Fatal("the older base is still refused")
	}
	eq(t, must(b.Update(ctx, tk.ID, Input{Notes: S("tre")}, first.Updated)).Task.Notes, "tre")
}

func TestRepeatDoneOnARepeatingTaskReArmsTheSameTask(t *testing.T) {
	b := newBook(t)
	blocker := mustAdd(t, b, Input{Title: "Before"})
	tk := mustAdd(t, b, Input{Title: "Pay the invoice", Due: S("2026-10-26"), Repeat: S("FREQ=MONTHLY;BYMONTHDAY=26"),
		BlockedBy: L(blocker.ID), Status: Doing})
	mustUpdate(t, b, tk.ID, Input{Notes: S("## Steps\n\n- [x] check\n- [x] pay\n")})
	mustUpdate(t, b, blocker.ID, Input{Status: Done})
	r := mustUpdate(t, b, tk.ID, Input{Status: Done})
	eq(t, r.Rearmed, true)
	got := mustGet(t, b, tk.ID)
	eq(t, got.Status, Todo)
	eq(t, got.Due, "2026-11-26")
	eq(t, got.Done, "")
	eq(t, len(got.BlockedBy), 0)
	eq(t, strings.Contains(got.Notes, "[x]"), false)
	log := must(b.LogOf(ctx, got))
	eq(t, len(log.Lines), 1)
	eq(t, log.Lines[0].Text, "done on 2026-10-26")
	eq(t, len(must(b.List(ctx))), 2) // no second task is made
	mustUpdate(t, b, tk.ID, Input{Notes: S("## Steps\n\n- [x] check\n- [x] pay\n")})
	eq(t, mustUpdate(t, b, tk.ID, Input{Status: Done}).Task.Due, "2026-12-26")
}

func TestRepeatFromDoneCountsFromTheDayItWasDoneDueDoesNotDrift(t *testing.T) {
	b := newBook(t)
	a := mustAdd(t, b, Input{Title: "A", Due: S("2026-10-26"), Repeat: S("FREQ=WEEKLY"), RepeatFrom: S("done")})
	c := mustAdd(t, b, Input{Title: "C", Due: S("2026-10-26"), Repeat: S("FREQ=WEEKLY")})
	b.clockAt("2026-10-30T10:00:00")
	eq(t, mustUpdate(t, b, a.ID, Input{Status: Done}).Task.Due, "2026-11-06")
	eq(t, mustUpdate(t, b, c.ID, Input{Status: Done}).Task.Due, "2026-11-02") // the first Monday after the 30th
}

func TestARulePastItsUntilClosesTheTaskForGoodAnOldWordIsStoredAsTheRuleItMeant(t *testing.T) {
	b := newBook(t)
	last := mustAdd(t, b, Input{Title: "Last", Due: S("2026-10-26"), Repeat: S("FREQ=DAILY;UNTIL=20261026")})
	r := mustUpdate(t, b, last.ID, Input{Status: Done})
	eq(t, r.Rearmed, false)
	eq(t, r.Task.Status, Done)
	old := mustAdd(t, b, Input{Title: "Old", Due: S("2026-01-31"), Repeat: S("monthly")})
	b.clockAt("2026-01-31T10:00:00")
	r2 := mustUpdate(t, b, old.ID, Input{Status: Done})
	eq(t, r2.Task.Due, "2026-02-28")
	eq(t, r2.Task.Repeat, "FREQ=MONTHLY;BYMONTHDAY=-1")
}

func TestRepeatIsCheckedABadRuleNoDayRepeatFromAloneClearingRepeatClearsRepeatFrom(t *testing.T) {
	b := newBook(t)
	_, err := b.Add(ctx, Input{Title: "x", Due: S("2026-10-26"), Repeat: S("FREQ=WEEKLY;COUNT=3")})
	wantErr(t, err, "COUNT")
	_, err = b.Add(ctx, Input{Title: "x", Repeat: S("FREQ=DAILY")})
	wantErr(t, err, "first day")
	tk := mustAdd(t, b, Input{Title: "x", Due: S("2026-10-26"), Repeat: S("FREQ=DAILY"), RepeatFrom: S("done")})
	eq(t, tk.RepeatFrom, "done")
	u := mustUpdate(t, b, tk.ID, Input{Repeat: S("")}).Task
	eq(t, u.Repeat+u.RepeatFrom, "")
}

func TestClosingATaskWithAStepOpenIsRefusedTickingTheLastStepDoesNotCloseItDroppingIsFree(t *testing.T) {
	b := newBook(t)
	tk := mustAdd(t, b, Input{Title: "Task", Notes: S("## Steps\n\n- [ ] a\n- [ ] b")})
	_, err := b.Update(ctx, tk.ID, Input{Status: Done}, "")
	wantErr(t, err, "2 steps are still open")
	_, err = b.UpdateWith(ctx, tk.ID, func(cur Task) (Input, error) {
		return Input{Notes: S(SetStep(SetStep(cur.Notes, 0, Ticked(true)), 1, Ticked(true)))}, nil
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, mustGet(t, b, tk.ID).Status, Todo) // the last tick does not close it
	eq(t, mustUpdate(t, b, tk.ID, Input{Status: Done}).Task.Status, Done)
	// an open promoted step is an open task
	p := mustAdd(t, b, Input{Title: "Parent", Notes: S("## Steps\n\n- [ ] x")})
	sub := must(b.Promote(ctx, p.ID, []int{0})).Promoted[0]
	_, err = b.Update(ctx, p.ID, Input{Status: Done}, "")
	wantErr(t, err, "still open")
	mustUpdate(t, b, sub.ID, Input{Status: Done})
	eq(t, mustUpdate(t, b, p.ID, Input{Status: Done}).Task.Status, Done)
	d := mustAdd(t, b, Input{Title: "Dropped", Notes: S("## Steps\n\n- [ ] a")})
	eq(t, mustUpdate(t, b, d.ID, Input{Status: Dropped}).Task.Status, Dropped)
}

func TestPromoteATaskPartOfTheParentTheStepStaysAsALinkAndCountsWhenThatTaskCloses(t *testing.T) {
	b := newBook(t)
	write(t, b, "progetti/acme.md", page("Acme"))
	p := mustAdd(t, b, Input{Title: "Release", Project: S("acme"), Notes: S("## Steps\n\n- [ ] scrivere\n- [ ] provare")})
	r := must(b.Promote(ctx, p.ID, []int{1}))
	sub := r.Promoted[0]
	eq(t, sub.Title, "provare")
	eq(t, sub.Parent, p.ID)
	eq(t, sub.Project, "acme")
	eq(t, strings.Contains(r.Task.Notes, "- [ ] provare → [["+sub.ID+"]]"), true)
	eq(t, ProgressOf(r.Task.Notes, func(string) bool { return false }).Done, 0)
	_, err := b.Promote(ctx, p.ID, []int{1})
	wantErr(t, err, "already")
	_, err = b.Promote(ctx, p.ID, []int{7})
	wantErr(t, err, "no step")
	mustUpdate(t, b, sub.ID, Input{Status: Done})
	if pr := ProgressOf(mustGet(t, b, p.ID).Notes, b.closedFn(ctx)); *pr != (Progress{1, 2, 50}) {
		t.Errorf("%+v", pr)
	}
	// a step too long for a title is told, not cut
	long := mustAdd(t, b, Input{Title: "Long", Notes: S("## Steps\n\n- [ ] " + strings.Repeat("x", 90))})
	_, err = b.Promote(ctx, long.ID, []int{0})
	wantErr(t, err, "too long")
}

func TestClosingTheServerWritesTheDiaryLineWithTheTasksLinkOnce(t *testing.T) {
	b := newBook(t)
	var lines []string
	b.Diary = func(_ context.Context, c Closed, _ time.Time) error {
		how := "completata"
		if !c.Done {
			how = "lasciata cadere"
		}
		lines = append(lines, "[["+c.ID+"]] "+c.Title+": "+how)
		return nil
	}
	tk := mustAdd(t, b, Input{Title: "Spedire"})
	mustUpdate(t, b, tk.ID, Input{Status: Done})
	if !reflect.DeepEqual(lines, []string{"[[" + tk.ID + "]] Spedire: completata"}) {
		t.Errorf("%v", lines)
	}
	// a second write of the same task closes nothing again
	mustUpdate(t, b, tk.ID, Input{Labels: L("x")})
	eq(t, len(lines), 1)
	d := mustAdd(t, b, Input{Title: "Lasciare"})
	mustUpdate(t, b, d.ID, Input{Status: Dropped})
	eq(t, lines[1], "[["+d.ID+"]] Lasciare: lasciata cadere")
	// a diary that refuses does not undo the close
	b.Diary = func(_ context.Context, _ Closed, _ time.Time) error { return errors.New("no") }
	e := mustAdd(t, b, Input{Title: "Chiudere"})
	eq(t, mustUpdate(t, b, e.ID, Input{Status: Done}).Task.Status, Done)
}

func TestTheLogIsAnEpisodePagePartOfTheTaskNotATaskNotInTheIndex(t *testing.T) {
	b := newBook(t)
	tk := mustAdd(t, b, Input{Title: "Con log"})
	must(b.Log(ctx, tk.ID, "primo\ncon due righe", "2026-10-26"))
	must(b.Log(ctx, tk.ID, "secondo", "2026-10-27"))
	doc := must(b.Store.Get(ctx, LogPath(tk.ID)))
	eq(t, string(doc.Kind), "episode")
	if got := logLines(doc.Body); !reflect.DeepEqual(got, []string{"2026-10-26 · primo con due righe", "2026-10-27 · secondo"}) {
		t.Errorf("%v", got)
	}
	eq(t, strings.Contains(doc.Body, "- part_of [["+tk.ID+"]]"), true)
	links := must(b.Store.Links(ctx, LogPath(tk.ID), nil))
	eq(t, len(links.Out), 1)
	eq(t, links.Out[0].Rel+":"+links.Out[0].Target, "part_of:"+tk.ID)
	// the task page has no log section; the book finds the lines
	eq(t, strings.Contains(mustGet(t, b, tk.ID).Notes, "Log"), false)
	lv := must(b.LogOf(ctx, mustGet(t, b, tk.ID)))
	eq(t, lv.Lines[0].Text, "primo con due righe")
	eq(t, lv.Lines[1].Day, "2026-10-27")
	// it is not a task
	eq(t, len(must(b.List(ctx))), 1)
	if got := must(Query(ctx, b.DB, Filter{Me: "alice"})); !reflect.DeepEqual(got, []string{tk.ID}) {
		t.Errorf("%v", got)
	}
	var n int
	b.DB.QueryRow("select count(*) from task_index").Scan(&n)
	eq(t, n, 1)
	// the memory lists it as an episode, outside the pages' list
	eps := must(b.Store.List(ctx, "tasks/", memory.ListOpts{Kind: memory.KindEpisode}))
	eq(t, len(eps), 1)
	eq(t, eps[0].Path, LogPath(tk.ID))
	must(b.Log(ctx, tk.ID, "terzo", "2026-10-28"))
	// removing the task's log leaves no stale index row for the episode
	if err := b.Store.Remove(ctx, LogPath(tk.ID), "t"); err != nil {
		t.Fatal(err)
	}
	if got := must(Query(ctx, b.DB, Filter{Me: "alice"})); !reflect.DeepEqual(got, []string{tk.ID}) {
		t.Errorf("%v", got)
	}
}
func TestAWriteMovesTheLogOfThatTaskAloneAndKeepsWhatTheEpisodeHas(t *testing.T) {
	b := newBook(t)
	write(t, b, "tasks/t-1.md", oldFile("t-1", nil, "## Log\n\n- 2026-10-01 · vecchio"))
	must(b.Log(ctx, "t-1", "nuovo", "2026-10-26"))
	if got := logLines(must(b.Store.Get(ctx, LogPath("t-1"))).Body); !reflect.DeepEqual(got, []string{"2026-10-01 · vecchio", "2026-10-26 · nuovo"}) {
		t.Errorf("%v", got)
	}
	// the notes an editor sends back without a log leave the episode as it is
	mustUpdate(t, b, "t-1", Input{Notes: S("Solo testo.")})
	eq(t, len(logLines(must(b.Store.Get(ctx, LogPath("t-1"))).Body)), 2)
}

func TestTheProjectItsSlugFindsTheProjectsPageInTheProjectsAreaAndStaysTheSlugInTheJSON(t *testing.T) {
	b := newBook(t)
	write(t, b, "progetti/work/acme/site.md", page("Site"))
	tk := mustAdd(t, b, Input{Title: "Uno", Project: S("work/acme/site")})
	eq(t, tk.Project, "work/acme/site")
	eq(t, strings.Contains(must(b.Store.Get(ctx, "tasks/"+tk.ID+".md")).Body, "- part_of [[progetti/work/acme/site]]"), true)
	eq(t, len(must(Query(ctx, b.DB, Filter{Me: "alice", Project: "work/acme"}))), 1)
	links := must(b.Store.Links(ctx, "tasks/"+tk.ID+".md", nil))
	eq(t, links.Out[0].Path, "progetti/work/acme/site.md")
	// an editor that sends the slug back changes nothing
	eq(t, mustUpdate(t, b, tk.ID, Input{Project: S("work/acme/site")}).Task.Project, tk.Project)
	// no page for it: the slug is kept as it was
	eq(t, mustAdd(t, b, Input{Title: "Due", Project: S("senza/pagina")}).Project, "senza/pagina")
	// a page of that exact path elsewhere is the one
	write(t, b, "work/acme.md", page("Acme"))
	a := mustAdd(t, b, Input{Title: "Tre", Project: S("work/acme")})
	eq(t, strings.Contains(must(b.Store.Get(ctx, "tasks/"+a.ID+".md")).Body, "- part_of [[work/acme]]"), true)
}

func TestResolversReportAPageListThatFailsInsteadOfGuessing(t *testing.T) {
	b := newBook(t)
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.peopleResolver(gone); err == nil {
		t.Error("a people resolver built on a failed list must say so")
	}
	if _, err := b.projectResolver(gone)("work/site"); err == nil {
		t.Error("a project resolver on a failed list must say so")
	}
}

// A page in the old shape, with its owner, project, parent, blocked_by and a log, is read as before and saved in the new one.
func TestAnOldShapePageIsReadWithItsFieldsAndSavedInTheNewShapeKeepingThemAndMovingTheLog(t *testing.T) {
	b := newBook(t)
	write(t, b, "tasks/t-1.md", oldFile("t-1", [][2]string{{"owner", `"bob"`}, {"project", `"work/acme"`}, {"parent", `"t-0"`}, {"blocked_by", `["t-2"]`}},
		"Testo.\n\n## Log\n\n- 2026-10-01 · uno"))
	tk := mustGet(t, b, "t-1")
	eq(t, tk.Owner+"|"+tk.Project+"|"+tk.Parent+"|"+strings.Join(tk.BlockedBy, ","), "bob|work/acme|t-0|t-2")
	eq(t, strings.Join(roles(&tk), ","), "part_of:acme,part_of:t-0,blocked_by:t-2,assigned_to:bob")
	mustUpdate(t, b, "t-1", Input{Labels: L("x")})
	text := must(b.Store.Get(ctx, "tasks/t-1.md")).Body
	for _, want := range []string{`kind: "task"`, "- part_of [[work/acme]]", "- part_of [[t-0]]", "- blocked_by [[t-2]]", "- assigned_to name:bob"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	eq(t, strings.Contains(text, "## Log"), false)
	if got := logLines(must(b.Store.Get(ctx, LogPath("t-1"))).Body); !reflect.DeepEqual(got, []string{"2026-10-01 · uno"}) {
		t.Errorf("%v", got)
	}
}

// The old owner equal to the account is the default, not an assignment: the book and the index read it with the
// account's id exactly as FromFile does, so a page that says `owner: alice` on alice's account is assigned to no one.
func TestAnOldShapePageOwnedByTheAccountIsAssignedToNoOneInTheBookAndInTheIndex(t *testing.T) {
	b := newBook(t)
	old := oldFile("t-1", [][2]string{{"owner", `"alice"`}, {"project", `"work/acme"`}, {"parent", `"t-0"`}, {"blocked_by", `["t-2"]`}}, "Testo.")
	const want = "part_of:acme,part_of:t-0,blocked_by:t-2"
	write(t, b, "tasks/t-1.md", old)

	got := must(b.List(ctx))
	eq(t, len(got), 1)
	eq(t, strings.Join(roles(&got[0]), ","), want)
	eq(t, strings.Join(roles(func() *Task { tk := mustGet(t, b, "t-1"); return &tk }()), ","), want)

	assigned := func(label string) {
		t.Helper()
		var n int
		if err := b.DB.QueryRow("select count(*) from task_rels where id = 't-1' and rel = 'assigned_to'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		var col sql.NullString
		if err := b.DB.QueryRow("select assigned_to from task_index where id = 't-1'").Scan(&col); err != nil {
			t.Fatal(err)
		}
		if n != 0 || col.Valid {
			t.Errorf("%s: the account's own old owner became an assignment (%d rels, column %q)", label, n, col.String)
		}
	}
	// IndexTask
	tx, err := b.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (Index{Me: "alice"}).IndexTask(ctx, tx, "tasks/t-1.md", old); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assigned("IndexTask")
	// Reindex, through the book and the index itself
	eq(t, must(b.Reindex(ctx)), 1)
	assigned("Book.Reindex")
	eq(t, must((Index{Me: "alice"}).Reindex(ctx, b.DB)), 1)
	assigned("Index.Reindex")

	// and with no account the same page does assign it, so the checks above can fail
	eq(t, must((Index{}).Reindex(ctx, b.DB)), 1)
	var n int
	b.DB.QueryRow("select count(*) from task_rels where id = 't-1' and rel = 'assigned_to'").Scan(&n)
	eq(t, n, 1)
}
