package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"kedalio/core/db"
	"kedalio/core/memory/search"
)

// recallStore is a store over a fresh in-memory database with the real search index, as recall reads them.
func recallStore(t *testing.T) (*Store, *search.Index) {
	t.Helper()
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := Migrate(ctx, sqldb); err != nil {
		t.Fatal(err)
	}
	ix, err := search.New(ctx, sqldb)
	if err != nil {
		t.Fatal(err)
	}
	return italian(New(sqldb, ix)), ix
}

// page is a page that passes the rules' shape: a title and some content.
func pageOf(title, rest string) string { return "# " + title + "\n\n" + rest }

func write(t *testing.T, s *Store, path, body string) {
	t.Helper()
	if _, err := s.Write(ctx, path, body, "t"); err != nil {
		t.Fatal(err)
	}
}

// Words that mean the same thing share a dimension, so meaning finds what words miss; any other word lands on a
// dimension of its own by its hash.
var concepts = map[string]int{"car": 0, "automobile": 0, "vehicle": 0, "dog": 1, "hound": 1, "puppy": 1}

var letters = regexp.MustCompile(`\p{L}+`)

type fakeEmbedder struct{ model string }

func (f *fakeEmbedder) Model() string { return f.model }

func (f *fakeEmbedder) Embed(_ context.Context, input []string) ([][]float32, error) {
	out := make([][]float32, len(input))
	for i, t := range input {
		v := make([]float32, 64)
		for _, w := range letters.FindAllString(strings.ToLower(t), -1) {
			h := uint32(7)
			for _, c := range w {
				h = h*31 + uint32(c)
			}
			d, ok := concepts[w]
			if !ok {
				d = 8 + int(h%(64-8))
			}
			v[d]++
		}
		out[i] = v
	}
	return out, nil
}

