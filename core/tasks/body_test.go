package tasks

import (
	"reflect"
	"strings"
	"testing"
)

func stepTexts(notes string) []string {
	out := []string{}
	for _, s := range Steps(notes, nil) {
		out = append(out, s.Text)
	}
	return out
}

func TestStepsAreReadAnywhereWithoutASectionProgressTickByIndex(t *testing.T) {
	notes := "Descrizione.\n\n## Steps\n\n- [ ] uno\n- [x] due\n* [X] tre\n\nfine\n- [ ] quattro"
	got := Steps(notes, nil)
	// the section runs to the next heading, so the last line is a step too
	if len(got) != 4 || got[0].Done || !got[1].Done || !got[2].Done || got[3].Done {
		t.Errorf("%+v", got)
	}
	noSection := "- [ ] uno\n- [x] due\n* [X] tre\n- [ ] quattro"
	if p := ProgressOf(noSection, nil); *p != (Progress{2, 4, 50}) {
		t.Errorf("%+v", p)
	}
	if ProgressOf("niente", nil) != nil {
		t.Error("no steps, no progress")
	}
	ticked := SetStep(noSection, 3, nil)
	if !Steps(ticked, nil)[3].Done {
		t.Error("toggled on")
	}
	if Steps(SetStep(ticked, 1, Ticked(false)), nil)[1].Done {
		t.Error("unticked")
	}
	// the rest of the text is left alone
	eq(t, strings.Replace(SetStep(noSection, 0, nil), "- [x] uno", "- [ ] uno", 1), noSection)
}

