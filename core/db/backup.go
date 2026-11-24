package db

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// DefaultKeep is how many copies a backup folder holds: with one copy a day, two weeks. A damage that goes unnoticed for
// a few days (a bad import, a page deleted by mistake) is still behind the newest copies, and the database of one owner is
// small enough that fourteen copies cost less than one afternoon of lost work.
const DefaultKeep = 14

const (
	backupPrefix = "core-"
	backupSuffix = ".db"
	stampLayout  = "20060102T150405Z"
)

// uriPath is a file name as it stands in a "file:" URI.
var uriPath = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")

// uriParam escapes a path given as the value of a URI parameter, where «&» would also end it.
var uriParam = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23", "&", "%26")

// Backup writes a consistent copy of d to dir as core-<UTC time>.db and gives its path. It is SQLite's own VACUUM INTO, so
// it reads one snapshot while the core keeps writing, and the copy is as encrypted as the database (key: the same as
// OpenEncrypted's, or nil for a plain one). The copy is checked before it takes its name: a copy that does not open is
// removed, never kept beside good ones. The folder belongs to the owner and should be off this machine's disk (decision
// 11): the core only writes into it.
func Backup(ctx context.Context, d *sql.DB, dir string, key []byte, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	stamp := now.UTC().Format(stampLayout)
	final := filepath.Join(dir, backupPrefix+stamp+backupSuffix)
	if _, err := os.Stat(final); err == nil {
		return "", fmt.Errorf("%s exists already", final)
	}
	// a name a rotation does not count, so a crash leaves nothing that looks like a copy
	partial := filepath.Join(dir, ".partial-"+stamp)
	os.Remove(partial)
	target := "file:" + uriPath.Replace(partial)
	if key != nil {
		target += "?vfs=adiantum&hexkey=" + hex.EncodeToString(key)
	}
	if _, err := d.ExecContext(ctx, "vacuum into ?", target); err != nil {
		os.Remove(partial)
		// the error may quote target, which holds the key: keep the text, mask the key
		return "", fmt.Errorf("backup: %s", redact(err, key))
	}
	if err := check(partial, key); err != nil {
		os.Remove(partial)
		return "", fmt.Errorf("backup: the copy does not read back: %w", err)
	}
	if key != nil {
		if err := writeTag(ctx, d, partial, final, stamp, key); err != nil {
			os.Remove(partial)
			return "", fmt.Errorf("backup: %w", err)
		}
	}
	if err := os.Rename(partial, final); err != nil {
		os.Remove(partial)
		os.Remove(TagPath(final))
		return "", err
	}
	return final, nil
}

// check reads every page of a copy, without leaving a WAL or any other file beside it.
func check(path string, key []byte) error {
	dsn := "file:" + uriPath.Replace(path) + "?mode=ro&immutable=1"
	if key != nil {
		dsn += "&vfs=adiantum&hexkey=" + hex.EncodeToString(key)
	}
	d, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return err
	}
	defer d.Close()
	var res string
	if err := d.QueryRow("pragma integrity_check").Scan(&res); err != nil {
		return errors.New("it does not open with the key")
	}
	if res != "ok" {
		return errors.New(res)
	}
	return nil
}

// Copies are the backups in dir, oldest first. A missing folder has none.
func Copies(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if _, ok := CopyTime(e.Name()); ok && !e.IsDir() {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	// the stamp is fixed-width UTC, so names sort by time
	slices.Sort(out)
	return out, nil
}

// CopyTime is when a backup file name says it was made.
func CopyTime(name string) (time.Time, bool) {
	name = filepath.Base(name)
	if !strings.HasPrefix(name, backupPrefix) || !strings.HasSuffix(name, backupSuffix) {
		return time.Time{}, false
	}
	t, err := time.Parse(stampLayout, strings.TrimSuffix(strings.TrimPrefix(name, backupPrefix), backupSuffix))
	return t, err == nil
}

// Rotate removes the oldest copies in dir beyond keep, and the leftovers of a backup that was interrupted. Only files that
// are named like copies are touched: whatever else the owner keeps in the folder stays. It is called after a good backup,
// so a failing one never eats the copies that are left.
func Rotate(dir string, keep int) error {
	if keep < 1 {
		return errors.New("a backup folder keeps at least one copy")
	}
	copies, err := Copies(dir)
	if err != nil {
		return err
	}
	for len(copies) > keep {
		if err := os.Remove(copies[0]); err != nil {
			return err
		}
		os.Remove(TagPath(copies[0]))
		copies = copies[1:]
	}
	// a tag whose copy is gone (a crash between the two, or a copy the owner deleted)
	tags, _ := filepath.Glob(filepath.Join(dir, backupPrefix+"*"+backupSuffix+tagSuffix))
	for _, t := range tags {
		if _, err := os.Stat(strings.TrimSuffix(t, tagSuffix)); errors.Is(err, os.ErrNotExist) {
			os.Remove(t)
		}
	}
	partials, _ := filepath.Glob(filepath.Join(dir, ".partial-*"))
	for _, p := range partials {
		os.Remove(p)
	}
	return nil
}
