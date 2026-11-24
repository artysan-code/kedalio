package upkeep

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"kedalio/core/db"
)

func open(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestMigrateFromEmptyAndIdempotent(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"upkeep_runs", "upkeep_dismissed"} {
		if _, err := d.ExecContext(ctx, "select count(*) from "+table); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
	}
}

func TestStartRefusesWhileARunIsOpenAndExpiresAfterTwoHours(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	t0 := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	id, err := Start(ctx, d, "weeks", t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(ctx, d, "logs", t0.Add(119*time.Minute)); !errors.Is(err, ErrOpen) {
		t.Fatalf("second start within 2 h = %v, want ErrOpen", err)
	}
	id2, err := Start(ctx, d, "logs", t0.Add(2*time.Hour+time.Minute))
	if err != nil || id2 == id {
		t.Fatalf("start after 2 h = %q, %v", id2, err)
	}
	if err := End(ctx, d, id2, "ok", "done", t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(ctx, d, "hubs", t0.Add(3*time.Hour)); err != nil {
		t.Fatalf("start after End: %v", err)
	}
	if err := End(ctx, d, "nope", "ok", "", t0); err == nil {
		t.Fatal("End of an unknown run succeeded")
	}
}

func TestLastAndDismissed(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if r, err := Last(ctx, d); r != nil || err != nil {
		t.Fatalf("Last on empty = %v, %v", r, err)
	}
	t0 := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	id, _ := Start(ctx, d, "weeks", t0)
	if err := End(ctx, d, id, "partial", "two of three", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	r, err := Last(ctx, d)
	if err != nil || r == nil || r.ID != id || r.Job != "weeks" || r.Status != "partial" || r.Report != "two of three" || r.Ended == "" {
		t.Fatalf("Last = %+v, %v", r, err)
	}
	for _, k := range []string{"a|b", "a|b", "c|d"} {
		if err := Dismiss(ctx, d, "duplicates", k, t0); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Dismissed(ctx, d, "duplicates")
	if err != nil || !reflect.DeepEqual(got, map[string]bool{"a|b": true, "c|d": true}) {
		t.Fatalf("Dismissed = %v, %v", got, err)
	}
	if other, _ := Dismissed(ctx, d, "conflicts"); len(other) != 0 {
		t.Fatalf("conflicts = %v", other)
	}
}

func TestLost(t *testing.T) {
	const before = `---
title: Venus
---
# Venus

The server runs the nightly backup at three.
- Uses restic
- ok

## Relations
- uses [[tools/restic]]
`
	cases := []struct {
		name, after string
		kept        []string
		want        int
	}{
		{"everything kept", "# Venus\nThe server runs the nightly backup at three.\n- Uses restic\n\n## Relations\n- uses [[restic]]", nil, 0},
		{"reworded sentence is lost", "The server does a nightly backup at three.\n- Uses restic\n- uses [[restic]]", nil, 1},
		{"moved to kept_in survives", "- Uses restic\n- uses [[restic]]", []string{"Backups\nThe server runs the nightly backup at three."}, 0},
		{"accents, links and Markdown normalised",
			"**The server** runs the nightly   backup at three.\n* _Uses_ [[restic]]\n[[tools/restic]]", nil, 0},
		{"relation target must stay", "The server runs the nightly backup at three.\n- Uses restic", nil, 1},
		{"relation target in kept_in", "The server runs the nightly backup at three.\n- Uses restic", []string{"[[restic]]"}, 0},
	}
	for _, c := range cases {
		if got := Lost(before, c.after, c.kept); len(got) != c.want {
			t.Errorf("%s: Lost = %q, want %d", c.name, got, c.want)
		}
	}
}

func TestLostNormalisesAccentsAndLabels(t *testing.T) {
	before := "La città è già pronta per la partenza.\nSee [[venus|the Venus host]] for the plan today."
	after := "la citta e gia pronta per la partenza.\nsee the venus host for the plan today."
	if got := Lost(before, after, nil); len(got) != 0 {
		t.Fatalf("Lost = %q", got)
	}
}

func TestLostIgnoresHeadingsFrontMatterAndShortFragments(t *testing.T) {
	before := "---\ntitle: A long front matter title here\n---\n## A long heading that says many words\nShort bit.\n- ok\n- part_of [[venus]] with more words here\nThree word line.\n"
	if got := Lost(before, "", nil); len(got) != 0 {
		t.Fatalf("Lost = %q, want none", got)
	}
}

func TestLostSplitsSentencesOfALine(t *testing.T) {
	before := "First sentence has enough words. Second sentence has enough words too."
	got := Lost(before, "First sentence has enough words.", nil)
	if !reflect.DeepEqual(got, []string{"Second sentence has enough words too."}) {
		t.Fatalf("Lost = %q", got)
	}
}

func day(d string, lines, sum bool) Day {
	return Day{Path: "diary/" + d + ".md", Day: d, HasLines: lines, Summarised: sum}
}

func TestDueWeeks(t *testing.T) {
	today := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	days := []Day{
		day("2026-10-12", true, false), // W42, Sunday 10-18: 8 days ago, due
		day("2026-10-10", true, false), // W41, Sunday 10-11
		day("2026-10-11", false, false),
		day("2026-10-05", true, true),  // W41... W40 summarised
		day("2026-10-19", true, false), // W43, Sunday 10-25: 1 day ago
		day("bogus", true, false),
	}
	got := DueWeeks(days, today, 0)
	if len(got) != 2 || got[0].Key != "2026-W41" || got[1].Key != "2026-W42" || got[1].End != "2026-10-18" {
		t.Fatalf("DueWeeks = %+v", got)
	}
	if got := DueWeeks(days, today, 1); len(got) != 1 || got[0].Key != "2026-W41" {
		t.Fatalf("max 1 = %+v", got)
	}
	// ended exactly 7 days ago is due, 6 is not
	if got := DueWeeks([]Day{day("2026-10-12", true, false)}, time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC), 0); len(got) != 1 {
		t.Fatalf("7 days: %+v", got)
	}
	if got := DueWeeks([]Day{day("2026-10-12", true, false)}, time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC), 0); len(got) != 0 {
		t.Fatalf("6 days: %+v", got)
	}
}

func TestDueWeeksIgnoresSummarisedAndEmptyDays(t *testing.T) {
	today := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if got := DueWeeks([]Day{day("2026-10-12", true, true), day("2026-10-13", false, false)}, today, 0); len(got) != 0 {
		t.Fatalf("DueWeeks = %+v", got)
	}
}

func TestISOWeekEdgesAcrossYears(t *testing.T) {
	for _, c := range []struct{ day, key, end string }{
		{"2026-10-12", "2026-W42", "2026-10-18"},
		{"2026-10-18", "2026-W42", "2026-10-18"},
		{"2027-01-01", "2026-W53", "2027-01-03"},
		{"2024-12-30", "2025-W01", "2025-01-05"},
		{"2021-01-03", "2020-W53", "2021-01-03"},
	} {
		key, end, err := ISOWeek(c.day)
		if err != nil || key != c.key || end != c.end {
			t.Errorf("ISOWeek(%s) = %s, %s, %v; want %s, %s", c.day, key, end, err, c.key, c.end)
		}
	}
	days := []Day{day("2027-01-01", true, false), day("2027-01-03", true, false), day("2026-12-31", true, false)}
	got := DueWeeks(days, time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC), 0)
	if len(got) != 1 || got[0].Key != "2026-W53" || len(got[0].Days) != 3 || got[0].Days[0].Day != "2026-12-31" {
		t.Fatalf("DueWeeks = %+v", got)
	}
}

func TestEndOfAnEndedRunIsRefusedAndKeepsTheReport(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	now := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	id, err := Start(ctx, d, "hubs", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := End(ctx, d, id, "ok", "first", now); err != nil {
		t.Fatal(err)
	}
	if err := End(ctx, d, id, "failed", "second", now); err == nil {
		t.Fatal("an ended run was ended again")
	}
	if r, _ := Last(ctx, d); r.Status != "ok" || r.Report != "first" {
		t.Errorf("run = %+v", r)
	}
}

func TestLostDoesNotMatchInsideAWordOrNumber(t *testing.T) {
	before := "The service listens on port 80 for plain requests.\n"
	if got := Lost(before, "The service listens on port 8080 for plain requests.\n", nil); len(got) != 1 {
		t.Errorf("a changed number counted as kept: %v", got)
	}
	if got := Lost(before, "Note: The service listens on port 80 for plain requests. (see above)\n", nil); len(got) != 0 {
		t.Errorf("a sentence inside a longer text was lost: %v", got)
	}
}

func TestRecentIsNewestFirstAndCapped(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if rs, err := Recent(ctx, d, 10); len(rs) != 0 || err != nil {
		t.Fatalf("Recent on empty = %v, %v", rs, err)
	}
	t0 := time.Date(2026, 11, 14, 2, 30, 0, 0, time.UTC)
	var ids []string
	for i, job := range []string{"conflicts", "duplicates", "hubs"} {
		at := t0.Add(time.Duration(i) * time.Minute)
		id, err := Start(ctx, d, job, at)
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if err := End(ctx, d, id, "ok", job+" done", at.Add(30*time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, id)
	}
	rs, err := Recent(ctx, d, 2)
	if err != nil || len(rs) != 2 {
		t.Fatalf("Recent(2) = %v, %v", rs, err)
	}
	if rs[0].ID != ids[2] || rs[0].Ended != "" || rs[0].Status != "" {
		t.Errorf("newest = %+v, want the open hubs run", rs[0])
	}
	if rs[1].ID != ids[1] || rs[1].Status != "ok" || rs[1].Report != "duplicates done" {
		t.Errorf("second = %+v", rs[1])
	}
}