func embedAll(t *testing.T, ix *search.Index, e search.Embedder) {
	t.Helper()
	for {
		n, err := ix.EmbedPending(ctx, e, 50)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

func recall(t *testing.T, s *Store, ix *search.Index, q string, opts RecallOptions) RecallAnswer {
	t.Helper()
	opts.Index = ix
	a, err := s.Recall(ctx, q, opts)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// shown is the paths of the pack, pages in full first.
func shown(a RecallAnswer) []string {
	var out []string
	for _, p := range a.Pages {
		out = append(out, p.Path)
	}
	for _, p := range a.Related {
		out = append(out, p.Path)
	}
	return out
}

func relatedOf(a RecallAnswer, path string) *RecallRelated {
	for i := range a.Related {
		if a.Related[i].Path == path {
			return &a.Related[i]
		}
	}
	return nil
}

func seedsOf(pairs ...[2]float64) *seedSet {
	s := newSeeds()
	for _, p := range pairs {
		s.set(int(p[0]), p[1])
	}
	return s
}

func keysOf(s *seedSet) []int { return s.keys }

func TestActivateTheSeedKeepsTheMostItsNeighbourMoreThanTwoStepsAwayNothingLeaks(t *testing.T) {
	// a — b — c — d, a line; e alone
	e := func(to int) edge { return edge{to: to, w: 1} }
	g := &graph{
		nodes: []string{"a", "b", "c", "d", "e"},
		adj:   [][]edge{{e(1)}, {e(0), e(2)}, {e(1), e(3)}, {e(2)}, {}},
	}
	r := activate(g, seedsOf([2]float64{0, 1}), DefaultRecallWeights.Damping, iterations)
	if !(r[0] > r[1] && r[1] > r[2] && r[2] > r[3]) {
		t.Errorf("activation does not fall with distance: %v", r)
	}
	if r[4] != 0 {
		t.Errorf("an unreachable node is activated: %v", r[4])
	}
	sum := 0.0
	for _, x := range r {
		sum += x
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Errorf("the mass is %v, want 1", sum)
	}
}

func TestCutToWholeLinesWithinTheBudgetAndSaysWhenItCut(t *testing.T) {
	body := "uno due tre.\n\n- primo punto\n- secondo punto\n" + strings.Repeat("parola ", 200)
	if got, cut := cutTo(body, 1000); got != body || cut {
		t.Errorf("cutTo(1000) = %q, %v", got, cut)
	}
	if got, cut := cutTo(body, 10); got != "uno due tre.\n\n- primo punto" || !cut {
		t.Errorf("cutTo(10) = %q, %v", got, cut)
	}
	long := "## Pump\n\n" + strings.TrimSpace(strings.Repeat("The pump runs at 2.5 bar. ", 40))
	if got, _ := cutTo(long, 30); got != "## Pump" {
		t.Errorf("cutTo(long, 30) = %q", got)
	}
	if got, cut := cutTo(strings.Repeat("a", 500), 30); len(got) != 120 || !cut {
		t.Errorf("one long line is cut to the budget in characters: %d, %v", len(got), cut)
	}
}

func TestTokensCountUTF16UnitsByFour(t *testing.T) {
	for in, want := range map[string]int{"": 0, "abcd": 1, "abcde": 2, "àèìò": 1, "😀😀": 1, "😀😀a": 2} {
		if got := Tokens(in); got != want {
			t.Errorf("Tokens(%q) = %d, want %d", in, got, want)
		}
	}
}

func brain(t *testing.T) (*Store, *search.Index) {
	s, ix := recallStore(t)
	write(t, s, "giochi/tabletop.md", pageOf("Tabletop RPG",
		"Il gioco di ruolo del giovedì.\n\n## Relazioni\n\n- gioca con [[alice]]\n- usa [[regole-2024]]\n"))
	write(t, s, "persone/alice.md", pageOf("Alice", "Un'amica che fa la maga."))
	write(t, s, "note/regole-2024.md", pageOf("Regole 2024", "Il manuale aggiornato, vedi [[persone/alice]]."))
	write(t, s, "progetti/app.md", pageOf("App della campagna", "L'app per il tavolo, vedi [[giochi/tabletop]]."))
	write(t, s, "note/fatture.md", pageOf("Fatture", "Il commercialista e le scadenze."))
	write(t, s, "io/me.md", pageOf("Chi sono", "Il proprietario della memoria."))
	return s, ix
}

func TestRecallFromThePagesTheWordsFindToTheOnesTheyReachWithTheEdgeThatBroughtThem(t *testing.T) {
	s, ix := brain(t)
	a := recall(t, s, ix, "tabletop", RecallOptions{})
	if a.Pages[0].Path != "giochi/tabletop.md" {
		t.Fatalf("first page = %s", a.Pages[0].Path)
	}
	got := shown(a)
	for _, p := range []string{"persone/alice.md", "note/regole-2024.md", "progetti/app.md"} {
		if !slices.Contains(got, p) {
			t.Errorf("%s is not in the pack: %v", p, got)
		}
	}
	if slices.Contains(got, "note/fatture.md") {
		t.Error("a page with no path to the subject is in the pack")
	}
	if alice := relatedOf(a, "persone/alice.md"); alice != nil && alice.Via != (Via{From: "giochi/tabletop.md", Rel: "gioca_con"}) {
		t.Errorf("alice via = %+v", alice.Via)
	}
	if a.Meaning {
		t.Error("meaning without an embedder")
	}
	if strings.HasPrefix(a.Pages[0].Body, "---") {
		t.Error("front matter is not left out")
	}
}

func TestRecallTheBudgetHoldsTheStrongestPageComesFirstAndIsCutRatherThanDropped(t *testing.T) {
	s, ix := brain(t)
	write(t, s, "giochi/tabletop.md", pageOf("Tabletop RPG",
		"Il gioco di ruolo del giovedì.\n\n"+strings.Repeat("parola ", 3000)+"\n\n[[alice]]"))
	a := recall(t, s, ix, "tabletop", RecallOptions{Budget: 400})
	if a.Pages[0].Path != "giochi/tabletop.md" || !a.Pages[0].Cut {
		t.Errorf("first page = %s, cut %v", a.Pages[0].Path, a.Pages[0].Cut)
	}
	if a.Tokens > 450 {
		t.Errorf("%d tokens over a budget of 400", a.Tokens)
	}
}

func TestRecallAQueryThatNamesAPageStartsFromItAnEmptyQueryIsTheCore(t *testing.T) {
	s, ix := brain(t)
	if a := recall(t, s, ix, "alice", RecallOptions{}); a.Pages[0].Path != "persone/alice.md" {
		t.Errorf("named page: first = %s", a.Pages[0].Path)
	}
	if a := recall(t, s, ix, "", RecallOptions{}); a.Pages[0].Path != "io/me.md" {
		t.Errorf("core: first = %s", a.Pages[0].Path)
	}
	empty, eix := recallStore(t)
	if a := recall(t, empty, eix, "anything", RecallOptions{}); len(a.Pages) != 0 {
		t.Errorf("an empty memory answers %v", a.Pages)
	}
}

func TestRecallTheCoreSeedsTheActiveTasksFromTheGroupsGiven(t *testing.T) {
	s, ix := brain(t)
	write(t, s, "tasks/t-20261026-aaaaaa.md", "---\nkind: task\n---\n# Lavoro in corso\n\nDa fare.\n\n## Relazioni\n\n- part_of [[note/fatture]]\n")
	groups := func(context.Context) (map[string]string, error) {
		return map[string]string{"tasks/t-20261026-aaaaaa.md": "active"}, nil
	}
	a := recall(t, s, ix, "", RecallOptions{TaskGroups: groups, Budget: 4000})
	if !slices.Contains(shown(a), "tasks/t-20261026-aaaaaa.md") || !slices.Contains(shown(a), "note/fatture.md") {
		t.Errorf("the active task and what it is about are in the core: %v", shown(a))
	}
}

func TestDropStrandedAWeakSeedStaysOnlyNextToAStrongOneByAWrittenLinkNotByAGuessOfMeaning(t *testing.T) {
	// s — n1 — n2 — n3 (n3 only by meaning), and x alone
	e := func(to int, meaning bool) edge { return edge{to: to, w: 1, meaning: meaning} }
	g := &graph{
		nodes: []string{"s", "n1", "n2", "n3", "x"},
		adj:   [][]edge{{e(1, false), e(3, true)}, {e(0, false), e(2, false)}, {e(1, false)}, {e(0, true)}, {}},
	}
	all := seedsOf([2]float64{0, 1}, [2]float64{1, 0.5}, [2]float64{2, 0.5}, [2]float64{3, 0.5}, [2]float64{4, 0.5})
	if got := keysOf(dropStranded(g, all, noSeed, DefaultRecallWeights.Strong, nil)); !reflect.DeepEqual(got, []int{0, 1}) {
		t.Errorf("dropStranded = %v", got)
	}
	some := seedsOf([2]float64{0, 1}, [2]float64{2, 0.5}, [2]float64{3, 0.5}, [2]float64{4, 0.5})
	if got := keysOf(dropStranded(g, some, 4, DefaultRecallWeights.Strong, nil)); !reflect.DeepEqual(got, []int{0, 4}) {
		t.Errorf("the page the query names stays: %v", got)
	}
}

func TestDropStrandedAWeakSeedHoldingMoreOfTheQueryThanAnyStrongOneStays(t *testing.T) {
	g := &graph{nodes: []string{"strong", "more", "same", "less"}, adj: [][]edge{{}, {}, {}, {}}}
	all := seedsOf([2]float64{0, 1}, [2]float64{1, 0.5}, [2]float64{2, 0.5}, [2]float64{3, 0.5})
	cover := map[int]int{0: 2, 1: 6, 2: 2, 3: 1}
	if got := keysOf(dropStranded(g, all, noSeed, DefaultRecallWeights.Strong, cover)); !reflect.DeepEqual(got, []int{0, 1}) {
		t.Errorf("only the weak seed that holds more of the query than the strong one stays: %v", got)
	}
}

func TestContentTermsAndCoverageStopwordsOutALongTermMatchesItsStem(t *testing.T) {
	if got := contentTerms("Meta e WhatsApp"); !reflect.DeepEqual(got, []string{"meta", "whatsapp"}) {
		t.Errorf("%v", got)
	}
	if got := contentTerms("chi gioca a Minecraft con me"); !reflect.DeepEqual(got, []string{"gioca", "minecraft"}) {
		t.Errorf("%v", got)
	}
	if got := coverage("Si giocare a Minecraft, ogni sera.", []string{"gioca", "minecraft"}); got != 2 {
		t.Errorf("coverage = %d", got)
	}
	if got := coverage("I metadati e WhatsApp Web.", []string{"meta", "whatsapp"}); got != 1 {
		t.Errorf("a short term is not a prefix: %d", got)
	}
}

func TestRecallAPageThatHoldsOnlyPartOfTheQueryWordsGoesWhenAnotherHoldsThemAll(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "aziende/meta.md", pageOf("Meta", "Meta è l'azienda di WhatsApp e Instagram."))
	write(t, s, "note/browser.md", pageOf("Browser", "Le notifiche di WhatsApp Web si rompono con la configurazione."))
	write(t, s, "note/altro.md", pageOf("Altro", "Una nota su Meta Quest."))
	a := recall(t, s, ix, "Meta e WhatsApp", RecallOptions{Budget: 4000})
	got := shown(a)
	if got[0] != "aziende/meta.md" {
		t.Errorf("first = %s", got[0])
	}
	if slices.Contains(got, "note/browser.md") {
		t.Error("holds WhatsApp only, and nothing joins it to the page")
	}
	if slices.Contains(got, "note/altro.md") {
		t.Error("holds Meta only")
	}
	// a link from the full page brings it back: it is then reached, not a stray hit
	write(t, s, "aziende/meta.md", pageOf("Meta", "Meta è l'azienda di WhatsApp. Vedi [[note/browser]]."))
	b := recall(t, s, ix, "Meta e WhatsApp", RecallOptions{Budget: 4000})
	if !slices.Contains(shown(b), "note/browser.md") {
		t.Errorf("a linked page comes back: %v", shown(b))
	}
}

func TestRecallEveryRelatedPageSaysWhyItIsThere(t *testing.T) {
	s, ix := brain(t)
	for _, n := range []string{"uno", "due", "tre"} {
		write(t, s, "note/"+n+".md", pageOf(n, "Vedi [[giochi/tabletop]]."))
	}
	a := recall(t, s, ix, "tabletop", RecallOptions{Budget: 4000})
	if len(a.Related) == 0 {
		t.Fatal("no related pages")
	}
	for _, r := range a.Related {
		if r.Via.From == "" && r.Via.Found == "" {
			t.Errorf("%s says nothing of why", r.Path)
		}
	}
}

func TestRecallAPageNearOnlyInMeaningWithNoPathToTheSubjectStaysOutOfThePack(t *testing.T) {
	s, ix := recallStore(t)
	// the fake model puts car and automobile on one dimension: the words find only the first page
	write(t, s, "note/car.md", pageOf("Car", "Car vehicle car."))
	write(t, s, "note/stray.md", pageOf("Stray", "Automobile automobile."))
	write(t, s, "note/near.md", pageOf("Near", "Automobile automobile, vedi [[note/car]]."))
	e := &fakeEmbedder{model: "fake"}
	embedAll(t, ix, e)
	a := recall(t, s, ix, "car", RecallOptions{Embedder: e, Budget: 4000})
	if !a.Meaning {
		t.Error("meaning did not take part")
	}
	got := shown(a)
	if !slices.Contains(got, "note/car.md") || !slices.Contains(got, "note/near.md") {
		t.Errorf("pack = %v", got)
	}
	if slices.Contains(got, "note/stray.md") {
		t.Error("weak, unlinked, unrelated")
	}
}

func TestRecallAtMostTwoDiaryDaysAmongTheRelatedPagesHoweverManyLinkTheSubject(t *testing.T) {
	s, ix := brain(t)
	for d := 10; d < 18; d++ {
		write(t, s, fmt.Sprintf("diario/2026-10-%d.md", d),
			fmt.Sprintf("# 2026-10-%d\n\nIl giorno.\n\n- 10:00 sessione di [[giochi/tabletop]]\n", d))
	}
	a := recall(t, s, ix, "tabletop", RecallOptions{Budget: 4000})
	n := 0
	for _, r := range a.Related {
		if r.Kind == KindEpisode {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d diary days among the related pages", n)
	}
	if !slices.Contains(shown(a), "persone/alice.md") {
		t.Error("the pages that answer are crowded out")
	}
}

func TestTimeWindowItalianAndEnglishExpressionsTheDaysTheyCoverAndTheQueryWithoutThem(t *testing.T) {
	now := time.Date(2026, 10, 26, 13, 0, 0, 0, time.Local) // Monday
	check := func(q, from, to, rest string) {
		t.Helper()
		w := timeWindow(q, now)
		if w == nil || *w != (timeWin{from, to, rest}) {
			t.Errorf("timeWindow(%q) = %+v, want %s..%s %q", q, w, from, to, rest)
		}
	}
	check("cosa è cambiato su Delta oggi", "2026-10-26", "2026-10-26", "cosa è cambiato su Delta")
	check("what changed on Delta yesterday", "2026-10-25", "2026-10-25", "what changed on Delta")
	for _, q := range []string{"Delta questa settimana", "Delta this week", "Delta ultimamente", "Delta lately", "Delta recently"} {
		check(q, "2026-10-20", "2026-10-26", "Delta")
	}
	check("Delta negli ultimi giorni", "2026-10-20", "2026-10-26", "Delta negli")
	check("Delta la settimana scorsa", "2026-10-13", "2026-10-19", "Delta")
	check("ieri e questa settimana", "2026-10-20", "2026-10-26", "e")
	if timeWindow("la storia di Delta", now) != nil {
		t.Error("history is no window")
	}
	if timeWindow("il progetto Todayish", now) != nil {
		t.Error("a word that only contains one is not one")
	}
}

func TestRecallAQuestionAboutTheLastDaysBringsTheDiaryDaysOfThatWindowHoweverManyThereAre(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "progetti/delta.md", pageOf("Delta", "Il progetto del tabellone.\n\nÈ nato nel 2025, molto tempo fa."))
	write(t, s, "note/storia.md", pageOf("Storia", "Delta, Delta, Delta: tutta la storia di Delta."))
	for _, d := range []int{20, 21, 22, 23, 25} {
		write(t, s, fmt.Sprintf("diario/2026-10-%d.md", d),
			fmt.Sprintf("# 2026-10-%d\n\nIl giorno.\n\n- 10:00 lavoro su [[progetti/delta]]\n", d))
	}
	write(t, s, "diario/2026-10-24.md", "# 2026-10-24\n\nIl giorno.\n\n- 10:00 ho riparato Delta sul server\n")
	write(t, s, "diario/2026-09-01.md", "# 2026-09-01\n\nIl giorno.\n\n- 10:00 idea per [[progetti/delta]]\n")
	now := time.Date(2026, 10, 26, 13, 0, 0, 0, time.Local)
	days := func(a RecallAnswer) []string {
		var out []string
		for _, p := range shown(a) {
			if strings.HasPrefix(p, "diario/") {
				out = append(out, p)
			}
		}
		return out
	}
	plain := recall(t, s, ix, "cosa è cambiato su Delta", RecallOptions{Budget: 6000, Now: now})
	if len(days(plain)) >= 6 {
		t.Errorf("without an expression of time, the diary days are held to a few: %v", days(plain))
	}
	week := recall(t, s, ix, "cosa è cambiato su Delta questa settimana", RecallOptions{Budget: 6000, Now: now})
	window := []string{"20", "21", "22", "23", "24", "25"}
	listed := shown(week)
	for _, d := range window {
		if !slices.Contains(listed, "diario/2026-10-"+d+".md") {
			t.Errorf("diario %s is not in the pack: %v", d, listed)
		}
	}
	// the old day may still come through the page's link, but after every day of the window
	if old := slices.Index(listed, "diario/2026-09-01.md"); old != -1 {
		for _, d := range window {
			if slices.Index(listed, "diario/2026-10-"+d+".md") > old {
				t.Errorf("the old day comes before %s: %v", d, listed)
			}
		}
	}
}

func TestBelowFloorNothingRelevantOnlyWhenNoSeedHoldsMostOfTheWordsOrSitsNearInMeaning(t *testing.T) {
	f := func(x float64) *float64 { return &x }
	weak := floorSeed{cover: 1}
	for name, c := range map[string]struct {
		seeds []floorSeed
		total int
		min   float64
		want  bool
	}{
		"far":           {[]floorSeed{weak, {cosine: f(0.4)}}, 3, 0.6, true},
		"two of three":  {[]floorSeed{weak, {cover: 2}}, 3, 0.6, false},
		"near":          {[]floorSeed{weak, {cosine: f(0.7)}}, 3, 0.6, false},
		"floor off":     {[]floorSeed{weak}, 3, 0, false},
		"no content":    {[]floorSeed{{cover: 0}}, 0, 0.6, false},
		"exactly three": {[]floorSeed{{cover: 3}}, 3, 0.6, false},
		"named":         {[]floorSeed{weak, {named: true}}, 3, 0.6, false},
	} {
		if got := belowFloor(c.seeds, c.total, c.min, 0.55); got != c.want {
			t.Errorf("%s: belowFloor = %v, want %v", name, got, c.want)
		}
	}
}

func TestRecallAQuestionTheMemoryCannotAnswerReturnsNothingOneItCanStillReturnsThePage(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "note/polizza.md", pageOf("Polizza RC",
		"La polizza di responsabilità professionale.\n\nL'assicurazione obbligatoria dello studio."))
	write(t, s, "note/studio.md", pageOf("Studio", "Lo studio ha una [[note/polizza]] per i clienti."))
	none := recall(t, s, ix, "la mia assicurazione dell'auto", RecallOptions{Budget: 4000})
	if len(none.Pages) != 0 || len(none.Related) != 0 || !reflect.DeepEqual(none.Notes, []string{"nothing relevant"}) {
		t.Errorf("none = %+v", none)
	}
	some := recall(t, s, ix, "assicurazione obbligatoria dello studio", RecallOptions{Budget: 4000})
	if len(some.Pages) == 0 || some.Pages[0].Path != "note/polizza.md" || some.Notes != nil {
		t.Errorf("some = %+v", some)
	}
}

func infra(t *testing.T) (*Store, *search.Index) {
	s, ix := recallStore(t)
	write(t, s, "infra/venus.md", pageOf("Server Venus",
		"Elenco lungo dei servizi di casa, uno per riga, con le loro porte e i loro orari di avvio, su venus. Server venus, server venus, server venus: il server venus.\n\n## Relazioni\n\n- has_part [[infra/venus/firewall]]\n"+
			"- has_part [[infra/venus/backup]]\n- has_part [[infra/venus/monitoraggio]]\n"))
	write(t, s, "infra/venus/firewall.md", pageOf("Firewall", "Il firewall blocca tutto tranne ssh.\n\n## Relazioni\n\n- part_of [[infra/venus]]\n"))
	write(t, s, "infra/venus/backup.md", pageOf("Backup", "Dump notturni del server venus.\n\n## Relazioni\n\n- part_of [[infra/venus]]\n"))
	write(t, s, "infra/venus/monitoraggio.md", pageOf("Monitoraggio", "Gli avvisi del server venus.\n\n## Relazioni\n\n- part_of [[infra/venus]]\n"))
	return s, ix
}

func TestRecallAWeakHubDoesNotEraseTheSiblingTheSearchFoundItself(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "note/guida.md", pageOf("Guida firewall ssh venus", "Firewall ssh venus: firewall ssh venus, firewall ssh venus."))
	write(t, s, "note/appunti.md", pageOf("Appunti firewall ssh", "Firewall ssh venus: firewall ssh venus."))
	write(t, s, "note/ricette.md", pageOf("Ricette firewall ssh", "Firewall ssh venus: firewall ssh venus."))
	write(t, s, "note/extra0.md", pageOf("Extra0 firewall ssh", "Firewall ssh venus: firewall ssh venus."))
	write(t, s, "note/extra1.md", pageOf("Extra1 firewall ssh", "Firewall ssh venus: firewall ssh venus."))
	write(t, s, "note/extra2.md", pageOf("Extra2 firewall ssh", "Firewall ssh venus: firewall ssh venus."))
	write(t, s, "note/extra3.md", pageOf("Extra3 firewall ssh", "Firewall ssh venus: firewall ssh venus."))
	write(t, s, "infra/venus.md", pageOf("Server", "Elenco lungo dei servizi di casa, uno per riga, con le loro porte e i loro orari di avvio, su venus.\n\n## Relazioni\n\n"+
		"- has_part [[infra/venus/firewall]]\n- has_part [[infra/venus/backup]]\n"))
	write(t, s, "infra/venus/firewall.md", pageOf("Regole", "Il firewall blocca tutto tranne ssh su venus.\n\n## Relazioni\n\n- part_of [[infra/venus]]\n"))
	write(t, s, "infra/venus/backup.md", pageOf("Dump", "Dump notturni di venus.\n\n## Relazioni\n\n- part_of [[infra/venus]]\n"))
	a := recall(t, s, ix, "firewall ssh venus dump", RecallOptions{Budget: 4000})
	got := shown(a)
	if !slices.Contains(got, "infra/venus/firewall.md") || !slices.Contains(got, "infra/venus/backup.md") {
		t.Errorf("the hub is not the subject of the question, so the lifted child's sibling that the search found stays: %v", got)
	}
}

