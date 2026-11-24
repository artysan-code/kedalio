package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func taggedCopy(t *testing.T, key []byte) (path string, tag *Tag) {
	t.Helper()
	d, err := OpenEncrypted(filepath.Join(t.TempDir(), "core.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Exec("create table notes (body text)")
	path, err = Backup(context.Background(), d, t.TempDir(), key, time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	tag, err = ReadTag(path)
	if err != nil || tag == nil {
		t.Fatalf("the copy has no tag: %v", err)
	}
	return path, tag
}

func flip(t *testing.T, path string) {
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

func TestACopyVerifiesAgainstItsTag(t *testing.T) {
	key := testKey(7)
	path, tag := taggedCopy(t, key)
	if err := VerifyCopy(path, filepath.Base(path), tag, key); err != nil {
		t.Fatal(err)
	}
}

func TestACopyWithOneByteFlippedIsRefused(t *testing.T) {
	key := testKey(7)
	path, tag := taggedCopy(t, key)
	flip(t, path)
	if err := VerifyCopy(path, filepath.Base(path), tag, key); !errors.Is(err, ErrTagInvalid) {
		t.Fatalf("a flipped copy: %v", err)
	}
}

func TestATruncatedCopyIsRefused(t *testing.T) {
	key := testKey(7)
	path, tag := taggedCopy(t, key)
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, b[:len(b)-4096], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCopy(path, filepath.Base(path), tag, key); !errors.Is(err, ErrTagInvalid) {
		t.Fatalf("a truncated copy: %v", err)
	}
}

func TestATagWithOneByteFlippedIsRefused(t *testing.T) {
	key := testKey(7)
	path, _ := taggedCopy(t, key)
	flip(t, TagPath(path))
	tag, err := ReadTag(path)
	if err == nil {
		err = VerifyCopy(path, filepath.Base(path), tag, key)
	}
	if !errors.Is(err, ErrTagInvalid) {
		t.Fatalf("a flipped tag: %v", err)
	}
}

func TestATagMadeWithAnotherKeyIsRefused(t *testing.T) {
	path, tag := taggedCopy(t, testKey(7))
	if err := VerifyCopy(path, filepath.Base(path), tag, testKey(8)); !errors.Is(err, ErrTagInvalid) {
		t.Fatalf("another instance's key: %v", err)
	}
}

func TestACopyRenamedToLookNewerIsRefused(t *testing.T) {
	key := testKey(7)
	path, tag := taggedCopy(t, key)
	if err := VerifyCopy(path, "core-20271103T100000Z.db", tag, key); !errors.Is(err, ErrTagInvalid) {
		t.Fatalf("a renamed copy: %v", err)
	}
}

func TestACopyWithoutATagIsUnsigned(t *testing.T) {
	key := testKey(7)
	path, _ := taggedCopy(t, key)
	os.Remove(TagPath(path))
	tag, err := ReadTag(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyCopy(path, filepath.Base(path), tag, key); !errors.Is(err, ErrUnsigned) {
		t.Fatalf("an untagged copy: %v", err)
	}
}

func TestRotateRemovesTheTagsOfTheCopiesItRemoves(t *testing.T) {
	key := testKey(7)
	d, err := OpenEncrypted(filepath.Join(t.TempDir(), "core.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	dir := t.TempDir()
	for i := range 3 {
		if _, err := Backup(context.Background(), d, dir, key, time.Date(2026, 11, 3+i, 10, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	if err := Rotate(dir, 2); err != nil {
		t.Fatal(err)
	}
	tags, _ := filepath.Glob(filepath.Join(dir, "*.tag"))
	if len(tags) != 2 {
		t.Fatalf("tags after a rotation: %v", tags)
	}
}
