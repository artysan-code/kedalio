package vault

import (
	"context"
	"crypto/cipher"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
)

// The instance key, the KEK (ADR 0002 decision 11): 32 random bytes in <data>/kek (mode 0600), made at the first start,
// outside the backups folder. It wraps the instance's data key. Its recovery code (the same bytes in base32) is shown once
// at the first sign-in and puts the key back after the volume is lost.
//
// A database whose instance already has a key (its id is in the settings) but no file is never given a new one: the
// secrets would be lost without a word. It stays locked until the recovery code arrives.
//

// ErrLocked is what a vault without its key answers.
var ErrLocked = errors.New("the vault is locked: it waits for the recovery code")

type kek struct {
	aead cipher.AEAD
	raw  []byte
	id   string
}

func newKEK(raw []byte) (*kek, error) {
	a, err := newAEAD(raw)
	if err != nil {
		return nil, err
	}
	return &kek{aead: a, raw: raw, id: keyID(raw)}, nil
}

// wrap is data sealed under the key and bound to aad.
func (k *kek) wrap(data []byte, aad string) string { return seal(k.aead, data, aad) }
func (k *kek) unwrap(sealed, aad string) ([]byte, error) {
	return open(k.aead, sealed, aad)
}

func (v *Vault) setting(ctx context.Context, k string) (string, error) {
	var s string
	err := v.db.QueryRowContext(ctx, "select v from vault_settings where k = ?", k).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return s, err
}

func (v *Vault) setSetting(ctx context.Context, k, val string) error {
	_, err := v.db.ExecContext(ctx,
		"insert into vault_settings (k, v) values (?, ?) on conflict (k) do update set v = excluded.v", k, val)
	return err
}

func (v *Vault) keyFile() string { return filepath.Join(v.dir, "kek") }

// load reads the key of this data folder: from its file, made on an instance that never had one, left locked otherwise.
// A file that is not this instance's key is refused rather than used.
func (v *Vault) load(ctx context.Context) error {
	known, err := v.setting(ctx, "kek_id")
	if err != nil {
		return err
	}
	// a database that knows its key but finds no file stays locked, and is never given a new key
	raw, err := HostKey(v.dir, known == "")
	if err != nil || raw == nil {
		return err
	}
	k, err := newKEK(raw)
	if err != nil {
		return err
	}
	if known != "" && known != k.id {
		return fmt.Errorf("%s is not this instance's key", v.keyFile())
	}
	if known == "" {
		// a key made just now, or before the database existed (HostKey): its code has not been shown yet
		if err := v.setSetting(ctx, "kek_id", k.id); err != nil {
			return err
		}
		if err := v.setSetting(ctx, "kek_recovery_shown", "0"); err != nil {
			return err
		}
	}
	v.kek = k
	return nil
}

// Locked reports whether the vault waits for the owner's recovery code.
func (v *Vault) Locked() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.kek == nil
}

// KeyID is the id of the instance key, empty while the vault is locked.
func (v *Vault) KeyID() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.kek == nil {
		return ""
	}
	return v.kek.id
}

// Unlock opens a locked vault with the recovery code. False when the code is not this instance's key. On a host-key
// instance the key file is put back.
func (v *Vault) Unlock(ctx context.Context, code string) (bool, error) {
	raw := fromRecoveryCode(code)
	if raw == nil {
		return false, nil
	}
	known, err := v.setting(ctx, "kek_id")
	if err != nil {
		return false, err
	}
	k, err := newKEK(raw)
	if err != nil || known == "" || k.id != known {
		return false, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := writeKey(v.keyFile(), raw, true); err != nil {
		return false, err
	}
	if err := v.setSetting(ctx, "kek_recovery_shown", "1"); err != nil { // the owner had it in hand
		return false, err
	}
	v.kek = k
	v.values = nil
	return true, nil
}

// PeekRecoveryCode is the recovery code while it has not been shown, without counting it as shown; empty after. The
// caller delivers it and then calls RecoveryShown, so a delivery that fails does not lose the code.
func (v *Vault) PeekRecoveryCode(ctx context.Context) (string, error) {
	v.mu.Lock()
	k := v.kek
	v.mu.Unlock()
	if k == nil {
		return "", ErrLocked
	}
	var shown string
	if err := v.db.QueryRowContext(ctx, "select v from vault_settings where k = 'kek_recovery_shown'").Scan(&shown); err != nil {
		return "", err
	}
	if shown != "0" {
		return "", nil
	}
	return RecoveryCode(k.raw), nil
}

// RecoveryShown records that the recovery code has been delivered.
func (v *Vault) RecoveryShown(ctx context.Context) error {
	_, err := v.db.ExecContext(ctx, "update vault_settings set v = '1' where k = 'kek_recovery_shown' and v = '0'")
	return err
}

// TakeRecoveryCode is the recovery code, the one time it is shown; empty when it was shown already.
func (v *Vault) TakeRecoveryCode(ctx context.Context) (string, error) {
	v.mu.Lock()
	k := v.kek
	v.mu.Unlock()
	if k == nil {
		return "", ErrLocked
	}
	r, err := v.db.ExecContext(ctx, "update vault_settings set v = '1' where k = 'kek_recovery_shown' and v = '0'")
	if err != nil {
		return "", err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return "", nil
	}
	return RecoveryCode(k.raw), nil
}
