package mcp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"kedalio/core/memory"
)

func TestSlugPath(t *testing.T) {
	for in, want := range map[string]string{
		"Progetti/Città Nuova.md": "progetti/citta-nuova.md",
		"/notes//Some  Page":      "notes/some-page.md",
		`a\B\C.MD`:                "a/b/c.md",
		"me/Ünï–cöde ✓":           "me/uni-code.md",
	} {
		if got := slugPath(in); got != want {
			t.Errorf("slugPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSimilarityAndNamedIn(t *testing.T) {
	if similarity("Project Alpha", "alpha project notes") != 1 || similarity("Cats", "Dogs") != 0 {
		t.Error("similarity")
	}
	pages := []memory.Info{
		{Path: "people/bob-smith.md", Title: "Bob Smith"},
		{Path: "people/bob.md", Title: "Bob"},
		{Path: "notes/app.md", Title: "App"},
	}
	if got := namedIn("Bob Smith called about the app", pages); got != "people/bob-smith.md" {
		t.Errorf("most specific = %q", got)
	}
	if got := namedIn("the app is slow", pages); got != "" {
		t.Errorf("a short title is a word, not a name: %q", got)
	}
}

func TestAddNote(t *testing.T) {
	got := addNote("# P\n\nLede.\n\n## Notes\n\n- old\n\n## Other\n\nx\n", "- new", "## Notes")
	if want := "# P\n\nLede.\n\n## Notes\n\n- old\n- new\n\n## Other\n\nx\n"; got != want {
		t.Errorf("got %q", got)
	}
	if got := addNote("# P\n\nLede.\n", "- new", "## Notes"); got != "# P\n\nLede.\n\n## Notes\n\n- new\n" {
		t.Errorf("no section: %q", got)
	}
}

func TestAddDiaryLineJoinsTheSamePages(t *testing.T) {
	day := "# d\n\nLede.\n- 10:00 Fixed the build of [[projects/site]]\n"
	got := addDiaryLine(day, "10:30", "Deployed [[projects/site]]")
	if want := "# d\n\nLede.\n- 10:00–10:30 Fixed the build of [[projects/site]]; Deployed [[projects/site]]\n"; got != want {
		t.Errorf("merged = %q", got)
	}
	if got := addDiaryLine(day, "10:30", "Called [[people/bob]]"); !strings.HasSuffix(got, "- 10:30 Called [[people/bob]]\n") || strings.Count(got, "\n- ") != 2 {
		t.Errorf("other pages = %q", got)
	}
	if got := addDiaryLine(day, "13:00", "Deployed [[projects/site]]"); strings.Count(got, "\n- ") != 2 {
		t.Errorf("past the window = %q", got)
	}
	if got := addDiaryLine(day, "10:30", "no links"); strings.Count(got, "\n- ") != 2 {
		t.Errorf("no links never merges = %q", got)
	}
}

func TestDiaryRewriteAndEntryErrors(t *testing.T) {
	cur := "# d\n\nLede.\n- 10:00 a\n- 11:00 b\n"
	if e := diaryRewriteErrors(cur, "# d\n\nLede, tidier.\n- 10:00 a\n- 11:00 b\n"); len(e) != 0 {
		t.Errorf("tidy: %v", e)
	}
	if e := diaryRewriteErrors(cur, "# d\n\nLede.\n- 10:00 a\n"); len(e) != 1 {
		t.Errorf("dropped line: %v", e)
	}
	if e := entryErrors(strings.Repeat("w ", 41)); len(e) != 1 {
		t.Errorf("long line: %v", e)
	}
	if e := entryErrors("see [[" + strings.Repeat("w ", 50) + "]]"); len(e) != 0 {
		t.Errorf("links are not counted: %v", e)
	}
}

func TestWithoutOrphanWhysAndCleanWhy(t *testing.T) {
	body := "# Inbox\n\n  why: orphan\n- 2026-11-03 10:00 one\n  why: a\n  why: b\n- 2026-11-03 10:05 two\n"
	got := withoutOrphanWhys(body)
	if want := "# Inbox\n\n- 2026-11-03 10:00 one\n  why: a\n- 2026-11-03 10:05 two\n"; got != want {
		t.Errorf("got %q", got)
	}
	if cleanWhy("see [[a/b|the page]] and [[c]]\nnow") != "see the page and c now" {
		t.Error("cleanWhy")
	}
}

func TestSchemaValidation(t *testing.T) {
	sc := object([]string{"op"}, choice("op", []string{"a", "b"}), integer("n", 1, 3), nullable(str("s")), ints("xs"))
	issues := sc.validate(map[string]any{"op": "c", "n": json_n("9"), "s": nil, "xs": []any{json_n("-1")}})
	var fields []string
	for _, i := range issues {
		fields = append(fields, i.Field)
	}
	if !slices.Equal(fields, []string{"op", "n", "xs.0"}) {
		t.Errorf("issues = %v", issues)
	}
	if i := sc.validate(map[string]any{}); len(i) != 1 || i[0].Problem != "missing" {
		t.Errorf("missing = %v", i)
	}
}

func json_n(s string) json.Number { return json.Number(s) }
