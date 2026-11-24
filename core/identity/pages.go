package identity

import (
	_ "embed"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// The few pages the core shows itself: signing in with a passkey, making the first passkey, and agreeing to connect an
// MCP client. No inline script (the CSP allows only /assets/auth.js); the strings are in the catalogue below, Italian
// carrying every English key (a test holds that).

//go:embed auth.js
var authJS string

// texts is the catalogue: language, then key.
var texts = map[string]map[string]string{
	"en": {
		"title":          "Kedalio",
		"tagline":        "Your memory, your tasks and your projects, for your agents.",
		"signIn":         "Sign in",
		"signInIntro":    "Sign in to your Kedalio.",
		"passkey":        "Sign in with a passkey",
		"passkeyWhat":    "A passkey is your fingerprint, your face or your device's PIN, in place of a password.",
		"noPasskey":      "There is no passkey yet. You can make one at /setup (it asks for an approval code on the server).",
		"signedIn":       "You are signed in. You can close this page.",
		"setupTitle":     "Make a passkey",
		"setupIntro":     "A passkey lets you sign in without a code. It is optional: clients can also be connected with an approval code.",
		"setupCreate":    "Get a code",
		"setupDone":      "Passkey saved. Next time a client connects, choose \"Sign in with a passkey\".",
		"connectTitle":   "Connect {client}",
		"connectIntro":   "{client} asks to use your Kedalio: memory, tasks and your agents.",
		"connectTo":      "After you allow, the sign-in goes to {to}. The name {client} is what the app calls itself: it is not verified.",
		"approveLead":    "To connect, approve this code. It is good for 10 minutes.",
		"approveWait":    "Getting a code...",
		"approveCli":     "On the server, run: docker compose exec core kedalio-core approve {code}",
		"approveAsk":     "Or ask a Claude already connected to this Kedalio, on this computer: approve {code}",
		"approveCheck":   "Approve only if you started this connection yourself, just now. Someone who asks you to approve a code may be trying to get into your Kedalio.",
		"approveLeft":    "Expires in {time}",
		"approveExpired": "The code expired.",
		"approveAgain":   "Get a new code",
		"approvePasskey": "Sign in with a passkey instead",
		"approveDenied":  "The request was refused.",
		"setupLead":      "To make the passkey, approve this code on the server. It is good for 10 minutes.",
		"allow":          "Connect",
		"deny":           "Cancel",
		"invalid":        "This link is not valid.",
		"failed":         "That did not work: {why}",
	},
	"it": {
		"title":          "Kedalio",
		"tagline":        "La tua memoria, i tuoi compiti e i tuoi progetti, per i tuoi agenti.",
		"signIn":         "Accedi",
		"signInIntro":    "Accedi al tuo Kedalio.",
		"passkey":        "Accedi con una passkey",
		"passkeyWhat":    "Una passkey è la tua impronta, il tuo volto o il PIN del dispositivo, al posto di una password.",
		"noPasskey":      "Non c'è ancora nessuna passkey. Puoi crearla su /setup (chiede un codice di approvazione sul server).",
		"signedIn":       "Hai effettuato l'accesso. Puoi chiudere questa pagina.",
		"setupTitle":     "Crea una passkey",
		"setupIntro":     "Una passkey ti permette di accedere senza codice. È facoltativa: i client si collegano anche con un codice di approvazione.",
		"setupCreate":    "Ottieni un codice",
		"setupDone":      "Passkey salvata. La prossima volta che un client si collega, scegli \"Accedi invece con una passkey\".",
		"connectTitle":   "Collega {client}",
		"connectIntro":   "{client} chiede di usare il tuo Kedalio: memoria, task e i tuoi agenti.",
		"connectTo":      "Dopo il consenso l'accesso va a {to}. Il nome {client} è quello che l'app dichiara di sé: non verificato.",
		"approveLead":    "Per collegarti, approva questo codice. Vale 10 minuti.",
		"approveWait":    "Preparo il codice...",
		"approveCli":     "Sul server esegui: docker compose exec core kedalio-core approve {code}",
		"approveAsk":     "Oppure chiedi a un Claude già collegato a questo Kedalio, su questo computer: approva {code}",
		"approveCheck":   "Approva solo se hai avviato tu questo collegamento, proprio ora. Chi ti chiede di approvare un codice potrebbe voler entrare nel tuo Kedalio.",
		"approveLeft":    "Scade tra {time}",
		"approveExpired": "Il codice è scaduto.",
		"approveAgain":   "Nuovo codice",
		"approvePasskey": "Accedi invece con una passkey",
		"approveDenied":  "La richiesta è stata rifiutata.",
		"setupLead":      "Per creare la passkey, approva questo codice sul server. Vale 10 minuti.",
		"allow":          "Collega",
		"deny":           "Annulla",
		"invalid":        "Questo link non è valido.",
		"failed":         "Non è andata: {why}",
	},
}

// langOf is the page's language from the browser's Accept-Language: Italian when it comes first, English otherwise.
func langOf(accept string) string {
	first, _, _ := strings.Cut(accept, ",")
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(first)), "it") {
		return "it"
	}
	return "en"
}

