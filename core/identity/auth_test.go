package identity

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRedirectsAreClaudesAndChatGPTsCallbacksOrLoopbackWithTheLoopbackPortFree(t *testing.T) {
	for uri, want := range map[string]bool{
		"https://claude.ai/api/mcp/auth_callback":               true,
		"https://claude.com/api/mcp/auth_callback":              true,
		"https://chatgpt.com/connector_platform_oauth_redirect": true,
		"https://claude.ai/other":                               false,
		"https://evil.example/api/mcp/auth_callback":            false,
		"http://claude.ai/api/mcp/auth_callback":                false,
		"https://claude.ai:8443/api/mcp/auth_callback":          false,
		"http://localhost:54321/callback":                       true,
		"http://127.0.0.1/cb":                                   true,
		"http://[::1]:9/cb":                                     true,
		"http://user:pw@localhost/cb":                           false,
		"https://localhost/cb":                                  false,
		"http://evil.example/cb":                                false,
		"not a url":                                             false,
		"javascript:alert(1)":                                   false,
	} {
		if got := redirectAllowed(uri); got != want {
			t.Errorf("redirectAllowed(%q) = %v", uri, got)
		}
	}
	reg := []string{"http://localhost:1111/cb", "https://claude.ai/api/mcp/auth_callback"}
	for uri, want := range map[string]bool{
		"http://localhost:2222/cb":                 true,
		"http://127.0.0.1:3333/cb":                 true,
		"http://[::1]:4444/cb":                     true,
		"http://localhost:2222/other":              false,
		"https://claude.ai/api/mcp/auth_callback":  true,
		"https://claude.ai/api/mcp/auth_callbackx": false,
		"http://evil.example/cb":                   false,
	} {
		if got := redirectMatches(reg, uri); got != want {
			t.Errorf("redirectMatches(%q) = %v", uri, got)
		}
	}
}

