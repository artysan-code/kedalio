package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"kedalio/core/memory"
)

func TestClearNullsTurnsANullIntoTheEmptyValueOfItsField(t *testing.T) {
	got := string(clearNulls([]byte(`{"id":"t-1","priority":null,"labels":null,"blocked_by":null,"due":null,"title":"x"}`)))
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"id": `"t-1"`, "priority": "0", "labels": "[]", "blocked_by": "[]", "due": `""`, "title": `"x"`}
	for k, v := range want {
		if string(m[k]) != v {
			t.Errorf("%s = %s, want %s", k, m[k], v)
		}
	}
	if raw := `not json`; string(clearNulls([]byte(raw))) != raw {
		t.Error("arguments that are not an object are left as they are")
	}
	if raw := `{"a":1}`; string(clearNulls([]byte(raw))) != raw {
		t.Error("without a null nothing changes")
	}
}

func TestCleanSessionPrefsKeepsAValidShapeAndRefusesTheRest(t *testing.T) {
	for _, c := range []struct{ name, in, want, problem string }{
		{"null inherits", `null`, `null`, ""},
		{"trims the steps", `{"mode":"work","start":["  read the card  "]}`, `{"mode":"work","start":["read the card"]}`, ""},
		{"empty object", `{}`, `{}`, ""},
		{"unknown mode", `{"mode":"sleep"}`, "", "invalid option"},
		{"unknown field", `{"modes":"work"}`, "", ""},
		{"nine steps", `{"end":["a","b","c","d","e","f","g","h","i"]}`, "", "at most 8 steps"},
		{"blank step", `{"start":["ok","   "]}`, "", "a step says something"},
		{"long step", `{"end":["` + strings.Repeat("x", 201) + `"]}`, "", "a step says something"},
		{"200 characters is fine", `{"end":["` + strings.Repeat("è", 200) + `"]}`, `{"end":["` + strings.Repeat("è", 200) + `"]}`, ""},
	} {
		got, err := cleanSessionPrefs(json.RawMessage(c.in))
		if c.want == "" {
			var ce *callError
			if !errors.As(err, &ce) {
				t.Errorf("%s: err = %v, want a refusal", c.name, err)
			} else if c.problem != "" && !strings.Contains(ce.issues[0].Problem, c.problem) {
				t.Errorf("%s: problem %q", c.name, ce.issues[0].Problem)
			}
			continue
		}
		if err != nil || string(got) != c.want {
			t.Errorf("%s: %s, %v", c.name, got, err)
		}
	}
}

func TestCleanEnvironmentsNeedsANameOnceAndAKindOfTwo(t *testing.T) {
	for _, c := range []struct {
		name, in string
		ok       bool
	}{
		{"null", `null`, true},
		{"empty list", `[]`, true},
		{"two", `[{"name":"local","kind":"development"},{"name":"live","kind":"production","match":{"branches":["main"]}}]`, true},
		{"same name twice", `[{"name":"a","kind":"development"},{"name":"a","kind":"production"}]`, false},
		{"blank name", `[{"name":"  ","kind":"development"}]`, false},
		{"another kind", `[{"name":"a","kind":"staging"}]`, false},
		{"not a list", `{"name":"a","kind":"development"}`, false},
		{"unknown field", `[{"name":"a","kind":"development","x":1}]`, false},
	} {
		_, err := cleanEnvironments(json.RawMessage(c.in))
		var ce *callError
		if c.ok != (err == nil) || (err != nil && !errors.As(err, &ce)) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}

func TestKnownFixNamesTheNextStepWhereTheErrorIsKnown(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{memory.ErrChanged, "base_rev"},
		{fmt.Errorf("wrapped: %w", memory.ErrChanged), "base_rev"},
		{context.DeadlineExceeded, "call it again"},
		{errors.New("no task t-1"), "tasks_get"},
		{errors.New("no collection x"), "memory_list"},
		{errors.New("no document a.md"), "memory_recall"},
		{errors.New("no version 3"), "history"},
		{errors.New("not a document path: x"), "folder/name.md"},
	} {
		if got, ok := knownFix(c.err); !ok || !strings.Contains(got, c.want) {
			t.Errorf("knownFix(%v) = %q, want it to mention %q", c.err, got, c.want)
		}
	}
}

func TestCappedKeepsListMaxLinesTheLastSayingHowManyWereLeftOut(t *testing.T) {
	mk := func(n int) []string {
		var l []string
		for i := 0; i < n; i++ {
			l = append(l, fmt.Sprint("line", i))
		}
		return l
	}
	if got := capped(mk(listMax)); len(got) != listMax || got[listMax-1] != fmt.Sprint("line", listMax-1) {
		t.Errorf("at the limit: %v", got)
	}
	got := capped(mk(listMax + 4))
	if len(got) != listMax || got[listMax-2] != fmt.Sprint("line", listMax-2) || got[listMax-1] != "+5 more" {
		t.Errorf("over the limit: %v", got)
	}
	if got := capped(nil); len(got) != 0 {
		t.Errorf("none: %v", got)
	}
}

func TestLongDateSaysTheDayInEitherLanguage(t *testing.T) {
	d := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if got := longDate(d, true); got != "martedì 3 novembre 2026" {
		t.Errorf("italian: %q", got)
	}
	if got := longDate(d, false); got != "Tuesday 3 November 2026" {
		t.Errorf("english: %q", got)
	}
	if got := longDate(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), false); got != "Friday 1 January 2027" {
		t.Errorf("year end: %q", got)
	}
}

func TestPagesFirstPutsTasksAndDiaryLast(t *testing.T) {
	isLog := func(p string) bool { return strings.HasPrefix(p, "tasks/") || strings.HasPrefix(p, "diario/") }
	got := pagesFirst([]string{"diario/2026-01-01.md", "notes/a.md", "tasks/t-1.md", "people/b.md"}, isLog)
	want := []string{"notes/a.md", "people/b.md", "diario/2026-01-01.md", "tasks/t-1.md"}
	if !slices.Equal(got, want) {
		t.Errorf("pagesFirst = %v", got)
	}
}
