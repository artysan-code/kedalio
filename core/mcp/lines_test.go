package mcp

import (
	"errors"
	"strings"
	"testing"

	"kedalio/core/tasks"
)

func TestTaskLineShowsRefTitleAndOnlyTheWhenItWasAskedFor(t *testing.T) {
	task := tasks.Task{ID: "t-1", Ref: "TASK-4", Title: "Pay the bill", Due: "2026-01-05", Time: "09:30"}
	for _, c := range []struct {
		withDay bool
		t       tasks.Task
		want    string
	}{
		{true, task, "t-1 TASK-4 Pay the bill · 2026-01-05 09:30"},
		{false, task, "t-1 TASK-4 Pay the bill · 09:30"},
		{true, tasks.Task{ID: "t-2", Title: "Call Alice"}, "t-2 Call Alice"},
		{true, tasks.Task{ID: "t-3", Title: "Plan", Due: "2026-02-01"}, "t-3 Plan · 2026-02-01"},
	} {
		if got := taskLine(c.t, c.withDay); got != c.want {
			t.Errorf("taskLine(withDay=%v) = %q, want %q", c.withDay, got, c.want)
		}
	}
}

func TestEditedCardReplacesAPassageThatOccursOnceAndRefusesTheOthers(t *testing.T) {
	got, err := editedCard("# Card\n\nstatus: open\n", "open", "closed")
	if err != nil || got != "# Card\n\nstatus: closed\n" {
		t.Errorf("got %q, %v", got, err)
	}
	for name, find := range map[string]string{"absent": "nothing", "twice": "a"} {
		_, err := editedCard("a card with a and a", find, "x")
		var ce *callError
		if !errors.As(err, &ce) || ce.issues[0].Field != "find" {
			t.Errorf("%s: err = %v, want a refusal on find", name, err)
		}
	}
}

func TestClosureTextCountsAndLinksInBothLanguages(t *testing.T) {
	links := []string{"[[t-1]]", "[[t-2]]", "[[t-3]]"}
	for _, c := range []struct {
		done, dropped int
		italian       bool
		want          string
	}{
		{2, 1, false, "closed 3 tasks: 2 done, 1 dropped — [[t-1]], [[t-2]], [[t-3]]"},
		{3, 0, false, "closed 3 tasks: 3 done — [[t-1]], [[t-2]], [[t-3]]"},
		{0, 3, true, "chiuse 3 task: 3 lasciate cadere — [[t-1]], [[t-2]], [[t-3]]"},
		{1, 2, true, "chiuse 3 task: 1 completate, 2 lasciate cadere — [[t-1]], [[t-2]], [[t-3]]"},
	} {
		if got := closureText(c.done, c.dropped, links, c.italian); got != c.want {
			t.Errorf("closureText(%d, %d, italian=%v) = %q, want %q", c.done, c.dropped, c.italian, got, c.want)
		}
	}
	if got := closureText(1, 0, links[:1], false); !strings.HasPrefix(got, "closed 1 tasks") {
		t.Errorf("one task: %q", got)
	}
}
