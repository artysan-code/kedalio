package identity

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kedalio/core/db"
)

// approvalRig is the rig on a database file, and a second Auth on the same file: the side of `kedalio-core approve`,
// another process.
type approvalRig struct {
	*rig
	cli      *Auth
	redirect string
	clientID string
	q        url.Values
	verifier string
}

func newApprovalRig(t *testing.T) *approvalRig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kedalio.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	r := newRigOn(t, d)
	d2, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d2.Close() })
	a := &approvalRig{rig: r, cli: NewAuth(d2, r.srv.URL), redirect: "http://127.0.0.1:43210/callback", verifier: randomToken(32)}
	anon := &http.Client{CheckRedirect: r.owner.CheckRedirect}
	_, c := r.postJSON(t, anon, "/oauth/register", map[string]any{"redirect_uris": []string{a.redirect}, "client_name": "Claude Code"},
		"Sec-Fetch-Site", "cross-site")
	a.clientID, _ = c["client_id"].(string)
	a.q = url.Values{"client_id": {a.clientID}, "redirect_uri": {a.redirect}, "response_type": {"code"}, "state": {"xyz"},
		"code_challenge": {pkce(t, a.verifier)}, "code_challenge_method": {"S256"}, "resource": {r.srv.URL + "/mcp"}}
	return a
}

func (a *approvalRig) params() map[string]string {
	m := map[string]string{}
	for k := range a.q {
		m[k] = a.q.Get(k)
	}
	return m
}

func (a *approvalRig) start(t *testing.T) (status int, body map[string]any) {
	t.Helper()
	res, body := a.postJSON(t, a.owner, "/api/auth/approval/start", map[string]any{"params": a.params()})
	return res.StatusCode, body
}

func (a *approvalRig) poll(t *testing.T, waiter string) map[string]any {
	t.Helper()
	res, body := a.postJSON(t, a.owner, "/api/auth/approval/poll", map[string]any{"waiter": waiter})
	if res.StatusCode != 200 {
		t.Fatalf("poll: %d %v", res.StatusCode, body)
	}
	return body
}

func (a *approvalRig) count(t *testing.T, table string) (n int) {
	t.Helper()
	if err := a.idn.Auth.db.QueryRow("select count(*) from " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestANewClientConnectsWithAnApprovedCodeAndNoPasskeyAndTheBrowserGetsNoSession(t *testing.T) {
	a := newApprovalRig(t)

	// the page is side-effect free: a link scanner opening it makes no code
	res := a.do(t, a.owner, "GET", "/oauth/authorize?"+a.q.Encode(), nil)
	page := string(readBodyOf(res))
	if res.StatusCode != 200 || !strings.Contains(page, `data-view="approval"`) || !strings.Contains(page, "Connect Claude Code") ||
		!strings.Contains(page, `name="client_id"`) {
		t.Fatalf("approval page: %d %s", res.StatusCode, page)
	}
	if a.count(t, "approvals") != 0 {
		t.Fatal("opening the page made a code")
	}
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Error("the approval page has no CSP")
	}

	// a page of another origin cannot start one
	b, _ := jsonBody(map[string]any{"params": a.params()})
	if res := a.do(t, a.owner, "POST", "/api/auth/approval/start", strings.NewReader(b), "Content-Type", "application/json",
		"Sec-Fetch-Site", "cross-site"); res.StatusCode != 403 {
		t.Fatalf("cross-site start = %d", res.StatusCode)
	}

	status, started := a.start(t)
	code, _ := started["code"].(string)
	waiter, _ := started["waiter"].(string)
	if status != 200 || code == "" || waiter == "" || started["local"] != true || started["expires_in"] != float64(600) {
		t.Fatalf("start: %d %v", status, started)
	}
	if got := a.poll(t, waiter); got["state"] != "pending" || got["redirect"] != nil {
		t.Fatalf("poll before: %v", got)
	}

	// the other process approves
	if _, err := a.cli.Decide(ctx, code, true, "cli"); err != nil {
		t.Fatal(err)
	}
	got := a.poll(t, waiter)
	to, _ := url.Parse(got["redirect"].(string))
	if got["state"] != "approved" || to == nil || to.Query().Get("state") != "xyz" || to.Query().Get("iss") != a.srv.URL ||
		to.Scheme+"://"+to.Host+to.Path != a.redirect {
		t.Fatalf("poll after: %v", got)
	}
	if again := a.poll(t, waiter); again["state"] != "expired" {
		t.Errorf("a second poll: %v", again)
	}

	// the code is good for the token endpoint, and the token for /mcp
	anon := &http.Client{CheckRedirect: a.owner.CheckRedirect}
	res = a.postForm(t, anon, "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {to.Query().Get("code")},
		"client_id": {a.clientID}, "redirect_uri": {a.redirect}, "code_verifier": {a.verifier}}, "Sec-Fetch-Site", "cross-site")
	tok := readJSONBody(t, res)
	access, _ := tok["access_token"].(string)
	if res.StatusCode != 200 || access == "" {
		t.Fatalf("token: %d %v", res.StatusCode, tok)
	}
	s, err := a.mcpClient(t, access)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if tools, err := s.ListTools(ctx, &sdk.ListToolsParams{}); err != nil || len(tools.Tools) == 0 {
		t.Fatalf("tools: %v %v", tools, err)
	}

	// approving one request gave the browser no session: the next one is the approval page again
	if page := string(readBodyOf(a.do(t, a.owner, "GET", "/oauth/authorize?"+a.q.Encode(), nil))); !strings.Contains(page, `data-view="approval"`) {
		t.Error("an approval gave the browser a session")
	}
}