// pageLang is the language of a page: the instance's own setting when it has one, else the browser's.
func (i *Identity) pageLang(r *http.Request) string {
	switch l := strings.ToLower(strings.TrimSpace(i.opt.Language)); {
	case l == "it" || strings.HasPrefix(l, "ital"):
		return "it"
	case l != "":
		return "en"
	}
	return langOf(r.Header.Get("Accept-Language"))
}

var varRe = regexp.MustCompile(`\{(\w+)\}`)

func t(lang, key string, vars map[string]string) string {
	return html.EscapeString(varRe.ReplaceAllStringFunc(texts[lang][key], func(m string) string { return vars[m[1:len(m)-1]] }))
}

// pageHeaders are the headers every page carries: no inline script, no framing.
func pageHeaders(h http.Header) {
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; connect-src 'self'; "+
		"form-action 'self' https: http://localhost:* http://127.0.0.1:*; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
}

const css = `:root{--bg:#fcfafa;--surface:#fff;--raised:#f3eff0;--line:#e7e1e3;--fg:#140e11;--dim:#665d61;--accent:#a82f5e;
--accent-hi:#8e244d;--accent-wash:#f6e3ea;--on-accent:#fff;--crit:#b5361c;--crit-wash:#fbe4de;
--shadow:0 1px 2px rgba(20,14,17,.05),0 0 0 1px rgba(20,14,17,.07),0 12px 40px rgba(20,14,17,.08);
--sans:"Inter",ui-sans-serif,system-ui,sans-serif;color-scheme:light}
@media(prefers-color-scheme:dark){:root{--bg:#0f0d0e;--surface:#141113;--raised:#181517;--line:#2b2729;--fg:#f3eff0;--dim:#a69ea1;
--accent:#e889aa;--accent-hi:#f2a9c2;--accent-wash:#331622;--on-accent:#140e11;--crit:#f08a70;--crit-wash:#34160f;
--shadow:0 0 0 1px rgba(255,255,255,.07),0 12px 40px rgba(0,0,0,.5);color-scheme:dark}}
*{box-sizing:border-box}
body{margin:0;min-height:100dvh;display:grid;place-items:center;padding:32px 16px;background:var(--bg);color:var(--fg);font:15px/1.5 var(--sans)}
.card{width:100%;max-width:27rem;background:var(--surface);border-radius:6px;box-shadow:var(--shadow);padding:32px}
.brand{margin-bottom:24px;font-weight:700;font-size:18px;letter-spacing:-.01em}
h1{margin:0 0 6px;font-size:24px;line-height:1.2;font-weight:700;letter-spacing:-.02em;text-wrap:balance}
p{margin:0 0 12px;text-wrap:pretty}
.lead,.hint{color:var(--dim)}
.hint{font-size:13px;margin:6px 0 0}
.lead{margin-bottom:20px}
button{display:flex;align-items:center;justify-content:center;width:100%;height:44px;padding:0 16px;border-radius:6px;
border:1px solid var(--line);background:var(--surface);color:var(--fg);font:inherit;font-weight:600;cursor:pointer}
button:hover{background:var(--raised)}
button:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
button.pri{background:var(--accent);color:var(--on-accent);border-color:transparent}button.pri:hover{background:var(--accent-hi)}
button.ghost{background:none;border-color:transparent;color:var(--accent)}button.ghost:hover{background:var(--accent-wash)}
#error{margin:0 0 16px;padding:10px 12px;border-radius:6px;background:var(--crit-wash);color:var(--crit);font-weight:600;font-size:14px}
#error:empty{display:none}
.stack{display:grid;gap:8px;margin-top:20px}
.note{margin:0 0 20px;padding:10px 12px;border-radius:6px;background:var(--accent-wash);font-size:14px}
.tag{margin:24px 0 0;padding-top:16px;border-top:1px solid var(--line);color:var(--dim);font-size:13px;text-wrap:balance}
.code{margin:16px 0 8px;padding:16px;border-radius:6px;background:var(--raised);text-align:center;font:700 32px/1.2 ui-monospace,monospace;letter-spacing:.12em}
.cmd{margin:0 0 12px;padding:8px 10px;border-radius:6px;background:var(--raised);font:13px ui-monospace,monospace;word-break:break-all}
a{color:var(--accent)}
.hidden{display:none!important}
@media(max-width:560px){body{display:block;padding:0}.card{max-width:none;min-height:100dvh;border-radius:0;box-shadow:none;padding:28px 20px 32px}
button{height:46px}}`

