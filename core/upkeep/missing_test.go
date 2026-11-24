package upkeep

import (
	"slices"
	"testing"
)

func TestMissingListsTheSentencesNoPageHolds(t *testing.T) {
	text := "Alice likes tea. Bob likes coffee.\n- Carol walks"
	pages := []string{"# Notes\n\nalice LIKES tea.", "Carol walks to work"}
	got := Missing(text, pages)
	if want := []string{"Bob likes coffee."}; !slices.Equal(got, want) {
		t.Errorf("Missing = %v, want %v", got, want)
	}
	if got := Missing(text, []string{text}); len(got) != 0 {
		t.Errorf("a page that holds it all: %v", got)
	}
	if got := Missing("A lone thing", nil); !slices.Equal(got, []string{"A lone thing"}) {
		t.Errorf("no pages: %v", got)
	}
}
