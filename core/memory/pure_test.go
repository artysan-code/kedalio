package memory

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"kedalio/core/memory/search"
)

func TestPrefixUnitsCutsInUTF16UnitsAndDropsAPairCutInHalf(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 0, ""},
		{"abc", -1, ""},
		{"abc", 2, "ab"},
		{"abc", 9, "abc"},
		{"a😀b", 1, "a"},
		{"a😀b", 2, "a"}, // the emoji is two units: it does not fit in the second
		{"a😀b", 3, "a😀"},
		{"è😀", 1, "è"}, // a two-byte letter is one unit
		{"😀", 1, ""},
	} {
		if got := prefixUnits(c.in, c.n); got != c.want {
			t.Errorf("prefixUnits(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestWordMatcherFindsPhrasesThatStandAloneAndNotInsideAWord(t *testing.T) {
	m := newWordMatcher("oggi|today|la settimana scorsa")
	for _, c := range []struct {
		in   string
		want [][2]int
	}{
		{"", nil},
		{"oggi", [][2]int{{0, 4}}},
		{"Today and TODAY", [][2]int{{0, 5}, {10, 15}}},
		{"todayish", nil},
		{"mytoday", nil},
		{"oggi, oggi.", [][2]int{{0, 4}, {6, 10}}},
		{"la settimana scorsa ieri", [][2]int{{0, 19}}},
		{"è oggi", [][2]int{{3, 7}}}, // offsets are bytes: è takes two
		{"àoggi", nil},               // a letter before it, even a non-ASCII one
	} {
		if got := m.find(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("find(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if got := m.replace("ieri oggi o today", "X"); got != "ieri X o X" {
		t.Errorf("replace = %q", got)
	}
	if got := m.replace("nothing here", "X"); got != "nothing here" {
		t.Errorf("replace without a match = %q", got)
	}
}

func TestPassageOfCitesADocumentResultAndIsNilForAPage(t *testing.T) {
	c := search.Citation{Text: "The pump runs at 2.5 bar.", Context: "Manual › Pump", Position: 3,
		At: search.Locator{Page: 4}}
	if p := PassageOf(search.Result{Path: "notes/a.md", Title: "A"}, c, 100); p != nil {
		t.Errorf("a page has no passage: %+v", p)
	}
	r := search.Result{Path: "doc:d1", Title: "Manual", Document: &search.DocumentRef{
		ID: "d1", Collection: "Docs", Origin: "manual.pdf", Version: "2", Superseded: true}}
	want := &Passage{Title: "Manual", Origin: "manual.pdf", Text: c.Text, Collection: "Docs", Document: "d1",
		Version: "2", Superseded: true, Section: "Manual › Pump", At: search.Locator{Page: 4}, Chunk: 3}
	if got := PassageOf(r, c, 100); !reflect.DeepEqual(got, want) {
		t.Errorf("PassageOf = %+v", got)
	}
	long := search.Citation{Text: strings.Repeat("The pump runs. ", 100)}
	got := PassageOf(r, long, 20)
	if Tokens(got.Text) > 20 || !strings.HasSuffix(got.Text, ".") {
		t.Errorf("cut to the budget at a sentence: %q", got.Text)
	}
}

func TestRefreshSimilarTakesAtMostARoundOfPagesAtATime(t *testing.T) {
	s, ix := recallStore(t)
	total := simPerRound + 5
	for i := 0; i < total; i++ {
		write(t, s, fmt.Sprintf("note/p%03d.md", i), pageOf("P", fmt.Sprintf("Page number %d about subject%d.", i, i)))
	}
	e := &fakeEmbedder{model: "fake"}
	embedAll(t, ix, e)
	if n := refresh(t, s, e.model); n != simPerRound {
		t.Errorf("first round = %d, want %d", n, simPerRound)
	}
	if n := refresh(t, s, e.model); n != total-simPerRound {
		t.Errorf("second round = %d, want %d", n, total-simPerRound)
	}
	if n := refresh(t, s, e.model); n != 0 {
		t.Errorf("third round = %d, want 0", n)
	}
}
