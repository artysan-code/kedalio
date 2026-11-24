package identity

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"
)

func wantErr(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Errorf("error = %v, want one containing %q", err, contains)
	}
}

func TestCborReadsWhatWebAuthnUsesAndRefusesTheRest(t *testing.T) {
	v, next, err := cborAt(cat(head(5, 2), cint(1), cint(-7), ctext("a"), cbytes([]byte{1, 2})), 0, 0)
	if err != nil || next != 8 {
		t.Fatalf("next=%d err=%v", next, err)
	}
	m := v.(map[any]any)
	if m[int64(1)] != int64(-7) || !bytes.Equal(m["a"].([]byte), []byte{1, 2}) {
		t.Errorf("map = %v", m)
	}
	if n, _, _ := cborAt([]byte{0x19, 0x01, 0x00}, 0, 0); n != int64(256) {
		t.Errorf("256 = %v", n)
	}
	for _, bad := range [][]byte{{0x5f}, {0x82, 1}, {0x59, 0, 9, 1}, {0xf9, 0, 0}, {0xa1, 0x40, 0}, {0x9f}, {0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}} {
		if _, _, err := cborAt(bad, 0, 0); err == nil {
			t.Errorf("% x was accepted", bad)
		}
	}
}

func TestBase64urlRoundTripsAndRefusesWhatIsNot(t *testing.T) {
	b := []byte{1, 2, 3, 250, 251}
	if got, ok := unb64u(b64.EncodeToString(b)); !ok || !bytes.Equal(got, b) {
		t.Errorf("round trip: %v %v", got, ok)
	}
	for _, bad := range []string{"a", "ab+/", "a b"} {
		if _, ok := unb64u(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if got, ok := unb64u(""); !ok || len(got) != 0 {
		t.Error("empty is empty")
	}
}

func TestSiteOfKeepsThePortInTheOriginAndNotInTheRPID(t *testing.T) {
	s, _ := SiteOf("https://app.example.com/")
	if s != (Site{"https://app.example.com", "app.example.com"}) {
		t.Errorf("%+v", s)
	}
	s, _ = SiteOf("http://localhost:8080")
	if s != (Site{"http://localhost:8080", "localhost"}) {
		t.Errorf("%+v", s)
	}
	if _, err := SiteOf("ftp://x"); err == nil {
		t.Error("ftp accepted")
	}
}

func TestRegistrationGivesTheIDAndTheUncompressedKey(t *testing.T) {
	a := newAuthenticator()
	reg := a.register("chal-1", regOver{})
	if clientChallenge(reg.Response.ClientDataJSON) != "chal-1" {
		t.Error("challenge")
	}
	got, err := parseRegistration(reg, "chal-1", testSite)
	if err != nil || got != (Credential{a.id(), a.publicKey()}) {
		t.Errorf("%+v %v", got, err)
	}
}

func TestRegistrationIsRefusedForEachThingWrongWithIt(t *testing.T) {
	a := newAuthenticator()
	check := func(reg Registration, want string) {
		t.Helper()
		_, err := parseRegistration(reg, "c", testSite)
		wantErr(t, err, want)
	}
	check(a.register("other", regOver{}), "challenge")
	check(a.register("c", regOver{typ: "webauthn.get"}), "type")
	check(a.register("c", regOver{origin: "https://evil.example"}), "origin")
	check(a.register("c", regOver{crossOrigin: true}), "another origin")
	check(a.register("c", regOver{fmt: "packed"}), "none only")
	check(a.register("c", regOver{authOver: authOver{rpID: "evil.example"}}), "rpIdHash")
	check(a.register("c", regOver{authOver: authOver{flags: flagsOf(flagAT | flagUV)}}), "presence")
	check(a.register("c", regOver{authOver: authOver{flags: flagsOf(flagAT | flagUP)}}), "verification")
	check(a.register("c", regOver{authOver: authOver{flags: flagsOf(flagUP | flagUV)}}), "no credential")
	for _, k := range []keyOver{{2, -257, 1}, {2, -7, 2}, {3, -7, 1}} {
		check(a.register("c", regOver{authOver: authOver{key: coseKey(a.point, k)}}), "ES256")
	}
	off := bytes.Clone(a.point)
	off[40] ^= 1
	check(a.register("c", regOver{authOver: authOver{key: coseKey(off, keyOver{})}}), "not a point")
	reg := a.register("c", regOver{})
	other := reg
	other.ID = "AAAA"
	check(other, "differs")
	other = reg
	other.Response.AttestationObject = "AAAA"
	check(other, "attestation object")
	other = reg
	other.Response.ClientDataJSON = "AAAA"
	check(other, "not JSON")
}

func TestAssertionChecks(t *testing.T) {
	a := newAuthenticator()
	h := sha256.Sum256([]byte("challenge"))
	c := h[:]
	verify := func(o assertOver) error { return verifyAssertion(a.assert(c, o), a.publicKey(), c, testSite) }
	if err := verify(assertOver{}); err != nil {
		t.Fatal(err)
	}
	if err := verify(assertOver{rawSig: true}); err != nil {
		t.Errorf("a raw signature: %v", err)
	}
	wantErr(t, verify(assertOver{typ: "webauthn.create"}), "type")
	wantErr(t, verify(assertOver{origin: "https://evil.example"}), "origin")
	wantErr(t, verify(assertOver{crossOrigin: true}), "another origin")
	wantErr(t, verify(assertOver{authOver: authOver{rpID: "evil.example"}}), "rpIdHash")
	wantErr(t, verify(assertOver{authOver: authOver{flags: flagsOf(flagUV)}}), "presence")
	wantErr(t, verify(assertOver{authOver: authOver{flags: flagsOf(flagUP)}}), "verification")

	good := a.assert(c, assertOver{})
	short := good
	short.AuthenticatorData = b64.EncodeToString(make([]byte, 36))
	wantErr(t, verifyAssertion(short, a.publicKey(), c, testSite), "too short")
	h2 := sha256.Sum256([]byte("another"))
	wantErr(t, verifyAssertion(good, a.publicKey(), h2[:], testSite), "challenge")
	sig, _ := unb64u(good.Signature)
	sig[len(sig)-1] ^= 1
	flipped := good
	flipped.Signature = b64.EncodeToString(sig)
	wantErr(t, verifyAssertion(flipped, a.publicKey(), c, testSite), "signature")
	garbage := good
	garbage.Signature = b64.EncodeToString(make([]byte, 5))
	wantErr(t, verifyAssertion(garbage, a.publicKey(), c, testSite), "ES256")
	// another origin on the same host (a port) is another site
	port, _ := SiteOf("https://core.example.com:8443")
	wantErr(t, verifyAssertion(good, a.publicKey(), c, port), "origin")
	// another passkey's key does not verify it
	wantErr(t, verifyAssertion(good, newAuthenticator().publicKey(), c, testSite), "signature")
}

func TestAssertionCleanKeepsFourPartsOfSize(t *testing.T) {
	a := newAuthenticator()
	good := a.assert([]byte("x"), assertOver{})
	if !good.clean() {
		t.Fatal("a good assertion is not clean")
	}
	for name, mod := range map[string]func(*Assertion){
		"empty":   func(x *Assertion) { x.Signature = "" },
		"not b64": func(x *Assertion) { x.ClientDataJSON = "not base64!" },
		"big":     func(x *Assertion) { x.Signature = strings.Repeat("A", 500) },
	} {
		x := good
		mod(&x)
		if x.clean() {
			t.Errorf("%s accepted", name)
		}
	}
}
