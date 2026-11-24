package identity

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Wrap serves the identity's routes in front of next: discovery (/.well-known), dynamic client registration and the
// token endpoint for MCP clients, the authorization page, the sign-in and the first passkey's setup. Everything else
// goes to next, with the headers every answer carries.
func (i *Identity) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		if i.https {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if !i.serve(w, r) {
			next.ServeHTTP(w, r)
		}
	})
}

func (i *Identity) serve(w http.ResponseWriter, r *http.Request) bool {
	p, m := r.URL.Path, r.Method
	// the MCP clients' own endpoints answer any origin: they carry a bearer token or nothing, never the cookie
	clientFacing := strings.HasPrefix(p, "/.well-known/") || p == "/oauth/token" || p == "/oauth/register"
	if clientFacing {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		if m == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "authorization, content-type, mcp-protocol-version")
			w.WriteHeader(http.StatusNoContent)
			return true
		}
	}
	if m == http.MethodPost {
		if wait := i.wait(p, IPSource{i.opt.ClientIPHeader, i.opt.TrustedProxies}.ClientIP(r)); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(wait))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many requests"})
			return true
		}
		// what acts with the session cookie comes from this origin's own pages; the OAuth client endpoints are called by
		// the clients themselves, with no cookie
		if (strings.HasPrefix(p, "/api/") || p == "/oauth/authorize") && !fromOwnPages(r.Header, i.url) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "cross-origin request refused"})
			return true
		}
	}
	lang := i.pageLang(r)
	get := m == http.MethodGet || m == http.MethodHead
	switch {
	case get && (p == "/.well-known/oauth-protected-resource" || p == "/.well-known/oauth-protected-resource/mcp"):
		writeJSON(w, http.StatusOK, i.Auth.ResourceMeta())
	case get && (p == "/.well-known/oauth-authorization-server" || p == "/.well-known/openid-configuration"):
		writeJSON(w, http.StatusOK, i.Auth.ServerMeta())
	case m == http.MethodPost && p == "/oauth/register":
		var body map[string]any
		if !readJSON(w, r, &body) {
			return true
		}
		reply, err := i.Auth.Register(r.Context(), body)
		i.reply(w, reply, err)
	case m == http.MethodPost && p == "/oauth/token":
		form, ok := readForm(w, r)
		if !ok {
			return true
		}
		reply, err := i.Auth.Token(r.Context(), form)
		i.reply(w, reply, err)
	case p == "/oauth/authorize" && (m == http.MethodGet || m == http.MethodPost):
		i.authorize(w, r, lang)
	case get && p == "/sign-in":
		n, err := i.Passkeys.Count(r.Context())
		if err != nil {
			i.fail(w, err)
			return true
		}
		writeHTML(w, http.StatusOK, signInPage(lang, safeNext(r.URL.Query().Get("next")), n > 0))
	case get && p == "/setup":
		writeHTML(w, http.StatusOK, setupPage(lang))
	case get && p == "/assets/auth.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(w, authJS)
	case m == http.MethodPost && p == "/api/auth/passkey/options":
		o, err := i.Passkeys.RequestOptions(r.Context())
		i.send(w, o, err)
	case m == http.MethodPost && p == "/api/auth/passkey":
		var body struct {
			Assertion Assertion `json:"assertion"`
		}
		if !readJSON(w, r, &body) {
			return true
		}
		if err := i.Passkeys.Verify(r.Context(), body.Assertion); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
			return true
		}
		i.signedIn(w, r)
	case m == http.MethodPost && p == "/api/auth/sign-out":
		i.Auth.EndSession(r.Context(), r)
		http.SetCookie(w, Cookie("", i.https))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case m == http.MethodPost && p == "/api/auth/approval/passkey":
		var body struct {
			Waiter       string       `json:"waiter"`
			Label        string       `json:"label"`
			Registration Registration `json:"registration"`
		}
		if !readJSON(w, r, &body) {
			return true
		}
		if err := i.passkeyAccept(r.Context(), body.Waiter, body.Registration, body.Label); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return true
		}
		// no session: the owner signs in with the passkey just made
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case m == http.MethodPost && p == "/api/auth/approval/start":
		i.approvalStart(w, r)
	case m == http.MethodPost && p == "/api/auth/approval/poll":
		var body struct {
			Waiter string `json:"waiter"`
		}
		if !readJSON(w, r, &body) {
			return true
		}
		state, to, err := i.Auth.PollApproval(r.Context(), body.Waiter)
		if err != nil {
			i.fail(w, err)
			return true
		}
		reply := map[string]any{"state": state}
		if to != "" {
			reply["redirect"] = to
		} else if state == "approved" {
			// an approved request with nowhere to redirect is a passkey's: the page makes it with these options
			o, err := i.Passkeys.CreationOptions(r.Context(), i.opt.Name)
			if err != nil {
				i.fail(w, err)
				return true
			}
			reply["options"] = o
		}
		writeJSON(w, http.StatusOK, reply)
	case m == http.MethodPost && p == "/api/auth/approval/cancel":
		var body struct {
			Waiter string `json:"waiter"`
		}
		if !readJSON(w, r, &body) {
			return true
		}
		i.send(w, map[string]any{"ok": true}, i.Auth.CancelApproval(r.Context(), body.Waiter))
	default:
		return false
	}
	return true
}

