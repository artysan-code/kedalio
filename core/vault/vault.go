package vault

import (
	"context"
	"database/sql"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Vault is the instance's key and what it knows to hide. The secrets store of the cut product is gone; a database that
// still holds its sealed secrets keeps them hidden from answers (Mask), and nothing writes or reads them back.
type Vault struct {
	db  *sql.DB
	dir string
	now func() time.Time

	mu     sync.Mutex // guards what follows
	kek    *kek
	values []string // what Mask hides, longest first; nil when to be rebuilt
}

// Open is the vault of a migrated database, with its key read from dir (see kek.go).
func Open(ctx context.Context, d *sql.DB, dir string) (*Vault, error) {
	v := &Vault{db: d, dir: dir, now: time.Now}
	if err := v.load(ctx); err != nil {
		return nil, err
	}
	return v, nil
}

// minMasked: a value shorter than this is not hidden from texts, or an empty or tiny one would hide half of them.
const minMasked = 4

const dataKeyAAD = "kedalio data key v1"

func secretAAD(name string) string { return "kedalio secret v1\n" + name }

// Mask hides every secret value the vault holds, wherever it stands in s, and secrets by their shape (a JWT, a token of
// a well-known prefix, a Bearer credential, the password of an address). It is for what leaves the core in an answer and
// for what memory refuses to keep, so it leaves ordinary words alone: name = value pairs are SecretPairs' business. A value
// shorter than four characters is not hidden, nor is one that is plainly a word (dictionaryLike). A value that could
// be a word or a name (letters only, or under eight characters) is hidden only as a whole token, never inside a longer
// word (replaceToken); a longer value with a number or a symbol in it is hidden wherever it stands. While the vault is locked only the shapes are.
func (v *Vault) Mask(s string) string {
	for _, val := range v.maskValues() {
		if wordLike(val) {
			s = replaceToken(s, val, Hidden)
		} else {
			s = strings.ReplaceAll(s, val, Hidden)
		}
	}
	return MaskField("", s)
}

// maskValues is the values to hide, longest first so that a value containing another is hidden whole. They are the
// secrets an older version stored in this database, when it has any (a fresh one has no such table) and the key is here.
func (v *Vault) maskValues() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.values != nil {
		return v.values
	}
	set := map[string]bool{}
	if v.kek != nil {
		set = v.storedSecrets(context.Background())
	}
	vals := []string{}
	for val := range set {
		if len(val) >= minMasked && !dictionaryLike(val) {
			vals = append(vals, val)
		}
	}
	slices.SortFunc(vals, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	v.values = vals
	return vals
}

// storedSecrets opens the sealed values an older version left, read only. Anything missing (no table, no data key, a row
// that does not open) is skipped: it only means there is less to hide.
func (v *Vault) storedSecrets(ctx context.Context) map[string]bool {
	set := map[string]bool{}
	var wrapped string
	if v.db.QueryRowContext(ctx, "select wrapped from data_keys where kind = 'kek'").Scan(&wrapped) != nil {
		return set
	}
	raw, err := v.kek.unwrap(wrapped, dataKeyAAD)
	if err != nil {
		return set
	}
	key, err := newAEAD(raw)
	if err != nil {
		return set
	}
	rows, err := v.db.QueryContext(ctx, "select name, sealed from secrets")
	if err != nil {
		return set
	}
	defer rows.Close()
	for rows.Next() {
		var name, sealed string
		if rows.Scan(&name, &sealed) != nil {
			continue
		}
		if val, err := open(key, sealed, secretAAD(name)); err == nil {
			set[string(val)] = true
		}
	}
	return set
}

var dictWord = regexp.MustCompile(`^[a-z]{1,12}$`)

// dictionaryLike: a value of 12 or fewer lowercase letters only is indistinguishable from an ordinary word, and hiding
// it would cut prose and titles. What escapes: a real secret that is a short lowercase word (a weak passphrase); any
// value with a number, a symbol, a capital or more than 12 characters is still hidden.
func dictionaryLike(val string) bool { return dictWord.MatchString(val) }

var lettersOnly = regexp.MustCompile(`^[A-Za-z]+$`)

// wordLike: values that can occur inside ordinary words, so they must stand alone to be hidden.
func wordLike(val string) bool { return len(val) < 8 || lettersOnly.MatchString(val) }

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

// replaceToken replaces val where it stands as a whole token: a side of the match that is a word character must not
// touch another word character, so «tokenization» keeps its «token» while «key=hunter2!» still hides «hunter2!».
func replaceToken(s, val, with string) string {
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(s[i:], val)
		if j < 0 {
			break
		}
		j += i
		end := j + len(val)
		if isWordByte(val[0]) && j > 0 && isWordByte(s[j-1]) || isWordByte(val[len(val)-1]) && end < len(s) && isWordByte(s[end]) {
			b.WriteString(s[i : j+1])
			i = j + 1
			continue
		}
		b.WriteString(s[i:j])
		b.WriteString(with)
		i = end
	}
	b.WriteString(s[i:])
	return b.String()
}