func TestRecallForASpecificQuestionTheChildThatHoldsTheExtraWordLeadsAndItsSiblingsStayOut(t *testing.T) {
	s, ix := infra(t)
	a := recall(t, s, ix, "il firewall del server venus", RecallOptions{Budget: 4000})
	if a.Pages[0].Path != "infra/venus/firewall.md" {
		t.Errorf("first = %s", a.Pages[0].Path)
	}
	got := shown(a)
	if slices.Contains(got, "infra/venus/backup.md") || slices.Contains(got, "infra/venus/monitoraggio.md") {
		t.Errorf("a sibling the question does not ask for: %v", got)
	}
	// the hub itself is still the answer to a question about the server
	if hub := recall(t, s, ix, "server venus", RecallOptions{Budget: 4000}); hub.Pages[0].Path != "infra/venus.md" {
		t.Errorf("hub first = %s", hub.Pages[0].Path)
	}
}

func TestRecallAPageThatCoversMoreOfTheQueryWordsBeatsOneThatRepeatsASingleWord(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "progetti/leads.md", pageOf("WhatsApp Lead Qualificator", strings.Repeat("WhatsApp, WhatsApp Business, WhatsApp API. ", 30)))
	write(t, s, "giochi/campagna.md", pageOf("Dungeons e Draghi",
		"La campagna della sera: il gruppo su WhatsApp avvisa. "+strings.Repeat("Si gioca con i dadi e le schede. ", 40)))
	write(t, s, "note/altro.md", pageOf("Altro", "Una nota sul gruppo di lettura."))
	for i := 0; i < 12; i++ {
		write(t, s, fmt.Sprintf("note/n%d.md", i), pageOf(fmt.Sprintf("Nota %d", i), fmt.Sprintf("Una nota sul gruppo della campagna numero %d.", i)))
	}
	a := recall(t, s, ix, "il gruppo WhatsApp della campagna di giovedì", RecallOptions{Budget: 4000})
	if a.Pages[0].Path != "giochi/campagna.md" {
		t.Errorf("first = %s", a.Pages[0].Path)
	}
}

