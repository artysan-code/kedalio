package offsite

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kedalio/core/db"
	"kedalio/core/offsite/fakes3"
)

var tagKey = bytes.Repeat([]byte{7}, 32)

var unsignedOK = RestoreOptions{Key: tagKey, AllowUnsigned: true}

// signedStore makes two real copies (the older on the 3rd, the newer on the 4th), lets tamper change them in the local
// folder, and syncs the folder to a fake store.
func signedStore(t *testing.T, tamper func(dir string)) *Client {
	t.Helper()
	d, err := db.OpenEncrypted(filepath.Join(t.TempDir(), "core.db"), tagKey)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Exec("create table notes (body text)")
	dir := t.TempDir()
	for day := 3; day <= 4; day++ {
		if _, err := db.Backup(context.Background(), d, dir, tagKey, time.Date(2026, 11, day, 3, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	if tamper != nil {
		tamper(dir)
	}
	c := clientFor(fakes3.New(t))
	if _, err := (&Uploader{Client: c, Keep: 5}).Sync(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	return c
}

func flipByte(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 1
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

const (
	olderCopy  = "core-20261103T030000Z.db"
	newestCopy = "core-20261104T030000Z.db"
)

func TestARestoreOfAValidSignedCopyWorksAndTheTagTravelsWithIt(t *testing.T) {
	c := signedStore(t, nil)
	to := t.TempDir()
	name, err := Restore(context.Background(), c, "", to, RestoreOptions{Key: tagKey})
	if err != nil || name != newestCopy {
		t.Fatalf("%s %v", name, err)
	}
	if _, err := os.Stat(filepath.Join(to, "core.db")); err != nil {
		t.Fatal(err)
	}
}

func TestARestoreRefusesACopyWithOneByteFlipped(t *testing.T) {
	c := signedStore(t, func(dir string) { flipByte(t, filepath.Join(dir, newestCopy)) })
	to := t.TempDir()
	_, err := Restore(context.Background(), c, "", to, RestoreOptions{Key: tagKey, AllowUnsigned: true, AllowOlder: true})
	if !errors.Is(err, db.ErrTagInvalid) {
		t.Fatalf("a flipped copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(to, "core.db")); err == nil {
		t.Fatal("a refused copy was kept")
	}
}

func TestARestoreRefusesATagWithOneByteFlipped(t *testing.T) {
	c := signedStore(t, func(dir string) { flipByte(t, filepath.Join(dir, newestCopy+".tag")) })
	_, err := Restore(context.Background(), c, "", t.TempDir(), RestoreOptions{Key: tagKey, AllowUnsigned: true, AllowOlder: true})
	if !errors.Is(err, db.ErrTagInvalid) {
		t.Fatalf("a flipped tag: %v", err)
	}
}

func TestARestoreRefusesACopyOfAnotherInstance(t *testing.T) {
	c := signedStore(t, nil)
	_, err := Restore(context.Background(), c, "", t.TempDir(), RestoreOptions{Key: bytes.Repeat([]byte{8}, 32)})
	if !errors.Is(err, db.ErrTagInvalid) {
		t.Fatalf("another key: %v", err)
	}
}

func TestAnUnsignedCopyNeedsTheFlagAndIsWarnedAbout(t *testing.T) {
	c := signedStore(t, func(dir string) {
		os.Remove(filepath.Join(dir, newestCopy+".tag"))
		os.Remove(filepath.Join(dir, olderCopy+".tag"))
	})
	_, err := Restore(context.Background(), c, "", t.TempDir(), RestoreOptions{Key: tagKey})
	if err == nil || !strings.Contains(err.Error(), "-allow-unsigned") {
		t.Fatalf("an unsigned copy without the flag: %v", err)
	}
	var warned []string
	name, err := Restore(context.Background(), c, "", t.TempDir(),
		RestoreOptions{Key: tagKey, AllowUnsigned: true, Warn: func(m string) { warned = append(warned, m) }})
	if err != nil || name != newestCopy {
		t.Fatalf("%s %v", name, err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "no tag") {
		t.Fatalf("warnings: %v", warned)
	}
}

func TestAnOlderCopyThanTheNewestSignedOneNeedsTheFlag(t *testing.T) {
	c := signedStore(t, nil)
	_, err := Restore(context.Background(), c, olderCopy, t.TempDir(), RestoreOptions{Key: tagKey})
	if err == nil || !strings.Contains(err.Error(), "-allow-older") {
		t.Fatalf("an older copy without the flag: %v", err)
	}
	var warned []string
	name, err := Restore(context.Background(), c, olderCopy, t.TempDir(),
		RestoreOptions{Key: tagKey, AllowOlder: true, Warn: func(m string) { warned = append(warned, m) }})
	if err != nil || name != olderCopy {
		t.Fatalf("%s %v", name, err)
	}
	if len(warned) != 1 {
		t.Fatalf("warnings: %v", warned)
	}
}

func TestARestoreNeedsTheKey(t *testing.T) {
	c := signedStore(t, nil)
	if _, err := Restore(context.Background(), c, "", t.TempDir(), RestoreOptions{}); err == nil {
		t.Fatal("no key, no check, no restore")
	}
}

func TestSyncSendsTheTagsAndPrunesThemWithTheirCopies(t *testing.T) {
	d, err := db.OpenEncrypted(filepath.Join(t.TempDir(), "core.db"), tagKey)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	dir := t.TempDir()
	f := fakes3.New(t)
	u := &Uploader{Client: clientFor(f), Keep: 2}
	for day := 3; day <= 5; day++ {
		if _, err := db.Backup(context.Background(), d, dir, tagKey, time.Date(2026, 11, day, 3, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
		if _, err := u.Sync(context.Background(), dir); err != nil {
			t.Fatal(err)
		}
	}
	if keys := f.Keys(); len(keys) != 4 {
		t.Fatalf("two copies and two tags expected, the store holds %v", keys)
	}
}
