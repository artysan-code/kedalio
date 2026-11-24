package tasks

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func task(over func(*Task)) Task {
	t := Task{ID: "t-x", Title: "x", Status: Todo, Created: "2026-09-29T08:00:00.000Z", Updated: "2026-09-29T08:00:00.000Z"}
	if over != nil {
		over(&t)
	}
	return t
}

// oldFile is a task file as the old shape wrote it: the fields in the front matter, no kind, no relations section.
func oldFile(id string, fields [][2]string, notes string) string {
	var extra strings.Builder
	for _, f := range fields {
		fmt.Fprintf(&extra, "%s: %s\n", f[0], f[1])
	}
	return fmt.Sprintf("---\nid: %q\ntitle: \"Task %s\"\nstatus: \"todo\"\n%screated: \"2026-10-01T08:00:00.000Z\"\nupdated: \"2026-10-02T08:00:00.000Z\"\n---\n\n# Task %s\n\n%s\n",
		id, id, extra.String(), id, notes)
}

func TestFileFormatRoundTripQuotingNotesAfterTheTitle(t *testing.T) {
	in := task(func(t *Task) {
		t.ID, t.Title, t.Due, t.Time, t.Priority = "t-20260930-abcdef", `Chiamare "Mario": fattura`, "2026-10-01", "10:30", 1
		t.Notes = "riga uno\n\nriga due"
	})
	back := FromFile(ToFile(in), "")
	if !reflect.DeepEqual(*back, in) {
		t.Errorf("%+v\nwant %+v", *back, in)
	}
	if FromFile("not a task", "") != nil {
		t.Error("not a task")
	}
}

func TestFromFileCRLFKeepsStepsAndUnknownFrontMatterKeysAreWrittenBack(t *testing.T) {
	crlf := "---\r\nid: \"t-1\"\r\ntitle: \"x\"\r\nstatus: \"todo\"\r\ntags: [\"a\",\"b\"]\r\ncreated: \"c\"\r\nupdated: \"u\"\r\n---\r\n\r\n# x\r\n\r\n- [ ] uno\r\n- [x] due\r\n"
	task := FromFile(crlf, "")
	if p := ProgressOf(task.Notes, nil); *p != (Progress{1, 2, 50}) {
		t.Errorf("%+v", p)
	}
	if !strings.Contains(ToFile(*task), `tags: ["a","b"]`) {
		t.Error(ToFile(*task))
	}
}

func TestFileValuesAreWrittenAsJSONDoesNotEscapeMarkup(t *testing.T) {
	in := task(func(t *Task) { t.Title = "a <b> & c" })
	if !strings.Contains(ToFile(in), `title: "a <b> & c"`) {
		t.Error(ToFile(in))
	}
}

func TestBothShapesReadAsOneTaskTheNewShapeIsWhatIsWritten(t *testing.T) {
	old := oldFile("t-1", [][2]string{{"owner", `"claude"`}, {"project", `"work/acme"`}, {"parent", `"t-0"`}, {"blocked_by", `["t-2","t-3"]`}},
		"Testo.\n\n## Steps\n\n- [ ] passo")
	if strings.Contains(old, "kind:") {
		t.Fatal("old")
	}
	tk := FromFile(old, "")
	eq(t, tk.Owner, "claude")
	eq(t, tk.Project, "work/acme")
	eq(t, tk.Parent, "t-0")
	if !reflect.DeepEqual(tk.BlockedBy, []string{"t-2", "t-3"}) {
		t.Error(tk.BlockedBy)
	}
	if w := WorkedByOf(*tk); w == nil || *w != (Rel{Rel: "worked_by", Kind: KindEngine, Target: "claude"}) {
		t.Errorf("%+v", w)
	}
	eq(t, tk.Notes, "Testo.\n\n## Steps\n\n- [ ] passo")

	text := ToFile(*tk)
	for _, want := range []string{`kind: "task"`, "## Relations", "- worked_by engine:claude", "- part_of [[work/acme]]", "- blocked_by [[t-2]]"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	for _, key := range []string{"owner:", "project:", "parent:", "blocked_by:"} {
		if strings.Contains(strings.ReplaceAll(text, "- blocked_by", ""), "\n"+key) {
			t.Errorf("%s stays in the front matter", key)
		}
	}
	back := FromFile(text, "")
	eq(t, back.Owner+back.Project+back.Parent, tk.Owner+tk.Project+tk.Parent)
	eq(t, back.Notes, tk.Notes)
	eq(t, ToFile(*back), text) // writing it again changes nothing
	wantRels := []Rel{{Rel: "part_of", Kind: KindPage, Target: "work/acme"}, {Rel: "part_of", Kind: KindTask, Target: "t-0"},
		{Rel: "blocked_by", Kind: KindTask, Target: "t-2"}, {Rel: "blocked_by", Kind: KindTask, Target: "t-3"},
		{Rel: "worked_by", Kind: KindEngine, Target: "claude"}}
	if got := RelsOf(*tk); !reflect.DeepEqual(got, wantRels) {
		t.Errorf("%+v", got)
	}
	if got := RelsOf(*back); len(got) != len(wantRels) {
		t.Errorf("a rewritten page keeps its relations: %+v", got)
	}
}
