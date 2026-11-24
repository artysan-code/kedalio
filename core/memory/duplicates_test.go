package memory

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func setEdge(t *testing.T, s *Store, a, b string, w float64) {
	t.Helper()
	if _, err := s.db.Exec("insert into similar (src, dst, w) values (?, ?, ?)", a, b, w); err != nil {
		t.Fatal(err)
	}
}

func kinded(kind, title string) string {
	return "---\nkind: " + kind + "\n---\n# " + title + "\n\nText."
}

func keys(ds []Duplicate) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Key)
	}
	return out
}

func TestDuplicateCandidatesEachRuleAndWhatIsNotOne(t *testing.T) {
	s, _ := recallStore(t)
	// same text: the cosine alone, names unrelated
	write(t, s, "note/aaa.md", kinded("concept", "Zebra"))
	write(t, s, "note/bbb.md", kinded("entity", "Giraffe"))
	setEdge(t, s, "note/bbb.md", "note/aaa.md", 0.97)
	// near meaning plus alike names; 0.92 is the edge
	write(t, s, "note/router-setup.md", kinded("concept", "Router setup guide"))
	write(t, s, "net/wifi-config.md", kinded("entity", "Router config guide"))
	setEdge(t, s, "note/router-setup.md", "net/wifi-config.md", 0.92)
	// near meaning, names share nothing: related, not doubles
	write(t, s, "note/cat.md", kinded("concept", "Felines"))
	write(t, s, "note/dog.md", kinded("entity", "Canines"))
	setEdge(t, s, "note/cat.md", "note/dog.md", 0.95)
	// just under the near edge, same names: no
	write(t, s, "note/deploy-one.md", kinded("concept", "Deploy guide"))
	write(t, s, "note/deploy-two.md", kinded("entity", "Deploy guide"))
	setEdge(t, s, "note/deploy-one.md", "note/deploy-two.md", 0.919)
	// titles the same, one kind, different folders, no edge at all: not a pair
	write(t, s, "note/billing-a.md", kinded("concept", "Billing process"))
	write(t, s, "misc/ledger.md", kinded("concept", "Billing process"))

	got, err := s.DuplicateCandidates(ctx, AreasEN, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []Duplicate{
		{"note/aaa.md|note/bbb.md", "note/aaa.md", "note/bbb.md", "same text", 0.97},
		{"net/wifi-config.md|note/router-setup.md", "net/wifi-config.md", "note/router-setup.md", "near meaning, alike names", 0.92},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestDuplicateCandidatesThresholdEdges(t *testing.T) {
	s, _ := recallStore(t)
	// sure: just under 0.97 with names that share nothing is no pair
	write(t, s, "note/x1.md", kinded("concept", "Alpha"))
	write(t, s, "note/y1.md", kinded("entity", "Beta"))
	setEdge(t, s, "note/x1.md", "note/y1.md", 0.9699)
	// names, one word of three shared (0.333) is under 0.34
	write(t, s, "note/q1.md", kinded("concept", "Mango one"))
	write(t, s, "note/q2.md", kinded("entity", "Mango two"))
	setEdge(t, s, "note/q1.md", "note/q2.md", 0.93)
	// names, two of five shared (0.4) is over
	write(t, s, "note/r1.md", kinded("concept", "Peach grape one"))
	write(t, s, "note/r2.md", kinded("entity", "Peach grape two extra"))
	setEdge(t, s, "note/r1.md", "note/r2.md", 0.93)
	// the same title without an edge is no pair
	write(t, s, "note/u1.md", kinded("concept", "Olive cherry lime"))
	write(t, s, "note/u2.md", kinded("concept", "Olive cherry lime"))

	got, err := s.DuplicateCandidates(ctx, AreasEN, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"note/r1.md|note/r2.md"}; !reflect.DeepEqual(keys(got), want) {
		t.Errorf("got %v", keys(got))
	}
}

func TestDuplicateCandidatesLeaveOutTasksEpisodesDiaryAndInbox(t *testing.T) {
	s, _ := recallStore(t)
	for _, p := range []string{"diary/2026-01-01.md", "diary/2026-01-02.md", "inbox/a.md", "inbox/b.md", "tasks/t-a.md", "tasks/t-b.md"} {
		write(t, s, p, kinded("concept", "Same title"))
	}
	write(t, s, "note/e1.md", kinded("episode", "Same title"))
	write(t, s, "note/e2.md", kinded("episode", "Same title"))
	got, err := s.DuplicateCandidates(ctx, AreasEN, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v", keys(got))
	}
}

func TestDuplicateCandidatesSkipMaxAndOrder(t *testing.T) {
	s, _ := recallStore(t)
	for _, n := range []string{"a", "b", "c", "d"} {
		write(t, s, "note/"+n+".md", kinded("concept", "Zeta "+n))
	}
	// the edge of every pair of the four, 0.93 with titles that share «zeta»: 6 pairs by their names
	for i, x := range []string{"a", "b", "c", "d"} {
		for _, y := range []string{"a", "b", "c", "d"}[i+1:] {
			setEdge(t, s, "note/"+x+".md", "note/"+y+".md", 0.93)
		}
	}
	write(t, s, "note/e.md", kinded("concept", "Other"))
	write(t, s, "note/f.md", kinded("concept", "Another"))
	setEdge(t, s, "note/f.md", "note/e.md", 0.98)
	all, err := s.DuplicateCandidates(ctx, AreasEN, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(all); len(got) != 7 || got[0] != "note/e.md|note/f.md" || got[1] != "note/a.md|note/b.md" {
		t.Fatalf("surest first, then by key: %v", got)
	}
	again, _ := s.DuplicateCandidates(ctx, AreasEN, nil, 0)
	if !reflect.DeepEqual(all, again) {
		t.Error("not deterministic")
	}
	top, _ := s.DuplicateCandidates(ctx, AreasEN, nil, 2)
	if !reflect.DeepEqual(top, all[:2]) {
		t.Errorf("max: %v", keys(top))
	}
	skipped, _ := s.DuplicateCandidates(ctx, AreasEN, map[string]bool{"note/e.md|note/f.md": true, "note/a.md|note/b.md": true}, 0)
	if len(skipped) != 5 || skipped[0].Key != "note/a.md|note/c.md" {
		t.Errorf("skip: %v", keys(skipped))
	}
}

// naiveDuplicates is every page against every other, as the rules read: the reference the indexed search must equal.
func naiveDuplicates(pages []*dupPage, weight map[string]float64) map[string]string {
	out := map[string]string{}
	for i, x := range pages {
		for _, y := range pages[i+1:] {
			k := dupKey(x.path, y.path)
			w := weight[k]
			nested, family := pageFamily(x.path, y.path)
			if nested || (family && w < duplicateSure) {
				continue
			}
			switch {
			case w >= duplicateSure:
				out[k] = "same text"
			case w >= duplicateNear && jaccard(x.name, y.name) >= duplicateName:
				out[k] = "near meaning, alike names"
			}
		}
	}
	return out
}

func TestDuplicateCandidatesOnThousandsOfPagesIsFastAndEqualToComparingAllPairs(t *testing.T) {
	s, _ := recallStore(t)
	words := []string{"router", "billing", "garden", "kitchen", "server", "backup", "invoice", "travel", "health", "budget",
		"camera", "design", "report", "meeting", "recipe", "network", "printer", "holiday", "storage", "account"}
	const n = 3000
	var pages []*dupPage
	rows, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		title := words[i%20] + " " + words[(i/20)%20] + " " + words[(i/400)%20] + fmt.Sprintf(" %d", i/7)
		path := fmt.Sprintf("note/p%04d-%s.md", i, words[(i*7)%20])
		kind := []string{"concept", "entity", ""}[i%3]
		body := "# " + title + "\n\nText."
		if kind != "" {
			body = kinded(kind, title)
		}
		if _, err := rows.Exec(`insert into docs (path, title, body, kind, deleted, rev, created, updated, by) values (?, ?, ?, ?, 0, 1, '2026-01-01', '2026-01-01', 'owner')`, path, title, body, kind); err != nil {
			t.Fatal(err)
		}
		p := &dupPage{path: path, title: title, kind: kind}
		p.stem = strings.ReplaceAll(strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".md"), "-", " ")
		p.name, p.titleWords = TitleWords(title+" "+p.stem), TitleWords(title)
		pages = append(pages, p)
	}
	weight := map[string]float64{}
	for i := 0; i < n; i += 5 {
		j := (i*31 + 17) % n
		if i == j {
			continue
		}
		w := []float64{0.5, 0.93, 0.98}[i%3]
		if _, err := rows.Exec("insert into similar (src, dst, w) values (?, ?, ?)", pages[i].path, pages[j].path, w); err != nil {
			t.Fatal(err)
		}
		weight[dupKey(pages[i].path, pages[j].path)] = max(weight[dupKey(pages[i].path, pages[j].path)], w)
	}
	if err := rows.Commit(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got, err := s.DuplicateCandidates(ctx, AreasEN, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	t.Logf("%d pages: %v", n, took)
	// about 150 ms alone; the gate runs it beside go_vuln, where it has taken 1.2 s
	if took > 3*time.Second {
		t.Errorf("%d pages took %v", n, took)
	}
	want := naiveDuplicates(pages, weight)
	if len(got) != len(want) || len(got) == 0 {
		t.Fatalf("%d candidates, the all-pairs search finds %d", len(got), len(want))
	}
	for _, d := range got {
		if want[d.Key] != d.Reason {
			t.Errorf("%s: %q, all-pairs says %q", d.Key, d.Reason, want[d.Key])
		}
	}
}

func TestDuplicateCandidatesNeverPairAProjectWithItsCard(t *testing.T) {
	for _, a := range []Areas{AreasEN, AreasIT} {
		s, _ := recallStore(t)
		p := a.Projects + "/work/falcon/twin"
		write(t, s, p+".md", kinded("entity", "Twin"))
		write(t, s, p+"/card.md", kinded("entity", "Twin"))
		setEdge(t, s, p+".md", p+"/card.md", 0.99)
		got, err := s.DuplicateCandidates(ctx, a, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("%s: got %v", a.Projects, keys(got))
		}
	}
}

func TestPageFamily(t *testing.T) {
	for _, c := range []struct {
		a, b           string
		nested, family bool
	}{
		{"p/x.md", "p/x/y.md", true, true},
		{"p/x.md", "p/x/y/z.md", true, true},
		{"p/x-more.md", "p/x.md", false, true},
		{"p/x.md", "p/x-more/y.md", false, true},
		{"p/x-more/y/z.md", "p/x.md", false, true},
		{"p/x-a.md", "p/x-b.md", false, true},
		{"p/x-a/y.md", "p/x-b.md", false, true},
		{"p/x-a/y.md", "p/x.md", false, true},
		{"p/x-a/y.md", "p/x-b/z.md", false, true},
		{"p/alpha.md", "p/beta.md", false, false},
		{"p/alpha/one.md", "p/beta/two.md", false, false},
		{"p/x.md", "q/x.md", false, false},
		{"p/xy.md", "p/x.md", false, false},
	} {
		n, f := pageFamily(c.a, c.b)
		n2, f2 := pageFamily(c.b, c.a)
		if n != c.nested || f != c.family || n2 != n || f2 != f {
			t.Errorf("%s, %s: nested %v family %v (reversed %v %v), want %v %v", c.a, c.b, n, f, n2, f2, c.nested, c.family)
		}
	}
}

func TestDuplicateCandidatesHubsAndPartsAreNotDoubles(t *testing.T) {
	s, _ := recallStore(t)
	pairs := [][2]string{
		{"progetti/personal/dnd/dragons-lair-new-version-stato.md", "progetti/personal/dnd/dragons-lair-new-version.md"},
		{"progetti/personal/dnd/dragons-lair-requisiti-combattimento.md", "progetti/personal/dnd/dragons-lair-requisiti-scheda.md"},
		{"progetti/work/products/mars-ops-ricerca.md", "progetti/work/products/mars-ops-ricerca/differenziazione.md"},
		{"progetti/personal/dnd/dragons-lair-new-version/lavori-in-development.md", "progetti/personal/dnd/dragons-lair.md"},
		{"clienti/escworks-persone-e-fatti.md", "clienti/escworks.md"},
		{"progetti/work/products/mars-ops-decisioni/repo.md", "progetti/work/products/mars-ops.md"},
	}
	for _, p := range pairs {
		// the same title and a strong edge, to show it is the family that leaves them out
		write(t, s, p[0], kinded("entity", "Same title"))
		write(t, s, p[1], kinded("entity", "Same title"))
		setEdge(t, s, p[0], p[1], 0.93)
	}
	got, err := s.DuplicateCandidates(ctx, AreasIT, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range got {
		for _, p := range pairs {
			if d.Key == dupKey(p[0], p[1]) {
				t.Errorf("%s is a candidate (%s)", d.Key, d.Reason)
			}
		}
	}
}

func TestDuplicateCandidatesRealDoublesStay(t *testing.T) {
	s, _ := recallStore(t)
	write(t, s, "persone/anna-rossi.md", kinded("entity", "Anna Rossi"))
	write(t, s, "persone/anna.md", kinded("entity", "Anna"))
	setEdge(t, s, "persone/anna-rossi.md", "persone/anna.md", 0.98)
	write(t, s, "persone/bob.md", kinded("entity", "Robert Example"))
	write(t, s, "clienti/robert.md", kinded("entity", "Robert Example"))
	setEdge(t, s, "persone/bob.md", "clienti/robert.md", 0.93)
	got, err := s.DuplicateCandidates(ctx, AreasIT, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"persone/anna-rossi.md|persone/anna.md", "clienti/robert.md|persone/bob.md"}
	if !reflect.DeepEqual(keys(got), want) {
		t.Errorf("got %v", keys(got))
	}
}
