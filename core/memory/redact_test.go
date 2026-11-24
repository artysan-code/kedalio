package memory

import (
	"strings"
	"testing"
)

// fakeRedact stands for vault.Redact: it takes the word SEKRIT out.
func fakeRedact(s string) (string, int) {
	return strings.ReplaceAll(s, "SEKRIT", "[gone]"), strings.Count(s, "SEKRIT")
}

func writeAll(t *testing.T, s *Store, path string, bodies ...string) {
	t.Helper()
	for _, b := range bodies {
		if _, err := s.Write(ctx, path, b, "alice"); err != nil {
			t.Fatal(err)
		}
	}
}

func bodyAt(t *testing.T, s *Store, path string, rev int) string {
	t.Helper()
	r, err := s.Revision(ctx, path, rev)
	if err != nil || r == nil {
		t.Fatalf("%s@%d: %v", path, rev, err)
	}
	return r.Body
}

func TestRedactRewritesOneOldRevisionInPlaceAndNothingElse(t *testing.T) {
	s, _ := recallStore(t)
	writeAll(t, s, "notes/n8n.md", "# n8n\n\nkey SEKRIT here\n", "# n8n\n\nkey SEKRIT and SEKRIT\n", "# n8n\n\nkey in sops\n")
	before2, live := bodyAt(t, s, "notes/n8n.md", 2), bodyAt(t, s, "notes/n8n.md", 3)
	if _, err := s.db.ExecContext(ctx, "insert into fact_conflicts (path, old, new, day) values ('notes/n8n.md', 'key SEKRIT here', 'key in sops', '2026-11-01')"); err != nil {
		t.Fatal(err)
	}

	r, err := s.Redact(ctx, "notes/n8n.md", 2, fakeRedact, true)
	if err != nil || r.Replaced != 2 || r.Live || r.Conflicts != 1 {
		t.Fatalf("dry run: %+v %v", r, err)
	}
	if bodyAt(t, s, "notes/n8n.md", 2) != before2 {
		t.Fatal("the dry run wrote")
	}

	r, err = s.Redact(ctx, "notes/n8n.md", 2, fakeRedact, false)
	if err != nil || r.Replaced != 2 || r.Live || r.Conflicts != 1 {
		t.Fatalf("redact: %+v %v", r, err)
	}
	if got := bodyAt(t, s, "notes/n8n.md", 2); got != strings.Replace(before2, "SEKRIT and SEKRIT", "[gone] and [gone]", 1) {
		t.Errorf("@2 is %q", got)
	}
	if !strings.Contains(bodyAt(t, s, "notes/n8n.md", 1), "SEKRIT") {
		t.Error("@1 was touched though not named")
	}
	if d, _ := s.Get(ctx, "notes/n8n.md"); d.Body != live || d.Rev != 3 {
		t.Errorf("the live page changed: rev %d %q", d.Rev, d.Body)
	}
	if h, _ := s.History(ctx, "notes/n8n.md"); len(h) != 3 {
		t.Errorf("history has %d revisions, want 3", len(h))
	}
	var old string
	if err := s.db.QueryRowContext(ctx, "select old from fact_conflicts where path = 'notes/n8n.md'").Scan(&old); err != nil || old != "key [gone] here" {
		t.Errorf("fact_conflicts old = %q %v", old, err)
	}

	if _, err := s.Redact(ctx, "notes/n8n.md", 9, fakeRedact, false); err == nil {
		t.Error("a missing revision was accepted")
	}
}

func TestRedactOfTheLivePageRewritesThePageAndItsIndex(t *testing.T) {
	s, _ := recallStore(t)
	writeAll(t, s, "notes/k.md", "# k\n\nthe key is SEKRIT\n")
	r, err := s.Redact(ctx, "notes/k.md", 0, fakeRedact, false)
	if err != nil || !r.Live || r.Rev != 1 || r.Replaced != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	d, _ := s.Get(ctx, "notes/k.md")
	if strings.Contains(d.Body, "SEKRIT") || d.Rev != 1 || bodyAt(t, s, "notes/k.md", 1) != d.Body {
		t.Errorf("page %d %q", d.Rev, d.Body)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "select count(*) from search_chunks where text like '%SEKRIT%'").Scan(&n); err != nil || n != 0 {
		t.Errorf("search chunks still hold it: %d %v", n, err)
	}
	if err := s.db.QueryRowContext(ctx, "select count(*) from search_chunks_fts where search_chunks_fts match 'sekrit'").Scan(&n); err != nil || n != 0 {
		t.Errorf("the full-text index still finds it: %d %v", n, err)
	}
	if _, err := s.Redact(ctx, "notes/none.md", 0, fakeRedact, false); err == nil {
		t.Error("a missing page was accepted")
	}
}

// Remove only marks a page gone: its last text stays in docs, and naming any revision of it cleans that text too.
func TestRedactOfARemovedPageCleansTheTextItKeeps(t *testing.T) {
	s, _ := recallStore(t)
	writeAll(t, s, "notes/gone.md", "# gone\n\nfirst SEKRIT\n", "# gone\n\nthen SEKRIT again\n")
	if err := s.Remove(ctx, "notes/gone.md", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Redact(ctx, "notes/gone.md", 0, fakeRedact, false); err == nil {
		t.Error("a removed page was taken for a live one")
	}
	r, err := s.Redact(ctx, "notes/gone.md", 2, fakeRedact, true)
	if err != nil || r.Replaced != 1 || r.Removed != 1 || r.Live {
		t.Fatalf("dry run: %+v %v", r, err)
	}
	if r, err = s.Redact(ctx, "notes/gone.md", 2, fakeRedact, false); err != nil || r.Removed != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	var kept string
	var deleted int
	if err := s.db.QueryRowContext(ctx, "select body, deleted from docs where path = 'notes/gone.md'").Scan(&kept, &deleted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(kept, "SEKRIT") || !strings.Contains(kept, "then [gone] again") || deleted != 1 {
		t.Errorf("removed page keeps %q (deleted %d)", kept, deleted)
	}
	if !strings.Contains(bodyAt(t, s, "notes/gone.md", 1), "SEKRIT") {
		t.Error("@1 was touched though not named")
	}
}