func TestARemoteClientIsOneWithAnyRedirectOffThisMachine(t *testing.T) {
	claude, local := "https://claude.ai/api/mcp/auth_callback", "http://localhost:4567/callback"
	for _, c := range []struct {
		redirects []string
		want      bool
	}{
		{[]string{local}, false},
		{[]string{local, "http://127.0.0.1:9/cb", "http://[::1]/cb"}, false},
		{[]string{claude}, true},
		{[]string{"https://chatgpt.com/connector_platform_oauth_redirect"}, true},
		{[]string{local, claude}, true}, // one remote redirect is enough: a code can go there
		{nil, true},                     // nothing proves it is local
	} {
		if got := isRemote(c.redirects); got != c.want {
			t.Errorf("isRemote(%v) = %v", c.redirects, got)
		}
	}

	i := testIdentity(t)
	now := time.Now().UTC().Format(time.RFC3339)
	for _, q := range []string{
		"insert into oauth_clients values ('cloud', 'Claude', '[\"" + claude + "\"]', '" + now + "')",
		"insert into oauth_clients values ('cli', 'Claude Code', '[\"" + local + "\"]', '" + now + "')",
	} {
		if _, err := i.Auth.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for tok, client := range map[string]string{"t-cloud": "cloud", "t-cli": "cli"} {
		if _, err := i.Auth.db.ExecContext(ctx, "insert into tokens (hash, kind, client, family, expires, created) values (?, 'access', ?, 'f', ?, ?)",
			hash(tok), client, time.Now().Add(time.Hour).UnixMilli(), now); err != nil {
			t.Fatal(err)
		}
	}
	if name, remote := i.Auth.ClientOf(ctx, "t-cloud"); name != "Claude" || !remote {
		t.Errorf("the claude.ai client: %q, remote %v", name, remote)
	}
	if name, remote := i.Auth.ClientOf(ctx, "t-cli"); name != "Claude Code" || remote {
		t.Errorf("the loopback client: %q, remote %v", name, remote)
	}
	if name, _ := i.Auth.ClientOf(ctx, "t-none"); name != "" {
		t.Errorf("an unknown token: %q", name)
	}
}

func TestCleanNameTakesControlCharactersOutAndCapsTheLength(t *testing.T) {
	for in, want := range map[string]string{"Claude": "Claude", "A\x1b[2JB": "A [2JB", "a\r\nb\tc": "a  b c", "": "MCP client",
		"\x1b\n": "MCP client", "a\u202eb\u2028c\u2029d\u200be": "a b c d e", strings.Repeat("é", 100): strings.Repeat("é", 80)} {
		if got := CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSameComparesEqualStringsOnly(t *testing.T) {
	if !same("abc", "abc") || same("abc", "abd") || same("abc", "abcd") || same("", "a") {
		t.Error("same is wrong")
	}
}

func TestSafeNextIsAPathOnThisOriginAndNothingThatLeavesIt(t *testing.T) {
	for in, want := range map[string]string{"/oauth/authorize?a=1": "/oauth/authorize?a=1", "": "", "https://evil.example": "",
		"//evil.example": "", "/\\evil.example": "", "evil": "", "/\t/evil.com": "", "/\r/evil.com": "", "/\n/evil.com": "",
		"/%09/evil.com": "", "/%0a/evil.com": "", "/%5Cevil.com": "", "/%2f/evil.com": "", "/a\x7f": "", "/a\x00": "",
		"/ok%20page": "/ok%20page", "/a\\b": ""} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q", in, got)
		}
	}
}

func TestRegisterChargesTheGlobalBucketOnlyWhenTheAddressMayGo(t *testing.T) {
	i := &Identity{register: NewBuckets(1, time.Hour), registerAll: NewBuckets(3, time.Hour)}
	for n := 0; n < 50; n++ {
		i.wait("/oauth/register", "203.0.113.9")
	}
	// the one address was refused after its first try, so it drained one of the global three and no more
	for _, ip := range []string{"198.51.100.1", "198.51.100.2"} {
		if w := i.wait("/oauth/register", ip); w != 0 {
			t.Errorf("%s waits %ds: another address drained the global bucket", ip, w)
		}
	}
}

func TestLangOfIsItalianWhenTheBrowserPutsItFirst(t *testing.T) {
	for in, want := range map[string]string{"it-IT,it;q=0.9,en;q=0.8": "it", "en-GB,it;q=0.9": "en", "": "en", "de": "en"} {
		if got := langOf(in); got != want {
			t.Errorf("langOf(%q) = %q", in, got)
		}
	}
}

func TestItalianCarriesEveryEnglishKeyAndOnlyThose(t *testing.T) {
	for k := range texts["en"] {
		if texts["it"][k] == "" {
			t.Errorf("Italian lacks %q", k)
		}
	}
	for k := range texts["it"] {
		if texts["en"][k] == "" {
			t.Errorf("English lacks %q", k)
		}
	}
}

func TestPagesEscapeWhatTheyShowAndKeepTheStringsSafeInTheirScript(t *testing.T) {
	p := consentPage("en", `<script>alert(1)</script>`, url.Values{"state": {`"><b>`}})
	if strings.Contains(p, "<script>alert") || strings.Contains(p, `"><b>`) {
		t.Error("a client name or a parameter reached the page unescaped")
	}
	if strings.Count(setupPage("it"), "</script>") != 2 { // the data block and the script: no string closes one early
		t.Error("script data not safe")
	}
	if !strings.Contains(consentPage("it", "Claude", nil), "Collega Claude") {
		t.Error("Italian title missing")
	}
}

func TestPurgeForgetsExpiredRowsAndKeepsValidOnes(t *testing.T) {
	i := testIdentity(t)
	now := time.Now()
	i.Auth.Now = func() time.Time { return now }
	ms, day := now.UnixMilli(), (24 * time.Hour).Milliseconds()
	iso := func(d time.Time) string { return d.UTC().Format(time.RFC3339) }
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := i.Auth.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("insert into oauth_clients values ('live', 'x', '[]', ?)", iso(now))
	tok := "insert into tokens (hash, kind, client, family, expires, created) values (?, 'access', 'live', 'f', ?, ?)"
	exec(tok, "fresh", ms+1000, iso(now))
	exec(tok, "recent", ms-day, iso(now)) // expired, but inside the grace the client rules rely on
	exec(tok, "old", ms-91*day, iso(now))
	code := "insert into oauth_codes (hash, client, redirect, challenge, expires) values (?, 'live', 'r', 'c', ?)"
	exec(code, "c-live", ms+1000)
	exec(code, "c-dead", ms-1000)
	ses := "insert into sessions (id, hash, created, used, expires, ends) values (?, ?, 'x', 'x', ?, ?)"
	exec(ses, "1", "s-live", ms+1000, ms+2000)
	exec(ses, "2", "s-dead", ms-1000, ms+2000)
	exec("insert into challenges values ('ch-live', 'sign-in', ?)", ms+1000)
	exec("insert into challenges values ('ch-dead', 'sign-in', ?)", ms-1000)

	if n, err := i.Auth.Purge(ctx); err != nil || n != 4 { // the old token, the dead code, session and challenge
		t.Errorf("purged %d, %v", n, err)
	}
	for q, want := range map[string]int{"tokens": 2, "oauth_codes": 1, "sessions": 1, "challenges": 1, "oauth_clients": 1} {
		var n int
		_ = i.Auth.db.QueryRowContext(ctx, "select count(*) from "+q).Scan(&n)
		if n != want {
			t.Errorf("%s: %d rows, want %d", q, n, want)
		}
	}
	if n, _ := i.Auth.Purge(ctx); n != 0 {
		t.Errorf("a second purge removed %d", n)
	}
}

func TestSessionsAWeekPutDownStillSignedInAMonthIdleOrNinetyDaysInAllSignsOut(t *testing.T) {
	i := testIdentity(t)
	now := time.Now()
	i.Auth.Now = func() time.Time { return now }
	req := func(token string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		return r
	}
	day := 24 * time.Hour
	at := func(d time.Duration) { now = now.Add(d) }
	a, _ := i.Auth.NewSession(ctx, "")
	at(day)
	if !i.Auth.Session(ctx, req(a)) {
		t.Error("a day later")
	}
	at(7 * day)
	if !i.Auth.Session(ctx, req(a)) {
		t.Error("a week later")
	}
	at(SessionIdle + time.Second)
	if i.Auth.Session(ctx, req(a)) {
		t.Error("idle for a month")
	}
	// used every few weeks, it still ends at the cap
	start := now
	b, _ := i.Auth.NewSession(ctx, "")
	for d := 20 * day; d < SessionMax; d += 20 * day {
		now = start.Add(d)
		if !i.Auth.Session(ctx, req(b)) {
			t.Fatalf("signed out at day %d", d/day)
		}
	}
	now = start.Add(SessionMax + time.Second)
	if i.Auth.Session(ctx, req(b)) {
		t.Error("past ninety days")
	}
	// the token is kept as its hash only
	var n int
	_ = i.Auth.db.QueryRowContext(ctx, "select count(*) from sessions where hash = ?", b).Scan(&n)
	if n != 0 {
		t.Error("a session token is stored as itself")
	}
}

func TestPublicURLMustBeAnOriginOnHTTPSOrLoopback(t *testing.T) {
	for in, ok := range map[string]bool{"https://core.example.com": true, "https://core.example.com/": true,
		"http://127.0.0.1:8787": true, "http://localhost:8787": true, "http://core.example.com": false,
		"https://core.example.com/path": false, "core.example.com": false, "https://u:p@core.example.com": false, "": false} {
		_, err := New(ctx, testDB(t), Options{PublicURL: in})
		if (err == nil) != ok {
			t.Errorf("%q: err = %v", in, err)
		}
	}
}

func TestConsentPageShowsWhereTheSignInGoesAndThatTheNameIsSelfDeclared(t *testing.T) {
	p := consentPage("en", "Claude", url.Values{"redirect_uri": {"http://127.0.0.1:41234/callback?x=1"}})
	if !strings.Contains(p, "127.0.0.1:41234") {
		t.Error("the redirect host:port is not on the consent page")
	}
	if !strings.Contains(p, "not verified") {
		t.Error("the page does not say the client name is unverified")
	}
	if it := consentPage("it", "Claude", url.Values{"redirect_uri": {"https://app.example.com/cb"}}); !strings.Contains(it, "app.example.com") ||
		!strings.Contains(it, "non verificato") {
		t.Error("Italian page lacks the host or the warning")
	}
	if strings.Contains(consentPage("en", "Claude", url.Values{"redirect_uri": {"https://x.example.com/<b>"}}), "<b>") {
		t.Error("redirect not escaped")
	}
}

func TestRedirectMismatchLogIsTruncatedAndRateLimited(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	i := testIdentity(t)
	now := time.Now()
	i.Auth.Now = func() time.Time { return now }
	if _, err := i.Auth.db.ExecContext(ctx, "insert into oauth_clients (id, name, redirects, created) values ('c1', 'x', '[\"https://app.example.com/cb\"]', ?)",
		now.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	ask := func(uri string) {
		if _, msg, err := i.Auth.CheckAuthorize(ctx, url.Values{"client_id": {"c1"}, "redirect_uri": {uri}}); err != nil || msg == "" {
			t.Fatalf("want a refusal, got %q, %v", msg, err)
		}
	}
	ask("https://evil.example.com/" + strings.Repeat("a", 5000))
	ask("https://evil.example.com/second")
	out := buf.String()
	if strings.Count(out, "redirect differs") != 1 {
		t.Errorf("want one log line in the interval, got:\n%.300s", out)
	}
	if len(out) > 1500 {
		t.Errorf("log line not truncated: %d bytes", len(out))
	}
	now = now.Add(time.Minute)
	ask("https://evil.example.com/third")
	if strings.Count(buf.String(), "redirect differs") != 2 {
		t.Error("the log line did not come back after the interval")
	}
}

func TestApprovalPageEscapesTheNameShowsWhereTheSignInGoesAndCarriesTheParameters(t *testing.T) {
	p := approvalPage("en", `<script>alert(1)</script>`, url.Values{"state": {`"><b>`}, "redirect_uri": {"https://app.example.com/cb"}}, "")
	if strings.Contains(p, "<script>alert") || strings.Contains(p, `"><b>`) {
		t.Error("a client name or a parameter reached the page unescaped")
	}
	for _, want := range []string{`data-view="approval"`, "app.example.com", "not verified", `name="redirect_uri"`, "started this connection yourself"} {
		if !strings.Contains(p, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(p, "/sign-in") {
		t.Error("a passkey link with no passkey")
	}
	if it := approvalPage("it", "Claude", nil, "/oauth/authorize?a=1"); !strings.Contains(it, "Collega Claude") ||
		!strings.Contains(it, `href="/sign-in?next=%2Foauth%2Fauthorize%3Fa%3D1"`) {
		t.Error("Italian page or the passkey link is wrong")
	}
}

func TestCleanLineTakesOutBidiOverridesAndLineSeparatorsToo(t *testing.T) {
	if got := cleanLine("ok\u202e\u2066x\u2028y\x1bz", 50); got != "ok  x y z" {
		t.Errorf("cleanLine = %q", got)
	}
}
