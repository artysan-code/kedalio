package recalleval

import (
	"math"
	"reflect"
	"testing"
)

func TestParseQuestions(t *testing.T) {
	qs, err := ParseQuestions([]byte(`{"questions":[
		{"q":"who is alice","expect":["people/alice.md"],"not":["notes/example.md"]},
		{"q":"bob","expect":[],"mode":"search"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 || qs[0].Not[0] != "notes/example.md" || qs[1].Mode != "search" {
		t.Fatalf("got %+v", qs)
	}
	for _, bad := range []string{`{}`, `{"questions":[{"q":"x"}]}`, `{"questions":[{"expect":[]}]}`,
		`{"questions":[{"q":"x","expect":[],"mode":"both"}]}`, `nope`} {
		if _, err := ParseQuestions([]byte(bad)); err == nil {
			t.Errorf("%s: want an error", bad)
		}
	}
}

func TestModes(t *testing.T) {
	cases := []struct {
		q    Question
		want string
		got  []string
	}{
		{Question{}, "both", []string{"recall", "search"}},
		{Question{}, "search", []string{"search"}},
		{Question{Mode: "search"}, "both", []string{"search"}},
		{Question{Mode: "search"}, "recall", nil},
	}
	for _, c := range cases {
		if got := Modes(c.q, c.want); !reflect.DeepEqual(got, c.got) {
			t.Errorf("%+v %s: got %v, want %v", c.q, c.want, got, c.got)
		}
	}
}

func TestScorePlacesPagesThenRelated(t *testing.T) {
	q := Question{Q: "q", Expect: []string{"people/alice.md", "people/bob.md", "notes/missing.md"}, Not: []string{"notes/example.md", "notes/other.md"}}
	a := Answer{Pages: []string{"notes/example.md", "people/alice.md"}, Related: []string{"people/bob.md"}}
	o := Score(q, ModeRecall, a)
	want := []Found{{"people/alice.md", "pages", 2}, {"people/bob.md", "related", 3}, {"notes/missing.md", "", 0}}
	if !reflect.DeepEqual(o.Found, want) {
		t.Errorf("found %+v, want %+v", o.Found, want)
	}
	if !reflect.DeepEqual(o.Noise, []string{"notes/example.md"}) {
		t.Errorf("noise %v", o.Noise)
	}
}

func TestScoreCountsTheNearestLast(t *testing.T) {
	q := Question{Q: "q", Expect: []string{"people/alice.md"}}
	o := Score(q, ModeRecall, Answer{Nearest: []string{"notes/example.md", "people/alice.md"}})
	if want := (Found{"people/alice.md", "nearest", 2}); o.Found[0] != want {
		t.Errorf("got %+v, want %+v", o.Found[0], want)
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestTotals(t *testing.T) {
	outs := []Outcome{
		{Found: []Found{{"a", "pages", 1}, {"b", "", 0}}, Noise: []string{"x"}},
		{Found: []Found{{"c", "pages", 4}, {"d", "related", 2}}},
		{Found: []Found{{"e", "", 0}}},
		{Err: "boom", Found: []Found{{"f", "", 0}}},
	}
	got := TotalsOf(outs)
	if got.Count != 4 || got.Errors != 1 || got.Noise != 1 {
		t.Fatalf("%+v", got)
	}
	// 3 of 5 expected pages of the questions that ran are found
	if !near(got.HitRate, 3.0/5) {
		t.Errorf("hit rate %v", got.HitRate)
	}
	// first ranks 1, 2, none: (1 + 1/2 + 0) / 3
	if !near(got.MRR, 0.5) || !near(got.Rank1Share, 1.0/3) {
		t.Errorf("mrr %v rank1 %v", got.MRR, got.Rank1Share)
	}
}

func TestTotalsOfNothing(t *testing.T) {
	if got := TotalsOf(nil); got != (Totals{}) {
		t.Fatalf("%+v", got)
	}
}

func TestByMode(t *testing.T) {
	g := ByMode([]Outcome{
		{Mode: ModeRecall, Found: []Found{{"a", "pages", 1}}},
		{Mode: ModeSearch, Found: []Found{{"a", "", 0}}},
		{Mode: ModeSearch, Found: []Found{{"a", "pages", 2}}},
	})
	if g[ModeRecall].Count != 1 || g[ModeSearch].Count != 2 || !near(g[ModeSearch].MRR, 0.25) {
		t.Fatalf("%+v", g)
	}
}
