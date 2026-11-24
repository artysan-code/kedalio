package identity

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// MaxClients is the registered OAuth clients kept at most: a client is one connection of an MCP client, a few per
	// person.
	MaxClients = 1000
	// SessionIdle and SessionMax: the owner signs in from their own phone and computer, so a session lasts thirty days
	// from its last use and ninety at most.
	SessionIdle = 30 * 24 * time.Hour
	SessionMax  = 90 * 24 * time.Hour
	// SessionCookie is the cookie's name.
	SessionCookie = "kedalio_session"
	// Scope is the one scope a token is given: the owner's MCP connector.
	Scope = "kedalio"

	accessTTL  = time.Hour
	refreshTTL = 30 * 24 * time.Hour
	codeTTL    = time.Minute
)

// Auth is the sessions of the owner's browser and OAuth 2.1 for MCP clients: claude.ai, the Claude apps, Claude Code and
// ChatGPT expect it from a remote MCP server. The protected resource points at this same origin as its authorization
// server; clients register themselves (RFC 7591) with a redirect back to a known client or a loopback port; the code is
// bound to a PKCE S256 challenge; access tokens last an hour, refresh tokens rotate and a reused one revokes its whole
// family.
type Auth struct {
	db  *sql.DB
	url string
	// Now is the clock; time.Now when nil.
	Now func() time.Time

	logMu   sync.Mutex
	lastLog time.Time
}

