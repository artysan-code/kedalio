package mcp

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// text is the answer exactly as the client receives it.
func text(t *testing.T, s *sdk.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := s.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("%s refused: %s", name, res.Content[0].(*sdk.TextContent).Text)
	}
	return res.Content[0].(*sdk.TextContent).Text
}

func chars(s string) int { return utf8.RuneCountInString(s) }

// longPage is a Markdown page with sections, quotes and many short lines: what makes JSON longer than its text.
func longPage(title string, sections int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nOpening of %s.\n\n", title, title)
	for j := range sections {
		fmt.Fprintf(&b, "## Section %d\n\n- \"quoted\" line about espresso machines, grinders%d and beans\n- second line %d\n\n", j, j, j)
	}
	return b.String()
}

func TestMemoryReadCutsALongPageAndReadsTheRestByOffsetOrSection(t *testing.T) {
	r := newRig(t, nil)
	s := r.session(t)
	body := longPage("Long page", 300)
	if _, err := r.store.Write(ctx, "notes/long.md", body, "owner"); err != nil {
		t.Fatal(err)
	}
	a := mustCall(t, s, "memory_read", map[string]any{"path": "notes/long.md"})
	got := a["body"].(string)
	if chars(got) > 8000 || !strings.HasPrefix(a["truncated"].(string), "truncated, ") ||
		!strings.Contains(a["truncated"].(string), fmt.Sprintf("%d characters in total", int(a["total_chars"].(float64)))) {
		t.Fatalf("body %d characters, truncated = %v", chars(got), a["truncated"])
	}
	if a["total_chars"].(float64) < float64(chars(body)) || a["next_offset"] == nil || len(a["sections"].([]any)) != 40 {
		t.Errorf("cut fields = %v %v %d", a["total_chars"], a["next_offset"], len(a["sections"].([]any)))
	}
	// following next_offset gives the whole body back
	next := int(a["next_offset"].(float64))
	for next > 0 {
		p := mustCall(t, s, "memory_read", map[string]any{"path": "notes/long.md", "offset": next})
		got += p["body"].(string)
		next = 0
		if n, ok := p["next_offset"].(float64); ok {
			next = int(n)
		}
	}
	if !strings.HasSuffix(got, body) {
		t.Errorf("the pieces do not make the page: %d characters of %d", chars(got), chars(body))
	}
	// a section by its heading, and the cap changed or lifted
	sec := mustCall(t, s, "memory_read", map[string]any{"path": "notes/long.md", "section": "section 7"})
	if !strings.HasPrefix(sec["body"].(string), "## Section 7\n") || strings.Contains(sec["body"].(string), "Section 8") || sec["section"] != "section 7" {
		t.Errorf("section = %v", sec)
	}
	small := mustCall(t, s, "memory_read", map[string]any{"path": "notes/long.md", "max_chars": 1000})
	if chars(small["body"].(string)) > 1000 || small["truncated"] == nil {
		t.Errorf("max_chars 1000 gave %d characters", chars(small["body"].(string)))
	}
	all := mustCall(t, s, "memory_read", map[string]any{"path": "notes/long.md", "max_chars": 0})
	if !strings.HasSuffix(all["body"].(string), body) || all["truncated"] != nil {
		t.Errorf("max_chars 0 gave %d characters of %d, truncated = %v", chars(all["body"].(string)), chars(body), all["truncated"])
	}
	out, failed := call(t, s, "memory_read", map[string]any{"path": "notes/long.md", "section": "nothing here"})
	if !failed || !strings.Contains(fmt.Sprint(out), "its headings are") {
		t.Errorf("a missing section: %v", out)
	}
	// a short page is whole and has none of the cut fields
	if _, err := r.store.Write(ctx, "notes/short.md", "# Short\n\nA few words.\n", "owner"); err != nil {
		t.Fatal(err)
	}
	sh := mustCall(t, s, "memory_read", map[string]any{"path": "notes/short.md"})
	if sh["truncated"] != nil || sh["total_chars"] != nil {
		t.Errorf("short page = %v", sh)
	}
}