func TestAddStepMakesTheSectionBeforeTheAttachments(t *testing.T) {
	step := func(notes, text string) string {
		out, err := AddStep(notes, text)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	eq(t, step("", "primo"), "## Steps\n\n- [ ] primo\n")
	eq(t, step("Testo.", "primo"), "Testo.\n\n## Steps\n\n- [ ] primo\n")
	two := step(step("Testo.", "primo"), "secondo")
	if !reflect.DeepEqual(stepTexts(two), []string{"primo", "secondo"}) {
		t.Error(stepTexts(two))
	}
	out := step("Testo.\n\n## Allegati\n\n- [a](https://a.it)\n", "primo")
	if strings.Index(out, "## Steps") > strings.Index(out, "## Allegati") || len(Attachments(out)) != 1 {
		t.Errorf("the steps go before the attachments:\n%s", out)
	}
	if _, err := AddStep("", "  "); err == nil {
		t.Error("a step needs text")
	}
}

func TestAttachmentsUrlsPathsStoredFilesAndPagesAddAndRemove(t *testing.T) {
	n := "Testo."
	for _, a := range [][2]string{
		{"https://docs.google.com/x", "Specifiche"},
		{"/srv/work/acme/site/Documento finale.pdf", ""},
		{"files/t-1/foto.png", ""},
		{"[[projects/claude-multi/visione-assistente]]", ""},
	} {
		n = must(AddAttachment(n, a[0], a[1]))
	}
	want := []Attachment{
		{"Specifiche", "https://docs.google.com/x", "url"},
		{"Documento finale.pdf", "/srv/work/acme/site/Documento finale.pdf", "path"},
		{"foto.png", "files/t-1/foto.png", "file"},
		{"visione-assistente", "projects/claude-multi/visione-assistente", "page"},
	}
	if got := Attachments(n); !reflect.DeepEqual(got, want) {
		t.Errorf("%+v", got)
	}
	other := Attachments("## Allegati\n\n- ~/doc.txt\n\n## Altro\n- non un allegato")
	if len(other) != 1 || other[0].Target != "~/doc.txt" || other[0].Kind != "path" {
		t.Errorf("%+v", other)
	}
}

func TestAttachmentNamesWithParenthesesAndSpacesSurviveLineBreaksAreRefused(t *testing.T) {
	n := must(AddAttachment("", "files/t-1/Screenshot (1).png", ""))
	if got := Attachments(n); !reflect.DeepEqual(got, []Attachment{{"Screenshot (1).png", "files/t-1/Screenshot (1).png", "file"}}) {
		t.Errorf("%+v", got)
	}
	eq(t, Attachments(must(AddAttachment("", "report(1).pdf", "")))[0].Target, "report(1).pdf")
	for _, bad := range []string{"a\nb", "a<b>", " "} {
		if _, err := AddAttachment("", bad, ""); err == nil {
			t.Errorf("%q is refused", bad)
		}
	}
}

func TestNotesAreDatedLinesBeforeTheAttachmentsAndEditNotesReplacesOnePassage(t *testing.T) {
	n := must(AddAttachment("Descrizione", "https://x.test/a", "spec"))
	n = must(AddNote(n, SectionLog, "su dev\ncon la CI verde", "2026-10-05"))
	n = must(AddNote(n, SectionDecisions, "opzione C (Alice)", "2026-10-05"))
	n = must(AddNote(n, SectionLog, "in prod", "2026-10-06"))
	if strings.Index(n, "## Log") > strings.Index(n, "## Attachments") || strings.Index(n, "## Decisions") > strings.Index(n, "## Attachments") {
		t.Errorf("sections go before the attachments:\n%s", n)
	}
	for _, line := range []string{"- 2026-10-05 · su dev con la CI verde", "- 2026-10-06 · in prod", "- 2026-10-05 · opzione C (Alice)"} {
		if !strings.Contains(n, line) {
			t.Errorf("no line %q in:\n%s", line, n)
		}
	}
	eq(t, len(Attachments(n)), 1)
	eq(t, must(EditNotes("a b a", "b", "c")), "a c a")
	_, err := EditNotes("a b a", "a", "c")
	wantErr(t, err, "2 times")
	_, err = EditNotes("a b a", "z", "c")
	wantErr(t, err, "not in the notes")
	// the replacement is text, not a pattern
	eq(t, must(EditNotes("a b", "b", "$&")), "a $&")
}

func TestUntickStepsOpensEveryStepAndMakesAPromotedOneAPlainStep(t *testing.T) {
	got := UntickSteps("Text\n\n## Steps\n\n- [x] one\n- [X] two → [[t-1]]\n- [ ] three\n\n## Attachments\n\n- [x] not a step\n")
	eq(t, got, "Text\n\n## Steps\n\n- [ ] one\n- [ ] two\n- [ ] three\n\n## Attachments\n\n- [x] not a step\n")
}

func TestTidyJoinsStrayCheckboxesOnceEachWithTicksKept(t *testing.T) {
	notes := "- [x] uno\n- [ ] due\n\nDescrizione.\n\n## Steps\n\n- [ ] uno\n- [ ] tre\n\n## Attachments\n\n- [a](http://x)\n"
	if !NeedsTidy(notes) {
		t.Fatal("needs tidy")
	}
	out, log := Tidy(notes)
	eq(t, out, "Descrizione.\n\n## Steps\n\n- [x] uno\n- [ ] tre\n- [ ] due\n\n## Attachments\n\n- [a](http://x)\n")
	eq(t, len(log), 0)
	if NeedsTidy(out) {
		t.Error("tidy now")
	}
	again, _ := Tidy(out)
	eq(t, again, out)
	// the steps already there keep their numbers
	if !reflect.DeepEqual(stepTexts(out), []string{"uno", "tre", "due"}) {
		t.Error(stepTexts(out))
	}
}

func TestTidyWithNoStepsSectionMakesOneAndMergesADuplicate(t *testing.T) {
	out, _ := Tidy("Testo.\n\n- [ ] a\n- [x] a\n\n## Attachments\n\n- [l](http://x)\n")
	eq(t, out, "Testo.\n\n## Steps\n\n- [x] a\n\n## Attachments\n\n- [l](http://x)\n")
	out, _ = Tidy("## Steps\n\n- [ ] a\n- [ ] a\n")
	eq(t, out, "## Steps\n\n- [ ] a\n")
	out, _ = Tidy("niente da fare")
	eq(t, out, "niente da fare")
}

func TestTidyTheLogSectionLeavesAndItsCheckboxesAreLinesNotSteps(t *testing.T) {
	out, log := Tidy("Testo.\n\n## Log\n\n- 2026-10-01 · uno\n- 2026-10-02 · - [ ] non un passo\n\n## Decisions\n\n- d\n")
	if !reflect.DeepEqual(log, []string{"2026-10-01 · uno", "2026-10-02 · - [ ] non un passo"}) {
		t.Error(log)
	}
	eq(t, out, "Testo.\n\n## Decisions\n\n- d\n")
}

func TestProgressCountsStepsOnlyBeforeAndAfterTheMove(t *testing.T) {
	raw := "- [x] uno\n- [ ] due\n\n## Steps\n\n- [x] uno\n- [ ] due\n- [ ] tre\n"
	if p := ProgressOf(raw, nil); *p != (Progress{1, 3, 33}) {
		t.Errorf("%+v", p)
	}
	if !strings.Contains(SetStep(raw, 2, Ticked(true)), "- [x] tre") {
		t.Error("tick by index in Steps")
	}
	if p := ProgressOf("- [x] solo\n- [ ] senza sezione", nil); *p != (Progress{1, 2, 50}) {
		t.Errorf("%+v", p)
	}
}

func TestAPromotedStepIsDoneWhenItsTaskIsClosed(t *testing.T) {
	notes := must(PromoteStep("## Steps\n\n- [ ] uno\n- [ ] due\n", 1, "t-2"))
	eq(t, notes, "## Steps\n\n- [ ] uno\n- [ ] due → [[t-2]]\n")
	if got := Steps(notes, nil); !reflect.DeepEqual(got, []Step{{Text: "uno"}, {Text: "due", Promoted: "t-2"}}) {
		t.Errorf("%+v", got)
	}
	if p := ProgressOf(notes, func(id string) bool { return id == "t-2" }); *p != (Progress{1, 2, 50}) {
		t.Errorf("%+v", p)
	}
	_, err := PromoteStep(notes, 1, "t-3")
	wantErr(t, err, "already")
	_, err = PromoteStep(notes, 5, "t-3")
	wantErr(t, err, "no step")
}

func TestWithoutStepsDropsTheCheckboxesAndTheHeadingWhenNothingElseIsUnderIt(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"no section", "Intro.\n\n- [ ] loose", "Intro.\n\n- [ ] loose"},
		{"only checkboxes: heading goes", "Intro.\n\n## Steps\n\n- [x] a\n- [ ] b", "Intro."},
		{"other lines under the heading stay", "## Steps\n\nwhy these\n- [x] a\n- [ ] b", "## Steps\n\nwhy these"},
		{"the next section is intact", "Intro.\n\n## Steps\n\n- [x] a\n\n## Decisions\n\n- [ ] not a step\nd1", "Intro.\n\n## Decisions\n\n- [ ] not a step\nd1"},
		{"italian heading", "## Passi\n- [ ] a\n## Log\nx", "## Log\nx"},
	}
	for _, c := range cases {
		if got := WithoutSteps(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