func TestChildrenOfPartOfAndTheHubsFolderMakeChildrenADiaryDayOrATaskPartOfTheHubDoesNot(t *testing.T) {
	s, _ := recallStore(t)
	write(t, s, "progetti/delta.md", pageOf("Delta", "Il progetto."))
	write(t, s, "progetti/delta/api.md", pageOf("API", "Le rotte."))
	write(t, s, "note/design.md", pageOf("Design", "Il disegno.\n\n## Relazioni\n\n- part_of [[progetti/delta]]\n"))
	write(t, s, "diario/2026-10-10.md", "# 2026-10-10\n\nIl giorno.\n\n## Relazioni\n\n- part_of [[progetti/delta]]\n")
	rc := s.recaller(ctx)
	g, err := rc.loadGraph(DefaultRecallWeights, "2026-11-03")
	if err != nil {
		t.Fatal(err)
	}
	kinds, _, err := rc.nodeKinds()
	if err != nil {
		t.Fatal(err)
	}
	kids, err := rc.childrenOf(g, kinds)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, i := range kids[g.index["progetti/delta.md"]] {
		got = append(got, g.nodes[i])
	}
	slices.Sort(got)
	if !reflect.DeepEqual(got, []string{"note/design.md", "progetti/delta/api.md"}) {
		t.Errorf("children = %v", got)
	}
}

func TestRecallChildrenTiedToTheirHubByPlainLinksOrByNamingItTheirParentPageAreRecognised(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "note/server-x.md", pageOf("Server X",
		"Il server x di casa. Server x, server x, server x.\n\nSicurezza: [[note/server-x-sicurezza]]; hardware: "+
			"[[note/server-x-hardware]]; backup: [[note/server-x-backup]]; ambiente: [[io/ambiente]]; altro: [[io/altro]]."))
	write(t, s, "note/server-x-sicurezza.md", pageOf("Sicurezza di X", "Il firewall e le porte aperte; la pagina madre → [[note/server-x]]."))
	write(t, s, "note/server-x-hardware.md", pageOf("Hardware di X", "La CPU del server x; la pagina madre → [[note/server-x]]."))
	write(t, s, "note/server-x-backup.md", pageOf("Backup di X", "Dump notturni del server x, vedi [[note/server-x]]."))
	write(t, s, "io/ambiente.md", pageOf("Ambiente", "Le macchine."))
	write(t, s, "io/altro.md", pageOf("Altro", "Altro ancora."))
	a := recall(t, s, ix, "il firewall del server x", RecallOptions{Budget: 4000})
	if a.Pages[0].Path != "note/server-x-sicurezza.md" {
		t.Errorf("first = %s", a.Pages[0].Path)
	}
	got := shown(a)
	if slices.Contains(got, "note/server-x-hardware.md") {
		t.Error("a sibling (named parent)")
	}
	if slices.Contains(got, "note/server-x-backup.md") {
		t.Error("a sibling (mutual link)")
	}
}

func TestRecallAQuestionOfTimeLooksForTheEpisodesOfThePageItsTitleNamesSeedOrNot(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "progetti/delta.md", pageOf("Delta", "Il progetto del tabellone."))
	// the words «cambiato» and «delta» are all in this page, so the page titled Delta is no seed of the question
	write(t, s, "note/storia.md", pageOf("Storia", "Delta è cambiato molto: Delta cambiato di nuovo, cambiato ancora."))
	for _, d := range []int{22, 23, 25} {
		write(t, s, fmt.Sprintf("diario/2026-10-%d.md", d),
			fmt.Sprintf("# 2026-10-%d\n\nIl giorno.\n\n- 10:00 lavoro su [[progetti/delta]]\n", d))
	}
	a := recall(t, s, ix, "cosa è cambiato su Delta questa settimana", RecallOptions{Budget: 6000, Now: time.Date(2026, 10, 26, 13, 0, 0, 0, time.Local)})
	for _, d := range []int{22, 23, 25} {
		if !slices.Contains(shown(a), fmt.Sprintf("diario/2026-10-%d.md", d)) {
			t.Errorf("diario %d is not in the pack", d)
		}
	}
}

