// Package recalleval scores recall answers against a file of questions: where each expected page came back, and
// the totals per mode. It reads no database and no clock, so the same answers always score the same; the runner
// (cmd/kedalio-recall-eval) is what asks the memory.
package recalleval

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// The two ways memory_recall answers.
const (
	ModeRecall = "recall"
	ModeSearch = "search"
)

// Question is one line of the questions file.
type Question struct {
	Q string `json:"q"`
	// Expect are the pages the answer should hold.
	Expect []string `json:"expect"`
	// Not are pages that were noise in a past answer and must not come back.
	Not []string `json:"not,omitempty"`
	// Mode limits the question to one mode (recall or search); empty runs it in every mode asked for.
	Mode string `json:"mode,omitempty"`
}

// ParseQuestions reads {"questions": [{q, expect, not?, mode?}]}, the format of the archived harness.
func ParseQuestions(raw []byte) ([]Question, error) {
	var f struct {
		Questions []Question `json:"questions"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("not a {questions:[{q, expect:[…]}]} file: %w", err)
	}
	if len(f.Questions) == 0 {
		return nil, errors.New("the file has no questions")
	}
	for i, q := range f.Questions {
		switch {
		case q.Q == "" || q.Expect == nil:
			return nil, fmt.Errorf("question %d lacks q or expect", i+1)
		case q.Mode != "" && q.Mode != ModeRecall && q.Mode != ModeSearch:
			return nil, fmt.Errorf("question %d: mode %q is neither %s nor %s", i+1, q.Mode, ModeRecall, ModeSearch)
		}
	}
	return f.Questions, nil
}

// Modes are the modes q runs in when the run asks for want (recall, search or both).
func Modes(q Question, want string) []string {
	all := []string{ModeRecall, ModeSearch}
	if want != "both" {
		all = []string{want}
	}
	if q.Mode == "" {
		return all
	}
	if slices.Contains(all, q.Mode) {
		return []string{q.Mode}
	}
	return nil
}

// Answer is the part of an answer the eval reads, as paths in the order the answer gave them. For recall, Pages are
// the pages in full and Related the ones beside them; for search, Pages are the results in order. Nearest are the
// pages recall hands over as a last resort when it found nothing relevant: they count after the others.
type Answer struct {
	Pages   []string `json:"pages"`
	Related []string `json:"related,omitempty"`
	Nearest []string `json:"nearest,omitempty"`
}

// Found says where one expected page sits: In is "pages", "related", "nearest" or "" when absent, Rank its 1-based place over
// the whole answer (0 when absent).
type Found struct {
	Path string `json:"path"`
	In   string `json:"in,omitempty"`
	Rank int    `json:"rank"`
}

// Outcome is the result of one question in one mode.
type Outcome struct {
	Q     string  `json:"q"`
	Mode  string  `json:"mode"`
	Found []Found `json:"found"`
	// Noise are the `not` pages that came back.
	Noise []string `json:"noise,omitempty"`
	// Err is why the question could not be asked; an errored question is left out of the totals' rates.
	Err string `json:"error,omitempty"`
}

// Score places q's expected pages in a: the order is the pages in full, the related ones, then the nearest, counted
// from 1.
func Score(q Question, mode string, a Answer) Outcome {
	type slot struct{ path, in string }
	var order []slot
	for _, p := range a.Pages {
		order = append(order, slot{p, "pages"})
	}
	for _, p := range a.Related {
		order = append(order, slot{p, "related"})
	}
	for _, p := range a.Nearest {
		order = append(order, slot{p, "nearest"})
	}
	at := func(path string) int {
		return slices.IndexFunc(order, func(s slot) bool { return s.path == path })
	}
	out := Outcome{Q: q.Q, Mode: mode, Found: []Found{}}
	for _, path := range q.Expect {
		f := Found{Path: path}
		if i := at(path); i >= 0 {
			f.In, f.Rank = order[i].in, i+1
		}
		out.Found = append(out.Found, f)
	}
	for _, path := range q.Not {
		if at(path) >= 0 {
			out.Noise = append(out.Noise, path)
		}
	}
	return out
}

// Totals are what the eval reports for one mode.
type Totals struct {
	// Count is the questions asked, Errors those that failed.
	Count  int `json:"count"`
	Errors int `json:"errors"`
	// HitRate is the expected pages found anywhere, over all expected pages.
	HitRate float64 `json:"hit_rate"`
	// MRR is the mean over questions of 1 / rank of the first expected page found (0 when none).
	MRR float64 `json:"mrr"`
	// Rank1Share is the share of questions whose best expected page is the first of the answer.
	Rank1Share float64 `json:"rank1_share"`
	// Noise counts the returned pages that a question lists in `not`.
	Noise int `json:"noise"`
}

// TotalsOf sums the outcomes of one mode.
func TotalsOf(outcomes []Outcome) Totals {
	var expected, hit, noise, first1, ok int
	var rr float64
	for _, o := range outcomes {
		if o.Err != "" {
			continue
		}
		ok++
		best := 0
		for _, f := range o.Found {
			expected++
			if f.Rank > 0 {
				hit++
				if best == 0 || f.Rank < best {
					best = f.Rank
				}
			}
		}
		if best > 0 {
			rr += 1 / float64(best)
		}
		if best == 1 {
			first1++
		}
		noise += len(o.Noise)
	}
	t := Totals{Count: len(outcomes), Errors: len(outcomes) - ok, Noise: noise}
	if expected > 0 {
		t.HitRate = float64(hit) / float64(expected)
	}
	if ok > 0 {
		t.MRR = rr / float64(ok)
		t.Rank1Share = float64(first1) / float64(ok)
	}
	return t
}

// ByMode groups the outcomes' totals by mode.
func ByMode(outcomes []Outcome) map[string]Totals {
	groups := map[string][]Outcome{}
	for _, o := range outcomes {
		groups[o.Mode] = append(groups[o.Mode], o)
	}
	out := map[string]Totals{}
	for m, g := range groups {
		out[m] = TotalsOf(g)
	}
	return out
}
