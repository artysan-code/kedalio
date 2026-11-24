package tasks

import (
	"slices"
	"testing"
)

func TestJoinLinesAddsAfterAndLeavesOutWhatIsThereAlready(t *testing.T) {
	have := []string{"a", "b"}
	got := joinLines(have, []string{"b", "c", "c2"})
	if want := []string{"a", "b", "c", "c2"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if !slices.Equal(have, []string{"a", "b"}) {
		t.Errorf("the input was changed: %v", have)
	}
	if got := joinLines(nil, nil); len(got) != 0 {
		t.Errorf("nothing and nothing: %v", got)
	}
}
