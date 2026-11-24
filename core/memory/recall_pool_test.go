package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"kedalio/core/db"
	"kedalio/core/memory/search"
)

// Another process (redact, restore, reindex) writes the file behind this store's back: its next recall must see it.
func TestRecallSeesAWriteFromAnotherHandleOnTheSameFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.db")
	open := func() (*Store, *search.Index) {
		d, err := db.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		if err := Migrate(ctx, d); err != nil {
			t.Fatal(err)
		}
		ix, err := search.New(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		return italian(New(d, ix)), ix
	}
	a, ixa := open()
	b, _ := open()
	write(t, a, "note/one.md", pageOf("One", "The pump runs at 3 bar."))
	g1, err := a.recaller(ctx).graph(DefaultRecallWeights, "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if g2, _ := a.recaller(ctx).graph(DefaultRecallWeights, "2026-01-01"); g2 != g1 {
		t.Error("nothing was written: the graph is read again")
	}
	write(t, b, "note/two.md", pageOf("Two", "The pump of the other process, see [[note/one]]."))
	g3, _ := a.recaller(ctx).graph(DefaultRecallWeights, "2026-01-01")
	if !slices.Contains(g3.nodes, "note/two.md") {
		t.Error("a page written through another handle is not in the graph")
	}
	if res, _ := a.Resolver(ctx); res("two") != "note/two.md" {
		t.Error("the resolver does not know a page written through another handle")
	}
	if got := shown(recall(t, a, ixa, "pump", RecallOptions{})); !slices.Contains(got, "note/two.md") {
		t.Errorf("recall does not reach it: %v", got)
	}
}

func fileStore(t *testing.T) *Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	return italian(New(d, nil))
}

// A doubt about the database state (the request is over) must never match a cached entry, not even another doubt.
func TestGraphStampOfADoneContextNeverRepeats(t *testing.T) {
	s := fileStore(t)
	live := s.graphStamp(ctx)
	if live != s.graphStamp(ctx) {
		t.Fatal("nothing was written: the state changed")
	}
	done, cancel := context.WithCancel(ctx)
	cancel()
	a, b := s.graphStamp(done), s.graphStamp(done)
	time.Sleep(time.Microsecond)
	if a == live || a == b || a == s.graphStamp(done) {
		t.Errorf("a stamp taken in doubt can match another: %+v %+v %+v", live, a, b)
	}
}

// data_version counts per connection: a new probe connection starts its own count, which may equal a cached one.
func TestGraphStampOfANewProbeDiffersFromTheOldOneWithTheSameCount(t *testing.T) {
	s := fileStore(t)
	before := s.graphStamp(ctx)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	after := s.graphStamp(ctx)
	// a fresh connection reads the same data_version when nothing was written, so only the generation tells them apart
	if after.disk == before.disk && after == before {
		t.Error("a new probe connection's counter is taken for the old one's")
	}
	if after.gen == before.gen {
		t.Error("the probe generation did not move")
	}
}

// The connections of a file database are capped: recalls and writes together must still finish, not wait on each
// other for a connection.
func TestRecallAndWritesAtOnceOnAFileDatabaseDoNotStarve(t *testing.T) {
	sqldb, err := db.Open(filepath.Join(t.TempDir(), "k.db"))
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
	s := italian(New(sqldb, ix))
	for i := range 30 {
		write(t, s, fmt.Sprintf("note/p%d.md", i), pageOf(fmt.Sprintf("Pump %d", i), fmt.Sprintf("Runs at %d bar, see [[note/p%d]].", i, (i+1)%30)))
	}
	done := make(chan error, 32)
	for c := range 32 {
		go func() {
			var err error
			for r := range 5 {
				if c%8 == 0 {
					_, err = s.Write(ctx, fmt.Sprintf("note/w%d-%d.md", c, r), pageOf("Fresh", "A page written during the recalls, see [[note/p1]]."), "t")
				} else {
					_, err = s.Recall(ctx, "pump bar", RecallOptions{Index: ix})
				}
				if err != nil {
					break
				}
			}
			done <- err
		}()
	}
	for range 32 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("recalls and writes wait on each other: the connection cap deadlocks")
		}
	}
}
