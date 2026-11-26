package vault

import (
	"strings"
	"testing"
)

// The cases of apps/server/mask_test.ts: what must not be handed back.

func TestMaskFieldSecretNamesHiddenCredentialsTakenOutOfAddressesTheRestAsItIs(t *testing.T) {
	eq := func(got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
	eq(MaskField("DB_PASSWORD", "x"), Hidden)
	eq(MaskField("DATABASE_URL", "postgres://u:p@db:5432/x"), "postgres://‹user›:‹password›@db:5432/x")
	eq(MaskField("FIVETOOLS_URL", "https://5etools.example.com"), "https://5etools.example.com")
	for _, k := range []string{"BRAIN_PASSPHRASE", "BRAIN_TOTP_SECRET", "DB_PASS", "ADMIN_PWD", "WALLET_SEED", "PIN", "SIM_PIN", "HASH_SALT", "SIGNING_KEY", "OTP"} {
		eq(MaskField(k, "x"), Hidden)
	}
	for _, k := range []string{"BRAIN_URL", "PORT", "BRAIN_EMBED_MODEL", "PASSENGER_COUNT", "SPINNER", "COMPASS"} {
		eq(MaskField(k, "x"), "x")
	}
}

// a sample JWT, not a secret, in parts so that no scanner takes it for one
const sampleJWT = "ey" + "JhbGciOiJIUzI1NiJ9" + "." + "ey" + "JzdWIiOiIxMjM0NTY3ODkwIn0" + "." + "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

func TestMaskValuesThatLookLikeSecretsAreCoveredWhereverTheyAre(t *testing.T) {
	for _, v := range []string{
		sampleJWT,
		"ghp_" + "a1B2c3D4e5F6g7H8i9J0",
		"github_" + "pat_11ABCDEFG0abcdefghij_klmnop",
		"sk-proj-abcdefghijklmnop",
		"sk-" + "ant-api03-abcdefghijklmnop",
		"xo" + "xb-123456789012-abcdefghij",
		"xo" + "xp-123456789012-abcdefghij",
		"xo" + "xc-123456789012-abcdefghij",
		"xo" + "xp-1-abcdefghijklmno",
		"AK" + "IAIOSFODNN7EXAMPLE",
		"gl" + "pat-abcdefghij1234567890",
		"kdl_" + "abcdefghij1234567890-_xyz",
		"Bearer abc.def-123",
	} {
		if got := MaskField("note", v); got != Hidden {
			t.Errorf("%s -> %s", v, got)
		}
		parts := strings.Fields(v)
		if got := MaskField("note", "see "+v+" here"); strings.Contains(got, parts[len(parts)-1]) {
			t.Errorf("%s survived in %s", v, got)
		}
	}
	if strings.Contains(MaskValues(`curl -H "Authorization: Bearer `+sampleJWT+`"`), "eyJ") {
		t.Error("bearer jwt")
	}
	out := MaskValues("header " + sampleJWT + " sent with ghp_" + strings.Repeat("x", 20))
	if strings.Contains(out, "eyJ") || strings.Contains(out, "ghp_") {
		t.Error(out)
	}
}

func TestMaskSlacksDCookieIsCoveredWholeURLEncodedOrNot(t *testing.T) {
	tail := "AbCdEfGh1234%2Fxyz%2BQrStUv%3D%3D"
	for _, v := range []string{"xo" + "xd-" + tail, "xo" + "xd-AbCdEfGh1234/xyz+QrStUv=="} {
		if got := MaskValues("cookie d=" + v + "; path=/"); got != "cookie d="+Hidden+"; path=/" {
			t.Errorf("%s -> %s", v, got)
		}
		if MaskField("note", v) != Hidden {
			t.Errorf("%s", v)
		}
	}
}

func TestMaskOrdinaryHexUUIDsAndWordsAreLeftAlone(t *testing.T) {
	for _, v := range []string{
		"5242d63a1b2c3d4e5f60718293a4b5c6d7e8f901", // a git SHA
		"0123456789abcdef0123456789abcdef",
		"3f2504e0-4f89-11d3-9a0c-0305e82c3301", // a UUID
		"task-management-handler",
		"risk-assessment-for-the-project",
		"https://example.com/a?b=1",
	} {
		if MaskField("note", v) != v {
			t.Errorf("%s was changed", v)
		}
	}
}

func TestMaskAuthDsnJwtBearerSessionAndConnectionNames(t *testing.T) {
	for _, k := range []string{"SENTRY_DSN", "dsn", "CONNECTION_STRING", "connectionString", "x-auth", "AUTH", "bearer", "JWT", "id_jwt", "session_id", "sessionId", "AWS_ACCESS_KEY_ID", "accessKeyId"} {
		if MaskField(k, "x") != Hidden {
			t.Errorf("%s", k)
		}
	}
	for _, k := range []string{"author", "authority", "AUTHORS", "dsnote", "session", "idle"} {
		if MaskField(k, "x") != "x" {
			t.Errorf("%s", k)
		}
	}
}

func TestVaultMaskHidesEveryValueTheVaultHoldsAndTheShapesOfSecrets(t *testing.T) {
	v, d, _ := newVault(t)
	seedSecret(t, d, v, "FORGE_TOKEN", "the-forge-token")
	seedSecret(t, d, v, "SHORT", "ab")
	if got := v.Mask("deploy with the-forge-token done, ab stays"); got != "deploy with "+Hidden+" done, ab stays" {
		t.Fatalf("%q", got)
	}
	if strings.Contains(v.Mask("see ghp_"+strings.Repeat("x", 20)), "ghp_") {
		t.Fatal("a token by its shape")
	}
	if v.Mask("an ordinary note: password policy") != "an ordinary note: password policy" {
		t.Fatal("prose is left alone: the mask also guards what memory keeps")
	}
}

func TestVaultMaskAValueThatIsAWordNeverCutsOrdinaryText(t *testing.T) {
	v, d, _ := newVault(t)
	seedSecret(t, d, v, "A", "production")
	seedSecret(t, d, v, "B", "Rome24")
	seedSecret(t, d, v, "C", "Tango")
	seedSecret(t, d, v, "D", "k3y-9f8e7d6c5b4a")
	// a lowercase word-like value cannot be told from prose: it escapes
	if got := v.Mask("production notes: reproduction of the productions"); got != "production notes: reproduction of the productions" {
		t.Fatalf("%q", got)
	}
	// not inside a longer word, but alone it is hidden
	if got := v.Mask("a Tangoed text, Tango. Rome245 and Rome24!"); got != "a Tangoed text, "+Hidden+". Rome245 and "+Hidden+"!" {
		t.Fatalf("%q", got)
	}
	// a long value with a symbol or a number is hidden even glued to other characters
	if got := v.Mask("xk3y-9f8e7d6c5b4ay"); strings.Contains(got, "9f8e7d6c5b4a") {
		t.Fatalf("%q", got)
	}
}

func TestSecretPairsFindsCredentialsInANoteAndLeavesItsProseAlone(t *testing.T) {
	names := func(s string) []string {
		var out []string
		for _, p := range SecretPairs(s) {
			out = append(out, p.Name)
		}
		return out
	}
	for _, prose := range []string{
		"Il login usa la passkey; auth: OAuth 2.1 con PKCE.",
		"token: la sezione dei segreti spiega dove sta.",
		"Il core legge il token da `KEDALIO_CORE_TOKEN`; password: vedi il pannello di Coolify.",
		"BACKUP_S3_SECRET_KEY=${BACKUP_S3_SECRET_KEY} nel compose.",
		"**Token:** quello del deploy, nel pannello.",
		"api_key: <your key>, token=..., secret: ***",
		"The author: Ada. The authority: the owner.",
		"secret: Kedalio keeps none of them.",
		"password: KEDALIO_VAULT_PASSWORD (in the environment)",
		"session_id: vault:session",
		"A token bucket limits requests; the secret sauce is caching.",
	} {
		if n := names(prose); len(n) > 0 {
			t.Errorf("%q: %v", prose, n)
		}
	}
	for text, want := range map[string]string{
		"password: hunter2":                         "password",
		"DB_PASSWORD=s3cr3t-Value":                  "DB_PASSWORD",
		"La password del router è password: Rx7!kz": "password",
		`api_key = "a long phrase of words"`:        "api_key",
		"token=abc123":                              "token",
		"x-auth: 9f8e7d6c":                          "x-auth",
	} {
		if n := names(text); len(n) != 1 || n[0] != want {
			t.Errorf("%q: %v, want %s", text, n, want)
		}
	}
	if a, b := SecretPairs("token=abc123"), SecretPairs("so token=abc123 again"); a[0] != b[0] {
		t.Error("the same pair in two texts is not equal")
	}
}

// Settings, numbers and stand-ins are no credentials: a tasks_save log line citing MAX_THINKING_TOKENS=0 was refused.
func TestSecretPairsLetsSwitchesNumbersAndStandInsThroughButNotARealValue(t *testing.T) {
	for _, s := range []string{
		"Set MAX_THINKING_TOKENS=0 in the session's env.",
		"The function runs with verify_jwt=false.",
		"VITE_GOOGLE_AUTH=on in the build.",
		"Supabase: secure_password_change=true, auth_token_ttl=3600",
		"SMTP_PASSWORD=changeme in the example file, API_KEY=your-key, DB_PASSWORD=redacted",
		"token: none yet; secret=TBD",
	} {
		if p := SecretPairs(s); len(p) > 0 {
			t.Errorf("%q: %v", s, p)
		}
	}
	for _, s := range []string{
		"SMTP_PASSWORD=Xk92mPq7", "MAX_THINKING_TOKENS=abc123def", "verify_jwt=eyJhbGciOi", "API_KEY=4f9a2c7e1b",
		"the door_pin=4821", "totp: 123456",
	} {
		if p := SecretPairs(s); len(p) != 1 {
			t.Errorf("%q: %d pairs, want 1", s, len(p))
		}
	}
}

func TestMaskLeavesAPlaceholderAndShapeKindsNamesWhatIsThere(t *testing.T) {
	if s := "Authorization: Bearer " + Hidden; MaskField("", s) != s || len(ShapeKinds(s)) != 0 {
		t.Errorf("a placeholder is masked again or flagged: %v", ShapeKinds(s))
	}
	if k := ShapeKinds("Bearer abcdefghij0123456 and ghp_abcdefghijklmnop1234 at https://a:b1@x.example.com"); len(k) != 3 ||
		k[0] != "token of a known prefix" || k[1] != "Bearer-header credential" || k[2] != "credentials in an address" {
		t.Errorf("ShapeKinds = %v", k)
	}
}

func TestAReferenceToWhereTheSecretIsIsNoSecret(t *testing.T) {
	for _, s := range []string{
		"curl -H 'Authorization: Bearer $N8N_API_KEY' https://n8n.example.com",
		"Authorization: Bearer ${TOKEN}", "Authorization: Bearer <token>", "Authorization: Bearer YOUR_TOKEN",
		"Supabase: `secure_password_change=true`, **verify_jwt=false**",
		"export CLOUDFLARE_API_TOKEN=$(sops -d --extract x secrets.json)", "N8N_API_KEY=$N8N_API_KEY",
	} {
		if MaskField("", s) != s || len(ShapeKinds(s)) > 0 || len(SecretPairs(s)) > 0 {
			t.Errorf("%q: kinds %v, pairs %v", s, ShapeKinds(s), SecretPairs(s))
		}
	}
	for _, s := range []string{"the API wants a Bearer token in the header", "Authorization: Bearer abcdefgh"} {
		if len(ShapeKinds(s)) > 0 {
			t.Errorf("%q is flagged by the scan: %v", s, ShapeKinds(s))
		}
	}
	for _, s := range []string{"Authorization: Bearer Xk92mPq7abc" + "Lw3nRt5", "CLOUDFLARE_API_TOKEN=" + "Xk92mPq7abcd"} {
		if MaskField("", s) == s && len(SecretPairs(s)) == 0 {
			t.Errorf("%q passes", s)
		}
	}
}

// False positives on the owner's live pages, 23/11: a sops key name after a variable, a word, a template, prose after
// «Bearer», a slug after «token: ». None holds a value; each was flagged by the scan or refused at write.
func TestReferencesWordsTemplatesAndProseAreNoSecret(t *testing.T) {
	for _, s := range []string{
		// 1. the name of a key in the sops file, new style or imported from the old vault
		"Esempio: `mcp-env CLOUDFLARE_API_TOKEN=cloudflare.alice.token -- npx wrangler deploy`.",
		"mcp-env SUPABASE_ACCESS_TOKEN=old.SUPABASE_ACCESS_TOKEN -- curl https://api.example.com",
		"R2_SECRET_KEY=r2.backups.secret_key in the connector",
		// 2. a word standing for the value
		"SMTP_PASSWORD=chiave nel file sops, API_KEY=valore",
		// 3. a template
		`N8N_API_KEY: "{secret}" nella credenziale di n8n`, "token: {{ $env.N8N_TOKEN }}",
		// 4. prose after Bearer
		"Il client manda il token Bearer in un passo solo.", "I token Bearer non sono valori da salvare.",
		// 5. a slug after «token: »
		"- Mappa dei token: kedalio-bot (scope), REGISTRY_TOKEN, gh con lo scope workflow.",
	} {
		if MaskField("", s) != s || len(ShapeKinds(s)) > 0 || len(SecretPairs(s)) > 0 {
			t.Errorf("%q: mask %v, kinds %v, pairs %v", s, MaskField("", s) != s, ShapeKinds(s), SecretPairs(s))
		}
	}
}

// What the leniency above must not let through: real-looking values in the same places. They are built here, never
// written whole, so that the gate's secret scan does not read the test as a leak.
func TestRealValuesInTheSamePlacesAreStillSecret(t *testing.T) {
	rnd := "Xk92mPq7" + "Lw3nRt5vB8"
	letters := "QwErTyUiOp" + "AsDfGhJkLzXcVb"
	for _, s := range []string{
		"CLOUDFLARE_API_TOKEN=" + rnd,
		"SMTP_PASSWORD=" + rnd,
		`N8N_API_KEY: "` + rnd + `"`,
		`N8N_API_KEY: "{` + rnd + `}"`,
		"Authorization: Bearer " + rnd,
		"Authorization: Bearer " + letters,
		"token: " + rnd,
		"API_KEY=a1." + rnd,                           // dots but not a key name
		"PASSWORD=ab" + "c4d.ef5" + "g6h",             // lowercase parts with digits: an id.secret token, not a key name
		"PASSWORD=correct" + ".horse.battery",         // a dotted passphrase does not end in a kind of secret
		"password: correct" + "-horse-battery-staple", // a hyphenated passphrase is no prose
		"password: correct" + "-horse",
		`SMTP_PASSWORD: "{hunter}"`, // a word in braces that stands for nothing
		"Authorization: Bearer qwertyuiop" + "asdfgh",
	} {
		if MaskField("", s) == s && len(SecretPairs(s)) == 0 {
			t.Errorf("%q passes", s)
		}
	}
	if MaskField("", "Authorization: Bearer "+rnd) == "Authorization: Bearer "+rnd || len(ShapeKinds("Bearer "+letters)) == 0 {
		t.Error("a real Bearer token is not hidden or not flagged")
	}
}
