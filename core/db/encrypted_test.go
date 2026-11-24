package db

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testKey(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

// everything is the bytes of the database and its side files, as a thief of the disk would read them.
func everything(t *testing.T, dir string) []byte {
	t.Helper()
	var all []byte
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, b...)
	}
	return all
}

func fill(t *testing.T, path string, key []byte, word string) {
	t.Helper()
	d, err := OpenEncrypted(path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec("create table notes (body text)"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec("insert into notes values (?)", "the "+word+" of the instance"); err != nil {
		t.Fatal(err)
	}
}

func TestEncryptedDatabaseHoldsNoPlaintextAndOnlyItsKeyOpensIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core.db")
	const word = "zanzibar-marker"
	fill(t, path, testKey(1), word)

	if bytes.Contains(everything(t, dir), []byte(word)) {
		t.Fatal("the plaintext is in the files")
	}
	if _, err := OpenEncrypted(path, testKey(2)); !errors.Is(err, ErrBadKey) {
		t.Fatalf("wrong key: %v", err)
	}
	if p, err := Open(path); err == nil {
		defer p.Close()
		var n int
		if p.QueryRow("select count(*) from sqlite_master").Scan(&n) == nil {
			t.Fatal("a plain open reads an encrypted file")
		}
	}
	d, err := OpenEncrypted(path, testKey(1))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var got string
	if err := d.QueryRow("select body from notes").Scan(&got); err != nil || !strings.Contains(got, word) {
		t.Fatalf("reopened: %q %v", got, err)
	}
	// a write that stays in the WAL (no checkpoint yet) is encrypted too
	if _, err := d.Exec("insert into notes values (?)", "wal-only-marker"); err != nil {
		t.Fatal(err)
	}
	if all := everything(t, dir); bytes.Contains(all, []byte("wal-only-marker")) || bytes.Contains(all, []byte(word)) {
		t.Fatal("the plaintext is in the WAL")
	}
}

func TestOpenEncryptedRefusesAPlainFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.db")
	p, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p.Exec("create table a (x)")
	p.Close()
	if _, err := OpenEncrypted(path, testKey(1)); !errors.Is(err, ErrBadKey) {
		t.Fatalf("plain file: %v", err)
	}
}

func TestBackupIsAnEncryptedConsistentCopyThatRestoresWithTheKey(t *testing.T) {
	ctx := context.Background()
	dir, backups := t.TempDir(), t.TempDir()
	const word = "quokka-marker"
	d, err := OpenEncrypted(filepath.Join(dir, "core.db"), testKey(7))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Exec("create table notes (body text)")
	d.Exec("insert into notes values (?)", word)

	path, err := Backup(ctx, d, backups, testKey(7), time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "core-20261103T100000Z.db" {
		t.Fatalf("name %s", path)
	}
	if bytes.Contains(everything(t, backups), []byte(word)) {
		t.Fatal("the copy is not encrypted")
	}
	if left, _ := filepath.Glob(filepath.Join(backups, ".partial-*")); len(left) != 0 {
		t.Fatalf("leftovers %v", left)
	}

	// the volume is lost: the copy goes to a new folder, and the key opens it
	fresh := filepath.Join(t.TempDir(), "core.db")
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(fresh, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenEncrypted(fresh, testKey(8)); !errors.Is(err, ErrBadKey) {
		t.Fatalf("copy with the wrong key: %v", err)
	}
	r, err := OpenEncrypted(fresh, testKey(7))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var got string
	if err := r.QueryRow("select body from notes").Scan(&got); err != nil || got != word {
		t.Fatalf("restored %q %v", got, err)
	}
}

func TestRotateKeepsTheNewestAndOnlyTouchesCopies(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"core-20261101T000000Z.db", "core-20261102T000000Z.db", "core-20261103T000000Z.db",
		"notes.txt", ".partial-20261104T000000Z"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Rotate(dir, 2); err != nil {
		t.Fatal(err)
	}
	got, _ := Copies(dir)
	if len(got) != 2 || filepath.Base(got[0]) != "core-20261102T000000Z.db" {
		t.Fatalf("copies %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatal("a file that is not a copy was touched")
	}
	if _, err := os.Stat(filepath.Join(dir, ".partial-20261104T000000Z")); err == nil {
		t.Fatal("an interrupted copy is left")
	}
	if Rotate(dir, 0) == nil {
		t.Fatal("keep 0 would delete every copy")
	}
}

// A failure that is not a wrong key (here, a folder the core cannot write to) must not be reported as ErrBadKey: the
// owner would be told to replace a good key.
func TestOpenEncryptedUnwritableFolderIsNotABadKey(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	_, err := OpenEncrypted(filepath.Join(dir, "core.db"), testKey(1))
	if err == nil || errors.Is(err, ErrBadKey) {
		t.Fatalf("got %v, want an error that is not ErrBadKey", err)
	}
	if strings.Contains(err.Error(), "hexkey") || strings.Contains(err.Error(), "0101010101") {
		t.Fatalf("the error quotes the key: %v", err)
	}
}

// VACUUM INTO's error can quote its target, and the target holds the key: the message must not carry it.
func TestBackupErrorDoesNotQuoteTheKey(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	key := testKey(0xab)
	d, err := OpenEncrypted(filepath.Join(t.TempDir(), "core.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	dir := filepath.Join(t.TempDir(), "copies")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	_, err = Backup(context.Background(), d, dir, key, time.Now())
	if err == nil {
		t.Fatal("backup into a read-only folder succeeded")
	}
	if strings.Contains(err.Error(), "abababab") {
		t.Fatalf("the error quotes the key: %v", err)
	}
}
