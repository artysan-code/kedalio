package tasks

import (
	"fmt"
	"strings"
	"testing"

	"kedalio/protocol"
)

// the cap of 4000 is for the notes alone: the parts and the log of a busy task must not eat it.
func TestGetReturnsLongNotesWholeNextToPartsAndLogLines(t *testing.T) {
	b := newBook(t)
	notes := strings.Repeat("word ", 300) // 1500 characters
	parent := mustAdd(t, b, Input{Title: "Parent", Notes: S(notes)})
	var parts []Task
	for i := range 14 {
		parts = append(parts, mustAdd(t, b, Input{Title: fmt.Sprintf("Part number %02d with a long enough title", i), Parent: S(parent.ID)}))
	}
	for _, p := range parts[:4] {
		save(t, b, protocol.TasksSave{ID: p.ID, Status: "done"})
	}
	for i := range 8 {
		must(b.Log(ctx, parent.ID, fmt.Sprintf("log line %d: moved the work one small step along", i), "2026-10-26"))
	}
	a := must(b.GetOf(ctx, protocol.TasksGet{ID: parent.ID}))
	if a.NotesCut != nil || strings.TrimSpace(a.Task.Notes) != strings.TrimSpace(notes) {
		t.Errorf("notes cut to %d characters of %d: %+v", len(a.Task.Notes), len(notes), a.NotesCut)
	}
	eq(t, a.PartsDone, "4/14")
	eq(t, a.PartsMore, 0)
	if len(a.Parts) != 10 {
		t.Errorf("parts = %d lines, want the first 10 of the 10 open", len(a.Parts))
	}
	for _, p := range a.Parts {
		if strings.HasPrefix(p, "done · ") {
			t.Errorf("a done part is listed: %s", p)
		}
	}
	eq(t, a.Log.Total, 8)
	eq(t, len(a.Log.Last), 3)
	// the log argument still raises the lines
	eq(t, len(must(b.GetOf(ctx, protocol.TasksGet{ID: parent.ID, Log: 6})).Log.Last), 6)
}

func TestGetCountsTheOpenPartsItDoesNotList(t *testing.T) {
	b := newBook(t)
	parent := mustAdd(t, b, Input{Title: "Parent"})
	for i := range 13 {
		mustAdd(t, b, Input{Title: fmt.Sprintf("Part number %02d", i), Parent: S(parent.ID)})
	}
	a := must(b.GetOf(ctx, protocol.TasksGet{ID: parent.ID}))
	eq(t, len(a.Parts), 10)
	eq(t, a.PartsMore, 3)
}