func page(lang, view, heading, body string, data map[string]string) string {
	attrs := ` data-view="` + html.EscapeString(view) + `" data-lang="` + lang + `"`
	for k, v := range data {
		attrs += ` data-` + k + `="` + html.EscapeString(v) + `"`
	}
	script := "/assets/auth.js"
	strs, _ := json.Marshal(texts[lang]) // escapes < > & as \u00xx: safe inside a script element
	return `<!doctype html><html lang="` + lang + `"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark"><title>` + t(lang, "title", nil) + `</title><style>` + css + `</style></head>
<body` + attrs + `><main class="card"><header class="brand">` + t(lang, "title", nil) + `</header>` + heading +
		`<p id="error" role="alert"></p>` + body + `<p class="tag">` + t(lang, "tagline", nil) + `</p></main>
<script type="application/json" id="strings">` + string(strs) + `</script>
<script src="` + script + `"></script></body></html>`
}

// signInPage is the sign-in. `next` is where the browser goes after (a path on this origin, or empty); a core with no
// passkey yet says where to make the first.
func signInPage(lang, next string, hasPasskey bool) string {
	body := `<section id="main"><p class="lead">` + t(lang, "signInIntro", nil) + `</p>`
	if hasPasskey {
		body += `<button id="passkey" class="pri">` + t(lang, "passkey", nil) + `</button><p class="hint">` + t(lang, "passkeyWhat", nil) + `</p>`
	} else {
		body += `<p class="note">` + t(lang, "noPasskey", nil) + `</p>`
	}
	body += `</section><section id="done" class="hidden"><p>` + t(lang, "signedIn", nil) + `</p></section>`
	return page(lang, "sign-in", `<h1>`+t(lang, "signIn", nil)+`</h1>`, body, map[string]string{"next": next})
}

// waitBlock is what both approval pages show while the owner approves: the code, the time left, how to approve, and the
// buttons. The script fills it in.
func waitBlock(lang, leadKey string, hidden bool) string {
	class := ""
	if hidden {
		class = ` class="hidden"`
	}
	return `<div id="wait"` + class + `><p>` + t(lang, leadKey, nil) + `</p><p id="code" class="code" aria-live="polite">` + t(lang, "approveWait", nil) + `</p>` +
		`<p id="left" class="hint"></p><div id="how" class="hidden"><p id="cli" class="cmd"></p><p id="ask" class="hint hidden"></p></div>` +
		`<div class="stack"><button id="again" class="pri hidden">` + t(lang, "approveAgain", nil) + `</button><button id="cancel" class="ghost hidden">` +
		t(lang, "deny", nil) + `</button></div></div>`
}

