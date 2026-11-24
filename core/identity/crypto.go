// Package identity is who may talk to the core (ADR 0004: passkeys and OAuth for MCP clients carry over, accounts,
// invitations and passwords do not): one owner per instance, signing in with a passkey. It holds WebAuthn written by
// hand on the standard library, the owner's browser sessions, OAuth 2.1 for MCP clients such as claude.ai (dynamic client
// registration, PKCE S256, rotating refresh tokens), passkeys made with an approval code, and the few pages the flow needs.
//
// Nothing secret is kept as itself: a session, a code and a token are stored as their SHA-256.
package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
)

var b64 = base64.RawURLEncoding

// randomToken is n random bytes in base64url.
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("identity: no randomness: " + err.Error())
	}
	return b64.EncodeToString(b)
}

// hash is the form a secret is kept in: its SHA-256 in base64url.
func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return b64.EncodeToString(h[:])
}

// same compares two secrets, or their hashes, in time that does not depend on where they differ.
func same(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

var b64urlRe = regexp.MustCompile(`^[A-Za-z0-9_-]*={0,2}$`)

// unb64u decodes base64url, padded or not; ok is false for what is not base64url.
func unb64u(s string) ([]byte, bool) {
	if !b64urlRe.MatchString(s) {
		return nil, false
	}
	b, err := b64.DecodeString(strings.TrimRight(s, "="))
	return b, err == nil
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func fromJSON(s string, v any) error { return json.Unmarshal([]byte(s), v) }