func TestRecallWithManyPagesNamedLikeTheQuestionTheWindowsSubjectsAreTheBestTitlesNotTheFirst(t *testing.T) {
	s, ix := recallStore(t)
	// ten pages hold the word in their title and are written first; the page the diary links is the eleventh
	for i := 0; i < 10; i++ {
		write(t, s, fmt.Sprintf("note/delta-%d.md", i), pageOf(fmt.Sprintf("Delta nota %d", i), fmt.Sprintf("Appunto numero %d.", i)))
	}
	write(t, s, "progetti/delta.md", pageOf("Delta", "Il progetto del tabellone."))
	write(t, s, "note/storia.md", pageOf("Storia", "Delta è cambiato molto: Delta cambiato di nuovo, cambiato ancora."))
	for _, d := range []int{22, 25} {
		write(t, s, fmt.Sprintf("diario/2026-10-%d.md", d),
			fmt.Sprintf("# 2026-10-%d\n\nIl giorno.\n\n- 10:00 lavoro su [[progetti/delta]]\n", d))
	}
	a := recall(t, s, ix, "cosa è cambiato su Delta questa settimana", RecallOptions{Budget: 6000, Now: time.Date(2026, 10, 26, 13, 0, 0, 0, time.Local)})
	for _, d := range []int{22, 25} {
		if !slices.Contains(shown(a), fmt.Sprintf("diario/2026-10-%d.md", d)) {
			t.Errorf("diario %d is not in the pack", d)
		}
	}
}

func TestRecallOfTheChildrenThatHoldAWordTheHubLacksOnlyThoseHoldingMostOfTheQuestionsWordsLead(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "note/server-x.md", pageOf("Server X", "Il server x di casa. Server x, server x, server x."))
	write(t, s, "note/server-x-sicurezza.md", pageOf("Sicurezza", "Il firewall e le porte aperte del server x.\n\n## Relazioni\n\n- part_of [[note/server-x]]\n"))
	// it holds «porte» too, which the hub lacks, but nothing else of the question
	write(t, s, "note/server-x-hardware.md", pageOf("Hardware", "Una porta di rete sul server x.\n\n## Relazioni\n\n- part_of [[note/server-x]]\n"))
	a := recall(t, s, ix, "firewall e porte del server x", RecallOptions{Budget: 4000})
	if a.Pages[0].Path != "note/server-x-sicurezza.md" {
		t.Errorf("first = %s", a.Pages[0].Path)
	}
	for _, p := range a.Pages {
		if p.Path == "note/server-x-hardware.md" {
			t.Error("a sibling that only shares one word is lifted")
		}
	}
}

func TestRecallAChildTheSearchDidNotFindIsNotLiftedAboveItsHubForAWordTheHubLacks(t *testing.T) {
	s, ix := recallStore(t)
	// the guide holds every word of the question, so the child, which holds one and is no neighbour of the guide, is
	// not a seed: the only thing in its favour is a word its hub lacks
	write(t, s, "note/guida.md", pageOf("Guida", "Firewall, regole e ventola del server x. Vedi [[note/server-x]].\n"))
	write(t, s, "note/server-x.md", pageOf("Server X", "Il firewall e le regole del server x. Firewall, regole.\n\n## Relazioni\n\n- Vedi [[note/server-x-hardware]]\n"))
	write(t, s, "note/server-x-hardware.md", pageOf("Hardware", "Il disco e la ventola.\n\n## Relazioni\n\n- part_of [[note/server-x]]\n"))
	write(t, s, "note/altro.md", pageOf("Altro", "Una nota sul gruppo di lettura."))
	a := recall(t, s, ix, "firewall regole ventola", RecallOptions{Budget: 4000})
	if len(a.Pages) == 0 || a.Pages[0].Path != "note/server-x.md" {
		t.Errorf("pages in full = %v", shown(a))
	}
}

func TestRecallWhenAPageLinkedToTheHubAnswersTheQuestionBetterTheHubsOtherPlainLinksStayOut(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "note/venus.md", pageOf("Venus",
		"Il server venus. Venus, venus, venus. Vedi [[note/sicurezza-venus]], [[note/rete]], [[note/disco]], [[note/ups]]."))
	write(t, s, "note/sicurezza-venus.md", pageOf("Sicurezza di venus", "Il firewall e le porte aperte di venus; vedi [[note/venus]]."))
	for _, n := range []string{"rete", "disco", "ups"} {
		write(t, s, "note/"+n+".md", pageOf(n, "Appunti su venus e sul suo "+n+"."))
	}
	got := shown(recall(t, s, ix, "firewall e porte aperte di venus", RecallOptions{Budget: 4000}))
	if slices.Contains(got, "note/rete.md") {
		t.Errorf("a plain link of the hub is in the pack: %v", got)
	}
	if !slices.Contains(got, "note/sicurezza-venus.md") {
		t.Errorf("the better page is not: %v", got)
	}
}

func TestPreferChildrenAndWithoutSiblingsTheChildLeadsItsHubAHubKeepsEdgesOnlyToTheChildrenKept(t *testing.T) {
	if got := preferChildren([]int{5, 1, 2, 3}, map[int][]int{1: {3}}); !reflect.DeepEqual(got, []int{5, 3, 1, 2}) {
		t.Errorf("preferChildren = %v", got)
	}
	if got := preferChildren([]int{5, 1}, map[int][]int{1: {9}}); !reflect.DeepEqual(got, []int{5, 1}) {
		t.Errorf("a child the walk did not reach stays out: %v", got)
	}
	e := func(to int) edge { return edge{to: to, w: 1} }
	g := &graph{nodes: []string{"h", "a", "b", "x"}, adj: [][]edge{{e(1), e(2), e(3)}, {e(0)}, {e(0)}, {e(0)}}}
	cut := withoutSiblings(g, []siblingCut{{hub: 0, kids: []int{1, 2}, keep: []int{1}}})
	to := func(es []edge) (out []int) {
		for _, x := range es {
			out = append(out, x.to)
		}
		return
	}
	if got := to(cut.adj[0]); !reflect.DeepEqual(got, []int{1, 3}) {
		t.Errorf("b goes, a is kept, x is no child: %v", got)
	}
	if got := to(cut.adj[2]); !reflect.DeepEqual(got, []int{0}) {
		t.Errorf("the way back stays: %v", got)
	}
	if len(g.adj[0]) != 3 {
		t.Error("the graph itself is changed")
	}
}

func TestRecallTheWindowsDiaryDaysAboutTheSubjectReachThePackEvenPastAHubsManyNeighbours(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "progetti/delta.md", pageOf("Delta", "Il progetto del tabellone."))
	for i := 0; i < 12; i++ {
		write(t, s, fmt.Sprintf("progetti/delta/p%d.md", i),
			pageOf(fmt.Sprintf("Parte %d", i), "Un pezzo.\n\n## Relazioni\n\n- part_of [[progetti/delta]]\n"))
	}
	write(t, s, "diario/2026-10-25.md", "# 2026-10-25\n\nIl giorno.\n\n- 10:00 lavoro su [[progetti/delta]]\n")
	// a day that only logs a task part of the subject
	write(t, s, "tasks/t-20261026-aaaaaa.md", "---\nkind: task\n---\n# Taratura\n\nDa fare.\n\n## Relazioni\n\n- part_of [[progetti/delta]]\n")
	write(t, s, "diario/2026-10-26.md", "# 2026-10-26\n\nIl giorno.\n\n- 14:53 [[t-20261026-aaaaaa]] Taratura: completata\n")
	// outside the window, and about another subject
	write(t, s, "diario/2026-09-01.md", "# 2026-09-01\n\nIl giorno.\n\n- 10:00 idea per [[progetti/delta]]\n")
	write(t, s, "progetti/altro.md", pageOf("Altro", "Un altro progetto."))
	write(t, s, "diario/2026-10-24.md", "# 2026-10-24\n\nIl giorno.\n\n- 10:00 lavoro su [[progetti/altro]]\n")
	now := time.Date(2026, 10, 26, 13, 0, 0, 0, time.Local)
	week := shown(recall(t, s, ix, "cosa è cambiato su Delta questa settimana", RecallOptions{Budget: 6000, Now: now}))
	if !slices.Contains(week, "diario/2026-10-25.md") {
		t.Error("the day that links the hub is not in the pack")
	}
	if !slices.Contains(week, "diario/2026-10-26.md") {
		t.Error("the day that logs a task of the hub is not in the pack")
	}
	if slices.Contains(week, "diario/2026-09-01.md") {
		t.Error("outside the window")
	}
	if slices.Contains(week, "diario/2026-10-24.md") {
		t.Error("about another subject")
	}
	plain := shown(recall(t, s, ix, "cosa è cambiato su Delta", RecallOptions{Budget: 6000, Now: now}))
	if slices.Contains(plain, "diario/2026-10-24.md") {
		t.Error("without a time expression the window plays no part")
	}
}