func TestADeniedAndACancelledApprovalSendTheBrowserBackWithAccessDenied(t *testing.T) {
	a := newApprovalRig(t)
	_, s1 := a.start(t)
	if _, err := a.cli.Decide(ctx, s1["code"].(string), false, "cli"); err != nil {
		t.Fatal(err)
	}
	_, s2 := a.start(t)
	res, _ := a.postJSON(t, a.owner, "/api/auth/approval/cancel", map[string]any{"waiter": s2["waiter"]})
	if res.StatusCode != 200 {
		t.Fatalf("cancel: %d", res.StatusCode)
	}
	for _, s := range []map[string]any{s1, s2} {
		got := a.poll(t, s["waiter"].(string))
		to, _ := url.Parse(got["redirect"].(string))
		if got["state"] != "denied" || to.Query().Get("error") != "access_denied" || to.Query().Get("code") != "" {
			t.Errorf("poll: %v", got)
		}
	}
	// a cancelled code cannot be approved afterwards
	if _, err := a.cli.Decide(ctx, s2["code"].(string), true, "cli"); err == nil {
		t.Error("a cancelled code was approved")
	}
	if a.count(t, "oauth_codes") != 0 {
		t.Error("a denial left an OAuth code")
	}
}

func TestAnExpiredApprovalIsExpiredForThePageAndTheApprover(t *testing.T) {
	a := newApprovalRig(t)
	_, s := a.start(t)
	later := time.Now().Add(ApprovalTTL + time.Minute)
	a.idn.Auth.Now = func() time.Time { return later }
	a.cli.Now = a.idn.Auth.Now
	if got := a.poll(t, s["waiter"].(string)); got["state"] != "expired" {
		t.Errorf("poll: %v", got)
	}
	if _, err := a.cli.Decide(ctx, s["code"].(string), true, "cli"); err == nil {
		t.Error("an expired code was approved")
	}
}

func TestStartRefusesAnInvalidRequestFloodsAndTooManyWaitingApprovals(t *testing.T) {
	a := newApprovalRig(t)
	a.q.Set("client_id", "nobody")
	if status, body := a.start(t); status != 400 || body["error"] != "unknown client" {
		t.Errorf("unknown client: %d %v", status, body)
	}
	a.q.Set("client_id", a.clientID)
	a.idn.forms = NewBuckets(100, time.Hour)
	a.idn.opt.ClientIPHeader = "X-Forwarded-For"
	from := func(ip string) (int, map[string]any) {
		res, body := a.postJSON(t, a.owner, "/api/auth/approval/start", map[string]any{"params": a.params()}, "X-Forwarded-For", ip)
		return res.StatusCode, body
	}
	for n := 0; n < MaxPendingPerIP; n++ {
		if status, body := from("203.0.113.1"); status != 200 {
			t.Fatalf("start %d: %d %v", n, status, body)
		}
	}
	if status, body := from("203.0.113.1"); status != 429 || !strings.Contains(body["error"].(string), "from your address") {
		t.Errorf("a fourth from one address: %d %v", status, body)
	}
	for n := MaxPendingPerIP; n < MaxPendingApprovals; n++ {
		if status, body := from("198.51.100." + strconv.Itoa(n)); status != 200 {
			t.Fatalf("start %d: %d %v", n, status, body)
		}
	}
	if status, body := from("192.0.2.1"); status != 503 || !strings.Contains(body["error"].(string), "too many waiting approvals") {
		t.Errorf("over the cap: %d %v", status, body)
	}
	// the per-address bucket answers before the cap does
	a.idn.forms = NewBuckets(3, time.Hour)
	var last int
	for n := 0; n < 5; n++ {
		last, _ = from("192.0.2.2")
	}
	if last != 429 {
		t.Errorf("a flood of starts ends with %d, not 429", last)
	}
}

