package vault

import (
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The database is encrypted with a key derived from the instance key (the KEK), not from a data key: a database made
// before 1.0 keeps its data key inside (data_keys), which can only be unwrapped after the database is open. The KEK is the one
// key that lives outside it (a file, or the recovery code the owner types), which makes it the only possible root. The
// two uses are kept apart by deriving the database key with its own label: it never equals the key that wraps the data
// key.

// DatabaseKey is the 32-byte key that encrypts the database file, derived from the instance key.
func DatabaseKey(kek []byte) ([]byte, error) {
	if len(kek) != keyBytes {
		return nil, errors.New("not an instance key")
	}
	return hkdf.Key(sha256.New, kek, nil, "kedalio database key v1", keyBytes)
}

// KeyFromCode is the instance key a recovery code gives back; false when the text is not a recovery code.
func KeyFromCode(code string) ([]byte, bool) {
	raw := fromRecoveryCode(code)
	return raw, raw != nil
}

// HostKey is the instance key kept in dir, needed before the database that it unlocks can be opened. With none there and
// create set, a new one is made (the first start of an instance); with none and create unset, nil: the database already
// has an instance and waits for its recovery code. Open then finds the file and adopts it.
func HostKey(dir string, create bool) ([]byte, error) {
	path := filepath.Join(dir, "kek")
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(raw) != keyBytes {
			return nil, fmt.Errorf("%s is not a key of %d bytes", path, keyBytes)
		}
		return raw, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	case !create:
		return nil, nil
	}
	raw = random(keyBytes)
	if err := writeKey(path, raw, false); err != nil {
		return nil, err
	}
	return raw, nil
}

// writeKey puts a key in a file only the owner reads, flushed to disk before anything relies on it: the database is
// encrypted under it, so a key lost to a power cut loses the instance. With replace the file is swapped whole, so a crash
// leaves the old key or the new, never half of one; without, an existing file is an error.
func writeKey(path string, raw []byte, replace bool) error {
	target, flag := path, os.O_EXCL
	if replace {
		target, flag = path+".new", os.O_TRUNC
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|flag, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && replace {
		err = os.Rename(target, path)
	}
	if err != nil {
		os.Remove(target)
	}
	return err
}