func TestEpisodesInTheWindowsDiaryDaysComeBeforeTaskLogsAndTheInboxIsNoDay(t *testing.T) {
	s, _ := recallStore(t)
	write(t, s, "progetti/delta.md", pageOf("Delta", "Il progetto del tabellone."))
	write(t, s, "diario/2026-10-25.md", "# 2026-10-25\n\nIl giorno.\n\n- 10:00 lavoro su [[progetti/delta]]\n")
	write(t, s, "diario/2026-10-26.md", "# 2026-10-26\n\nIl giorno.\n\n- 11:00 ancora [[progetti/delta]]\n")
	write(t, s, "inbox/inbox.md", "# Inbox\n\n- 12:00 da smistare [[t-20261026-aaaaaa]]\n")
	for _, n := range []string{"aaaaaa", "bbbbbb", "cccccc", "dddddd"} {
		write(t, s, "tasks/t-20261026-"+n+".md",
			"---\nkind: task\n---\n# Task "+n+"\n\nDa fare.\n\n## Relazioni\n\n- part_of [[progetti/delta]]\n")
		write(t, s, "tasks/t-20261026-"+n+"/log.md",
			"---\nkind: episode\n---\n# Log "+n+"\n\n- 13:00 avanzamento.\n\n## Relazioni\n\n- part_of [[t-20261026-"+n+"]]\n")
	}
	rc := s.recaller(ctx)
	g, err := rc.loadGraph(DefaultRecallWeights, "2026-11-03")
	if err != nil {
		t.Fatal(err)
	}
	// a log is dated by when it was written, today: the window reaches today, or the logs fall out of it
	to := time.Now().UTC().Format(dayLayout)
	at := g.index["progetti/delta.md"]
	idx, err := rc.episodesIn(g, []int{at}, "2026-10-20", to, 3)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, i := range idx {
		got = append(got, g.nodes[i])
	}
	if !reflect.DeepEqual(got[:2], []string{"diario/2026-10-26.md", "diario/2026-10-25.md"}) {
		t.Errorf("the days lead, newest first: %v", got)
	}
	if !strings.HasSuffix(got[2], "/log.md") {
		t.Errorf("a log fills what the days leave: %v", got)
	}
	all, err := rc.episodesIn(g, []int{at}, "2026-10-20", to, 30)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range all {
		if strings.HasPrefix(g.nodes[i], "inbox/") {
			t.Error("the inbox is a day")
		}
	}
}

func TestRecallTheInboxIsNeverASeedWhateverWordsItHolds(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "note/moto.md", pageOf("Moto", "La moto ha il carburatore da pulire."))
	write(t, s, "inbox/inbox.md", "---\nkind: episode\n---\n\n# Inbox\n\n- carburatore della moto da ordinare\n")
	got := shown(recall(t, s, ix, "carburatore della moto", RecallOptions{Budget: 4000}))
	if !slices.Contains(got, "note/moto.md") {
		t.Errorf("pack = %v", got)
	}
	if slices.Contains(got, "inbox/inbox.md") {
		t.Errorf("the inbox is in the pack: %v", got)
	}
}

func TestSeedableTheInboxNeverAnEpisodeOnlyWhenItsWordsWereFoundAnyOtherPageByEither(t *testing.T) {
	for name, c := range map[string]struct {
		path  string
		kind  Kind
		words bool
		want  bool
	}{
		"inbox":                {"inbox/inbox.md", KindEpisode, true, false},
		"log found by meaning": {"tasks/t-1/log.md", KindEpisode, false, false},
		"log found by words":   {"tasks/t-1/log.md", KindEpisode, true, true},
		"diary day by words":   {"diario/2026-10-26.md", KindEpisode, true, true},
		"concept by meaning":   {"note/car.md", KindConcept, false, true},
	} {
		if got := seedable(c.path, c.kind, c.words, "inbox"); got != c.want {
			t.Errorf("%s: seedable = %v, want %v", name, got, c.want)
		}
	}
}

func TestAboutPastATimeWindowOrACueOfThePastAPlainQuestionIsNot(t *testing.T) {
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	for _, q := range []string{
		"cosa c'era prima sul sito", "una volta usavo hetzner", "cosa è successo a settembre", "quando ho cambiato server",
		"la storia del progetto", "server history", "what did it use before", "it used to run on contabo",
		"what changed in March", "cosa ho fatto ieri",
	} {
		if !aboutPast(q, now) {
			t.Errorf("aboutPast(%q) = false", q)
		}
	}
	for _, q := range []string{"dove gira il sito", "what is the plan", "la primavera", "in marzo2"} {
		if aboutPast(q, now) {
			t.Errorf("aboutPast(%q) = true", q)
		}
	}
}

func TestWithoutHistoryTheHistorySectionGoesUpToTheNextHeading(t *testing.T) {
	b := "# A\n\nFrase.\n\n## Storia\n\n- fino al 2026-10-20: vecchio\n\n## Altro\n\nResto.\n"
	if got, had := withoutHistory(b); got != "# A\n\nFrase.\n\n## Altro\n\nResto.\n" || !had {
		t.Errorf("withoutHistory = %q, %v", got, had)
	}
	if got, had := withoutHistory("# A\n\nFrase.\n"); got != "# A\n\nFrase.\n" || had {
		t.Errorf("no section: %q, %v", got, had)
	}
	if _, had := withoutHistory("# A\n\n## History\n\n- x\n"); !had {
		t.Error("## History is a history section")
	}
}

func TestRecallAPageOverItsShareLosesItsHistoryFirstUnlessTheQuestionLooksBack(t *testing.T) {
	s, ix := recallStore(t)
	var hist []string
	for i := 0; i < 200; i++ {
		hist = append(hist, fmt.Sprintf("- fino al 2026-09-%d: vecchio fatto numero %d", i%28+1, i))
	}
	write(t, s, "progetti/sito.md", pageOf("Sito",
		"Il sito della documentazione, prima e dopo.\n\n## Stato\n\n- gira su un server nuovo (2026-10-26)\n\n## Storia\n\n"+
			strings.Join(hist, "\n")+"\n"))
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	present := recall(t, s, ix, "sito", RecallOptions{Budget: 600, Now: now})
	if !strings.Contains(present.Pages[0].Body, "server nuovo") {
		t.Error("the present is cut")
	}
	if strings.Contains(present.Pages[0].Body, "vecchio fatto") {
		t.Error("history is not cut first")
	}
	back := recall(t, s, ix, "sito prima", RecallOptions{Budget: 600, Now: now})
	if !strings.Contains(back.Pages[0].Body, "vecchio fatto") {
		t.Error("the question looks back: history kept")
	}
}

