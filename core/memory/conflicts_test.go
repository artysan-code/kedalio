package memory

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"kedalio/core/db"
)

const longLine = "il dominio della documentazione punta con il record al servizio di hosting"

func TestDiffersInFactANumberACodeOrANameInOneLineAndNotTheOther(t *testing.T) {
	for _, c := range [][2]string{
		{"CNAME 1856rb87", "CNAME wkcppszo"},
		{"Il server è su Hetzner", "Il server è su Contabo"},
		{"Il piano costa 20 euro", "Il piano costa 25 euro"},
		{"2026-11-04 20:54 Il piano costa 20 euro", "Il piano costa 25 euro"},
	} {
		if !DiffersInFact(c[0], c[1]) {
			t.Errorf("%q / %q should differ", c[0], c[1])
		}
	}
	for _, c := range [][2]string{
		{"Supabase sul piano Pro", "Supabase sul piano Pro (26/10)"},
		{"Supabase sul piano Pro (2026-10-20)", "Supabase sul piano Pro (2026-10-26)"},
		{"Usa il tema scuro", "usa il tema scuro."},
		{"2026-11-04 20:54 Alice, decisioni del 2026-11-04: la barra scala a 1,5", "Alice, decisioni del 2026-11-04: la barra scala a 1,5"},
		{"2026-11-04 Supabase sul piano Pro", "2026-11-06 — Supabase sul piano Pro"},
	} {
		if DiffersInFact(c[0], c[1]) {
			t.Errorf("%q / %q should not differ", c[0], c[1])
		}
	}
}

func TestFactLinesListItemsAndSentencesNotHeadingsHistoryRelationsOrFrontMatter(t *testing.T) {
	body := "---\nkind: entity\n---\n# Kedalio\n\nIl progetto dell'agente personale.\n\n## Stato\n\n" +
		"- Il repo è privato su Forgejo\n- ok\n\n## Storia\n\n- fino al 2026-10-20: il repo non esiste ancora\n\n" +
		"## Relazioni\n\n- uses [[deno]] (dal 2026)\n"
	want := []string{"Il progetto dell'agente personale.", "Il repo è privato su Forgejo"}
	if got := FactLines(body); !reflect.DeepEqual(got, want) {
		t.Errorf("FactLines = %q", got)
	}
}

