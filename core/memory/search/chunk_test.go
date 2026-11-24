package search

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func texts(ps []piece) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.text)
	}
	return out
}

func TestChunkFrontMatterDroppedAndEmptyGivesNothing(t *testing.T) {
	if got := chunk("---\na: 1\n---\n", defaultSize, ""); got != nil {
		t.Fatalf("got %v", got)
	}
	want := []piece{{text: "hello"}}
	if got := chunk("---\na: 1\n---\nhello", defaultSize, ""); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestChunkNewHeadingStartsAPieceAndCarriesItsHeading(t *testing.T) {
	got := chunk("intro\n\n## One\n\nfirst\n\n## Two\n\nsecond", defaultSize, "")
	want := []piece{
		{text: "intro"},
		{text: "## One\n\nfirst", heading: "One", context: "One"},
		{text: "## Two\n\nsecond", heading: "Two", context: "Two"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestChunkParagraphsGatheredLongOneCutHeadingRepeated(t *testing.T) {
	long := strings.Repeat("x", 25)
	got := chunk("# H\n\naaaa\n\nbbbb\n\n"+long, 12, "")
	want := []string{"# H\n\naaaa", "# H\nbbbb", "# H\n" + strings.Repeat("x", 12), "# H\n" + strings.Repeat("x", 12), "# H\nx"}
	if !reflect.DeepEqual(texts(got), want) {
		t.Fatalf("got %q", texts(got))
	}
	for _, p := range got {
		if p.heading != "H" {
			t.Fatalf("heading %q", p.heading)
		}
	}
}

func TestChunkBreadcrumbIsTitleAndHeadingsAbove(t *testing.T) {
	got := chunk("intro\n\n# Guide\n\na\n\n## Install\n\nb\n\n### Linux\n\nc\n\n## Use\n\nd\n\n# Other\n\ne", defaultSize, "Docs")
	var ctxs []string
	for _, p := range got {
		ctxs = append(ctxs, p.context)
	}
	want := []string{"", "Docs › Guide", "Docs › Guide › Install", "Docs › Guide › Install › Linux", "Docs › Guide › Use", "Docs › Other"}
	if !reflect.DeepEqual(ctxs, want) {
		t.Fatalf("got %q", ctxs)
	}
}

func TestChunkCodeFenceHashIsNotAHeading(t *testing.T) {
	got := chunk("# H\n\n```\nx\n\n# comment\n```", defaultSize, "")
	if len(got) != 1 || got[0].heading != "H" {
		t.Fatalf("got %v", got)
	}
}

func TestTail(t *testing.T) {
	for _, c := range []struct {
		text string
		n    int
		want string
	}{
		{"One two. Three four five. Six seven eight nine.", 30, "Six seven eight nine."},
		{"alpha beta gamma delta epsilon", 14, "delta epsilon"},
		{"alpha beta gamma delta epsilon", 13, "delta epsilon"},
		{"alpha beta gamma delta epsilon", 10, "epsilon"},
		{"short", 10, ""},
		{"abcdefghijklmnopqrstuvwxyz", 5, ""},
	} {
		if got := tail(c.text, c.n); got != c.want {
			t.Errorf("tail(%q, %d) = %q, want %q", c.text, c.n, got, c.want)
		}
	}
}

func para(w string) string { return fmt.Sprintf("%s one. %s two. %s three. %s four.", w, w, w, w) }

func TestChunkPieceOpensWithEndOfPreviousUnderOneHeading(t *testing.T) {
	got := chunk("# H\n\n"+para("alpha")+"\n\n"+para("beta")+"\n\n"+para("gamma"), 60, "")
	want := []string{"# H\n\n" + para("alpha"), "# H\nfour.\n\n" + para("beta"), "# H\nfour.\n\n" + para("gamma")}
	if !reflect.DeepEqual(texts(got), want) {
		t.Fatalf("got %q", texts(got))
	}
}

func TestChunkNoOverlapAcrossAHeading(t *testing.T) {
	got := chunk("# A\n\n"+para("alpha")+"\n\n# B\n\n"+para("beta"), 60, "")
	want := []string{"# A\n\n" + para("alpha"), "# B\n\n" + para("beta")}
	if !reflect.DeepEqual(texts(got), want) {
		t.Fatalf("got %q", texts(got))
	}
}

func TestChunkHeadingRightAfterTextStartsAPiece(t *testing.T) {
	got := chunk("intro\n## One\nfirst", defaultSize, "")
	want := []piece{{text: "intro"}, {text: "## One\nfirst", heading: "One", context: "One"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestChunkCRLFHeadingsAreHeadings(t *testing.T) {
	got := chunk("intro\r\n\r\n## One\r\n\r\nfirst", defaultSize, "")
	want := []piece{{text: "intro"}, {text: "## One\n\nfirst", heading: "One", context: "One"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestChunkLongParagraphOpeningWithHeadingDoesNotRepeatIt(t *testing.T) {
	got := chunk("## One\n"+strings.Repeat("word ", 10), 20, "")
	if len(got) < 2 {
		t.Fatalf("got %q", texts(got))
	}
	if n := strings.Count(got[0].text, "## One"); n != 1 {
		t.Fatalf("heading appears %d times in %q", n, got[0].text)
	}
	if !strings.HasPrefix(got[1].text, "## One\n") {
		t.Fatalf("second piece lost its heading: %q", got[1].text)
	}
}

func TestChunkFenceHashAfterTextIsNotAHeading(t *testing.T) {
	got := chunk("# H\n\ntext\n```\n# comment\n```", defaultSize, "")
	if len(got) != 1 {
		t.Fatalf("got %q", texts(got))
	}
}
