package identity

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kedalio/core/mcp"
	"kedalio/core/memory"
	"kedalio/core/memory/search"
	"kedalio/core/tasks"
)

const staticToken = "static-token-not-a-secret"

// rig is the whole core behind an httptest server: the MCP connector with the identity in front of it.
type rig struct {
	srv   *httptest.Server
	idn   *Identity
	owner *http.Client // a browser: it keeps the session cookie and does not follow redirects
	phone *Authenticator
	store *memory.Store
}

func newRig(t *testing.T) *rig { return newRigOn(t, testDB(t)) }

// newRigOn is the rig on the given database, for a test that opens the same file a second time (the CLI's side).
func newRigOn(t *testing.T, d *sql.DB) *rig {
	t.Helper()
	var handler http.Handler
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	srv.Start()
	t.Cleanup(srv.Close)

	if err := memory.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	ix, err := search.New(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := tasks.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	store := memory.New(d, ix)
	store.Areas = memory.AreasEN
	idn, err := New(ctx, d, Options{PublicURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mcp.New(mcp.Config{Memory: store, Search: ix, Tasks: tasks.NewBook(store, d, "owner", time.UTC, "owner"),
		Token: staticToken, Zone: time.UTC, Owner: "Alice", Language: "English",
		Verify: idn.Verify, Client: idn.Client, ResourceMetadata: idn.ResourceMetadataURL()})
	if err != nil {
		t.Fatal(err)
	}
	handler = idn.Wrap(m.Handler())

	jar, _ := cookiejar.New(nil)
	phone := newAuthenticator()
	phone.site, _ = SiteOf(srv.URL)
	return &rig{srv, idn, &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, phone, store}
}

// do sends a request as a page of the server itself would (same origin) unless the test says otherwise.
func (r *rig) do(t *testing.T, c *http.Client, method, path string, body io.Reader, headers ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, r.srv.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func (r *rig) postJSON(t *testing.T, c *http.Client, path string, v any, headers ...string) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(v)
	res := r.do(t, c, "POST", path, strings.NewReader(string(b)), append([]string{"Content-Type", "application/json"}, headers...)...)
	return res, readJSONBody(t, res)
}

func readJSONBody(t *testing.T, res *http.Response) map[string]any {
	t.Helper()
	b, _ := io.ReadAll(res.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func (r *rig) postForm(t *testing.T, c *http.Client, path string, f url.Values, headers ...string) *http.Response {
	t.Helper()
	return r.do(t, c, "POST", path, strings.NewReader(f.Encode()), append([]string{"Content-Type", "application/x-www-form-urlencoded"}, headers...)...)
}

// setupPasskey makes a passkey and signs in with it.
func (r *rig) setupPasskey(t *testing.T) {
	t.Helper()
	r.addPasskey(t, r.phone)
	r.signIn(t)
}

// addPasskey makes a passkey for the authenticator through an approval, as the setup page's script does, with the
// approval given the way `kedalio-core approve` gives it. The browser gets no session from it.
func (r *rig) addPasskey(t *testing.T, phone *Authenticator) {
	t.Helper()
	res, started := r.postJSON(t, r.owner, "/api/auth/approval/start", map[string]any{"purpose": "passkey"})
	code, _ := started["code"].(string)
	waiter, _ := started["waiter"].(string)
	if res.StatusCode != 200 || code == "" || started["local"] != false {
		t.Fatalf("passkey start: %d %v", res.StatusCode, started)
	}
	if _, got := r.postJSON(t, r.owner, "/api/auth/approval/poll", map[string]any{"waiter": waiter}); got["state"] != "pending" || got["options"] != nil {
		t.Fatalf("passkey poll before approval: %v", got)
	}
	if _, err := r.idn.Auth.Decide(ctx, code, true, "cli"); err != nil {
		t.Fatal(err)
	}
	_, got := r.postJSON(t, r.owner, "/api/auth/approval/poll", map[string]any{"waiter": waiter})
	opts, _ := got["options"].(map[string]any)
	if got["state"] != "approved" || opts == nil || got["redirect"] != nil {
		t.Fatalf("passkey poll after approval: %v", got)
	}
	res, body := r.postJSON(t, r.owner, "/api/auth/approval/passkey", map[string]any{"waiter": waiter, "label": "test phone",
		"registration": phone.register(opts["challenge"].(string), regOver{})})
	if res.StatusCode != 200 {
		t.Fatalf("passkey accept: %d %v", res.StatusCode, body)
	}
}

// signIn signs the owner in with the passkey, as the sign-in page's script does.
func (r *rig) signIn(t *testing.T) {
	t.Helper()
	_, opts := r.postJSON(t, r.owner, "/api/auth/passkey/options", nil)
	ch, _ := unb64u(opts["challenge"].(string))
	res, got := r.postJSON(t, r.owner, "/api/auth/passkey", map[string]any{"assertion": r.phone.assert(ch, assertOver{})})
	if res.StatusCode != 200 {
		t.Fatalf("sign in: %d %v", res.StatusCode, got)
	}
}

func readBodyOf(res *http.Response) []byte {
	b, _ := io.ReadAll(res.Body)
	return b
}

func pkce(t *testing.T, verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return b64.EncodeToString(h[:])
}

func (r *rig) mcpClient(t *testing.T, token string) (*sdk.ClientSession, error) {
	c := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	return c.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: r.srv.URL + "/mcp", DisableStandaloneSSE: true,
		HTTPClient: &http.Client{Transport: bearerTransport(token)}}, nil)
}

type bearerTransport string

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

func TestAnMCPClientConnectsThroughOAuthWithAPasskey(t *testing.T) {
	r := newRig(t)
	anon := &http.Client{CheckRedirect: r.owner.CheckRedirect}

	// 1. a 401 from /mcp says where the authorization server is described
	res := r.do(t, anon, "POST", "/mcp", strings.NewReader("{}"))
	want := `Bearer realm="kedalio", resource_metadata="` + r.srv.URL + `/.well-known/oauth-protected-resource"`
	if res.StatusCode != 401 || res.Header.Get("WWW-Authenticate") != want {
		t.Fatalf("/mcp: %d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}

	// 2. discovery
	res = r.do(t, anon, "GET", "/.well-known/oauth-protected-resource", nil)
	meta := readJSONBody(t, res)
	if meta["resource"] != r.srv.URL+"/mcp" || meta["authorization_servers"].([]any)[0] != r.srv.URL {
		t.Fatalf("resource metadata = %v", meta)
	}
	srvMeta := readJSONBody(t, r.do(t, anon, "GET", "/.well-known/oauth-authorization-server", nil))
	if srvMeta["token_endpoint"] != r.srv.URL+"/oauth/token" || srvMeta["registration_endpoint"] != r.srv.URL+"/oauth/register" ||
		srvMeta["code_challenge_methods_supported"].([]any)[0] != "S256" {
		t.Fatalf("server metadata = %v", srvMeta)
	}

	// 3. dynamic client registration: a stranger's redirect is refused, a loopback one is not
	res, got := r.postJSON(t, anon, "/oauth/register", map[string]any{"redirect_uris": []string{"https://evil.example/cb"}}, "Sec-Fetch-Site", "cross-site")
	if res.StatusCode != 400 || got["error"] != "invalid_redirect_uri" {
		t.Fatalf("evil redirect: %d %v", res.StatusCode, got)
	}
	redirect := "http://127.0.0.1:43210/callback"
	res, client := r.postJSON(t, anon, "/oauth/register", map[string]any{"redirect_uris": []string{redirect}, "client_name": "Claude Code"}, "Sec-Fetch-Site", "cross-site")
	clientID, _ := client["client_id"].(string)
	if res.StatusCode != 201 || clientID == "" {
		t.Fatalf("register: %d %v", res.StatusCode, client)
	}

	// 4. the authorization request with no one signed in shows the approval page, and makes no code by itself
	verifier := randomToken(32)
	q := url.Values{"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "state": {"xyz"},
		"code_challenge": {pkce(t, verifier)}, "code_challenge_method": {"S256"}, "resource": {r.srv.URL + "/mcp"}}
	res = r.do(t, r.owner, "GET", "/oauth/authorize?"+q.Encode(), nil)
	page, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(page), `data-view="approval"`) {
		t.Fatalf("authorize signed out: %d", res.StatusCode)
	}

	// 5. the first passkey, then sign out and in again with it
	r.setupPasskey(t)
	r.postJSON(t, r.owner, "/api/auth/sign-out", nil)
	if res := r.do(t, r.owner, "GET", "/oauth/authorize?"+q.Encode(), nil); !strings.Contains(string(readBodyOf(res)), `data-view="approval"`) {
		t.Fatalf("signed out again, authorize = %d", res.StatusCode)
	}
	r.signIn(t)

	// 6. the consent page, then the owner agrees; a page from another origin cannot agree for them
	res = r.do(t, r.owner, "GET", "/oauth/authorize?"+q.Encode(), nil)
	page, _ = io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(page), "Claude Code") {
		t.Fatalf("consent: %d", res.StatusCode)
	}
	agree := url.Values{"decision": {"allow"}}
	for k, v := range q {
		agree[k] = v
	}
	if res := r.postForm(t, r.owner, "/oauth/authorize", agree, "Sec-Fetch-Site", "cross-site"); res.StatusCode != 403 {
		t.Fatalf("cross-site consent = %d", res.StatusCode)
	}
	codeOf := func() string {
		res := r.postForm(t, r.owner, "/oauth/authorize", agree)
		back, _ := url.Parse(res.Header.Get("Location"))
		if res.StatusCode != 302 || back.Query().Get("state") != "xyz" || back.Query().Get("iss") != r.srv.URL ||
			back.Scheme+"://"+back.Host+back.Path != redirect {
			t.Fatalf("agree: %d %v", res.StatusCode, back)
		}
		return back.Query().Get("code")
	}
	deny := url.Values{"decision": {"deny"}}
	for k, v := range q {
		deny[k] = v
	}
	if back, _ := url.Parse(r.postForm(t, r.owner, "/oauth/authorize", deny).Header.Get("Location")); back.Query().Get("error") != "access_denied" {
		t.Error("a refusal does not say access_denied")
	}

	// 7. the token endpoint: a wrong verifier is refused and spends the code, the right one gets tokens
	tokenReq := func(f url.Values) (int, map[string]any) {
		res := r.postForm(t, anon, "/oauth/token", f, "Sec-Fetch-Site", "cross-site")
		return res.StatusCode, readJSONBody(t, res)
	}
	exchange := func(code, v string) url.Values {
		return url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {redirect}, "code_verifier": {v}}
	}
	spent := codeOf()
	if status, _ := tokenReq(exchange(spent, strings.Repeat("x", 43))); status != 400 {
		t.Fatalf("a wrong verifier = %d", status)
	}
	if status, _ := tokenReq(exchange(spent, verifier)); status != 400 {
		t.Fatal("a code whose verifier was wrong still works")
	}
	status, tok := tokenReq(exchange(codeOf(), verifier))
	access, refresh := tok["access_token"].(string), tok["refresh_token"].(string)
	if status != 200 || access == "" || refresh == "" || tok["token_type"] != "Bearer" || tok["expires_in"] != float64(3600) || tok["scope"] != "kedalio" {
		t.Fatalf("token: %d %v", status, tok)
	}
	if status, _ := tokenReq(url.Values{"grant_type": {"authorization_code"}}); status != 400 {
		t.Error("an empty exchange was accepted")
	}

	// 8. /mcp takes the access token; the static token and a made-up one behave as before
	s, err := r.mcpClient(t, access)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tools, err := s.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) == 0 {
		t.Fatalf("tools/list with the access token: %v %v", tools, err)
	}
	// a write through the access token is recorded under the client's name
	if _, err := s.CallTool(ctx, &sdk.CallToolParams{Name: "memory_write", Arguments: map[string]any{"op": "write", "path": "notes/by-client.md", "body": "# By client\n\nWritten through OAuth.\n"}}); err != nil {
		t.Fatal(err)
	}
	if h, err := r.store.History(ctx, "notes/by-client.md"); err != nil || len(h) != 1 || h[0].By != "oauth:Claude Code" {
		t.Fatalf("recorded by: %v %v", h, err)
	}
	if st, err := r.mcpClient(t, staticToken); err != nil {
		t.Errorf("the static token no longer works: %v", err)
	} else {
		st.Close()
	}
	if _, err := r.mcpClient(t, "made-up"); err == nil {
		t.Error("a made-up token connected")
	}

	// 9. refresh rotation: the old refresh token is refused, and a reuse revokes the family
	refreshReq := func(rt string) (int, map[string]any) {
		return tokenReq(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {clientID}})
	}
	status, next := refreshReq(refresh)
	if status != 200 || next["access_token"] == access || next["refresh_token"] == refresh {
		t.Fatalf("refresh: %d %v", status, next)
	}
	if status, _ := refreshReq(refresh); status != 400 {
		t.Fatal("the old refresh token was accepted")
	}
	if r.idn.Verify(ctx, next["access_token"].(string)) {
		t.Error("a reused refresh token left its family alive")
	}
	if status, _ := refreshReq(next["refresh_token"].(string)); status != 400 {
		t.Error("the family's newer refresh token survived the reuse")
	}

	// tokens are kept as hashes only
	var n int
	_ = r.idn.Auth.db.QueryRowContext(ctx, "select count(*) from tokens where hash in (?, ?)", access, refresh).Scan(&n)
	if n != 0 {
		t.Error("a token is stored as itself")
	}
}

