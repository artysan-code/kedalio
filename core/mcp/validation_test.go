package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// A validation error of the task book is written for the client: it has to read it, not "failed inside the core".
func TestTaskValidationErrorsReachTheClient(t *testing.T) {
	s := newRig(t, nil).session(t)
	open := mustCall(t, s, "tasks_save", map[string]any{"title": "Parent", "steps_add": []string{"one"}})
	id := fmt.Sprint(open["id"])
	mustCall(t, s, "tasks_save", map[string]any{"id": id, "notes": "alpha beta alpha"})

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"long title", map[string]any{"title": strings.Repeat("long title words ", 20)}, "characters, at most"},
		{"time without due", map[string]any{"title": "Pay", "time": "09:30"}, "a time needs a day"},
		{"steps still open", map[string]any{"id": id, "status": "done"}, "still open"},
		{"passage not found", map[string]any{"id": id, "find": "missing", "replace": "x"}, "not in the notes"},
		{"passage twice", map[string]any{"id": id, "find": "alpha", "replace": "x"}, "occurs 2 times"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, failed := call(t, s, "tasks_save", c.args)
			if !failed {
				t.Fatalf("not refused: %v", out)
			}
			if got := fmt.Sprint(out); !strings.Contains(got, c.want) || strings.Contains(got, "failed inside the core") {
				t.Fatalf("want %q in the refusal, got %v", c.want, out)
			}
		})
	}
}