// logSlot says whether a rate-limited log line may be written now: at most one a minute.
func (a *Auth) logSlot(now time.Time) bool {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	if !a.lastLog.IsZero() && now.Sub(a.lastLog) < time.Minute {
		return false
	}
	a.lastLog = now
	return true
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// NewAuth makes it for the server whose public origin is url (no trailing slash).
func NewAuth(d *sql.DB, url string) *Auth { return &Auth{db: d, url: url} }

func (a *Auth) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// ---------------------------------------------------------------- redirects

func isLoopbackHost(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

// isLoopback is an http address on this machine, the only kind of redirect allowed besides the known clients'.
func isLoopback(uri string) bool {
	u, err := url.Parse(uri)
	return err == nil && u.Scheme == "http" && isLoopbackHost(u.Hostname()) && u.User == nil
}

// knownCallbacks are the callbacks of the MCP clients Kedalio serves: Claude (claude.ai, the apps) and ChatGPT.
// Anything else registers only with a loopback redirect (Claude Code, the CLI).
var knownCallbacks = []struct {
	hosts []string
	path  string
}{
	{[]string{"claude.ai", "claude.com"}, "/api/mcp/auth_callback"},
	{[]string{"chatgpt.com"}, "/connector_platform_oauth_redirect"},
}

// redirectAllowed is where a registered client may be sent back: a known client's callback, or a loopback port.
func redirectAllowed(uri string) bool {
	u, err := url.Parse(uri)
	if err != nil {
		return false
	}
	if u.Scheme == "https" {
		for _, k := range knownCallbacks {
			if slices.Contains(k.hosts, u.Hostname()) && u.Path == k.path && u.Port() == "" {
				return true
			}
		}
	}
	return isLoopback(uri)
}

// redirectMatches is whether a redirect matches a registered one. On loopback the port is free (RFC 8252) and
// localhost, 127.0.0.1 and [::1] are one host: a client that registered with one and comes back with another is the
// same program on the same machine (Claude Code keeps a client id across versions that changed which one they send).
// The path stays exact.
func redirectMatches(registered []string, uri string) bool {
	return slices.ContainsFunc(registered, func(r string) bool {
		if r == uri {
			return true
		}
		x, err1 := url.Parse(r)
		y, err2 := url.Parse(uri)
		return err1 == nil && err2 == nil && x.Scheme == "http" && y.Scheme == "http" &&
			isLoopbackHost(x.Hostname()) && isLoopbackHost(y.Hostname()) && x.Path == y.Path
	})
}

// ---------------------------------------------------------------- metadata

// ResourceMetadataURL is where a client learns which authorization server guards /mcp.
func (a *Auth) ResourceMetadataURL() string { return a.url + "/.well-known/oauth-protected-resource" }

// ResourceMeta is the protected resource metadata (RFC 9728).
func (a *Auth) ResourceMeta() map[string]any {
	return map[string]any{
		"resource":                 a.url + "/mcp",
		"authorization_servers":    []string{a.url},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{Scope},
	}
}

// ServerMeta is the authorization server metadata (RFC 8414).
func (a *Auth) ServerMeta() map[string]any {
	u := a.url
	return map[string]any{
		"issuer":                                u,
		"authorization_endpoint":                u + "/oauth/authorize",
		"token_endpoint":                        u + "/oauth/token",
		"registration_endpoint":                 u + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{Scope},
	}
}

// ---------------------------------------------------------------- sessions

var cookieValueRe = regexp.MustCompile(`^[\w-]+$`)

// sessionToken is the session token a request's cookies carry, or "".
func sessionToken(r *http.Request) string {
	c, err := r.Cookie(SessionCookie)
	if err != nil || !cookieValueRe.MatchString(c.Value) {
		return ""
	}
	return c.Value
}

// Cookie is the Set-Cookie of a session (or of its end, with an empty token).
func Cookie(token string, https bool) *http.Cookie {
	age := int(SessionMax / time.Second)
	if token == "" {
		age = -1
	}
	return &http.Cookie{Name: SessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: age, Secure: https}
}

// NewSession makes a session for the owner: it lasts thirty days from its last use, ninety at most. It returns the
// cookie's token; only its hash is kept.
func (a *Auth) NewSession(ctx context.Context, agent string) (string, error) {
	token, id, now := randomToken(32), randomToken(9), a.now()
	if _, err := a.db.ExecContext(ctx, "delete from sessions where expires < ?", now.UnixMilli()); err != nil {
		return "", err
	}
	if r := []rune(agent); len(r) > 120 {
		agent = string(r[:120])
	}
	iso := now.UTC().Format(time.RFC3339)
	_, err := a.db.ExecContext(ctx,
		"insert into sessions (id, hash, agent, created, used, expires, ends) values (?, ?, ?, ?, ?, ?, ?)",
		id, hash(token), agent, iso, iso, now.Add(SessionIdle).UnixMilli(), now.Add(SessionMax).UnixMilli())
	return token, err
}

// Session is whether the request carries the owner's live session; each use moves the idle limit on.
func (a *Auth) Session(ctx context.Context, r *http.Request) bool {
	t := sessionToken(r)
	if t == "" {
		return false
	}
	h, now := hash(t), a.now()
	var expires, ends int64
	if a.db.QueryRowContext(ctx, "select expires, ends from sessions where hash = ?", h).Scan(&expires, &ends) != nil ||
		expires < now.UnixMilli() {
		return false
	}
	_, _ = a.db.ExecContext(ctx, "update sessions set expires = ?, used = ? where hash = ?",
		min(now.Add(SessionIdle).UnixMilli(), ends), now.UTC().Format(time.RFC3339), h)
	return true
}

// EndSession ends the session the request carries.
func (a *Auth) EndSession(ctx context.Context, r *http.Request) {
	if t := sessionToken(r); t != "" {
		_, _ = a.db.ExecContext(ctx, "delete from sessions where hash = ?", hash(t))
	}
}

// ---------------------------------------------------------------- OAuth

// pruneClients: anyone may register a client (RFC 7591: that is how an MCP client connects), so the ones nobody uses
// go: a client that never got a code or a token after an hour, and one with no live token after ninety days.
func (a *Auth) pruneClients(ctx context.Context, now time.Time) error {
	hour := now.Add(-time.Hour).UTC().Format(time.RFC3339)
	quarter := now.Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := a.db.ExecContext(ctx,
		`delete from oauth_clients where created < ? and not exists (select 1 from tokens t where t.client = oauth_clients.id)
		   and not exists (select 1 from oauth_codes c where c.client = oauth_clients.id)
		   and not exists (select 1 from approvals p where p.client = oauth_clients.id and p.expires > ?)`, hour, now.UnixMilli()); err != nil {
		return err
	}
	_, err := a.db.ExecContext(ctx,
		`delete from oauth_clients where created < ? and not exists (select 1 from tokens t where t.client = oauth_clients.id
		   and t.revoked = 0 and t.expires > ?)
		   and not exists (select 1 from approvals p where p.client = oauth_clients.id and p.expires > ?)`,
		quarter, now.UnixMilli(), now.UnixMilli())
	return err
}

// Purge forgets the rows nothing will read again: spent codes, expired sessions, challenges and approvals, unused clients. Tokens
// go ninety days after they expire, not at once: pruneClients counts them to tell a client in use from one nobody
// uses. It returns how many rows went.
func (a *Auth) Purge(ctx context.Context) (int64, error) {
	now := a.now()
	var gone int64
	count := func(res sql.Result, err error) error {
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		gone += n
		return nil
	}
	var before int
	if err := a.db.QueryRowContext(ctx, "select count(*) from oauth_clients").Scan(&before); err != nil {
		return 0, err
	}
	if err := a.pruneClients(ctx, now); err != nil {
		return 0, err
	}
	var after int
	if err := a.db.QueryRowContext(ctx, "select count(*) from oauth_clients").Scan(&after); err != nil {
		return 0, err
	}
	gone += int64(before - after)
	ms := now.UnixMilli()
	for _, q := range []struct {
		sql string
		arg int64
	}{
		{"delete from oauth_codes where expires < ?", ms},
		{"delete from tokens where expires < ?", now.Add(-90 * 24 * time.Hour).UnixMilli()},
		{"delete from sessions where expires < ?", ms},
		{"delete from challenges where expires < ?", ms},
		{"delete from approvals where expires < ?", ms},
	} {
		if err := count(a.db.ExecContext(ctx, q.sql, q.arg)); err != nil {
			return gone, err
		}
	}
	return gone, nil
}

// Reply is an HTTP status and the JSON body that goes with it.
type Reply struct {
	Status int
	JSON   map[string]any
}

// Register is dynamic client registration (RFC 7591) for a public client.
func (a *Auth) Register(ctx context.Context, body map[string]any) (Reply, error) {
	now := a.now()
	if err := a.pruneClients(ctx, now); err != nil {
		return Reply{}, err
	}
	var n int
	if err := a.db.QueryRowContext(ctx, "select count(*) from oauth_clients").Scan(&n); err != nil {
		return Reply{}, err
	}
	if n >= MaxClients {
		return Reply{503, map[string]any{"error": "temporarily_unavailable", "error_description": "too many registered clients"}}, nil
	}
	var uris []string
	if list, ok := body["redirect_uris"].([]any); ok {
		for _, v := range list {
			s, isStr := v.(string)
			if !isStr {
				uris = nil
				break
			}
			uris = append(uris, s)
		}
	}
	if len(uris) == 0 || slices.ContainsFunc(uris, func(u string) bool { return !redirectAllowed(u) }) {
		return Reply{400, map[string]any{"error": "invalid_redirect_uri",
			"error_description": "only a known client's callback or a loopback address"}}, nil
	}
	name := "MCP client"
	if s, ok := body["client_name"].(string); ok && s != "" {
		name = s
	}
	name = CleanName(name)
	id := randomToken(16)
	if _, err := a.db.ExecContext(ctx, "insert into oauth_clients (id, name, redirects, created) values (?, ?, ?, ?)",
		id, name, mustJSON(uris), now.UTC().Format(time.RFC3339)); err != nil {
		return Reply{}, err
	}
	return Reply{201, map[string]any{
		"client_id": id, "client_name": name, "redirect_uris": uris, "token_endpoint_auth_method": "none",
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
	}}, nil
}

// CleanName is a client's name made safe to show anywhere, a terminal included: control characters (an escape sequence
// can rewrite a terminal's screen) become spaces and it is at most 80 characters. The name is whatever the registering
// stranger sent, and the core's consent page and `clients` command both show it.
func CleanName(name string) string {
	r := []rune(strings.Map(func(c rune) rune {
		if unsafeRune(c) {
			return ' '
		}
		return c
	}, name))
	if len(r) > 80 {
		r = r[:80]
	}
	if s := strings.TrimSpace(string(r)); s != "" {
		return s
	}
	return "MCP client"
}

// unsafeRune is a character that must not reach a terminal or a page as it is: a control character, a format character
// (bidi overrides and isolates, zero-width marks, which can make text read as something else) or a line or paragraph
// separator.
func unsafeRune(c rune) bool {
	return unicode.IsControl(c) || unicode.In(c, unicode.Cf, unicode.Zl, unicode.Zp)
}

// Client is a registered OAuth client.
type Client struct {
	ID, Name  string
	Redirects []string
}

func (a *Auth) client(ctx context.Context, id string) (*Client, error) {
	var c Client
	var redirects string
	err := a.db.QueryRowContext(ctx, "select id, name, redirects from oauth_clients where id = ?", id).Scan(&c.ID, &c.Name, &redirects)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, fromJSON(redirects, &c.Redirects)
}

var challengeRe = regexp.MustCompile(`^[\w-]{43,128}$`)

// CheckAuthorize checks an authorization request. When it is not valid, message is what the page shows.
func (a *Auth) CheckAuthorize(ctx context.Context, p url.Values) (c *Client, message string, err error) {
	c, err = a.client(ctx, p.Get("client_id"))
	if err != nil {
		return nil, "", err
	}
	if c == nil {
		return nil, "unknown client", nil
	}
	if !redirectMatches(c.Redirects, p.Get("redirect_uri")) {
		// what the client sent and what it registered, for the instance's log: neither is a secret
		// the address is the caller's and the endpoint is open: one line a minute, cut short, or it is a free log filler
		if a.logSlot(a.now()) {
			slog.Warn("oauth: redirect differs from the registered one", "client", c.ID, "name", c.Name,
				"asked", truncate(p.Get("redirect_uri"), 200), "registered", c.Redirects)
		}
		return nil, "this client's sign-in is out of date: remove its sign-in in the client and sign in again", nil
	}
	if p.Get("response_type") != "code" {
		return nil, "response_type must be code", nil
	}
	if p.Get("code_challenge_method") != "S256" || !challengeRe.MatchString(p.Get("code_challenge")) {
		return nil, "PKCE S256 required", nil
	}
	if res := p.Get("resource"); res != "" && res != a.url+"/mcp" && res != a.url {
		return nil, "unknown resource", nil
	}
	// a client may ask for nothing in particular, or for the one scope there is; MCP clients also send their own
	// names (claudeai), which mean the same here
	return c, "", nil
}

// IssueCode is a code for the owner who agreed: the tokens it turns into are the owner's. It returns where to send
// the browser back.
func (a *Auth) IssueCode(ctx context.Context, p url.Values) (string, error) {
	code := randomToken(32)
	if _, err := a.db.ExecContext(ctx,
		"insert into oauth_codes (hash, client, redirect, challenge, resource, scope, expires) values (?, ?, ?, ?, ?, ?, ?)",
		hash(code), p.Get("client_id"), p.Get("redirect_uri"), p.Get("code_challenge"), p.Get("resource"), Scope,
		a.now().Add(codeTTL).UnixMilli()); err != nil {
		return "", err
	}
	u, err := url.Parse(p.Get("redirect_uri"))
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("code", code)
	if s := p.Get("state"); s != "" {
		q.Set("state", s)
	}
	q.Set("iss", a.url)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Denied is where the browser goes back when the owner said no.
func (a *Auth) Denied(p url.Values) (string, error) {
	u, err := url.Parse(p.Get("redirect_uri"))
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("error", "access_denied")
	if s := p.Get("state"); s != "" {
		q.Set("state", s)
	}
	q.Set("iss", a.url)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (a *Auth) pair(ctx context.Context, tx *sql.Tx, client, family string) (map[string]any, error) {
	access, refresh, now := randomToken(32), randomToken(32), a.now()
	iso := now.UTC().Format(time.RFC3339)
	for _, t := range []struct {
		token, kind string
		ttl         time.Duration
	}{{access, "access", accessTTL}, {refresh, "refresh", refreshTTL}} {
		if _, err := tx.ExecContext(ctx,
			"insert into tokens (hash, kind, client, family, expires, created) values (?, ?, ?, ?, ?, ?)",
			hash(t.token), t.kind, client, family, now.Add(t.ttl).UnixMilli(), iso); err != nil {
			return nil, err
		}
	}
	return map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(accessTTL / time.Second),
		"refresh_token": refresh, "scope": Scope}, nil
}

func bad(error string, description ...string) Reply {
	r := Reply{400, map[string]any{"error": error}}
	if len(description) > 0 {
		r.JSON["error_description"] = description[0]
	}
	return r
}

// Token is the token endpoint: an authorization code with its PKCE verifier, or a refresh token, for a pair of
// tokens. Both are spent by the use; a refresh token used twice means someone else holds a copy, so its whole family
// goes.
func (a *Auth) Token(ctx context.Context, f url.Values) (Reply, error) {
	switch f.Get("grant_type") {
	case "authorization_code":
		return a.exchangeCode(ctx, f)
	case "refresh_token":
		return a.refresh(ctx, f)
	}
	return bad("unsupported_grant_type"), nil
}

func (a *Auth) exchangeCode(ctx context.Context, f url.Values) (Reply, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return Reply{}, err
	}
	defer tx.Rollback()
	var client, redirect, challenge string
	var expires int64
	// one use only: a wrong verifier spends the code too
	err = tx.QueryRowContext(ctx, "delete from oauth_codes where hash = ? returning client, redirect, challenge, expires",
		hash(f.Get("code"))).Scan(&client, &redirect, &challenge, &expires)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && expires < a.now().UnixMilli()) {
		_ = tx.Commit()
		return bad("invalid_grant", "code expired or used"), nil
	}
	if err != nil {
		return Reply{}, err
	}
	if client != f.Get("client_id") || redirect != f.Get("redirect_uri") {
		return commitWith(tx, bad("invalid_grant", "client or redirect differ"))
	}
	if !same(hash(f.Get("code_verifier")), challenge) {
		return commitWith(tx, bad("invalid_grant", "PKCE verifier does not match"))
	}
	pair, err := a.pair(ctx, tx, client, randomToken(12))
	if err != nil {
		return Reply{}, err
	}
	return commitWith(tx, Reply{200, pair})
}

func commitWith(tx *sql.Tx, r Reply) (Reply, error) { return r, tx.Commit() }

func (a *Auth) refresh(ctx context.Context, f url.Values) (Reply, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return Reply{}, err
	}
	defer tx.Rollback()
	h := hash(f.Get("refresh_token"))
	var client, family string
	var expires int64
	var revoked int
	err = tx.QueryRowContext(ctx, "select client, family, expires, revoked from tokens where hash = ? and kind = 'refresh'", h).
		Scan(&client, &family, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && expires < a.now().UnixMilli()) {
		return bad("invalid_grant"), nil
	}
	if err != nil {
		return Reply{}, err
	}
	if revoked != 0 {
		if _, err := tx.ExecContext(ctx, "update tokens set revoked = 1 where family = ?", family); err != nil {
			return Reply{}, err
		}
		return commitWith(tx, bad("invalid_grant", "token reused"))
	}
	if id := f.Get("client_id"); id != "" && id != client {
		return bad("invalid_grant"), nil
	}
	if _, err := tx.ExecContext(ctx, "update tokens set revoked = 1 where hash = ?", h); err != nil {
		return Reply{}, err
	}
	pair, err := a.pair(ctx, tx, client, family)
	if err != nil {
		return Reply{}, err
	}
	return commitWith(tx, Reply{200, pair})
}

