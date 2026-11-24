package memory

import (
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"kedalio/core/memory/search"
)

func TestMeanUnitAndNearestTheMeanIsUnitLengthOnlyPagesAboveTheThresholdNearestFirst(t *testing.T) {
	m := meanUnit([][]float32{search.Normalize([]float32{1, 0}), search.Normalize([]float32{0, 1})})
	if n := math.Hypot(float64(m[0]), float64(m[1])); math.Abs(n-1) > 1e-6 {
		t.Errorf("length = %v", n)
	}
	if meanUnit(nil) != nil {
		t.Error("meanUnit of none is not nil")
	}
	v := search.Normalize([]float32{1, 0, 0})
	got := nearest(v, []pageVec{
		{"a", search.Normalize([]float32{1, 0.1, 0})},
		{"b", search.Normalize([]float32{0, 1, 0})},
		{"c", search.Normalize([]float32{1, 0.5, 0})},
	}, "c", 5, 0.6)
	var paths []string
	for _, n := range got {
		paths = append(paths, n.path)
	}
	if !reflect.DeepEqual(paths, []string{"a"}) {
		t.Errorf("nearest = %v", paths)
	}
}

func TestQuantileAndThresholdOfTheAccountsOwnHighQuantileTheFloorTheFixedValueForASmallMemory(t *testing.T) {
	if got := quantile([]float64{5, 1, 4, 2, 3}, 0.8); got != 4 {
		t.Errorf("quantile = %v", got)
	}
	if got := thresholdOf([][]float32{search.Normalize([]float32{1, 0})}); got != Similar {
		t.Errorf("too few pages to measure: %v", got)
	}
	// twenty pages along one direction with a little spread: they are all near, and the threshold is high among them
	var close, apart [][]float32
	for i := 0; i < 20; i++ {
		close = append(close, search.Normalize([]float32{1, float32(i) / 100, 0}))
		apart = append(apart, search.Normalize([]float32{float32(math.Cos(float64(i))), float32(math.Sin(float64(i))), float32(i%3 - 1)}))
	}
	if got := thresholdOf(close); got <= 0.99 {
		t.Errorf("close: %v", got)
	}
	if got := thresholdOf(apart); got < 0.5 {
		t.Errorf("apart: %v", got)
	}
}

func similarEdges(t *testing.T, s *Store) [][2]string {
	t.Helper()
	rows, err := s.db.Query("select src, dst from similar order by src, dst")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var e [2]string
		if err := rows.Scan(&e[0], &e[1]); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func refresh(t *testing.T, s *Store, model string) int {
	t.Helper()
	n, err := s.RefreshSimilar(ctx, model)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRefreshSimilarPagesNearInMeaningBecomeEdgesOnceEmbeddedFollowRewritesAndDeletions(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "note/car.md", pageOf("Car", "Car automobile vehicle car vehicle."))
	write(t, s, "note/auto.md", pageOf("Automobile", "Automobile vehicle car automobile."))
	write(t, s, "note/dog.md", pageOf("Dog", "Dog puppy hound dog puppy."))
	e := &fakeEmbedder{model: "fake"}
	if n := refresh(t, s, e.model); n != 0 {
		t.Errorf("nothing before the chunks are embedded: %d", n)
	}
	embedAll(t, ix, e)
	if n := refresh(t, s, e.model); n != 3 {
		t.Errorf("refreshed %d", n)
	}
	want := [][2]string{{"note/auto.md", "note/car.md"}, {"note/car.md", "note/auto.md"}}
	if got := similarEdges(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %v", got)
	}
	if n := refresh(t, s, e.model); n != 0 {
		t.Errorf("nothing changed, nothing redone: %d", n)
	}

	// a page rewritten waits for its new chunks' vectors, then is compared again
	write(t, s, "note/car.md", pageOf("Car", "Dog puppy hound dog puppy."))
	if n := refresh(t, s, e.model); n != 0 {
		t.Errorf("a rewritten page waits: %d", n)
	}
	embedAll(t, ix, e)
	if n := refresh(t, s, e.model); n != 1 {
		t.Errorf("rewritten page refreshed: %d", n)
	}
	found := false
	for _, e := range similarEdges(t, s) {
		found = found || e == [2]string{"note/car.md", "note/dog.md"}
	}
	if !found {
		t.Errorf("car is not near dog: %v", similarEdges(t, s))
	}

	if err := s.Remove(ctx, "note/dog.md", "t"); err != nil {
		t.Fatal(err)
	}
	refresh(t, s, e.model)
	for _, e := range similarEdges(t, s) {
		if strings.Contains(e[0]+e[1], "dog") {
			t.Errorf("an edge of a deleted page: %v", e)
		}
	}
}

func TestRecallAPageNoOneLinkedComesUpThroughItsNearnessOfMeaningAndSaysSo(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "note/car.md", pageOf("Car", "Car automobile vehicle car vehicle."))
	write(t, s, "note/auto.md", pageOf("Automobile", "Automobile vehicle automobile."))
	write(t, s, "note/dog.md", pageOf("Dog", "Dog puppy hound dog puppy."))
	e := &fakeEmbedder{model: "fake"}
	embedAll(t, ix, e)
	refresh(t, s, e.model)
	// words alone find the car page; the automobile page shares no word with the query, nor a link
	a := recall(t, s, ix, "car", RecallOptions{})
	if a.Pages[0].Path != "note/car.md" {
		t.Fatalf("first = %s", a.Pages[0].Path)
	}
	if !slices.Contains(shown(a), "note/auto.md") {
		t.Fatalf("not reached by meaning: %v", shown(a))
	}
	if auto := relatedOf(a, "note/auto.md"); auto != nil && auto.Via != (Via{From: "note/car.md", Meaning: true}) {
		t.Errorf("via = %+v", auto.Via)
	}
	for _, p := range shown(a) {
		if p == "note/dog.md" {
			t.Error("dog is near nothing")
		}
	}
}

