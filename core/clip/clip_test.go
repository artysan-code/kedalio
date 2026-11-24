package clip

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWordsCutsOnAWordBoundaryWithAnEllipsisAndLeavesShortTextAlone(t *testing.T) {
	if got := Words("short  text\nhere", 50); got != "short text here" {
		t.Errorf("short = %q", got)
	}
	if got := Words("alpha beta gamma delta epsilon", 14); got != "alpha beta…" {
		t.Errorf("cut = %q", got)
	}
	if got := Words("àèìòù àèìòù àèìòù", 8); got != "àèìòù…" {
		t.Errorf("multibyte = %q", got)
	}
	if got := Words("unbrokenwordthatislong", 8); got != "unbroken…" {
		t.Errorf("one long word = %q", got)
	}
}

const doc = "# Top\n\nintro\n\n## A\n\nalpha\n\n```\n## not a heading\n```\n\n### A1\n\nsub\n\n## B\n\nbeta\n"

func TestHeadingsSkipCodeFences(t *testing.T) {
	var got []string
	for _, h := range Headings(doc) {
		got = append(got, h.Title)
	}
	if strings.Join(got, "|") != "Top|A|A1|B" {
		t.Fatalf("headings = %v", got)
	}
}

func TestSectionRunsToTheNextHeadingOfTheSameOrHigherLevel(t *testing.T) {
	body, off, ok := Section(doc, "a")
	if !ok || !strings.HasPrefix(body, "## A\n") || !strings.Contains(body, "### A1") || strings.Contains(body, "## B") {
		t.Fatalf("section = %q ok=%v", body, ok)
	}
	if doc[off:off+4] != "## A" {
		t.Errorf("offset = %d", off)
	}
	if b, _, ok := Section(doc, "## B"); !ok || b != "## B\n\nbeta\n" {
		t.Errorf("B = %q %v", b, ok)
	}
	if _, _, ok := Section(doc, "nothing"); ok {
		t.Error("a missing section was found")
	}
}

func TestSectionByPartOfTheTitleOnlyWhenUnique(t *testing.T) {
	text := "## Steps one\n\nx\n\n## Steps two\n\ny\n\n## Decisions\n\nz\n"
	if _, _, ok := Section(text, "steps"); ok {
		t.Error("an ambiguous part of a title was taken")
	}
	if b, _, ok := Section(text, "decis"); !ok || !strings.Contains(b, "z") {
		t.Errorf("unique part = %q %v", b, ok)
	}
}

func TestWindowCutsAtALineAndSaysWhereTheRestStarts(t *testing.T) {
	text := strings.Repeat("0123456789\n", 10) // 110 characters
	w, c := Window(text, 0, 55)
	if !c.Cut || c.Total != 110 || len(w) != 55 || c.Next != 55 {
		t.Fatalf("window = %d chars, %+v", len(w), c)
	}
	w, c = Window(text, 0, 50)
	if !c.Cut || !strings.HasSuffix(w, "\n") || c.Next != len(w) {
		t.Fatalf("cut at a line: %q %+v", w, c)
	}
	rest, c2 := Window(text, c.Next, 0)
	if c2.Cut || w+rest != text {
		t.Errorf("the rest does not complete the text: %+v", c2)
	}
	if w, c := Window("short", 0, 100); w != "short" || c.Cut {
		t.Errorf("short = %q %+v", w, c)
	}
	if w, c := Window("héllo wörld", 6, 0); w != "wörld" || c.Offset != 6 {
		t.Errorf("offset counts characters: %q %+v", w, c)
	}
	if w, c := Window("abc", 99, 10); w != "" || c.Offset != 3 {
		t.Errorf("past the end = %q %+v", w, c)
	}
}

func TestWindowJSONCountsTheTextAsSent(t *testing.T) {
	text := strings.Repeat("a \"b\"\n", 500)
	w, c := WindowJSON(text, 0, 1000)
	enc, _ := json.Marshal(w)
	if len(enc)-2 > 1000 || !c.Cut || len(enc)-2 < 700 {
		t.Fatalf("a window of %d sent characters, cut=%v", len(enc)-2, c.Cut)
	}
}

// a text shorter than max in characters but longer as sent has no cut to retry: it still has to shrink to fit
func TestWindowJSONShrinksATextThatFitsInCharactersButNotAsSent(t *testing.T) {
	text := strings.Repeat("a \"b\"\n", 500) // 3000 characters, 4500 as sent
	w, c := WindowJSON(text, 0, 4000)
	enc, _ := json.Marshal(w)
	if len(enc)-2 > 4000 || !c.Cut {
		t.Fatalf("a window of %d sent characters, cut=%v, want at most 4000 and a cut", len(enc)-2, c.Cut)
	}
}

func TestWindowJSONNeverShrinksToZeroAndReturnsTheWholeText(t *testing.T) {
	text := strings.Repeat("\n", 50) + "tail"
	piece, c := WindowJSON(text, 0, 1)
	if len([]rune(piece)) != 1 || !c.Cut {
		t.Errorf("piece %q, cut %+v: want one character, cut", piece, c)
	}
}