// Bearer is whether a bearer token is a live OAuth access token.
func (a *Auth) Bearer(ctx context.Context, token string) bool {
	if token == "" {
		return false
	}
	h, now := hash(token), a.now()
	var kind string
	var expires int64
	var revoked int
	if a.db.QueryRowContext(ctx, "select kind, expires, revoked from tokens where hash = ?", h).Scan(&kind, &expires, &revoked) != nil ||
		revoked != 0 || kind != "access" || expires < now.UnixMilli() {
		return false
	}
	// «last used» is a courtesy to the owner: written at most every five minutes, not on every request
	_, _ = a.db.ExecContext(ctx, "update tokens set used = ? where hash = ? and (used is null or used < ?)",
		now.UTC().Format(time.RFC3339), h, now.Add(-5*time.Minute).UTC().Format(time.RFC3339))
	return true
}

// ClientOf is the registered name of the client behind an access token, "client" when it has none, and whether the
// client is remote: registered with a redirect off this machine (claude.ai, ChatGPT), so it runs where no local hook
// guards what it writes. The name is the client's own choice; the redirect is not, for a code only ever goes back to
// a registered one.
func (a *Auth) ClientOf(ctx context.Context, token string) (name string, remote bool) {
	if token == "" {
		return "", false
	}
	var n, redirects sql.NullString
	if a.db.QueryRowContext(ctx, "select c.name, c.redirects from tokens t left join oauth_clients c on c.id = t.client where t.hash = ? and t.kind = 'access'",
		hash(token)).Scan(&n, &redirects) != nil {
		return "", false
	}
	var list []string
	if fromJSON(redirects.String, &list) != nil {
		list = nil
	}
	name = n.String
	if name == "" {
		name = "client"
	}
	return name, isRemote(list)
}

// isRemote is whether a client with these registered redirects runs off this machine: any of them is not loopback.
// A client with none (or with a list that does not read) proves nothing, and counts as remote.
func isRemote(redirects []string) bool {
	return len(redirects) == 0 || slices.ContainsFunc(redirects, func(r string) bool { return !isLoopback(r) })
}