func TestSigningInNeedsAKnownPasskeyAndAFreshChallenge(t *testing.T) {
	r := newRig(t)
	r.setupPasskey(t)
	_, opts := r.postJSON(t, r.owner, "/api/auth/passkey/options", nil)
	ch, _ := unb64u(opts["challenge"].(string))
	stranger := newAuthenticator()
	stranger.site = r.phone.site
	for name, a := range map[string]Assertion{
		"unknown passkey": stranger.assert(ch, assertOver{}),
		"other challenge": r.phone.assert([]byte("not the one asked for"), assertOver{}),
	} {
		if res, _ := r.postJSON(t, r.owner, "/api/auth/passkey", map[string]any{"assertion": a}); res.StatusCode != 401 {
			t.Errorf("%s: %d", name, res.StatusCode)
		}
	}
	// the challenge was spent by the failed tries: even the owner's own signature over it is now refused
	if res, _ := r.postJSON(t, r.owner, "/api/auth/passkey", map[string]any{"assertion": r.phone.assert(ch, assertOver{})}); res.StatusCode != 401 {
		t.Errorf("a spent challenge signed in: %d", res.StatusCode)
	}
	// and a session is only made by a good assertion
	if res := r.do(t, r.owner, "GET", "/oauth/authorize?client_id=x", nil); res.StatusCode != 400 {
		t.Errorf("unknown client = %d", res.StatusCode)
	}
}

func TestOAuthRoutesAreOffWithoutAPublicURLAndTheStaticTokenStillWorks(t *testing.T) {
	d := testDB(t)
	for _, f := range []func() error{func() error { return memory.Migrate(ctx, d) }, func() error { return tasks.Migrate(ctx, d) }} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	ix, _ := search.New(ctx, d)
	store := memory.New(d, ix)
	m, err := mcp.New(mcp.Config{Memory: store, Search: ix, Tasks: tasks.NewBook(store, d, "owner", time.UTC, "owner"),
		Token: staticToken})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	res, _ := http.Get(srv.URL + "/.well-known/oauth-authorization-server")
	if res.StatusCode != 404 {
		t.Errorf("discovery without OAuth = %d", res.StatusCode)
	}
	res, _ = http.Post(srv.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if res.StatusCode != 401 || strings.Contains(res.Header.Get("WWW-Authenticate"), "resource_metadata") {
		t.Errorf("/mcp = %d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}
}
