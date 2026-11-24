package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMigrateAppliesOnceAndInOrder(t *testing.T) {
	ctx := context.Background()
	d, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	steps := []string{"create table a (x)", "create virtual table f using fts5(body)"}
	for range 2 {
		if err := Migrate(ctx, d, "test", steps); err != nil {
			t.Fatal(err)
		}
	}
	steps = append(steps, "insert into a values (1)")
	if err := Migrate(ctx, d, "test", steps); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow("select count(*) from a").Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows in a = %d, %v; want 1", n, err)
	}
}

// A database written by a newer Kedalio may hold tables this code does not know: starting on it could corrupt it.
func TestMigrateRefusesADatabaseFromANewerVersion(t *testing.T) {
	ctx := context.Background()
	d, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	steps := []string{"create table a (x)", "insert into a values (1)"}
	if err := Migrate(ctx, d, "test", steps); err != nil {
		t.Fatal(err)
	}
	err = Migrate(ctx, d, "test", steps[:1])
	if !errors.Is(err, ErrNewerDatabase) {
		t.Fatalf("Migrate on a newer database = %v; want ErrNewerDatabase", err)
	}
	if !strings.Contains(err.Error(), "upgrade the binary") {
		t.Fatalf("error %q does not tell the owner to upgrade", err)
	}
}

// Unbounded, a burst of readers opens a connection each and the process grows by tens of MB.
func TestOpenCapsTheConnectionsOfAFileDatabase(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got := d.Stats().MaxOpenConnections; got != MaxConns {
		t.Fatalf("MaxOpenConnections = %d, want %d", got, MaxConns)
	}
}

// A hook and a background drain may open a queue file that does not exist yet at the same moment.
// A clean install showed core.db at 0644: the database and its WAL are the owner's only, whatever the umask.
func TestOpenCreatesTheDatabaseReadableByTheOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes")
	}
	path := filepath.Join(t.TempDir(), "core.db")
	d, err := OpenEncrypted(path, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec("create table t(x)"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal"} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if mode := st.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s has mode %o, want 600", filepath.Base(p), mode)
		}
	}
}

// A data folder is whatever the owner named it: «?» and «#» would end the file name in the DSN, «%» would be read as an
// escape.
func TestOpenKeepsAnOddFolderNameInTheFileName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a#b?c%41&d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "core.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the database is not where it was asked: %v", err)
	}
}

func TestOpenSameNewFileAtOnce(t *testing.T) {
	for round := range 20 {
		path := filepath.Join(t.TempDir(), "q.db")
		errs := make(chan error, 8)
		for range 8 {
			go func() {
				d, err := Open(path)
				if err == nil {
					d.Close()
				}
				errs <- err
			}()
		}
		for range 8 {
			if err := <-errs; err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
	}
}

// Two processes starting together migrate the same new file: each step must run once.
func TestMigrateFromTwoOpenersAtOnce(t *testing.T) {
	ctx := context.Background()
	steps := []string{"create table a (x)", "create table b (y)"}
	for round := range 20 {
		path := filepath.Join(t.TempDir(), "q.db")
		errs := make(chan error, 6)
		for range 6 {
			go func() {
				d, err := Open(path)
				if err == nil {
					err = Migrate(ctx, d, "test", steps)
					d.Close()
				}
				errs <- err
			}()
		}
		for range 6 {
			if err := <-errs; err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
	}
}
