package mcp

import (
	"fmt"
	"strings"
	"testing"

	"kedalio/core/tasks"
)

const dayHead = "# 2026-11-10\n\nCosa è successo il martedì 10 novembre 2026.\n"

func closing(id string, done bool) tasks.Closed {
	return tasks.Closed{ID: id, Title: "Title of " + id, Done: done}
}

// titles is what the tasks of these tests are called: the title a lone closure line must carry to be folded.
func titles(id string) string { return "Title of " + id }

func TestOneClosureIsTheLoneLineAsItAlwaysWas(t *testing.T) {
	got := addClosure(dayHead, "10:22", closing("t-a", true), true, titles)
	if want := dayHead + "- 10:22 [[t-a]] Title of t-a: completata\n"; got != want {
		t.Errorf("%q", got)
	}
	got = addClosure(dayHead, "10:22", closing("t-a", false), false, titles)
	if want := dayHead + "- 10:22 [[t-a]] Title of t-a: dropped\n"; got != want {
		t.Errorf("%q", got)
	}
}

func TestTwoClosuresBecomeOneGroupedLineWithLinksOnly(t *testing.T) {
	body := addClosure(dayHead, "10:22", closing("t-a", true), true, titles)
	body = addClosure(body, "10:22", closing("t-b", false), true, titles)
	if want := dayHead + "- 10:22 chiuse 2 task: 1 completate, 1 lasciate cadere — [[t-a]], [[t-b]]\n"; body != want {
		t.Errorf("%q", body)
	}
	if strings.Contains(body, "Title of") {
		t.Error("a title was kept")
	}
	en := addClosure(addClosure(dayHead, "09:00", closing("t-a", true), false, titles), "09:05", closing("t-b", true), false, titles)
	if want := dayHead + "- 09:00–09:05 closed 2 tasks: 2 done — [[t-a]], [[t-b]]\n"; en != want {
		t.Errorf("%q", en)
	}
}

func TestSixtyThreeClosuresKeepEveryLinkAndBothCounts(t *testing.T) {
	body := dayHead
	var links []string
	for i := 0; i < 63; i++ {
		id := fmt.Sprintf("t-%02d", i)
		links = append(links, "[["+id+"]]")
		body = addClosure(body, fmt.Sprintf("10:%02d", 22+i/4), closing(id, i < 41), true, titles)
	}
	want := dayHead + "- 10:22–10:37 chiuse 63 task: 41 completate, 22 lasciate cadere — " + strings.Join(links, ", ") + "\n"
	if body != want {
		t.Errorf("%q", body)
	}
	if n := strings.Count(body, "\n- "); n != 1 {
		t.Errorf("%d lines", n)
	}
	if errs := entryErrors(entryText(strings.Split(strings.TrimSpace(body), "\n")[3])); len(errs) > 0 {
		t.Errorf("the grouped line breaks the entry rules: %v", errs)
	}
}

func TestAClosureAfterTheThirtyMinutesStartsItsOwnLine(t *testing.T) {
	body := addClosure(dayHead, "10:00", closing("t-a", true), true, titles)
	// 30 minutes after the end: still the same line
	in := addClosure(body, "10:30", closing("t-b", true), true, titles)
	if want := dayHead + "- 10:00–10:30 chiuse 2 task: 2 completate — [[t-a]], [[t-b]]\n"; in != want {
		t.Errorf("%q", in)
	}
	// 31: a line of its own
	out := addClosure(body, "10:31", closing("t-b", true), true, titles)
	if want := body + "- 10:31 [[t-b]] Title of t-b: completata\n"; out != want {
		t.Errorf("%q", out)
	}
	// the window runs from the end of a grouped line
	more := addClosure(in, "11:00", closing("t-c", false), true, titles)
	if want := dayHead + "- 10:00–11:00 chiuse 3 task: 2 completate, 1 lasciate cadere — [[t-a]], [[t-b]], [[t-c]]\n"; more != want {
		t.Errorf("%q", more)
	}
}

func TestOtherLinesBetweenDoNotStopTheGrouping(t *testing.T) {
	body := addClosure(dayHead, "10:00", closing("t-a", true), false, titles)
	body = addDiaryLine(body, "10:05", "Called [[people/bob]]")
	body = addClosure(body, "10:10", closing("t-b", true), false, titles)
	want := dayHead + "- 10:00–10:10 closed 2 tasks: 2 done — [[t-a]], [[t-b]]\n- 10:05 Called [[people/bob]]\n"
	if body != want {
		t.Errorf("%q", body)
	}
	// a closure line too old is not reached past a fresh line, nor is a line that is not a closure taken for one
	old := dayHead + "- 08:00 [[projects/x]] Plan: done\n"
	got := addClosure(old, "08:10", closing("t-a", true), false, titles)
	if want := old + "- 08:10 [[t-a]] Title of t-a: done\n"; got != want {
		t.Errorf("%q", got)
	}
}

func TestAClosureLineOfEitherLanguageIsRead(t *testing.T) {
	body := dayHead + "- 10:00 [[t-a]] Title of t-a: lasciata cadere\n"
	got := addClosure(body, "10:01", closing("t-b", true), false, titles)
	if want := dayHead + "- 10:00–10:01 closed 2 tasks: 1 done, 1 dropped — [[t-a]], [[t-b]]\n"; got != want {
		t.Errorf("%q", got)
	}
}

func TestAnAgentLineThatLooksLikeAClosureIsNotFolded(t *testing.T) {
	// the words «Wrote the spec» are not the title of t-abc: they are the agent's, and stay a line of their own
	body := dayHead + "- 10:00 [[t-abc]] Wrote the spec: done\n"
	got := addClosure(body, "10:05", closing("t-b", true), false, titles)
	if want := body + "- 10:05 [[t-b]] Title of t-b: done\n"; got != want {
		t.Errorf("%q", got)
	}
	// a grouped-looking line whose count does not match its links is not the server's either
	odd := dayHead + "- 10:00 closed 9 tasks: 9 done — [[t-a]], [[t-b]]\n"
	if got := addClosure(odd, "10:05", closing("t-c", true), false, titles); got != odd+"- 10:05 [[t-c]] Title of t-c: done\n" {
		t.Errorf("%q", got)
	}
}

func TestClosingTheSameTaskAgainCountsItOnce(t *testing.T) {
	body := addClosure(dayHead, "10:00", closing("t-a", true), false, titles)
	body = addClosure(body, "10:01", closing("t-b", true), false, titles)
	again := addClosure(body, "10:02", closing("t-b", true), false, titles)
	if want := dayHead + "- 10:00–10:02 closed 2 tasks: 2 done — [[t-a]], [[t-b]]\n"; again != want {
		t.Errorf("%q", again)
	}
	// a lone line of the task closed again is folded without counting it twice
	lone := addClosure(dayHead, "10:00", closing("t-a", true), false, titles)
	if got := addClosure(lone, "10:01", closing("t-a", true), false, titles); got != dayHead+"- 10:00–10:01 closed 1 tasks: 1 done — [[t-a]]\n" {
		t.Errorf("%q", got)
	}
}