func TestLoadGraphARelationThatEndedBeforeTodayNoLongerSpreadsActivationOneStillOpenDoes(t *testing.T) {
	s, _ := recallStore(t)
	write(t, s, "persone/bob.md", pageOf("Bob", "Un collega."))
	write(t, s, "persone/carl.md", pageOf("Carl", "Un altro collega."))
	write(t, s, "progetti/sito.md", pageOf("Sito",
		"Il sito.\n\n## Relazioni\n\n- worked_on_by [[persone/bob]] (fino al 2026-09)\n- worked_on_by [[persone/carl]] (until 2026-12)\n"))
	rc := s.recaller(ctx)
	g, err := rc.loadGraph(DefaultRecallWeights, "2026-10-27")
	if err != nil {
		t.Fatal(err)
	}
	var to []string
	for _, e := range g.adj[g.index["progetti/sito.md"]] {
		to = append(to, g.nodes[e.to])
	}
	if slices.Contains(to, "persone/bob.md") || !slices.Contains(to, "persone/carl.md") {
		t.Errorf("neighbours = %v", to)
	}
	var n int
	if err := s.db.QueryRow("select count(*) from links where src = 'progetti/sito.md'").Scan(&n); err != nil || n != 2 {
		t.Errorf("the line stays on the page: %d, %v", n, err)
	}
}

func TestRecallAnEpisodeFoldedIntoItsWeekCountsLessThanTheWeekPage(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "progetti/delta.md", pageOf("Delta", "Il progetto del tabellone."))
	write(t, s, "diario/settimane/2026-w43.md", "# Settimana 43\n\nDelta è avanzato: [[progetti/delta]].\n")
	write(t, s, "diario/2026-10-22.md", "---\nsummarised: diario/settimane/2026-w43\n---\n# 2026-10-22\n\n- 10:00 lavoro su [[progetti/delta]]\n")
	rc := s.recaller(ctx)
	rec, err := rc.recordPages()
	if err != nil {
		t.Fatal(err)
	}
	if rec["diario/2026-10-22.md"] != "diario/settimane/2026-w43.md" {
		t.Errorf("records = %v", rec)
	}
	a := recall(t, s, ix, "cosa è cambiato su Delta questa settimana", RecallOptions{Budget: 6000, Now: time.Date(2026, 10, 26, 13, 0, 0, 0, time.Local)})
	if !slices.Contains(shown(a), "diario/settimane/2026-w43.md") {
		t.Errorf("the week page brings its days: %v", shown(a))
	}
}

func TestRecallWithoutAnIndexAQueryIsAnError(t *testing.T) {
	s, _ := recallStore(t)
	if _, err := s.Recall(ctx, "x", RecallOptions{}); err == nil {
		t.Error("no error")
	}
}

func TestViaOfTheShownNeighbourThatMostLikelyBroughtThePageUpAndTheNameOfTheEdge(t *testing.T) {
	g := &graph{
		nodes: []string{"a", "b", "c"},
		adj: [][]edge{
			{{to: 1, w: 1, rel: "plays_with"}, {to: 2, w: 0.5, meaning: true}},
			{{to: 0, w: 1, rel: "plays_with"}},
			{{to: 0, w: 0.5, meaning: true}},
		},
	}
	r := []float64{0.2, 0.5, 0.3}
	if got := viaOf(g, 0, map[int]bool{1: true, 2: true}, r); *got != (Via{From: "b", Rel: "plays_with"}) {
		t.Errorf("via = %+v", got)
	}
	if got := viaOf(g, 0, map[int]bool{2: true}, r); *got != (Via{From: "c", Meaning: true}) {
		t.Errorf("via = %+v", got)
	}
	if viaOf(g, 0, map[int]bool{}, r) != nil {
		t.Error("none of the neighbours is in the pack")
	}
	if got := viaOf(g, 0, nil, r); got.From != "b" {
		t.Errorf("any page: %+v", got)
	}
}

func TestRecallAPageWhoseTitleHoldsAQueryWordPassesTheGateHoweverFewOfTheOtherWordsItHolds(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "note/polizza.md", pageOf("Polizza RC", "La responsabilità professionale dello studio."))
	write(t, s, "note/studio.md", pageOf("Studio", "Lo studio ha una [[note/polizza]] per i clienti."))
	// two of the four words are not enough for the floor, but the title is the page the question is about
	a := recall(t, s, ix, "polizza rc scadenza rinnovo premio", RecallOptions{Budget: 4000})
	if len(a.Pages) == 0 || a.Pages[0].Path != "note/polizza.md" || a.Notes != nil {
		t.Errorf("answer = %+v", a)
	}
}

func TestRecallAPageNamedInsideALongerQueryIsPinned(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "progetti/nome-progetto.md", pageOf("Nome progetto", "Il piano di lavoro."))
	write(t, s, "note/altro.md", pageOf("Altro", "Scadenza, rinnovo e premio: la scadenza del rinnovo del premio."))
	a := recall(t, s, ix, "scadenza rinnovo premio Nome progetto", RecallOptions{Budget: 4000})
	if len(a.Pages) == 0 || a.Pages[0].Path != "progetti/nome-progetto.md" {
		t.Errorf("answer = %v", shown(a))
	}
}

func TestRecallTheLogOfAClosedTaskRanksBelowThatOfAnOpenOne(t *testing.T) {
	s, ix := recallStore(t)
	// the closed one sorts first by path, so only its group puts it second
	for _, n := range []string{"aaaaaa", "bbbbbb"} {
		write(t, s, "tasks/t-20261026-"+n+"/log.md",
			"---\nkind: episode\n---\n# Log "+n+"\n\n- 13:00 rollout del tabellone.\n")
	}
	groups := func(context.Context) (map[string]string, error) {
		return map[string]string{"tasks/t-20261026-aaaaaa/log.md": "closed"}, nil
	}
	a := recall(t, s, ix, "rollout tabellone", RecallOptions{TaskGroups: groups, Budget: 4000})
	got := shown(a)
	if len(got) < 2 || got[0] != "tasks/t-20261026-bbbbbb/log.md" {
		t.Errorf("order = %v", got)
	}
}

func TestRecallADocumentThatOutscoresTheBestPageDoesNotTakeThePageOutOfThePack(t *testing.T) {
	s, ix := recallStore(t)
	v := "1"
	col, err := ix.CreateCollection(ctx, "Manuale", search.CollectionFields{Version: &v})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.AddDocument(ctx, search.NewDocument{Collection: col, Kind: search.KindMarkdown, Title: "Firewall", Origin: "fw.md",
		Sections: []search.Section{{Text: "# Firewall\n\nIl firewall del router: regole, porte, ssh. Firewall firewall regole porte router ssh."}}}); err != nil {
		t.Fatal(err)
	}
	// the page holds three of the five words (enough for the floor), so its score is cut by the coverage and the document
	// leads it by more than the share a page needs; being the only page, it is still the best the memory has
	write(t, s, "note/rete.md", pageOf("Rete", "Il firewall, il router e ssh di casa."))
	a := recall(t, s, ix, "firewall regole porte router ssh", RecallOptions{Budget: 4000})
	if len(a.Documents) == 0 {
		t.Fatal("no document")
	}
	if len(a.Pages) == 0 || a.Pages[0].Path != "note/rete.md" {
		t.Errorf("pages in full = %v, pack = %v", a.Pages, shown(a))
	}
}

