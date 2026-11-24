package memory

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"kedalio/core/db"
)

var ctx = context.Background()

// fakeIndex records what the store hands to the search index.
type fakeIndex struct{ pages map[string]string }

func (f *fakeIndex) IndexPage(_ context.Context, tx *sql.Tx, path, title, body string) error {
	f.pages[path] = title + "\n" + body
	return nil
}

func (f *fakeIndex) RemovePage(_ context.Context, tx *sql.Tx, path string) error {
	delete(f.pages, path)
	return nil
}

func memStore(t *testing.T) (*Store, *fakeIndex) {
	t.Helper()
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := Migrate(ctx, sqldb); err != nil {
		t.Fatal(err)
	}
	idx := &fakeIndex{pages: map[string]string{}}
	return italian(New(sqldb, idx)), idx
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestCleanPath(t *testing.T) {
	for in, want := range map[string]string{"progetti/claude-multi": "progetti/claude-multi.md", "/a//b.md": "a/b.md"} {
		if got, err := CleanPath(in); err != nil || got != want {
			t.Errorf("CleanPath(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"../etc/passwd", "a/./b", "a/b\x00.md", "", "a/b?.md", strings.Repeat("a", 300)} {
		if got, err := CleanPath(in); err == nil {
			t.Errorf("CleanPath(%q) = %q, want an error", in, got)
		}
	}
}

func TestTitleOf(t *testing.T) {
	for _, c := range []struct{ path, body, want string }{
		{"x/a.md", "---\ntitle: 'Uno'\n---\n# Due", "Uno"},
		{"x/a.md", "testo\n# Due\n", "Due"},
		{"x/nome-file.md", "solo testo", "nome-file"},
	} {
		if got := TitleOf(c.path, c.body); got != c.want {
			t.Errorf("TitleOf = %q, want %q", got, c.want)
		}
	}
}

func TestLinksIn(t *testing.T) {
	got := LinksIn("[[a/b|B]] e [[c#sez]] e [[a/b]] e [[d.md]]")
	if want := []string{"a/b", "c", "d"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestLedeOf(t *testing.T) {
	for in, want := range map[string]string{
		"# T\n\nUna frase con [[progetti/x|un link]] e [[note/y]].\n\nAltro.": "Una frase con un link e y.",
		"---\ntitle: A\n---\n# T\n\nPrima.\n":                                 "Prima.",
		"# Solo titolo\n":                                                     "",
		"senza titolo\nsecondo":                                               "senza titolo",
	} {
		if got := LedeOf(in); got != want {
			t.Errorf("LedeOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMakeResolver(t *testing.T) {
	r := MakeResolver([]string{"a/deep/x.md", "b/x.md", "c/Y.md", "z.md"})
	for in, want := range map[string]string{
		"b/x": "b/x.md", "x": "b/x.md", "nowhere/x.md": "b/x.md", "y": "c/Y.md", "missing": "",
	} {
		if got := r(in); got != want {
			t.Errorf("resolve(%q) = %q, want %q", in, got, want)
		}
	}
	if got := MakeResolver([]string{"m/same.md", "k/same.md"})("same"); got != "k/same.md" {
		t.Errorf("ties by path order: %q", got)
	}
}

func TestStoreHistoryRestoreRemoveLinks(t *testing.T) {
	s, idx := memStore(t)
	must(s.Write(ctx, "p/claude-multi", "# claude-multi\nVedi [[alice]].", "test"))
	must(s.Write(ctx, "persone/alice.md", "Preferisce l'italiano.", "test"))
	if _, err := s.WriteAt(ctx, "persone/alice.md", "x", "test", 0); !errors.Is(err, ErrChanged) {
		t.Fatalf("want ErrChanged, got %v", err)
	}
	must(s.WriteAt(ctx, "persone/alice.md", "Preferisce l'italiano e risposte brevi.", "claude:app", 1))
	h := must(s.History(ctx, "persone/alice.md"))
	if len(h) != 2 || h[0].Rev != 2 || h[1].Rev != 1 {
		t.Fatalf("history %+v", h)
	}
	if back := must(s.Links(ctx, "persone/alice.md", nil)).Back; !reflect.DeepEqual(back, []string{"p/claude-multi.md"}) {
		t.Errorf("back %v", back)
	}
	if out := must(s.Links(ctx, "p/claude-multi.md", nil)).Out; out[0].Path != "persone/alice.md" {
		t.Errorf("out %+v", out)
	}
	back := must(s.Restore(ctx, "persone/alice.md", 1, "test"))
	if back.Body != "---\nkind: entity\nis: person\n---\nPreferisce l'italiano." || back.Kind != KindEntity {
		t.Errorf("restored %q %q", back.Body, back.Kind)
	}
	if h := must(s.History(ctx, "persone/alice.md")); h[0].Op != OpRestore || h[0].Rev != 3 {
		t.Errorf("restore is a revision on top: %+v", h[0])
	}
	if !strings.Contains(idx.pages["persone/alice.md"], "italiano") {
		t.Error("a page's write reaches the search index")
	}
	if err := s.Remove(ctx, "p/claude-multi.md", "test"); err != nil {
		t.Fatal(err)
	}
	if d := must(s.Get(ctx, "p/claude-multi.md")); d != nil {
		t.Errorf("removed page still there: %+v", d)
	}
	if _, ok := idx.pages["p/claude-multi.md"]; ok {
		t.Error("a removed page leaves the search index")
	}
	if h := must(s.History(ctx, "p/claude-multi.md")); h[0].Op != OpDelete {
		t.Errorf("deletion is a revision: %+v", h[0])
	}
	must(s.Write(ctx, "tasks/t-1.md", "una task", "test"))
	if _, ok := idx.pages["tasks/t-1.md"]; ok {
		t.Error("tasks are not indexed for search")
	}
	if got := paths(must(s.List(ctx, "", ListOpts{}))); !reflect.DeepEqual(got, []string{"persone/alice.md"}) {
		t.Errorf("list %v", got)
	}
	if n := len(must(s.List(ctx, "", ListOpts{Tasks: true}))); n != 2 {
		t.Errorf("list with tasks: %d", n)
	}
	// a deleted page written again goes on from its revision and is live again
	if d := must(s.Write(ctx, "p/claude-multi.md", "# again", "test")); d.Rev != 3 {
		t.Errorf("rev %d", d.Rev)
	}
}

func paths(in []Info) []string {
	var out []string
	for _, i := range in {
		out = append(out, i.Path)
	}
	return out
}
func TestStoreOneResolverServesLinks(t *testing.T) {
	s, _ := memStore(t)
	must(s.Write(ctx, "progetti/p.md", "vedi [[alice]] e [[nessuno]]", "t"))
	must(s.Write(ctx, "persone/alice.md", "x", "t"))
	r := must(s.Resolver(ctx))
	var got []string
	for _, l := range must(s.Links(ctx, "progetti/p.md", r)).Out {
		got = append(got, l.Path)
	}
	if want := []string{"persone/alice.md", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("out paths %v", got)
	}
}
func TestStoreKeepsKindAndNamesRelations(t *testing.T) {
	s, _ := memStore(t)
	must(s.Write(ctx, "persone/alice.md", "# Alice\n\nUna giocatrice.\n", "test"))
	if k := must(s.Get(ctx, "persone/alice.md")).Kind; k != KindEntity {
		t.Errorf("kind %q", k)
	}
	// a rewrite without front matter keeps the kind it had, not its folder's
	must(s.Write(ctx, "note/campagna.md", "---\nkind: entity\nis: campaign\n---\n# Campagna\n\nLa campagna del giovedì.\n", "test"))
	again := must(s.Write(ctx, "note/campagna.md", "# Campagna\n\nLa campagna del giovedì, al terzo arco.\n", "test"))
	if again.Kind != KindEntity || !strings.HasPrefix(again.Body, "---\nkind: entity\nis: campaign\n---\n") {
		t.Errorf("kept kind: %q %q", again.Kind, again.Body)
	}
	must(s.Write(ctx, "giochi-di-ruolo.md",
		"# Giochi di ruolo\n\nCome gioca il proprietario, vedi [[note/campagna]].\n\n## Relazioni\n\n- gioca con [[alice]]\n", "test"))
	if k := must(s.Get(ctx, "giochi-di-ruolo.md")).Kind; k != KindConcept {
		t.Errorf("kind %q", k)
	}
	wantOut := []Link{
		{Target: "alice", Path: "persone/alice.md", Rel: "gioca_con"},
		{Target: "note/campagna", Path: "note/campagna.md"},
	}
	if out := must(s.Links(ctx, "giochi-di-ruolo.md", nil)).Out; !reflect.DeepEqual(out, wantOut) {
		t.Errorf("out %+v", out)
	}
	l := must(s.Links(ctx, "persone/alice.md", nil))
	if !reflect.DeepEqual(l.Related, []Related{{"giochi-di-ruolo.md", "gioca_con"}}) ||
		!reflect.DeepEqual(l.Back, []string{"giochi-di-ruolo.md"}) {
		t.Errorf("related %+v back %v", l.Related, l.Back)
	}
	// a task is not a page: no kind, no front matter added; a task page is of kind task
	if d := must(s.Write(ctx, "tasks/t-1.md", "una task", "test")); d.Body != "una task" || d.Kind != KindTask {
		t.Errorf("task %+v", d)
	}
	if d := must(s.Write(ctx, "tasks/other.md", "no", "test")); d.Body != "no" || d.Kind != "" {
		t.Errorf("tasks/other %+v", d)
	}
}

func TestStoreWithoutIndexer(t *testing.T) {
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := Migrate(ctx, sqldb); err != nil {
		t.Fatal(err)
	}
	s := italian(New(sqldb, nil))
	must(s.Write(ctx, "a.md", "x", "t"))
	if err := s.Remove(ctx, "a.md", "t"); err != nil {
		t.Fatal(err)
	}
}

// fakeTasks records what the store hands to the tasks' index.
type fakeTasks struct{ paths map[string]string }

func (f *fakeTasks) IndexTask(_ context.Context, _ *sql.Tx, path, body string) error {
	f.paths[path] = body
	return nil
}

func (f *fakeTasks) DropTask(_ context.Context, _ *sql.Tx, path string) error {
	delete(f.paths, path)
	return nil
}

func TestStoreHandsTaskPagesToTheTaskIndexerAndNoOtherPage(t *testing.T) {
	s, _ := memStore(t)
	x := &fakeTasks{paths: map[string]string{}}
	s.SetTaskIndexer(x)
	for _, p := range []string{"tasks/t-1.md", "tasks/t-1/log.md", "notes/a.md"} {
		must(s.Write(ctx, p, "# x\n", "t"))
	}
	if len(x.paths) != 1 || x.paths["tasks/t-1.md"] == "" {
		t.Fatalf("only the task page is indexed as a task: %v", x.paths)
	}
	if err := s.Remove(ctx, "tasks/t-1.md", "t"); err != nil {
		t.Fatal(err)
	}
	if len(x.paths) != 0 {
		t.Errorf("a removed task leaves the index: %v", x.paths)
	}
}

// italian is the store the tests were written for: the pages live in the Italian areas (the default is English).
func italian(s *Store) *Store {
	s.Areas = AreasIT
	return s
}