func TestMemoryRecallAnswerStaysWithinItsBudgetAsSent(t *testing.T) {
	r := newRig(t, nil)
	s := r.session(t)
	for i := range 6 {
		if _, err := r.store.Write(ctx, fmt.Sprintf("notes/espresso-%d.md", i), longPage(fmt.Sprintf("Espresso %d", i), 120)+"See [[notes/espresso-0]].\n", "owner"); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 60 {
		if _, err := r.store.Write(ctx, fmt.Sprintf("notes/filler-%d.md", i), fmt.Sprintf("# Filler %d\n\nGardening tomatoes%d and basil%d.\n", i, i, i*3), "owner"); err != nil {
			t.Fatal(err)
		}
	}
	for _, budget := range []int{0, 600, 2000, 4000} {
		args := map[string]any{"query": "espresso machine grinders"}
		want := 2000
		if budget > 0 {
			args["budget"], want = budget, budget
		}
		out := text(t, s, "memory_recall", args)
		if n := chars(out); n > want*charsPerToken {
			t.Errorf("budget %d: answer of %d characters, at most %d", want, n, want*charsPerToken)
		}
		if !strings.Contains(out, `"pages":[`) || strings.Contains(out, `"pages":[]`) {
			t.Errorf("budget %d: no page in the answer: %.300s", want, out)
		}
	}
}

func TestMemoryRecallRelatedPagesAreAPathAndATitleAndABigBudgetIsClamped(t *testing.T) {
	r := newRig(t, nil)
	s := r.session(t)
	for i := range 8 {
		if _, err := r.store.Write(ctx, fmt.Sprintf("notes/espresso-%d.md", i), longPage(fmt.Sprintf("Espresso %d", i), 150)+"See [[notes/side]].\n", "owner"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.store.Write(ctx, "notes/side.md", "# Side\n\nA page next door with a lede that says what it is.\n", "owner"); err != nil {
		t.Fatal(err)
	}
	// the schema still takes up to 12000 (clients that ask for more must not get an error); the answer is held to maxBudget
	out := text(t, s, "memory_recall", map[string]any{"query": "espresso", "budget": 12000})
	if chars(out) > 16000 {
		t.Errorf("a budget of 12000 gave %d characters, at most 16000", chars(out))
	}
	rec := mustCall(t, s, "memory_recall", map[string]any{"query": "espresso machine grinders", "budget": 4000})
	related, _ := rec["related"].([]any)
	if len(related) == 0 {
		t.Fatalf("no related page in %v", rec)
	}
	for _, x := range related {
		if m := x.(map[string]any); len(m) != 2 || m["path"] == nil || m["title"] == nil {
			t.Errorf("related = %v, want path and title only", m)
		}
	}
}

func TestMemorySearchCutsItsCitationsAndReportsWhatItLeftOut(t *testing.T) {
	r := newRig(t, nil)
	s := r.session(t)
	long := strings.Repeat("the espresso grinder needs a burr set and a careful hand ", 20)
	for i := range 30 {
		body := fmt.Sprintf("# Espresso %d\n\n%s\n\n## Part two\n\n%s\n\n## Part three\n\n%s\n", i, long, long, long)
		if _, err := r.store.Write(ctx, fmt.Sprintf("notes/e%d.md", i), body, "owner"); err != nil {
			t.Fatal(err)
		}
	}
	out := text(t, s, "memory_recall", map[string]any{"mode": "search", "query": "espresso grinder", "limit": 20})
	if n := chars(out); n > searchChars {
		t.Errorf("search answer of %d characters, cap %d", n, searchChars)
	}
	if strings.Contains(out, `"chunk"`) {
		t.Errorf("the chunk number is still sent")
	}
	res := mustCall(t, s, "memory_recall", map[string]any{"mode": "search", "query": "espresso grinder", "limit": 20})
	results := res["results"].([]any)
	if len(results) == 0 {
		t.Fatal("no results")
	}
	for _, x := range results {
		cites, _ := x.(map[string]any)["citations"].([]any)
		if len(cites) > searchCitations {
			t.Errorf("%d citations in one result, at most %d", len(cites), searchCitations)
		}
		for _, c := range cites {
			if n := chars(c.(map[string]any)["text"].(string)); n > citationChars+1 {
				t.Errorf("citation of %d characters, cut at %d", n, citationChars)
			}
		}
	}
	if len(results) < 20 && !strings.Contains(fmt.Sprint(res["notes"]), "left out") {
		t.Errorf("%d of 20 results and no note says what was left out: %v", len(results), res["notes"])
	}
}

func TestTasksToolsTakeTheLimitArgumentsAndAnswerWithinTheirCaps(t *testing.T) {
	s := newRig(t, nil).session(t)
	notes := longPage("Notes", 200)
	for i := range 40 {
		mustCall(t, s, "tasks_save", map[string]any{"title": fmt.Sprintf("Task %02d", i), "notes": notes[:600]})
	}
	long := mustCall(t, s, "tasks_save", map[string]any{"title": "Long one", "notes": notes})["id"].(string)

	if n := chars(text(t, s, "tasks_list", map[string]any{"full": true, "limit": 100})); n > 7500 {
		t.Errorf("tasks_list default: %d characters", n)
	}
	all := mustCall(t, s, "tasks_list", map[string]any{"full": true, "limit": 100, "max_chars": 0})
	if n := len(all["tasks"].([]any)); n != 41 || all["hint"] != nil {
		t.Errorf("max_chars 0: %d tasks, hint %v", n, all["hint"])
	}

	g := text(t, s, "tasks_get", map[string]any{"id": long})
	if n := chars(g); n > 8000 || !strings.Contains(g, `"notes_cut"`) {
		t.Errorf("tasks_get default: %d characters", n)
	}
	sec := mustCall(t, s, "tasks_get", map[string]any{"id": long, "section": "Section 3", "log": 2})
	if !strings.HasPrefix(sec["task"].(map[string]any)["notes"].(string), "## Section 3\n") {
		t.Errorf("section = %v", sec["task"])
	}
	if whole := mustCall(t, s, "tasks_get", map[string]any{"id": long, "max_chars": 0}); whole["notes_cut"] != nil {
		t.Errorf("max_chars 0 still cut the notes")
	}
}