func TestRecallLeavesOutADocumentThatScoresUnderTheSeedShareOfTheBestHit(t *testing.T) {
	s, ix := recallStore(t)
	add := func(name, text string) {
		col, err := ix.CreateCollection(ctx, name, search.CollectionFields{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ix.AddDocument(ctx, search.NewDocument{Collection: col, Kind: search.KindMarkdown, Title: name, Origin: name + ".md",
			Sections: []search.Section{{Text: text}}}); err != nil {
			t.Fatal(err)
		}
	}
	add("Strong", "# Firewall\n\nIl firewall del router: regole, porte, ssh. Firewall firewall regole porte router ssh.")
	add("Weak", "# Elenco\n\n"+strings.Repeat("Una lunga lista di cose che non c entrano, ", 40)+"ssh.")
	a := recall(t, s, ix, "firewall regole porte router ssh", RecallOptions{Budget: 4000})
	titles := []string{}
	for _, d := range a.Documents {
		titles = append(titles, d.Title)
	}
	if len(titles) != 1 || titles[0] != "Strong" {
		t.Errorf("documents = %v, want only the one that answers", titles)
	}
}

func TestRecallADocumentIsWeighedAgainstAPageByTheScoreTheSearchGaveBothNotByTheCoverageShrunkOne(t *testing.T) {
	s, ix := recallStore(t)
	v := "1"
	col, err := ix.CreateCollection(ctx, "Manuale", search.CollectionFields{Version: &v})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.AddDocument(ctx, search.NewDocument{Collection: col, Kind: search.KindMarkdown, Title: "Firewall", Origin: "fw.md",
		Sections: []search.Section{{Text: "# Firewall\n\nIl firewall del router: regole, porte, ssh. Firewall firewall regole porte router ssh."}}}); err != nil {
		t.Fatal(err)
	}
	write(t, s, "note/rete.md", pageOf("Rete", "Firewall, regole, porte, router, ssh di casa. Il firewall, le regole, le porte, il router e ssh."))
	// next to the document in the search; only three of the five words cut its score under the share the document
	// allows, which the search's own score does not
	write(t, s, "note/casa.md", pageOf("Casa", "Il firewall, il router e ssh di casa, vedi [[note/rete]]. "+strings.Repeat("Firewall router ssh. ", 6)))
	a := recall(t, s, ix, "firewall regole porte router ssh", RecallOptions{Budget: 4000})
	var full []string
	for _, p := range a.Pages {
		full = append(full, p.Path)
	}
	if !slices.Contains(full, "note/casa.md") {
		t.Errorf("pages in full = %v, pack = %v", full, shown(a))
	}
}

func TestRecallAPageWhoseTitleHoldsTheQueryWordsLeadsOneThatOnlyRepeatsThemInItsBody(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "progetti/delta.md", pageOf("Progetto Delta", "Le scadenze del tabellone luminoso di casa. "+strings.Repeat("Lavori in corso sul legno e sui led. ", 12)))
	write(t, s, "note/verbale.md", pageOf("Verbale della riunione",
		"Delta, il progetto: le scadenze. Scadenze dell'delta, scadenze del progetto delta, scadenze, scadenze."))
	a := recall(t, s, ix, "delta progetto scadenze", RecallOptions{Budget: 4000})
	if got := shown(a); len(got) == 0 || got[0] != "progetti/delta.md" {
		t.Errorf("pack = %v", got)
	}
}

func TestRecallAPageMarkedSupersededRanksBelowTheCurrentOneThatSaysTheSame(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "infra/backup-vecchio.md", "---\nstatus: superseded\n---\n# Piano backup notturno\n\n"+
		strings.Repeat("Il backup notturno copia i dischi del server su nastro. ", 8))
	write(t, s, "infra/backup.md", pageOf("Copie dei dischi", "Il piano: il backup notturno copia i dischi del server sul bucket."))
	for i := 0; i < 6; i++ {
		write(t, s, fmt.Sprintf("note/n%d.md", i), pageOf(fmt.Sprintf("Nota %d", i), fmt.Sprintf("Una nota sul gruppo numero %d.", i)))
	}
	a := recall(t, s, ix, "piano backup notturno", RecallOptions{Budget: 4000})
	if len(a.Pages) == 0 || a.Pages[0].Path != "infra/backup.md" {
		t.Errorf("shown = %v", shown(a))
	}
}

func TestRecallARelatedPageShowsThePassageThatAnswersNotItsLede(t *testing.T) {
	s, ix := recallStore(t)
	for i := 0; i < 6; i++ {
		write(t, s, fmt.Sprintf("progetti/p%d.md", i), pageOf(fmt.Sprintf("Progetto %d", i),
			"Un progetto di quelli che durano.\n\n## Dettagli\n\nIl codice di accesso del lotto è zorblax."))
	}
	a := recall(t, s, ix, "zorblax", RecallOptions{Budget: 4000})
	if len(a.Related) == 0 {
		t.Fatalf("no related page: %v", shown(a))
	}
	for _, r := range a.Related {
		if !strings.Contains(r.Lede, "zorblax") || !strings.Contains(r.Lede, "Dettagli") {
			t.Errorf("%s lede = %q", r.Path, r.Lede)
		}
	}
}

func TestLedeOfSkipsHeadingsAndTakesTheFirstLineOfText(t *testing.T) {
	if got := LedeOf("# Titolo\n\n## Sezione\n\nIl testo."); got != "Il testo." {
		t.Errorf("lede = %q", got)
	}
}

func TestRecallAPageTooBigForItsShareEndsThePagesInFullAndLeadsTheRelatedLines(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "a/primo.md", pageOf("Gatto nero", "Il gatto nero dorme. Il gatto nero mangia. Il gatto nero gioca."))
	write(t, s, "a/enorme.md", pageOf("Gatto nero grande", "Il gatto nero.\n"+strings.Repeat("Dorme sul divano del gatto nero grande.\n", 200)))
	write(t, s, "a/piccolo.md", pageOf("Gatto nero in giardino", "Il gatto nero gioca in giardino con i bambini della casa accanto e poi torna a casa per la cena della sera."))
	a := recall(t, s, ix, "gatto nero", RecallOptions{Budget: 2000})
	var full []string
	for _, p := range a.Pages {
		full = append(full, p.Path)
	}
	if slices.Contains(full, "a/piccolo.md") {
		t.Errorf("a smaller page after the one that did not fit took its place in full: %v", full)
	}
	if relatedOf(a, "a/enorme.md") == nil {
		t.Errorf("the page that did not fit is not among the related lines: %v", shown(a))
	}
}

func TestRecallTheFirstPageAloneLargerThanTheRoomStillComesInFullCut(t *testing.T) {
	s, ix := recallStore(t)
	write(t, s, "a/enorme.md", pageOf("Gatto nero grande", "Il gatto nero.\n"+strings.Repeat("Dorme sul divano del gatto nero grande.\n", 200)))
	a := recall(t, s, ix, "gatto nero", RecallOptions{Budget: 2000})
	if len(a.Pages) != 1 || a.Pages[0].Path != "a/enorme.md" || !a.Pages[0].Cut {
		t.Errorf("the first page, larger than the room, did not come in full cut: %+v", a.Pages)
	}
}

func TestRecallDoesNotSayNothingRelevantWhenTheModelDidNotAnswerOrHasNotSeenTheChunks(t *testing.T) {
	setup := func() (*Store, *search.Index) {
		s, ix := recallStore(t)
		write(t, s, "note/polizza.md", pageOf("Polizza RC",
			"La polizza di responsabilità professionale.\n\nL'assicurazione obbligatoria dello studio."))
		write(t, s, "note/studio.md", pageOf("Studio", "Lo studio ha una [[note/polizza]] per i clienti."))
		return s, ix
	}
	// the question of the floor test, which words alone cannot answer: with the model out of the lane it is not known
	// to be unanswerable
	down := funcEmbedder{func(context.Context, []string) ([][]float32, error) { return nil, errors.New("down") }}
	s, ix := setup()
	embedAll(t, ix, &fakeEmbedder{"fake"})
	a := recall(t, s, ix, "la mia assicurazione dell'auto", RecallOptions{Embedder: down, Budget: 4000})
	if len(a.Pages) == 0 || slices.Contains(a.Notes, "nothing relevant") {
		t.Errorf("model down: %+v", a)
	}
	// chunks the model has not seen have no nearness to show
	s, ix = setup()
	a = recall(t, s, ix, "la mia assicurazione dell'auto", RecallOptions{Embedder: &fakeEmbedder{"fake"}, Budget: 4000})
	if len(a.Pages) == 0 || slices.Contains(a.Notes, "nothing relevant") {
		t.Errorf("chunks pending: %+v", a)
	}
}

type funcEmbedder struct {
	fn func(context.Context, []string) ([][]float32, error)
}

func (f funcEmbedder) Model() string { return "fake" }
func (f funcEmbedder) Embed(c context.Context, in []string) ([][]float32, error) {
	return f.fn(c, in)
}