func TestThePollHasItsOwnBucket(t *testing.T) {
	a := newApprovalRig(t)
	a.idn.poll = NewBuckets(2, time.Hour)
	var last int
	for n := 0; n < 4; n++ {
		res, _ := a.postJSON(t, a.owner, "/api/auth/approval/poll", map[string]any{"waiter": "x"})
		last = res.StatusCode
	}
	if last != 429 {
		t.Errorf("poll flood ends with %d", last)
	}
}

func TestAPasskeySessionKeepsTheOneClickConsent(t *testing.T) {
	a := newApprovalRig(t)
	a.setupPasskey(t)
	page := string(readBodyOf(a.do(t, a.owner, "GET", "/oauth/authorize?"+a.q.Encode(), nil)))
	if !strings.Contains(page, `data-view="consent"`) {
		t.Fatalf("a signed-in owner did not get the consent page: %s", page)
	}
	// signed out, with a passkey, the approval page offers the passkey too
	a.postJSON(t, a.owner, "/api/auth/sign-out", nil)
	page = string(readBodyOf(a.do(t, a.owner, "GET", "/oauth/authorize?"+a.q.Encode(), nil)))
	if !strings.Contains(page, `data-view="approval"`) || !strings.Contains(page, `href="/sign-in?next=`) {
		t.Errorf("the approval page does not offer the passkey: %s", page)
	}
}

func TestTheApprovalPageOffersNoPasskeyLinkWhenThereIsNone(t *testing.T) {
	a := newApprovalRig(t)
	if page := string(readBodyOf(a.do(t, a.owner, "GET", "/oauth/authorize?"+a.q.Encode(), nil))); strings.Contains(page, "/sign-in?next=") {
		t.Error("a passkey link with no passkey")
	}
}

func TestASecondPasskeyIsMadeWithAnotherApprovalAndGivesNoSession(t *testing.T) {
	r := newRig(t)
	r.addPasskey(t, r.phone)
	// a passkey is no session: the browser still has to sign in with it
	anon := &http.Client{CheckRedirect: r.owner.CheckRedirect}
	var sessions int
	if err := r.idn.Auth.db.QueryRow("select count(*) from sessions").Scan(&sessions); err != nil || sessions != 0 {
		t.Errorf("making a passkey made %d sessions (%v)", sessions, err)
	}
	second := newAuthenticator()
	second.site = r.phone.site
	r.addPasskey(t, second)
	if n, _ := r.idn.Passkeys.Count(ctx); n != 2 {
		t.Errorf("%d passkeys", n)
	}
	// the second one signs in
	_, opts := r.postJSON(t, anon, "/api/auth/passkey/options", nil)
	ch, _ := unb64u(opts["challenge"].(string))
	if res, got := r.postJSON(t, anon, "/api/auth/passkey", map[string]any{"assertion": second.assert(ch, assertOver{})}); res.StatusCode != 200 {
		t.Fatalf("sign in with the second passkey: %d %v", res.StatusCode, got)
	}
}

func TestStartTakesAPasskeyPurposeOnlyFromTheOwnPagesAndNothingElse(t *testing.T) {
	r := newRig(t)
	if res, _ := r.postJSON(t, r.owner, "/api/auth/approval/start", map[string]any{"purpose": "admin"}); res.StatusCode != 400 {
		t.Errorf("unknown purpose = %d", res.StatusCode)
	}
	if res, _ := r.postJSON(t, r.owner, "/api/auth/approval/start", map[string]any{"purpose": "passkey"}, "Sec-Fetch-Site", "cross-site"); res.StatusCode != 403 {
		t.Errorf("cross-site passkey start = %d", res.StatusCode)
	}
	// the passkey step is not reachable without an approval
	if res, _ := r.postJSON(t, r.owner, "/api/auth/approval/passkey", map[string]any{"waiter": "x"}); res.StatusCode != 400 {
		t.Errorf("passkey with no approval = %d", res.StatusCode)
	}
	// the setup routes of the link are gone
	for _, p := range []string{"/api/setup/options", "/api/setup/accept"} {
		if res, _ := r.postJSON(t, r.owner, p, map[string]any{"token": "x"}); res.StatusCode == 200 {
			t.Errorf("%s still answers", p)
		}
	}
}
