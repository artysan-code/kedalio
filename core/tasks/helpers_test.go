package tasks

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"kedalio/core/db"
	"kedalio/core/memory"
)

var ctx = context.Background()

// at is a moment in UTC, the zone the test books keep their days in.
func at(s string) time.Time {
	t, err := time.Parse("2006-01-02T15:04:05", s)
	if err != nil {
		panic(err)
	}
	return t
}

// newStore is a memory over a fresh in-memory database with the tasks' tables, and the database.
func newStore(t *testing.T) (*memory.Store, *sql.DB) {
	t.Helper()
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := memory.Migrate(ctx, sqldb); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, sqldb); err != nil {
		t.Fatal(err)
	}
	s := memory.New(sqldb, nil)
	s.Areas = memory.AreasIT
	return s, sqldb
}

// newBook is the tasks of "alice" in UTC; its clock is the one the test sets.
func newBook(t *testing.T) *Book {
	t.Helper()
	s, d := newStore(t)
	b := NewBook(s, d, "alice", time.UTC, "test")
	b.Now = func() time.Time { return at("2026-10-26T10:00:00") }
	return b
}

func (b *Book) clockAt(s string) { b.Now = func() time.Time { return at(s) } }

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func page(title string) string {
	return "# " + title + "\n\nUna frase che dice cos'è.\n\nContenuto.\n"
}

func mustAdd(t *testing.T, b *Book, in Input) Task {
	t.Helper()
	task, err := b.Add(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func mustUpdate(t *testing.T, b *Book, id string, in Input) Result {
	t.Helper()
	r, err := b.Update(ctx, id, in, "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mustGet(t *testing.T, b *Book, id string) Task {
	t.Helper()
	task, err := b.Get(ctx, id)
	if err != nil || task == nil {
		t.Fatalf("no task %s: %v", id, err)
	}
	return *task
}

func wantErr(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Errorf("error %v, want one saying %q", err, contains)
	}
}

func eq[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func write(t *testing.T, b *Book, path, body string) {
	t.Helper()
	if _, err := b.Store.Write(ctx, path, body, "t"); err != nil {
		t.Fatal(err)
	}
}

func titles(ts []Task) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t.Title)
	}
	return out
}

func idsList(ts []Task) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}

// L is a list for an Input.
func L(xs ...string) *[]string {
	if xs == nil {
		xs = []string{}
	}
	return &xs
}

// N is an int for an Input.
func N(n int) *int { return &n }
