package mcp

import (
	"testing"
	"time"
)

// With the marking off the days carry no `summarised:`, so a week whose page exists was due every night. It is due
// when its page is missing or a day changed after the page was written.
func TestUpkeepWeekIsDueOnlyWhileItsPageIsMissingOrOlderThanADay(t *testing.T) {
	r, _ := upkeepRig(t, false)
	s := r.session(t)
	day := func(d, line string) {
		t.Helper()
		if _, err := r.store.Write(ctx, "diary/"+d+".md", "# "+d+"\n\nWhat happened.\n\n- 10:00 "+line+"\n", "owner"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Millisecond)
	}
	due := func() []string {
		t.Helper()
		var keys []string
		for _, it := range mustCall(t, s, "memory_upkeep", map[string]any{"op": "worklist", "job": "weeks"})["items"].([]any) {
			keys = append(keys, it.(map[string]any)["key"].(string))
		}
		return keys
	}
	day("2026-10-05", "something happened")
	day("2026-10-07", "something else happened")
	if got := due(); len(got) != 1 || got[0] != "2026-W41" {
		t.Fatalf("no page yet: %v", got)
	}
	week := "# 2026-W41\n\nThe week.\n\n- One thing.\n- Another thing.\n- A third thing.\n"
	if _, err := r.store.Write(ctx, "diary/weeks/2026-w41.md", week, "upkeep"); err != nil {
		t.Fatal(err)
	}
	if got := due(); len(got) != 0 {
		t.Fatalf("the page is newer than its days: %v", got)
	}
	time.Sleep(3 * time.Millisecond)
	d, _ := r.store.Get(ctx, "diary/2026-10-07.md")
	if _, err := r.store.WriteAt(ctx, d.Path, d.Body+"- 18:00 a late line\n", "owner", d.Rev); err != nil {
		t.Fatal(err)
	}
	if got := due(); len(got) != 1 || got[0] != "2026-W41" {
		t.Fatalf("a day changed after the page: %v", got)
	}
}
