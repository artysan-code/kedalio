package tasks

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

var stallNow = time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)

func row(id string, over func(*StallRow)) StallRow {
	r := StallRow{ID: id, Title: "Task " + id, Status: Todo, Updated: "2026-10-25T08:00:00.000Z"}
	if over != nil {
		over(&r)
	}
	return r
}

// codes is the reasons found, by task.
func codes(rows []StallRow) map[string][]StallCode {
	out := map[string][]StallCode{}
	for _, s := range Stalled(rows, stallNow, DefaultThresholds, time.UTC) {
		for _, r := range s.Reasons {
			out[s.ID] = append(out[s.ID], r.Code)
		}
	}
	return out
}

func expectCodes(t *testing.T, got, want map[string][]StallCode) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStalledADoingTaskWithNothingWrittenForAWeek(t *testing.T) {
	old := "2026-10-10T08:00:00.000Z"
	expectCodes(t, codes([]StallRow{
		row("idle", func(r *StallRow) { r.Status, r.Updated = Doing, old }),
		row("fresh", func(r *StallRow) { r.Status = Doing }),
		row("engine", func(r *StallRow) { r.Status, r.Updated, r.WorkedBy = Doing, old, "engine:claude" }),
	}), map[string][]StallCode{"idle": {DoingIdle}, "engine": {DoingIdle}})
	// a line in its log counts as activity
	expectCodes(t, codes([]StallRow{row("logged", func(r *StallRow) {
		r.Status, r.Updated, r.Activity = Doing, old, "2026-10-25T08:00:00.000Z"
	})}), map[string][]StallCode{})
}

func TestStalledWaitingPastItsDayOrForNobody(t *testing.T) {
	w := func(id string, f func(*StallRow)) StallRow {
		return row(id, func(r *StallRow) { r.Status = Waiting; f(r) })
	}
	expectCodes(t, codes([]StallRow{
		w("past", func(r *StallRow) { r.WaitingOn = "2026-10-20" }),
		w("future", func(r *StallRow) { r.WaitingOn = "2026-11-20" }),
		w("person", func(r *StallRow) { r.WaitingOn = "bob" }),
		w("nobody", func(r *StallRow) {}),
		w("due-past", func(r *StallRow) { r.Due = "2026-10-01" }),
	}), map[string][]StallCode{
		"past": {WaitingPast}, "nobody": {WaitingNobody}, "due-past": {WaitingPast, WaitingNobody},
	})
}

func TestStalledATodoOverdueByMoreThanTheThresholdNotByExactlyIt(t *testing.T) {
	expectCodes(t, codes([]StallRow{
		row("late", func(r *StallRow) { r.Due = "2026-10-01" }),
		row("edge", func(r *StallRow) { r.Due = "2026-10-12" }),
		row("recent", func(r *StallRow) { r.Due = "2026-10-20" }),
		row("doing-late", func(r *StallRow) { r.Status, r.Due = Doing, "2026-09-01" }),
	}), map[string][]StallCode{"late": {Overdue}})
}

func TestStalledBlockedByADroppedMissingOrCyclicTaskADoneBlockerReleases(t *testing.T) {
	blocked := func(id string, by ...string) StallRow { return row(id, func(r *StallRow) { r.BlockedBy = by }) }
	status := func(id string, s Status) StallRow { return row(id, func(r *StallRow) { r.Status = s }) }
	expectCodes(t, codes([]StallRow{
		blocked("a", "gone"), blocked("b", "dropped"), status("dropped", Dropped),
		blocked("c", "done"), status("done", Done),
		blocked("d", "e"), blocked("e", "f"), blocked("f", "d"),
		blocked("g", "h"), row("h", nil),
	}), map[string][]StallCode{
		"a": {BlockedMissing}, "b": {BlockedDropped}, "d": {BlockedCycle}, "e": {BlockedCycle}, "f": {BlockedCycle},
	})
	// a cycle that runs through a done task is broken: nothing waits
	expectCodes(t, codes([]StallRow{blocked("x", "y"), row("y", func(r *StallRow) { r.Status, r.BlockedBy = Done, []string{"x"} })}),
		map[string][]StallCode{})
}

func TestStalledASnoozeLastsUntilItsDayAndEndsEarlyOnActivityClosedTasksAreNeverStalled(t *testing.T) {
	late := func(r *StallRow) { r.Due = "2026-09-01" }
	snoozed := func(day, at string, extra func(*StallRow)) map[string][]StallCode {
		return codes([]StallRow{row("s", func(r *StallRow) {
			late(r)
			r.Snooze, r.SnoozedAt = day, at
			if extra != nil {
				extra(r)
			}
		})})
	}
	expectCodes(t, snoozed("2026-11-10", "2026-10-25T08:00:00.000Z", nil), map[string][]StallCode{})
	// the day passed
	eq(t, len(snoozed("2026-10-25", "2026-10-20T00:00:00.000Z", nil)), 1)
	// the last day itself still holds
	expectCodes(t, snoozed("2026-10-26", "2026-10-25T08:00:00.000Z", nil), map[string][]StallCode{})
	// written to after the snooze: it is back
	eq(t, len(snoozed("2026-11-10", "2026-10-20T00:00:00.000Z", nil)), 1)
	eq(t, len(snoozed("2026-11-10", "2026-10-25T08:00:00.000Z", func(r *StallRow) { r.Activity = "2026-10-25T12:00:00.000Z" })), 1)
	expectCodes(t, codes([]StallRow{
		row("d", func(r *StallRow) { r.Status, r.Due = Done, "2020-01-01" }),
		row("p", func(r *StallRow) { r.Status = Dropped }),
	}), map[string][]StallCode{})
}

