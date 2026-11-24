package identity

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"unicode/utf8"
)

// Passkeys are WebAuthn with ES256 only (alg -7, P-256) and attestation «none»: the registration is parsed into a
// credential id and its public key, and an assertion is verified against that key. A passkey answers with user
// verification, always.

// Site is what a passkey is bound to: the server's origin and its relying party id (the host, no port).
type Site struct {
	Origin string
	RPID   string
}

// SiteOf is the site of the server's public URL.
func SiteOf(raw string) (Site, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return Site{}, fmt.Errorf("%q is not an http(s) address", raw)
	}
	return Site{Origin: u.Scheme + "://" + u.Host, RPID: u.Hostname()}, nil
}

// ---------------------------------------------------------------- CBOR (the little of it WebAuthn needs)

// cborAt decodes one item at `at`: definite lengths only, which is all an authenticator writes. Values are int64,
// string, []byte, bool, nil, []any and map[any]any (keys int64 or string). It returns where the next item starts.
func cborAt(b []byte, at, depth int) (any, int, error) {
	if depth > 8 {
		return nil, 0, errors.New("cbor: too deep")
	}
	if at >= len(b) {
		return nil, 0, errors.New("cbor: truncated")
	}
	head := b[at]
	at++
	major, info := head>>5, head&31
	var n uint64
	switch {
	case info < 24:
		n = uint64(info)
	case info <= 27:
		w := 1 << (info - 24)
		if at+w > len(b) {
			return nil, 0, errors.New("cbor: truncated")
		}
		for i := 0; i < w; i++ {
			n = n<<8 | uint64(b[at+i])
		}
		at += w
		if n > 1<<53 {
			return nil, 0, errors.New("cbor: number too large")
		}
	default:
		return nil, 0, errors.New("cbor: indefinite lengths are not supported")
	}
	switch major {
	case 0:
		return int64(n), at, nil
	case 1:
		return -1 - int64(n), at, nil
	case 2, 3:
		if n > uint64(len(b)-at) {
			return nil, 0, errors.New("cbor: truncated")
		}
		s := b[at : at+int(n)]
		at += int(n)
		if major == 2 {
			return bytes.Clone(s), at, nil
		}
		if !utf8.Valid(s) {
			return nil, 0, errors.New("cbor: text is not UTF-8")
		}
		return string(s), at, nil
	case 4, 5:
		if n > uint64(len(b)-at) { // every item takes a byte: a count beyond that is a lie
			return nil, 0, errors.New("cbor: truncated")
		}
		if major == 4 {
			out := make([]any, 0, n)
			for i := uint64(0); i < n; i++ {
				v, next, err := cborAt(b, at, depth+1)
				if err != nil {
					return nil, 0, err
				}
				out = append(out, v)
				at = next
			}
			return out, at, nil
		}
		m := make(map[any]any, n)
		for i := uint64(0); i < n; i++ {
			k, k2, err := cborAt(b, at, depth+1)
			if err != nil {
				return nil, 0, err
			}
			switch k.(type) {
			case int64, string:
			default:
				return nil, 0, errors.New("cbor: a map key is a number or text")
			}
			v, v2, err := cborAt(b, k2, depth+1)
			if err != nil {
				return nil, 0, err
			}
			m[k] = v
			at = v2
		}
		return m, at, nil
	case 7:
		switch info {
		case 20:
			return false, at, nil
		case 21:
			return true, at, nil
		case 22, 23:
			return nil, at, nil
		}
		return nil, 0, errors.New("cbor: unsupported simple value")
	}
	return nil, 0, errors.New("cbor: unsupported type")
}

// ---------------------------------------------------------------- registration

const (
	flagUP = 0x01
	flagUV = 0x04
	flagAT = 0x40
)

type clientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin bool   `json:"crossOrigin"`
}

func parseClientData(clientDataJSON string) (clientData, bool) {
	raw, ok := unb64u(clientDataJSON)
	if !ok {
		return clientData{}, false
	}
	var c clientData
	if json.Unmarshal(raw, &c) != nil {
		return clientData{}, false
	}
	return c, true
}

// clientChallenge is the challenge a clientDataJSON carries, or "" when it is not JSON with one.
func clientChallenge(clientDataJSON string) string {
	c, _ := parseClientData(clientDataJSON)
	return c.Challenge
}

// Registration is a registration as the browser's PublicKeyCredential.toJSON() gives it.
type Registration struct {
	ID       string `json:"id"`
	Response struct {
		AttestationObject string `json:"attestationObject"`
		ClientDataJSON    string `json:"clientDataJSON"`
	} `json:"response"`
}

// Credential is a passkey as kept: its id and its public key (an uncompressed P-256 point), both base64url.
type Credential struct {
	ID        string
	PublicKey string
}