// approvalStart makes the code the approval page shows. The code is returned to the browser that asked and nowhere else.
func (i *Identity) approvalStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Purpose string            `json:"purpose"`
		Params  map[string]string `json:"params"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	purpose := body.Purpose
	switch purpose {
	case "":
		purpose = PurposeOAuth
	case PurposeOAuth, PurposePasskey:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown purpose"})
		return
	}
	params := url.Values{}
	for k, v := range body.Params {
		params.Set(k, v)
	}
	code, waiter, _, err := i.Auth.StartApproval(r.Context(), purpose, params, IPSource{i.opt.ClientIPHeader, i.opt.TrustedProxies}.ClientIP(r),
		r.Header.Get("User-Agent"))
	var invalid *InvalidRequestError
	switch {
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": invalid.Message})
	case errors.Is(err, ErrTooManyFromAddress):
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many waiting approvals from your address, try in a few minutes"})
	case errors.Is(err, ErrTooManyApprovals), errors.Is(err, ErrTooManyPasskeyApprovals):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "too many waiting approvals, try in a few minutes"})
	case err != nil:
		i.fail(w, err)
	default:
		// the page says how the code can be approved: a cloud client's only by the server command
		v, err := i.Auth.ShowApproval(r.Context(), code)
		if err != nil {
			i.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": code, "waiter": waiter, "expires_in": int(ApprovalTTL / time.Second),
			"local": v.Local})
	}
}

// wait is how many seconds a POST must wait before it may go (0: now), by the path's bucket.
func (i *Identity) wait(path, ip string) int {
	now := time.Now()
	switch path {
	case "/api/auth/passkey", "/api/auth/passkey/options", "/api/auth/approval/passkey":
		return i.forms.Take(ip, now)
	case "/api/auth/approval/start", "/api/auth/approval/cancel":
		return i.forms.Take(ip, now)
	case "/api/auth/approval/poll":
		return i.poll.Take(ip, now)
	case "/oauth/token":
		return i.token.Take(ip, now)
	case "/oauth/register":
		// the address first: one that is already refused must not spend the global bucket, or it drains it for the rest
		if w := i.register.Take(ip, now); w > 0 {
			return w
		}
		return i.registerAll.Take("*", now)
	}
	return 0
}

func (i *Identity) authorize(w http.ResponseWriter, r *http.Request, lang string) {
	var params url.Values
	if r.Method == http.MethodPost {
		var ok bool
		if params, ok = readForm(w, r); !ok {
			return
		}
	} else {
		params = r.URL.Query()
	}
	decision := params.Get("decision")
	params.Del("decision")
	client, message, err := i.Auth.CheckAuthorize(r.Context(), params)
	if err != nil {
		i.fail(w, err)
		return
	}
	if client == nil {
		writeHTML(w, http.StatusBadRequest, errorPage(lang, message))
		return
	}
	if !i.Auth.Session(r.Context(), r) {
		// no code is made here: a link scanner or a prefetch opens this page, the page's script asks for the code
		n, err := i.Passkeys.Count(r.Context())
		if err != nil {
			i.fail(w, err)
			return
		}
		next := ""
		if n > 0 {
			next = "/oauth/authorize?" + params.Encode()
		}
		writeHTML(w, http.StatusOK, approvalPage(lang, client.Name, params, next))
		return
	}
	if r.Method == http.MethodGet {
		writeHTML(w, http.StatusOK, consentPage(lang, client.Name, params))
		return
	}
	var to string
	if decision == "allow" {
		to, err = i.Auth.IssueCode(r.Context(), params)
	} else {
		to, err = i.Auth.Denied(params)
	}
	if err != nil {
		i.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", to)
	w.WriteHeader(http.StatusFound)
}

// signedIn gives the browser a session.
func (i *Identity) signedIn(w http.ResponseWriter, r *http.Request) {
	token, err := i.Auth.NewSession(r.Context(), r.Header.Get("User-Agent"))
	if err != nil {
		i.fail(w, err)
		return
	}
	http.SetCookie(w, Cookie(token, i.https))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (i *Identity) reply(w http.ResponseWriter, r Reply, err error) {
	if err != nil {
		i.fail(w, err)
		return
	}
	writeJSON(w, r.Status, r.JSON)
}

func (i *Identity) send(w http.ResponseWriter, v map[string]any, err error) {
	if err != nil {
		i.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// fail answers a failure of the core itself; what went wrong is for the log, not for the client.
func (i *Identity) fail(w http.ResponseWriter, err error) {
	slog.Error("identity: request failed", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
}

// safeNext is a `next` the sign-in may send the browser to: a path on this origin, never elsewhere; empty otherwise.
// A browser drops tabs and newlines from a URL and reads a backslash as a slash, so "/\t/evil.com" and "/\evil.com"
// both become "//evil.com": no control character and no backslash passes, raw or percent-encoded, and nothing that
// decodes to a double slash.
func safeNext(next string) string {
	dec, err := url.PathUnescape(next)
	if err != nil {
		return ""
	}
	for _, s := range []string{next, dec} {
		if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.ContainsRune(s, '\\') {
			return ""
		}
		for _, c := range s {
			if c < 0x20 || c == 0x7f {
				return ""
			}
		}
	}
	return next
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeHTML(w http.ResponseWriter, status int, body string) {
	pageHeaders(w.Header())
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// readBody reads a body capped at bodyLimit; it answers 413 itself when the body is over.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, bodyLimit))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "request body too large"})
		return nil, false
	}
	return b, true
}

// readJSON decodes a JSON object body into v; a body that is not one is an empty object, as the clients that send
// nothing expect (the handlers refuse what is missing).
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	b, ok := readBody(w, r)
	if ok {
		_ = json.Unmarshal(b, v)
	}
	return ok
}

func readForm(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	b, ok := readBody(w, r)
	if !ok {
		return nil, false
	}
	f, _ := url.ParseQuery(string(b))
	if f == nil {
		f = url.Values{}
	}
	return f, true
}
