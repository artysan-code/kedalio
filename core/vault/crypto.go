// Package vault is the instance's secrets and its keys (ADR 0002 decision 11, ADR 0004): secrets sealed with AES-256-GCM
// under a data key, the data key wrapped by the instance key (the KEK), the KEK kept in a file outside the data volume's
// backups. A recovery code (the KEK's bytes in base32) is shown once
// and puts the key back. There is one owner per instance, so nothing here names an account.
//
// The vault is write-only from outside: set, replace, delete and list by name. Reveal is for the run that needs the
// values, and every use is audited by name, never by value. Only the standard library does the cryptography.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"strings"
)

const keyBytes = 32

var b64 = base64.RawURLEncoding

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("vault: no randomness: " + err.Error())
	}
	return b
}

// seal is AES-256-GCM with associated data, as the vault seals everything: <nonce>.<ciphertext> in base64url. The
// associated data binds the text to its place, so a row moved elsewhere does not open.
func seal(a cipher.AEAD, data []byte, aad string) string {
	nonce := random(a.NonceSize())
	return b64.EncodeToString(nonce) + "." + b64.EncodeToString(a.Seal(nil, nonce, data, []byte(aad)))
}

var errNotSealed = errors.New("not a sealed value")

func open(a cipher.AEAD, sealed, aad string) ([]byte, error) {
	n, ct, ok := strings.Cut(sealed, ".")
	if !ok || n == "" || ct == "" {
		return nil, errNotSealed
	}
	nonce, err1 := b64.DecodeString(n)
	body, err2 := b64.DecodeString(ct)
	if err1 != nil || err2 != nil || len(nonce) != a.NonceSize() {
		return nil, errNotSealed
	}
	return a.Open(nil, nonce, body, []byte(aad))
}

// keyID is the id of a key, its SHA-256 cut to 16 bytes: enough to recognise it, nothing to rebuild it.
func keyID(raw []byte) string {
	h := sha256.Sum256([]byte("kedalio kek id v1\n" + b64.EncodeToString(raw)))
	return b64.EncodeToString(h[:16])
}

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// RecoveryCode is the recovery code of a key: its bytes in base32 in groups of four, ABCD-EFGH-…
func RecoveryCode(raw []byte) string {
	s := base32NoPad.EncodeToString(raw)
	var groups []string
	for len(s) > 4 {
		groups, s = append(groups, s[:4]), s[4:]
	}
	return strings.Join(append(groups, s), "-")
}

// fromRecoveryCode is the key a recovery code gives back, or nil when it is not one. Spaces, dashes and case do not matter.
func fromRecoveryCode(code string) []byte {
	clean := strings.NewReplacer(" ", "", "\t", "", "\n", "", "-", "", "=", "").Replace(strings.ToUpper(code))
	raw, err := base32NoPad.DecodeString(clean)
	if err != nil || len(raw) != keyBytes {
		return nil
	}
	return raw
}
