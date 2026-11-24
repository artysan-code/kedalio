package search

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestFuseRewardsWhatBothRankingsHold(t *testing.T) {
	var got []string
	for _, x := range fuse([][]string{{"a", "b"}, {"b", "c"}}, rrfK) {
		got = append(got, x.item)
	}
	if !slices.Equal(got, []string{"b", "a", "c"}) {
		t.Fatalf("got %v", got)
	}
	if len(fuse([][]string{nil, nil}, rrfK)) != 0 {
		t.Fatal("empty")
	}
}

func TestMatchExpression(t *testing.T) {
	for in, want := range map[string]string{
		`Perché "NOT" la-casa?`: `"perché"* OR "not" OR "la" OR "casa"`,
		"Meta e WhatsApp conf":  `"meta" OR "e" OR "whatsapp"* OR "conf"`,
		"tabletop":              `"tabletop"*`,
		"  ?! ":                 "",
	} {
		if got := matchExpression(in); got != want {
			t.Errorf("matchExpression(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiversifyTwoChunksOfAPageFirstTheRestOnlyToFill(t *testing.T) {
	var r []ranked
	for id := int64(1); id <= 5; id++ {
		p := "a"
		if id == 5 {
			p = "b"
		}
		r = append(r, ranked{id, p})
	}
	if got := diversify(r, 10, perSource); !slices.Equal(got, []int64{1, 2, 5, 3, 4}) {
		t.Fatalf("got %v", got)
	}
	if got := diversify(r, 3, perSource); !slices.Equal(got, []int64{1, 2, 5}) {
		t.Fatalf("got %v", got)
	}
}

func paths(a Answer) []string {
	out := []string{}
	for _, r := range a.Results {
		out = append(out, r.Path)
	}
	return out
}

func titles(a Answer) []string {
	out := []string{}
	for _, r := range a.Results {
		out = append(out, r.Title)
	}
	return out
}

func search(t *testing.T, ix *Index, q string, o Options) Answer {
	t.Helper()
	a, err := ix.Search(context.Background(), q, o)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSearchShortQueryWordDoesNotMatchTheLongerWordsItStarts(t *testing.T) {
	ix := newTestIndex(t)
	ix.page(t, "notes/metadata.md", "Metadati", "I metadati del browser e la sua configurazione.")
	ix.page(t, "notes/meta.md", "Meta", "Meta è l'azienda di WhatsApp.")
	if got := paths(search(t, ix, "Meta", Options{})); !slices.Equal(got, []string{"notes/meta.md"}) {
		t.Fatalf("got %v", got)
	}
	if got := paths(search(t, ix, "configur", Options{})); !slices.Equal(got, []string{"notes/metadata.md"}) {
		t.Fatalf("a long enough word still matches by prefix: %v", got)
	}
}

// brain is a wiki of two pages, embedded.
func brain(t *testing.T) *Index {
	ix := newTestIndex(t)
	ix.page(t, "notes/garage.md", "Garage", "# Car\n\nThe car needs new tyres in spring.")
	ix.page(t, "notes/pets.md", "Pets", "The dog eats twice a day, perché ha fame.")
	ix.embedAll(t, newFake("fake"), 32)
	return ix
}

func TestSearchWordsAloneFindPagesWithCitations(t *testing.T) {
	a := search(t, brain(t), "tyres", Options{})
	if a.Meaning || len(a.Notes) != 0 {
		t.Fatalf("answer %+v", a)
	}
	r := a.Results[0]
	if r.Path != "notes/garage.md" || r.Title != "Garage" || !r.Words || r.Meaning || r.Cosine != nil {
		t.Fatalf("result %+v", r)
	}
	c := r.Citations[0]
	if c.Heading != "Car" || c.Context != "Garage › Car" || c.Position != 0 || !strings.Contains(c.Text, "new tyres") {
		t.Fatalf("citation %+v", c)
	}
}

func TestSearchAccentsDoNotMatterToWords(t *testing.T) {
	if got := titles(search(t, brain(t), "perche", Options{})); !slices.Equal(got, []string{"Pets"}) {
		t.Fatalf("got %v", got)
	}
}

func TestSearchMeaningFindsWhatWordsMissAndTheTwoAreFused(t *testing.T) {
	ix := brain(t)
	ix.page(t, "notes/manual.md", "Manual", "Check the automobile oil every month.")
	ix.embedAll(t, newFake("fake"), 32)
	if got := search(t, ix, "vehicle", Options{}).Results; len(got) != 0 {
		t.Fatalf("no page says vehicle: %v", got)
	}
	both := search(t, ix, "vehicle", Options{Embedder: newFake("fake")})
	if !both.Meaning {
		t.Fatal("meaning")
	}
	got := titles(both)[:2]
	slices.Sort(got)
	if !slices.Equal(got, []string{"Garage", "Manual"}) {
		t.Fatalf("got %v", got)
	}
	if both.Results[0].Cosine == nil || *both.Results[0].Cosine <= 0 || !both.Results[0].Meaning || both.Results[0].Words {
		t.Fatalf("by %+v", both.Results[0])
	}
	// words and meaning, then meaning
	fused := search(t, ix, "automobile oil", Options{Embedder: newFake("fake")})
	if got := titles(fused); !slices.Equal(got[:2], []string{"Manual", "Garage"}) {
		t.Fatalf("got %v", got)
	}
}

func TestSearchModelThatFailsLeavesWordsAloneAndSaysSo(t *testing.T) {
	down := funcEmbedder{"fake", func(context.Context, []string) ([][]float32, error) {
		return nil, fmt.Errorf("HTTP 502: %w", errBadAnswer)
	}}
	a := search(t, brain(t), "tyres", Options{Embedder: down})
	if a.Meaning || len(a.Results) != 1 || !slices.Equal(a.Notes, []string{"meaning left out: the model gave no usable answer"}) {
		t.Fatalf("got %+v", a)
	}
}

func TestSearchSaysWhenSomeChunksAreNotEmbeddedYet(t *testing.T) {
	ix := brain(t)
	if a := search(t, ix, "tyres", Options{Embedder: newFake("fake")}); len(a.Notes) != 0 {
		t.Fatalf("all embedded, no note: %v", a.Notes)
	}
	ix.page(t, "notes/boat.md", "Boat", "The boat is moored at the harbour.")
	a := search(t, ix, "tyres", Options{Embedder: newFake("fake")})
	if !a.Meaning || !slices.Equal(a.Notes, []string{"meaning partial: 1 of 3 chunks still wait for the model"}) {
		t.Fatalf("got %+v", a)
	}
}

func TestSearchOnlyTheChunksOfTheEmbeddersOwnModelAreCompared(t *testing.T) {
	a := search(t, brain(t), "vehicle", Options{Embedder: newFake("another")})
	if !a.Meaning || len(a.Results) != 0 {
		t.Fatalf("got %+v", a)
	}
}
func TestSearchCitationsAreTheBestChunksAtMostAsManyAsAsked(t *testing.T) {
	ix := newTestIndex(t)
	ix.page(t, "long.md", "Long", "# A\n\nalpha one\n\n# B\n\nalpha two\n\n# C\n\nalpha three\n\n# D\n\nalpha four")
	a := search(t, ix, "alpha", Options{Citations: 2})
	if len(a.Results) != 1 || len(a.Results[0].Citations) != 2 {
		t.Fatalf("got %+v", a)
	}
	if got := search(t, ix, "", Options{}).Results; len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestSearchInt8FirstPassKeepsWhatAnExactScanRanksBest(t *testing.T) {
	ix := newTestIndex(t)
	// a vector per text, drawn from a seeded generator: the same text always gets the same vector
	vectorOf := func(s string) []float32 {
		h := uint64(7)
		for _, c := range s {
			h = h*31 + uint64(c)
		}
		rng := rand.New(rand.NewPCG(h, 1))
		v := make([]float32, 32)
		for i := range v {
			v[i] = float32(rng.Float64() - 0.5)
		}
		return v
	}
	random := funcEmbedder{"r", func(_ context.Context, in []string) ([][]float32, error) {
		out := make([][]float32, len(in))
		for i, s := range in {
			out[i] = vectorOf(s)
		}
		return out, nil
	}}
	ctx := context.Background()
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 1500 {
		if err := ix.IndexPage(ctx, tx, fmt.Sprintf("p%d.md", i), fmt.Sprintf("P%d", i), fmt.Sprintf("body %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	ix.embedAll(t, random, 500)
	q := Normalize(vectorOf("the question"))
	type hit struct {
		path string
		s    float64
	}
	var all []hit
	for i := range 1500 {
		all = append(all, hit{fmt.Sprintf("p%d.md", i), dotUnit(q, Normalize(vectorOf(fmt.Sprintf("P%d\nbody %d", i, i))))})
	}
	slices.SortFunc(all, func(a, b hit) int { return int(math.Copysign(1, b.s-a.s)) })
	var want []string
	for _, h := range all[:10] {
		want = append(want, h.path)
	}
	got := paths(search(t, ix, "the question", Options{Embedder: random, Limit: 10, Citations: 1}))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestSearchQueryAskedAgainTakesItsVectorFromTheCache(t *testing.T) {
	ix := brain(t)
	e := newFake("fake")
	first := search(t, ix, "vehicle", Options{Embedder: e})
	again := search(t, ix, "vehicle", Options{Embedder: e})
	search(t, ix, "hound", Options{Embedder: e})
	if want := [][]string{{"vehicle"}, {"hound"}}; !reflect.DeepEqual(e.calls, want) {
		t.Fatalf("calls %v", e.calls)
	}
	if !reflect.DeepEqual(again.Results, first.Results) {
		t.Fatal("results differ")
	}
}

func TestSearchOneLongPageDoesNotTakeEveryPlaceOfTheRanking(t *testing.T) {
	ix := newTestIndex(t)
	var sections []string
	for i := range 70 {
		sections = append(sections, fmt.Sprintf("# S%d\n\ntyres tyres tyres tyres part %d", i, i))
	}
	ix.page(t, "long.md", "Long", strings.Join(sections, "\n\n"))
	ix.page(t, "short.md", "Short", "one note about tyres")
	r := search(t, ix, "tyres", Options{})
	got := titles(r)
	slices.Sort(got)
	if !slices.Equal(got, []string{"Long", "Short"}) {
		t.Fatalf("got %v", got)
	}
	for _, x := range r.Results {
		if x.Title == "Long" && len(x.Citations) > 3 {
			t.Fatal("too many citations")
		}
	}
}

func TestSearchAPageWithMoreMatchingChunksThanTheWindowDoesNotHideTheNextPage(t *testing.T) {
	ix := newTestIndex(t)
	var sections []string
	for i := range perRanking * pool * 2 {
		sections = append(sections, fmt.Sprintf("# S%d\n\ntyres tyres tyres tyres tyres tyres part %d", i, i))
	}
	ix.page(t, "long.md", "Long", strings.Join(sections, "\n\n"))
	ix.page(t, "short.md", "Short", "one note about tyres and a few other words around it to rank lower than the long page")
	ids, err := ix.byWords(context.Background(), "tyres", Options{})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, id := range ids {
		var p string
		if err := ix.db.QueryRow("select path from search_chunks where id = ?", id).Scan(&p); err != nil {
			t.Fatal(err)
		}
		paths[p] = true
	}
	if !paths["short.md"] {
		t.Fatalf("the short page is out of the words' ranking: %v", paths)
	}
}

func TestSearchAPageWithMoreNearChunksThanTheCandidatesDoesNotHideTheNextPageFromMeaning(t *testing.T) {
	ix := brain(t)
	var sections []string
	for i := range candidates + 100 {
		sections = append(sections, fmt.Sprintf("# S%d\n\ncar part %d", i, i))
	}
	ix.page(t, "long.md", "Long", strings.Join(sections, "\n\n"))
	ix.page(t, "short.md", "Short", "an automobile and a few more words")
	ix.embedAll(t, newFake("fake"), 64)
	ids, _, err := ix.byMeaning(context.Background(), "vehicle", newFake("fake"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range ids {
		var p string
		if err := ix.db.QueryRow("select path from search_chunks where id = ?", id).Scan(&p); err != nil {
			t.Fatal(err)
		}
		found = found || p == "short.md"
	}
	if !found {
		t.Fatal("the short page is out of the meaning's ranking")
	}
}

func TestSearchAPageNamedForTheQueryLeadsOneThatOnlyMentionsIt(t *testing.T) {
	ix := newTestIndex(t)
	// the body of the second repeats the word far more often, so by body alone it leads
	ix.page(t, "notes/log.md", "Weekly log", strings.Repeat("kestrel ", 12)+"seen over the field")
	ix.page(t, "notes/kestrel.md", "Kestrel", "A small falcon that hovers in place while it hunts over open ground and verges.")
	if got := paths(search(t, ix, "kestrel", Options{})); !slices.Equal(got, []string{"notes/kestrel.md", "notes/log.md"}) {
		t.Fatalf("got %v", got)
	}
}

func TestSearchAPageNamedForOnlyOneWordOfALongQueryDoesNotLeadThePageThatCoversMore(t *testing.T) {
	ix := newTestIndex(t)
	ix.page(t, "notes/kestrel.md", "Kestrel", "A small falcon that hovers in place.")
	ix.page(t, "notes/field.md", "Field notes", "The kestrel nests in the old barn at the edge of the field, and the barn roof is low.")
	if got := paths(search(t, ix, "kestrel nests barn", Options{})); !slices.Equal(got, []string{"notes/field.md", "notes/kestrel.md"}) {
		t.Fatalf("got %v", got)
	}
}
