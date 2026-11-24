package db

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Adiantum keeps a copy secret but does not notice a copy that was altered, cut short or swapped for an older one, and
// "pragma integrity_check" only sees damage that breaks SQLite's own structure. So each copy has a sidecar,
// core-<time>.db.tag, with an HMAC over the copy's digest and its metadata. The HMAC key is derived from the database
// key with its own label: the owner has no new secret to keep, and the key that encrypts is never used to authenticate.

// tagSuffix is added to a copy's name for its sidecar. It does not end in backupSuffix, so Copies never takes it for a copy.
const tagSuffix = ".tag"

const tagVersion = 1

var (
	// ErrUnsigned is a copy with no tag: one made before tags existed, or one whose tag was removed. The two look alike, so
	// it is restorable only when the owner says so.
	ErrUnsigned = errors.New("the copy has no tag")
	// ErrTagInvalid is a copy whose tag, or whose bytes, are not what the tag signed.
	ErrTagInvalid = errors.New("the copy does not match its tag")
)

// Tag is the content of a sidecar.
type Tag struct {
	Version  int    `json:"version"`
	Instance string `json:"instance"` // a fingerprint of the key, not the key: copies of another instance are refused
	Created  string `json:"created"`  // the copy's stamp (UTC), the sequence rollback is judged by
	Schema   string `json:"schema"`   // the migration versions inside the copy, name=version, sorted
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"` // of the copy: the MAC covers it, so the tag can be checked before the copy is read
	MAC      string `json:"mac"`
}

// TagPath is where the sidecar of the copy at path lives.
func TagPath(path string) string { return path + tagSuffix }

// Time is when the tag says the copy was made.
func (t Tag) Time() (time.Time, error) { return time.Parse(stampLayout, t.Created) }

func macKey(dbKey []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, dbKey, nil, "kedalio backup mac v1", 32)
}

func (t Tag) sum(dbKey []byte) (string, error) {
	k, err := macKey(dbKey)
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, k)
	// every field is bound, in a fixed order, one per line
	fmt.Fprintf(m, "kedalio backup tag v%d\n%s\n%s\n%s\n%d\n%s", t.Version, t.Instance, t.Created, t.Schema, t.Size, t.SHA256)
	return hex.EncodeToString(m.Sum(nil)), nil
}

func instanceID(dbKey []byte) (string, error) {
	id, err := hkdf.Key(sha256.New, dbKey, nil, "kedalio instance id v1", 8)
	return hex.EncodeToString(id), err
}

// ParseTag reads a sidecar's bytes.
func ParseTag(b []byte) (Tag, error) {
	var t Tag
	if err := json.Unmarshal(b, &t); err != nil {
		return Tag{}, fmt.Errorf("%w: the tag is not readable", ErrTagInvalid)
	}
	return t, nil
}

// CheckTag verifies the MAC of a tag alone, which proves who wrote it and when, without the copy.
func CheckTag(t Tag, dbKey []byte) error {
	want, err := t.sum(dbKey)
	if err != nil {
		return err
	}
	id, err := instanceID(dbKey)
	if err != nil {
		return err
	}
	got, derr := hex.DecodeString(t.MAC)
	exp, _ := hex.DecodeString(want)
	if derr != nil || t.Version != tagVersion || !hmac.Equal(got, exp) {
		return fmt.Errorf("%w: the tag is not signed with this instance's key", ErrTagInvalid)
	}
	if t.Instance != id {
		return fmt.Errorf("%w: the copy belongs to another instance", ErrTagInvalid)
	}
	return nil
}

// VerifyCopy checks the copy at path against its tag t. name is the copy's name in its folder or in the store: the stamp in
// it must be the signed one, so a copy renamed to look newer is refused. A nil tag is ErrUnsigned.
func VerifyCopy(path, name string, t *Tag, dbKey []byte) error {
	if t == nil {
		return ErrUnsigned
	}
	if err := CheckTag(*t, dbKey); err != nil {
		return err
	}
	if when, ok := CopyTime(name); !ok || when.UTC().Format(stampLayout) != t.Created {
		return fmt.Errorf("%w: the name does not carry the signed time", ErrTagInvalid)
	}
	size, sum, err := digest(path)
	if err != nil {
		return err
	}
	if size != t.Size || sum != t.SHA256 {
		return fmt.Errorf("%w: its bytes were changed", ErrTagInvalid)
	}
	return nil
}

// ReadTag is the sidecar of the copy at path; nil, nil when there is none.
func ReadTag(path string) (*Tag, error) {
	b, err := os.ReadFile(TagPath(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	t, err := ParseTag(b)
	return &t, err
}

func digest(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}

// schemaOf is the migration versions in d.
func schemaOf(ctx context.Context, d *sql.DB) string {
	rows, err := d.QueryContext(ctx, "select name, version from schema_versions order by name")
	if err != nil {
		// a database that has not migrated has no versions yet
		return ""
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var name string
		var v int
		if rows.Scan(&name, &v) == nil {
			parts = append(parts, name+"="+strconv.Itoa(v))
		}
	}
	return strings.Join(parts, ",")
}

// writeTag signs the file at src, which is about to become the copy final made at stamp, and writes final's sidecar. It
// goes before the copy takes its name, so a crash leaves at worst a tag with no copy, which Rotate removes.
func writeTag(ctx context.Context, d *sql.DB, src, final, stamp string, dbKey []byte) error {
	size, sum, err := digest(src)
	if err != nil {
		return err
	}
	id, err := instanceID(dbKey)
	if err != nil {
		return err
	}
	t := Tag{Version: tagVersion, Instance: id, Created: stamp, Schema: schemaOf(ctx, d), Size: size, SHA256: sum}
	if t.MAC, err = t.sum(dbKey); err != nil {
		return err
	}
	b, _ := json.Marshal(t)
	partial := filepath.Join(filepath.Dir(final), ".partial-tag-"+stamp)
	if err := os.WriteFile(partial, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(partial, TagPath(final)); err != nil {
		os.Remove(partial)
		return err
	}
	return nil
}