// setupPage makes a passkey. A code is asked for only when the owner presses the button, so opening the page starts nothing.
func setupPage(lang string) string {
	return page(lang, "setup", `<h1>`+t(lang, "setupTitle", nil)+`</h1>`,
		`<section id="main"><p class="lead">`+t(lang, "setupIntro", nil)+`</p><button id="create" class="pri">`+t(lang, "setupCreate", nil)+
			`</button><p class="hint">`+t(lang, "passkeyWhat", nil)+`</p>`+waitBlock(lang, "setupLead", true)+`</section><section id="done" class="hidden"><p>`+
			t(lang, "setupDone", nil)+`</p></section>`, nil)
}

// consentPage is agreeing to connect an MCP client. The OAuth parameters ride along as hidden fields.
func consentPage(lang, client string, params map[string][]string) string {
	var hidden strings.Builder
	for k, vs := range params {
		for _, v := range vs {
			hidden.WriteString(`<input type="hidden" name="` + html.EscapeString(k) + `" value="` + html.EscapeString(v) + `">`)
		}
	}
	vars := map[string]string{"client": client}
	// the name is whatever the client registered with, so the page also shows where the sign-in will be sent
	to := map[string]string{"client": client, "to": redirectTarget(params["redirect_uri"])}
	return page(lang, "consent", `<h1>`+t(lang, "connectTitle", vars)+`</h1>`,
		`<p class="lead">`+t(lang, "connectIntro", vars)+`</p><p class="hint">`+t(lang, "connectTo", to)+`</p><form method="post" action="/oauth/authorize">`+hidden.String()+
			`<div class="stack"><button name="decision" value="allow" class="pri">`+t(lang, "allow", nil)+
			`</button><button name="decision" value="deny" class="ghost">`+t(lang, "deny", nil)+`</button></div></form>`, nil)
}

// approvalPage is how a client with no session is connected: the page asks for a code and waits for the owner to
// approve it (kedalio-core approve, or a connected client), then goes on to the client. The OAuth parameters ride along
// as hidden fields and the script posts them. passkeyNext is where signing in with a passkey instead goes back to,
// empty when there is no passkey.
func approvalPage(lang, client string, params map[string][]string, passkeyNext string) string {
	var hidden strings.Builder
	for k, vs := range params {
		for _, v := range vs {
			hidden.WriteString(`<input type="hidden" name="` + html.EscapeString(k) + `" value="` + html.EscapeString(v) + `">`)
		}
	}
	vars := map[string]string{"client": client}
	to := map[string]string{"client": client, "to": redirectTarget(params["redirect_uri"])}
	body := `<p class="lead">` + t(lang, "connectIntro", vars) + `</p><p class="hint">` + t(lang, "connectTo", to) + `</p>` +
		`<form id="params">` + hidden.String() + `</form><p class="note">` + t(lang, "approveCheck", nil) + `</p>` +
		waitBlock(lang, "approveLead", false)
	if passkeyNext != "" {
		body += `<p class="hint"><a href="/sign-in?next=` + html.EscapeString(url.QueryEscape(passkeyNext)) + `">` + t(lang, "approvePasskey", nil) + `</a></p>`
	}
	return page(lang, "approval", `<h1>`+t(lang, "connectTitle", vars)+`</h1>`, `<section id="main">`+body+`</section>`, nil)
}

func errorPage(lang, message string) string {
	return page(lang, "error", `<h1>`+t(lang, "invalid", nil)+`</h1>`, `<p class="lead">`+html.EscapeString(message)+`</p>`, nil)
}

// redirectTarget is the scheme and host:port a redirect URI points at, without its path or query, for the consent page.
func redirectTarget(uris []string) string {
	if len(uris) == 0 {
		return "?"
	}
	u, err := url.Parse(uris[0])
	if err != nil {
		return "?"
	}
	if u.Host == "" {
		if u.Scheme != "" {
			return u.Scheme + ":"
		}
		return "?"
	}
	return u.Scheme + "://" + u.Host
}