func TestRefreshSimilarDiaryDaysGetNoVectorAndNoEdges(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "note/car.md", pageOf("Car", "Car automobile vehicle car vehicle."))
	write(t, s, "note/auto.md", pageOf("Automobile", "Automobile vehicle car automobile."))
	write(t, s, "diario/2026-10-25.md", pageOf("2026-10-25", "- 10:00 car automobile vehicle car."))
	e := &fakeEmbedder{model: "fake"}
	embedAll(t, ix, e)
	if n := refresh(t, s, e.model); n != 2 {
		t.Errorf("refreshed %d", n)
	}
	var n int
	if err := s.db.QueryRow("select count(*) from similar where src like 'diario/%' or dst like 'diario/%'").Scan(&n); err != nil || n != 0 {
		t.Errorf("diary edges: %d, %v", n, err)
	}
}

func TestNearestPageAndThreshold(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "note/car.md", pageOf("Car", "Car automobile vehicle car vehicle."))
	write(t, s, "note/dog.md", pageOf("Dog", "Dog puppy hound dog puppy."))
	e := &fakeEmbedder{model: "fake"}
	embedAll(t, ix, e)
	refresh(t, s, e.model)
	if thr, err := s.NearThreshold(ctx); err != nil || thr < simFloor {
		t.Errorf("threshold = %v, %v", thr, err)
	}
	v, err := e.Embed(ctx, []string{"car vehicle automobile"})
	if err != nil {
		t.Fatal(err)
	}
	path, w, err := s.NearestPage(ctx, search.Normalize(v[0]), e.model, 0.5)
	if err != nil || path != "note/car.md" || w < 0.5 {
		t.Errorf("nearest = %q %v, %v", path, w, err)
	}
	if path, _, _ := s.NearestPage(ctx, search.Normalize(v[0]), e.model, 1.01); path != "" {
		t.Errorf("nothing reaches 1.01, got %q", path)
	}
}

func TestNearThresholdFallsBackToSimilarForAStoredValueThatIsNoMeasure(t *testing.T) {
	s, _ := recallStore(t)
	for _, raw := range []string{"0", "-0.3", "junk"} {
		if _, err := s.db.ExecContext(ctx, "insert or replace into memory_meta (k, v) values (?, ?)", similarThreshold, raw); err != nil {
			t.Fatal(err)
		}
		if got, err := s.NearThreshold(ctx); err != nil || got != Similar {
			t.Errorf("stored %q: %v %v", raw, got, err)
		}
	}
}

func TestRefreshSimilarACollectionsSourceHubGetsNoVectorAndNoEdges(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "note/car.md", pageOf("Car", "Car automobile vehicle car vehicle."))
	write(t, s, "note/auto.md", pageOf("Automobile", "Automobile vehicle car automobile."))
	// a hub lists its documents' titles, so its meaning sits near whatever they are about
	write(t, s, "fonti/manuale.md", "---\nkind: source\n---\n# Manuale\n\nCar automobile vehicle car.\n")
	e := &fakeEmbedder{model: "fake"}
	embedAll(t, ix, e)
	if n := refresh(t, s, e.model); n != 2 {
		t.Errorf("refreshed %d", n)
	}
	var n int
	if err := s.db.QueryRow("select count(*) from similar where src like 'fonti/%' or dst like 'fonti/%'").Scan(&n); err != nil || n != 0 {
		t.Errorf("hub edges: %d, %v", n, err)
	}
}
