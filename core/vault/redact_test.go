package vault

import (
	"strings"
	"testing"
)

// fakeValue is a credential-looking value made at run time, so that no scanner of the repository takes the test for a
// leak.
func fakeValue(seed string) string { return seed + strings.Repeat("7Kq", 8) }

func TestRedactHidesWhatTheScanFindsAndLeavesTheRestByteForByte(t *testing.T) {
	pair, bearer, known, pass := fakeValue("p"), fakeValue("b"), "gh"+"p_"+fakeValue("g"), fakeValue("u")
	in := "# Steps\n- [ ] call n8n with N8N_API_TOKEN=" + pair + " and wait\n" +
		"curl -H 'Authorization: Bearer " + bearer + "' https://example.com\n" +
		"git push with " + known + "\n" +
		"db at postgres://alice:" + pass + "@db.example.com/app\n" +
		"token: la sezione dei token è in sops; TOKEN=$N8N_TOKEN, Bearer $TOKEN\n"
	want := "# Steps\n- [ ] call n8n with N8N_API_TOKEN=" + Hidden + " and wait\n" +
		"curl -H 'Authorization: " + Hidden + "' https://example.com\n" +
		"git push with " + Hidden + "\n" +
		"db at postgres" + urlCredentialsHidden + "db.example.com/app\n" +
		"token: la sezione dei token è in sops; TOKEN=$N8N_TOKEN, Bearer $TOKEN\n"
	got, n := Redact(in)
	if got != want || n != 4 {
		t.Fatalf("Redact: %d replaced\n%s\nwant\n%s", n, got, want)
	}
	for _, v := range []string{pair, bearer, known, pass} {
		if strings.Contains(got, v) {
			t.Error("a value survived")
		}
	}
	if again, n := Redact(got); again != got || n != 0 {
		t.Errorf("a redacted text changed again (%d):\n%s", n, again)
	}
	if len(SecretPairs(got)) != 0 || len(ShapeKinds(got)) != 0 {
		t.Errorf("the scan still flags the redacted text: %v %v", SecretPairs(got), ShapeKinds(got))
	}
}

func TestRedactLeavesATextWithoutSecretsAlone(t *testing.T) {
	for _, s := range []string{
		"",
		"key name in sops: N8N_API_TOKEN (old.N8N_API_TOKEN)",
		"MAX_THINKING_TOKENS=0 verify_jwt=false SMTP_PASSWORD=changeme",
		"the Bearer token goes in the header; author: Ada",
		"the value is " + Hidden,
	} {
		if got, n := Redact(s); got != s || n != 0 {
			t.Errorf("%q became %q (%d)", s, got, n)
		}
	}
}