// parseRegistration is the passkey a registration makes, or why it is not acceptable: type webauthn.create, the
// challenge the server made, the server's origin and rpIdHash, user present and verified, attestation none, ES256.
func parseRegistration(reg Registration, challenge string, site Site) (Credential, error) {
	cd, ok := parseClientData(reg.Response.ClientDataJSON)
	if !ok {
		return Credential{}, errors.New("the client data is not JSON")
	}
	if cd.Type != "webauthn.create" {
		return Credential{}, errors.New("not a registration (type)")
	}
	if cd.Challenge != challenge {
		return Credential{}, errors.New("the challenge is not the one asked for")
	}
	if cd.Origin != site.Origin {
		return Credential{}, errors.New("the origin is not this server's")
	}
	if cd.CrossOrigin {
		return Credential{}, errors.New("made from another origin's frame")
	}
	att, ok := unb64u(reg.Response.AttestationObject)
	if !ok {
		return Credential{}, errors.New("the attestation is not base64url")
	}
	o, _, err := cborAt(att, 0, 0)
	om, isMap := o.(map[any]any)
	if err == nil && !isMap {
		err = errors.New("not a map")
	}
	if err != nil {
		return Credential{}, fmt.Errorf("the attestation object: %w", err)
	}
	if om["fmt"] != "none" {
		return Credential{}, fmt.Errorf("attestation %v is not accepted: none only", om["fmt"])
	}
	auth, isBytes := om["authData"].([]byte)
	if !isBytes {
		return Credential{}, errors.New("the attestation object: no authData")
	}
	if len(auth) < 55 {
		return Credential{}, errors.New("authenticator data too short")
	}
	if rp := sha256.Sum256([]byte(site.RPID)); !bytes.Equal(auth[:32], rp[:]) {
		return Credential{}, errors.New("the rpIdHash is not this server's")
	}
	flags := auth[32]
	switch {
	case flags&flagUP == 0:
		return Credential{}, errors.New("user presence is not set")
	case flags&flagUV == 0:
		return Credential{}, errors.New("user verification is not set")
	case flags&flagAT == 0:
		return Credential{}, errors.New("no credential in the authenticator data")
	}
	idLen := int(auth[53])<<8 | int(auth[54])
	if idLen < 16 || idLen > 1023 || 55+idLen > len(auth) {
		return Credential{}, errors.New("the credential id has a wrong length")
	}
	id := b64.EncodeToString(auth[55 : 55+idLen])
	if reg.ID != "" && reg.ID != id {
		return Credential{}, errors.New("the credential id differs from the one in the data")
	}
	k, _, err := cborAt(auth, 55+idLen, 0)
	if err != nil {
		return Credential{}, fmt.Errorf("the public key: %w", err)
	}
	key, isMap := k.(map[any]any)
	if !isMap {
		return Credential{}, errors.New("the public key is not a COSE key")
	}
	if key[int64(1)] != int64(2) || key[int64(3)] != int64(-7) || key[int64(-1)] != int64(1) {
		return Credential{}, errors.New("the key is not ES256 (EC2, P-256)")
	}
	x, xok := key[int64(-2)].([]byte)
	y, yok := key[int64(-3)].([]byte)
	if !xok || !yok || len(x) != 32 || len(y) != 32 {
		return Credential{}, errors.New("the key's coordinates are not 32 bytes")
	}
	point := append(append([]byte{4}, x...), y...)
	if _, err := ecdh.P256().NewPublicKey(point); err != nil { // checks the point is on the curve
		return Credential{}, errors.New("the key is not a point of P-256")
	}
	return Credential{ID: id, PublicKey: b64.EncodeToString(point)}, nil
}

// ---------------------------------------------------------------- assertion

// Assertion is a passkey's answer as the browser gives it, each part base64url.
type Assertion struct {
	Credential        string `json:"credential"`
	AuthenticatorData string `json:"authenticatorData"`
	ClientDataJSON    string `json:"clientDataJSON"`
	Signature         string `json:"signature"`
}

// clean reports whether the assertion has all four parts, each base64url and within size.
func (a Assertion) clean() bool {
	part := func(v string, max int) bool {
		_, ok := unb64u(v)
		return v != "" && len(v) <= max && ok
	}
	return part(a.Credential, 1400) && part(a.AuthenticatorData, 1400) && part(a.ClientDataJSON, 4000) &&
		part(a.Signature, 400)
}

// verifyAssertion says why an assertion does not hold, or nil when it does: for the key `publicKey` and the challenge,
// on this site. The signature is DER, as an authenticator writes it, or the raw r || s of 64 bytes.
func verifyAssertion(a Assertion, publicKey string, challenge []byte, site Site) error {
	cd, ok := parseClientData(a.ClientDataJSON)
	if !ok {
		return errors.New("the client data is not JSON")
	}
	if cd.Type != "webauthn.get" {
		return errors.New("the client data is not an assertion (type)")
	}
	if cd.Challenge != b64.EncodeToString(challenge) {
		return errors.New("the challenge is not the one asked for")
	}
	if cd.Origin != site.Origin {
		return errors.New("the origin is not the server's")
	}
	if cd.CrossOrigin {
		return errors.New("made from another origin's frame")
	}
	auth, ok := unb64u(a.AuthenticatorData)
	if !ok || len(auth) < 37 {
		return errors.New("authenticator data too short")
	}
	if rp := sha256.Sum256([]byte(site.RPID)); !bytes.Equal(auth[:32], rp[:]) {
		return errors.New("the rpIdHash is not the server's")
	}
	if auth[32]&flagUP == 0 {
		return errors.New("user presence is not set")
	}
	if auth[32]&flagUV == 0 {
		return errors.New("user verification is not set")
	}
	point, ok1 := unb64u(publicKey)
	sig, ok2 := unb64u(a.Signature)
	if !ok1 || !ok2 || (len(sig) != 64 && (len(sig) < 8 || sig[0] != 0x30)) {
		return errors.New("the signature is not ES256")
	}
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
	if err != nil {
		return errors.New("the signature is not the passkey's")
	}
	raw, _ := unb64u(a.ClientDataJSON)
	clientHash := sha256.Sum256(raw)
	signed := sha256.Sum256(append(bytes.Clone(auth), clientHash[:]...))
	if ecdsa.VerifyASN1(key, signed[:], sig) {
		return nil
	}
	if len(sig) == 64 && ecdsa.Verify(key, signed[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil
	}
	return errors.New("the signature is not the passkey's")
}