func TestStalledTheThresholdsAreTheAccounts(t *testing.T) {
	rows := []StallRow{row("t-1", func(r *StallRow) { r.Status, r.Updated = Doing, "2026-10-22T08:00:00.000Z" })}
	eq(t, len(Stalled(rows, stallNow, DefaultThresholds, time.UTC)), 0)
	eq(t, len(Stalled(rows, stallNow, Thresholds{3, 14}, time.UTC)), 1)
}

func TestStalledMostReasonsFirstThenById(t *testing.T) {
	got := Stalled([]StallRow{
		row("b", func(r *StallRow) { r.Due = "2026-09-01" }),
		row("a", func(r *StallRow) { r.Due = "2026-09-01" }),
		row("c", func(r *StallRow) { r.Due, r.BlockedBy = "2026-09-01", []string{"gone"} }),
	}, stallNow, DefaultThresholds, time.UTC)
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	eq(t, strings.Join(ids, ","), "c,a,b")
}

func TestStalledOverADatabaseThresholdsInSettingsTheSnoozeOnThePageTheLogAsActivity(t *testing.T) {
	b := newBook(t)
	before := at("2026-10-01T10:00:00")
	b.Now = func() time.Time { return before }
	tk := mustAdd(t, b, Input{Title: "Scrivere la guida", Status: Doing, WorkedBy: S("run:r-1")})
	quiet := mustAdd(t, b, Input{Title: "Una cosa in sospeso", Status: Doing})
	now := at("2026-10-26T10:00:00")
	found := func(at time.Time) map[string][]StallCode {
		out := map[string][]StallCode{}
		for _, s := range must(b.StalledNow(ctx, at)) {
			for _, r := range s.Reasons {
				out[s.ID] = append(out[s.ID], r.Code)
			}
		}
		return out
	}
	expectCodes(t, found(now), map[string][]StallCode{tk.ID: {DoingIdle}, quiet.ID: {DoingIdle}})

	// the thresholds: validated, kept per account
	eq(t, must(LoadThresholds(ctx, b.DB)), DefaultThresholds)
	wantErr(t, SetThresholds(ctx, b.DB, 0, 400), "todoDays is a whole number of days, 1 to 365")
	wantErr(t, SetThresholds(ctx, b.DB, -1, 0), "doingDays is a whole number of days, 1 to 365")
	if err := SetThresholds(ctx, b.DB, 60, 0); err != nil {
		t.Fatal(err)
	}
	eq(t, must(LoadThresholds(ctx, b.DB)), Thresholds{60, 14})
	eq(t, len(found(now)), 0) // 25 days idle is under the 60 days set

	// snooze through the book: it holds, then ends when the task is written to
	b.clockAt("2026-10-26T10:00:00")
	mustUpdate(t, b, tk.ID, Input{Snooze: S("2026-11-30")})
	eq(t, len(found(at("2026-10-27T10:00:00"))), 0)
	b.clockAt("2026-10-27T11:00:00")
	mustUpdate(t, b, tk.ID, Input{Priority: N(1)})
	if err := SetThresholds(ctx, b.DB, 7, 0); err != nil {
		t.Fatal(err)
	}
	expectCodes(t, found(at("2026-11-06T11:00:00")), map[string][]StallCode{tk.ID: {DoingIdle}, quiet.ID: {DoingIdle}})
	// the page keeps the snooze as written
	eq(t, mustGet(t, b, tk.ID).Snooze, "2026-11-30")
	mustUpdate(t, b, tk.ID, Input{Snooze: S("")})
	eq(t, mustGet(t, b, tk.ID).SnoozedAt, "")

	// a line in the log episode beside the task is activity: it was idle for 25 days, and a line written today makes
	// it healthy for three more
	if err := SetThresholds(ctx, b.DB, 7, 0); err != nil {
		t.Fatal(err)
	}
	soon := at("2026-10-29T10:00:00")
	if _, ok := found(soon)[quiet.ID]; !ok {
		t.Fatal("idle")
	}
	b.clockAt("2026-10-26T12:00:00")
	must(b.Log(ctx, quiet.ID, "Una riga.", "2026-10-26"))
	if _, ok := found(soon)[quiet.ID]; ok {
		t.Error("a log line is activity")
	}
}

func TestReasonTextSaysEveryCode(t *testing.T) {
	for _, s := range []Stall{{Code: DoingIdle, Days: 9}, {Code: WaitingPast, On: "2026-01-01", Days: 3},
		{Code: WaitingNobody}, {Code: BlockedDropped, On: "t-1"}, {Code: BlockedMissing, On: "t-1"}, {Code: BlockedCycle},
		{Code: Overdue, On: "2026-01-01", Days: 40}} {
		if txt := ReasonText(s); txt == "" || txt == string(s.Code) {
			t.Errorf("%v: %q", s.Code, txt)
		}
	}
}