func checkConflicts(t *testing.T, s *Store) int {
	t.Helper()
	n, err := s.CheckConflicts(ctx, &fakeEmbedder{model: "fake"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func pending(t *testing.T, s *Store) []Conflict {
	t.Helper()
	got, err := s.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func checkedRev(t *testing.T, s *Store, path string) int {
	t.Helper()
	var rev int
	if err := s.db.QueryRow("select rev from fact_checked where path = ?", path).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	return rev
}

func TestCheckConflictsAChangedNumberOnANearIdenticalLineIsFlaggedARephrasingIsNot(t *testing.T) {
	s, _ := recallStore(t)
	write(t, s, "progetti/sito.md", pageOf("Sito", longLine+" CNAME 1856rb87.\n\n- altro che non c'entra affatto"))
	write(t, s, "progetti/uso.md", pageOf("Uso", "- il server gira su Hetzner con due nodi"))
	if n := checkConflicts(t, s); n != 2 {
		t.Fatalf("first look at new pages: %d", n)
	}
	if got := pending(t, s); len(got) != 0 {
		t.Fatalf("a new page has no old lines: %v", got)
	}
	write(t, s, "progetti/sito.md", pageOf("Sito", longLine+" CNAME 1856rb87.\n\n- altro che non c'entra affatto\n- "+longLine+" CNAME wkcppszo."))
	// the same fact in other words order: same vector, nothing differs
	write(t, s, "progetti/uso.md", pageOf("Uso", "- il server gira su Hetzner con due nodi\n- con due nodi il server gira su Hetzner"))
	before := checkedRev(t, s, "progetti/sito.md")
	if n := checkConflicts(t, s); n != 2 {
		t.Fatalf("checked %d", n)
	}
	got := pending(t, s)
	want := []Conflict{{"progetti/sito.md", longLine + " CNAME 1856rb87.", longLine + " CNAME wkcppszo.", got[0].Day}}
	if len(got) != 1 || got[0] != want[0] || got[0].Day == "" {
		t.Fatalf("pending = %+v", got)
	}
	if after := checkedRev(t, s, "progetti/sito.md"); after <= before {
		t.Errorf("fact_checked did not advance: %d -> %d", before, after)
	}
	if n := checkConflicts(t, s); n != 0 {
		t.Errorf("nothing left to check: %d", n)
	}
}

func TestCheckConflictsTasksEpisodesDiaryAndInboxAreSkipped(t *testing.T) {
	s, _ := recallStore(t)
	paths := []string{"tasks/t-abc.md", "diario/2026-11-01.md", "inbox/a.md", "note/ep.md"}
	bodies := map[string]string{"note/ep.md": "---\nkind: episode\n---\n# Ep\n\n"}
	for _, p := range paths {
		write(t, s, p, bodies[p]+pageOf("X", longLine+" CNAME 1856rb87."))
	}
	if n := checkConflicts(t, s); n != 0 {
		t.Errorf("checked %d skipped pages", n)
	}
	for _, p := range paths {
		write(t, s, p, bodies[p]+pageOf("X", longLine+" CNAME 1856rb87.\n- "+longLine+" CNAME wkcppszo."))
	}
	checkConflicts(t, s)
	if got := pending(t, s); len(got) != 0 {
		t.Errorf("pending = %+v", got)
	}
}

func TestCheckConflictsStopsAtThePagesGivenAndAFailingEmbedderLeavesThePageUnchecked(t *testing.T) {
	s, _ := recallStore(t)
	for _, p := range []string{"note/a.md", "note/b.md", "note/c.md"} {
		write(t, s, p, pageOf("P", longLine+" CNAME 1856rb87."))
	}
	if n, err := s.CheckConflicts(ctx, &fakeEmbedder{model: "fake"}, 2); err != nil || n != 2 {
		t.Fatalf("n = %d, %v", n, err)
	}
	write(t, s, "note/a.md", pageOf("P", longLine+" CNAME 1856rb87.\n- "+longLine+" CNAME wkcppszo."))
	down := funcEmbedder{func(c context.Context, in []string) ([][]float32, error) { return nil, errors.New("down") }}
	if _, err := s.CheckConflicts(ctx, down, 5); err == nil {
		t.Fatal("no error from a failing embedder")
	}
	// note/c.md has no old lines, so the embedder is not asked for it and it may be checked before or after note/a.md
	// (their timestamps can tie); only note/a.md, whose comparison failed, must be left unchecked
	var rev, checked int
	if err := s.db.QueryRow("select rev from docs where path = 'note/a.md'").Scan(&rev); err != nil {
		t.Fatal(err)
	}
	if checked = checkedRev(t, s, "note/a.md"); checked >= rev {
		t.Errorf("note/a.md marked checked after the failure: %d of %d", checked, rev)
	}
	if n := checkConflicts(t, s); n < 1 {
		t.Errorf("note/a.md not checked again: %d", n)
	}
	if got := pending(t, s); len(got) != 1 {
		t.Errorf("pending = %+v", got)
	}
}

func TestPendingDropsPairsWhoseLinesAreNoLongerOnThePage(t *testing.T) {
	s, _ := recallStore(t)
	write(t, s, "note/a.md", pageOf("A", longLine+" CNAME 1856rb87."))
	checkConflicts(t, s)
	write(t, s, "note/a.md", pageOf("A", longLine+" CNAME 1856rb87.\n- "+longLine+" CNAME wkcppszo."))
	checkConflicts(t, s)
	if got := pending(t, s); len(got) != 1 {
		t.Fatalf("pending = %+v", got)
	}
	write(t, s, "note/a.md", pageOf("A", longLine+" CNAME wkcppszo."))
	if got := pending(t, s); len(got) != 0 {
		t.Errorf("pending after the fix = %+v", got)
	}
	var n int
	if err := s.db.QueryRow("select count(*) from fact_conflicts").Scan(&n); err != nil || n != 0 {
		t.Errorf("queue rows = %d, %v", n, err)
	}
}

func TestMigrationThreeSeedsFactCheckedFromTheLivePagesOfADatabaseMadeAtStepTwo(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(ctx, sqldb, "memory", Migrations[:2]); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"insert into docs (path, title, body, rev, created, updated, by, deleted) values ('a.md', 'A', 'x', 3, 't', 't', 'o', 0)",
		"insert into docs (path, title, body, rev, created, updated, by, deleted) values ('b.md', 'B', '', 4, 't', 't', 'o', 1)",
	} {
		if _, err := sqldb.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, sqldb); err != nil {
		t.Fatal(err)
	}
	var v int
	if err := sqldb.QueryRow("select version from schema_versions where name = 'memory'").Scan(&v); err != nil || v != 3 {
		t.Fatalf("version = %d, %v", v, err)
	}
	rows, err := sqldb.Query("select path, rev from fact_checked")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var p string
		var r int
		if err := rows.Scan(&p, &r); err != nil {
			t.Fatal(err)
		}
		got[p] = r
	}
	if !reflect.DeepEqual(got, map[string]int{"a.md": 3}) {
		t.Errorf("fact_checked = %v", got)
	}
	if _, err := sqldb.Exec("select 1 from fact_conflicts"); err != nil {
		t.Error(err)
	}
}

func TestOnePageThatFailsTheEmbedderDoesNotHoldUpThePagesAfterIt(t *testing.T) {
	s, _ := recallStore(t)
	for _, p := range []string{"note/a.md", "note/b.md", "note/c.md"} {
		write(t, s, p, pageOf("P", longLine+" CNAME 1856rb87."))
	}
	checkConflicts(t, s)
	for _, p := range []string{"note/a.md", "note/b.md", "note/c.md"} {
		extra := longLine + " CNAME wkcppszo."
		if p == "note/a.md" {
			extra += " POISON"
		}
		write(t, s, p, pageOf("P", longLine+" CNAME 1856rb87.\n- "+extra))
	}
	inner := &fakeEmbedder{model: "fake"}
	poison := funcEmbedder{func(c context.Context, in []string) ([][]float32, error) {
		for _, l := range in {
			if strings.Contains(l, "POISON") {
				return nil, errors.New("this text breaks the model")
			}
		}
		return inner.Embed(c, in)
	}}
	rev := func(p string) int {
		var r int
		if err := s.db.QueryRow("select rev from docs where path = ?", p).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	// the first round fails on note/a.md and still checks the pages after it
	if _, err := s.CheckConflicts(ctx, poison, 10); err == nil {
		t.Fatal("no error")
	}
	if checkedRev(t, s, "note/b.md") != rev("note/b.md") || checkedRev(t, s, "note/c.md") != rev("note/c.md") {
		t.Fatal("the pages after the failing one were not checked")
	}
	if got := pending(t, s); len(got) != 2 {
		t.Fatalf("pending = %+v", got)
	}
	// two more failed rounds and the page is passed over, since the embedder answers other texts
	for i := 0; i < 2; i++ {
		if checkedRev(t, s, "note/a.md") >= rev("note/a.md") {
			t.Fatalf("passed over after only %d rounds", i+1)
		}
		_, _ = s.CheckConflicts(ctx, poison, 10)
	}
	if checkedRev(t, s, "note/a.md") != rev("note/a.md") {
		t.Fatal("a page that failed three rounds is still held")
	}
	if n, err := s.CheckConflicts(ctx, poison, 10); err != nil || n != 0 {
		t.Errorf("after it: n = %d, %v", n, err)
	}
}

func TestADownEmbedderPassesNoPageOver(t *testing.T) {
	s, _ := recallStore(t)
	write(t, s, "note/a.md", pageOf("P", longLine+" CNAME 1856rb87."))
	checkConflicts(t, s)
	write(t, s, "note/a.md", pageOf("P", longLine+" CNAME 1856rb87.\n- "+longLine+" CNAME wkcppszo."))
	down := funcEmbedder{func(c context.Context, in []string) ([][]float32, error) { return nil, errors.New("down") }}
	for i := 0; i < 6; i++ {
		_, _ = s.CheckConflicts(ctx, down, 10)
	}
	var rev int
	_ = s.db.QueryRow("select rev from docs where path = 'note/a.md'").Scan(&rev)
	if checkedRev(t, s, "note/a.md") >= rev {
		t.Error("a page was passed over while the embedder was down")
	}
	if n := checkConflicts(t, s); n != 1 {
		t.Errorf("after it came back: %d", n)
	}
}

func TestConflictLinesAreCutBeforeTheyAreEmbedded(t *testing.T) {
	s, _ := recallStore(t)
	long := strings.Repeat("parola ", 400)
	write(t, s, "note/a.md", pageOf("P", longLine+" CNAME 1856rb87."))
	checkConflicts(t, s)
	write(t, s, "note/a.md", pageOf("P", longLine+" CNAME 1856rb87.\n- "+long+"CNAME wkcppszo."))
	inner := &fakeEmbedder{model: "fake"}
	longest := 0
	e := funcEmbedder{func(c context.Context, in []string) ([][]float32, error) {
		for _, l := range in {
			longest = max(longest, len([]rune(l)))
		}
		return inner.Embed(c, in)
	}}
	if _, err := s.CheckConflicts(ctx, e, 10); err != nil {
		t.Fatal(err)
	}
	if longest == 0 || longest > conflictLineRunes {
		t.Errorf("longest line embedded: %d runes", longest)
	}
}

func TestFailureCountsAreDroppedWhenThePageLaterSucceeds(t *testing.T) {
	s, _ := recallStore(t)
	write(t, s, "note/a.md", pageOf("P", longLine+" CNAME 1856rb87."))
	checkConflicts(t, s)
	write(t, s, "note/a.md", pageOf("P", longLine+" CNAME 1856rb87.\n- "+longLine+" CNAME wkcppszo."))
	down := funcEmbedder{func(c context.Context, in []string) ([][]float32, error) { return nil, errors.New("down") }}
	_, _ = s.CheckConflicts(ctx, down, 10)
	if len(s.factFails) != 1 {
		t.Fatalf("factFails = %v", s.factFails)
	}
	checkConflicts(t, s)
	if len(s.factFails) != 0 {
		t.Errorf("a page that succeeded keeps its count: %v", s.factFails)
	}
}

func TestIndexEntriesAreNotFactsAndTheirQueuedPairsAreDropped(t *testing.T) {
	entries := "- [[note/notte-11-13]] — run notte del 13/11\n- [[note/notte-11-14]] — run notte del 14/11\n"
	if got := FactLines("# Notti\n\n" + entries + "- [[alice]] vive a Roma dal 2020\n"); !reflect.DeepEqual(got, []string{"[[alice]] vive a Roma dal 2020"}) {
		t.Errorf("FactLines = %q", got)
	}
	s, _ := recallStore(t)
	write(t, s, "note/notti.md", pageOf("Notti", longLine+".\n"+entries))
	// a pair queued before index entries were left out
	if _, err := s.db.Exec("insert into fact_conflicts (path, old, new, day) values (?, ?, ?, ?)", "note/notti.md",
		"[[note/notte-11-13]] — run notte del 13/11", "[[note/notte-11-14]] — run notte del 14/11", "2026-11-14"); err != nil {
		t.Fatal(err)
	}
	if got := pending(t, s); len(got) != 0 {
		t.Errorf("pending = %+v", got)
	}
}
