package memory

import (
	"reflect"
	"strings"
	"testing"
)

func TestHealthOrphansBrokenLinksAndLongPagesTheDiaryAndEntryPointsExcused(t *testing.T) {
	s, _ := memStore(t)
	for path, body := range map[string]string{
		"notes/hub.md":     "# Hub\n\nSee [[notes/linked]] and [[notes/missing]].",
		"notes/linked.md":  "# Linked\n\nA page.",
		"notes/alone.md":   "# Alone\n\nNothing points here.",
		"notes/long.md":    "# Long\n\n" + strings.Repeat("word ", 30),
		"diary/2026-01.md": "# Diary\n\n- 09:00 saw [[notes/gone]]",
		"me/me.md":         "# Me\n\nEntry point.",
	} {
		if _, err := s.Write(ctx, path, body, "test"); err != nil {
			t.Fatal(err)
		}
	}
	h, err := s.Health(ctx, AreasEN, func(string) int { return 20 }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.Pages != 6 {
		t.Errorf("pages = %d", h.Pages)
	}
	if want := []string{"notes/alone.md", "notes/hub.md", "notes/long.md"}; !reflect.DeepEqual(h.Orphans, want) {
		t.Errorf("orphans = %v", h.Orphans)
	}
	if want := []BrokenLink{{"notes/hub.md", "notes/missing"}}; !reflect.DeepEqual(h.BrokenLinks, want) {
		t.Errorf("broken = %v", h.BrokenLinks)
	}
	if len(h.TooLong) != 1 || h.TooLong[0].Page != "notes/long.md" {
		t.Errorf("long = %v", h.TooLong)
	}
}
