package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/json"
	"math/big"
)

// A software authenticator for the tests: a real P-256 key that makes real authenticator data, client data, attestation
// objects and DER signatures, as a phone does. The tests' site is the one the e2e server is given.

var testSite = Site{Origin: "https://core.example.com", RPID: "core.example.com"}

// ---- a minimal CBOR writer for the attestation object and the COSE key
func head(major byte, n int) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 256:
		return []byte{major<<5 | 24, byte(n)}
	}
	return []byte{major<<5 | 25, byte(n >> 8), byte(n)}
}

func cint(n int) []byte {
	if n >= 0 {
		return head(0, n)
	}
	return head(1, -1-n)
}
func cbytes(b []byte) []byte { return append(head(2, len(b)), b...) }
func ctext(s string) []byte  { return append(head(3, len(s)), s...) }
func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

type keyOver struct{ kty, alg, crv int }

func coseKey(point []byte, o keyOver) []byte {
	if o == (keyOver{}) {
		o = keyOver{2, -7, 1}
	}
	return cat(head(5, 5), cint(1), cint(o.kty), cint(3), cint(o.alg), cint(-1), cint(o.crv),
		cint(-2), cbytes(point[1:33]), cint(-3), cbytes(point[33:65]))
}

// derSig is r || s as DER, as an authenticator writes it.
func derSig(raw []byte) []byte {
	b, _ := asn1.Marshal(struct{ R, S *big.Int }{new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:])})
	return b
}

type Authenticator struct {
	key    *ecdsa.PrivateKey
	point  []byte
	credID []byte
	site   Site
}

func newAuthenticator() *Authenticator {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	pt, _ := k.PublicKey.Bytes()
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	return &Authenticator{k, pt, id, testSite}
}

func (a *Authenticator) id() string        { return b64.EncodeToString(a.credID) }
func (a *Authenticator) publicKey() string { return b64.EncodeToString(a.point) }

type authOver struct {
	flags    *byte
	rpID     string
	attested bool
	key      []byte
}

func (a *Authenticator) authData(o authOver) []byte {
	flags := byte(flagUP | flagUV)
	if o.attested {
		flags |= flagAT
	}
	if o.flags != nil {
		flags = *o.flags
	}
	rp := o.rpID
	if rp == "" {
		rp = a.site.RPID
	}
	h := sha256.Sum256([]byte(rp))
	out := append(append([]byte{}, h[:]...), flags, 0, 0, 0, 1)
	if !o.attested {
		return out
	}
	key := o.key
	if key == nil {
		key = coseKey(a.point, keyOver{})
	}
	return cat(out, make([]byte, 16), []byte{byte(len(a.credID) >> 8), byte(len(a.credID))}, a.credID, key)
}

type regOver struct {
	authOver
	typ, origin, fmt string
	crossOrigin      bool
}

func (a *Authenticator) register(challenge string, o regOver) Registration {
	cd, _ := json.Marshal(map[string]any{"type": or(o.typ, "webauthn.create"), "challenge": challenge,
		"origin": or(o.origin, a.site.Origin), "crossOrigin": o.crossOrigin})
	o.attested = true
	att := cat(head(5, 3), ctext("fmt"), ctext(or(o.fmt, "none")), ctext("attStmt"), head(5, 0), ctext("authData"),
		cbytes(a.authData(o.authOver)))
	var reg Registration
	reg.ID = a.id()
	reg.Response.AttestationObject = b64.EncodeToString(att)
	reg.Response.ClientDataJSON = b64.EncodeToString(cd)
	return reg
}

type assertOver struct {
	authOver
	typ, origin string
	crossOrigin bool
	rawSig      bool
}

func (a *Authenticator) assert(challenge []byte, o assertOver) Assertion {
	cd := map[string]any{"type": or(o.typ, "webauthn.get"), "challenge": b64.EncodeToString(challenge),
		"origin": or(o.origin, a.site.Origin)}
	if o.crossOrigin {
		cd["crossOrigin"] = true
	}
	cdJSON, _ := json.Marshal(cd)
	auth := a.authData(o.authOver)
	h := sha256.Sum256(cdJSON)
	signed := sha256.Sum256(append(append([]byte{}, auth...), h[:]...))
	r, s, err := ecdsa.Sign(rand.Reader, a.key, signed[:])
	if err != nil {
		panic(err)
	}
	raw := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	sig := derSig(raw)
	if o.rawSig {
		sig = raw
	}
	return Assertion{Credential: a.id(), AuthenticatorData: b64.EncodeToString(auth),
		ClientDataJSON: b64.EncodeToString(cdJSON), Signature: b64.EncodeToString(sig)}
}

func or(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

func flagsOf(f byte) *byte { return &f }
