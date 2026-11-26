// Package mcp is the Kedalio MCP server: the tools every client sees (protocol/tools.go), over Streamable HTTP,
// stateless: nothing is kept between two requests, so a restart costs the clients nothing.
//
// The memory's writing rules (rules.go) are the ones its tools enforce, because agents write without asking.
package mcp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kedalio/core/memory"
	"kedalio/core/memory/docs"
	"kedalio/core/memory/search"
	"kedalio/core/tasks"
)

// Config is what a Server is made of.
type Config struct {
	Memory *memory.Store
	// Search is the index recall and memory_recall mode search read.
	Search *search.Index
	Tasks  *tasks.Book
	// Embedder adds meaning to search and to remember; nil: words alone.
	Embedder search.Embedder
	// Fetcher gets the URLs documents are added from; nil is the guarded one, which never reaches a private address.
	Fetcher *docs.Fetcher

	// Token is a bearer token /mcp accepts: how a local Claude Code connects with no browser. Required.
	Token string
	// UpkeepToken, when set, is a second bearer token: the upkeep agent's. A request that carries it is recorded as author
	// «upkeep» (whose writes memory.go checks) and is otherwise as able as the owner's. Empty: there is none.
	UpkeepToken string
	// Upkeep is what memory_upkeep works with; nil: the tool answers that the upkeep is not set up.
	Upkeep *Upkeep
	// Verify, when set, accepts a bearer token besides Token: an OAuth access token the identity issued.
	Verify func(ctx context.Context, token string) bool
	// Client, when set, names the OAuth client an access token was issued to ("" for any other token): what its writes
	// are recorded under, «oauth:<client>», so a revision says which connector wrote it. remote is a client registered
	// with a redirect off this machine (claude.ai, ChatGPT): it gets the cloud policy (cloud.go).
	Client func(ctx context.Context, token string) (name string, remote bool)
	// Named, when set, accepts a bearer token that starts with «kdl_»: a static token an operator made for a headless
	// agent, bound to a policy by name (tokenPolicies). It gives the token's name and policy; revoked and unknown
	// tokens are not ok. A «kdl_» bearer is judged here only, never by Verify.
	Named func(ctx context.Context, token string) (name, policy string, ok bool)
	// ResourceMetadata is the address of the protected resource metadata (RFC 9728), told in the WWW-Authenticate of a
	// 401 so that an OAuth client finds the authorization server; empty when OAuth is off.
	ResourceMetadata string
	// Now pins the clock memory_recall asks its questions of time by; zero is the current time. Only the recall
	// evaluation sets it, so that two runs over the same copy of the memory answer the same.
	Now time.Time
	// Mask hides what must not leave the core in an answer (the vault's secrets); nil leaves it as it is. It runs on every
	// string of every answer, and on a text the memory is about to keep: a text it would change is refused.
	Mask func(string) string

	// AuthFailures, when set, limits wrong bearer tokens per client address (ClientIP): an address that has failed too
	// often is answered 429 before its token is looked at, so a guess cannot be repeated at the speed of the network.
	// identity.Buckets satisfies it. ClientIP names the address of a request (identity.IPSource.ClientIP).
	AuthFailures FailureLimit
	ClientIP     func(*http.Request) string

	// Approvals is what clients_approve approves codes with (approvals.go); nil: the tool is not offered.
	Approvals Approvals

	// Ops are the stores of the session tools (ops.go); nil: those tools are not offered.
	Ops *Ops

	// By is the author kept on every revision and task log; «owner» when empty.
	By string
	// Owner names whose memory it is in the instructions, Language is what the model answers in.
	Owner, Language string
	// Zone is the owner's time zone, for the diary's days and hours; the tasks' zone when nil.
	Zone *time.Location
	// Version is told to clients at initialize.
	Version string
	// PublicURL is the address the core is reached at; Label is a word the deployment gives it («trial»). Both are told
	// to clients at initialize, so that a session with two Kedalio connectors knows which one it is talking to.
	PublicURL, Label string
}

// FailureLimit counts failures per key: Blocked is 0, or the seconds until the key may try again, without counting; Take
// counts one failure (and answers like Blocked).
type FailureLimit interface {
	Blocked(key string, now time.Time) int
	Take(key string, now time.Time) int
}

// Server is the connector.
type Server struct {
	cfg   Config
	areas memory.Areas
	zone  *time.Location
	tools map[string]*tool
	sdk   *sdk.Server
	// cloudSDK is the same server with only the tools a cloud client may call (cloudMay)
	cloudSDK *sdk.Server
	// collections are the collections of documents memory_sources and the memory tools work on.
	collections *memory.Collections
}

// New makes the server; it refuses to without a token.
func New(cfg Config) (*Server, error) {
	if cfg.Token == "" {
		return nil, errors.New("mcp: a bearer token is required")
	}
	if cfg.Memory == nil || cfg.Tasks == nil || cfg.Search == nil {
		return nil, errors.New("mcp: memory, search and tasks are required")
	}
	// the token is a password for a machine that writes to the memory: a short one is a guess away
	if t := cfg.UpkeepToken; t != "" && len(t) < minUpkeepToken {
		return nil, fmt.Errorf("mcp: the upkeep token is %d characters: at least %d (KEDALIO_UPKEEP_TOKEN)", len(t), minUpkeepToken)
	}
	if cfg.By == "" {
		cfg.By = "owner"
	}
	if cfg.Version == "" {
		cfg.Version = "0.0.0"
	}
	s := &Server{cfg: cfg, areas: cfg.Memory.Areas, zone: cfg.Zone, tools: map[string]*tool{}}
	if s.zone == nil {
		s.zone = cfg.Tasks.Zone
	}
	if s.areas == (memory.Areas{}) {
		s.areas = memory.AreasEN
	}
	impl := &sdk.Implementation{Name: "kedalio-core", Title: s.identity(), Version: cfg.Version}
	s.sdk = sdk.NewServer(impl, &sdk.ServerOptions{Instructions: s.instructions()})
	// a cloud client is shown only the tools it may call: a shorter tools/list in every one of its threads, and no
	// tool it would be refused (the handler refuses them all the same)
	s.cloudSDK = sdk.NewServer(impl, &sdk.ServerOptions{Instructions: s.instructions()})
	// a task closed leaves its line in the diary
	if cfg.Tasks.Diary == nil {
		cfg.Tasks.Diary = s.diaryLine
	}
	s.collections = &memory.Collections{Store: cfg.Memory, Index: cfg.Search, Fetch: cfg.Fetcher, Slug: slugPath}
	for _, group := range [][]*tool{s.memoryTools(), s.sourceTools(), s.taskTools(), s.opsTools(), s.approvalTools(), {s.upkeepTool()}} {
		for _, t := range group {
			s.tools[t.name] = t
			s.add(s.sdk, t)
			if cloudMay[t.name] {
				s.add(s.cloudSDK, t)
			}
		}
	}
	return s, nil
}

// diaryLine is what the task book writes in the diary when a task closes.
func (s *Server) diaryLine(ctx context.Context, c tasks.Closed, now time.Time) error {
	_, errs, err := s.appendClosure(ctx, c, now)
	if err == nil && len(errs) > 0 {
		err = errors.New(strings.Join(errs, "; "))
	}
	return err
}

// identity says which Kedalio this is, in plain words: «the Go core of Kedalio at core.example.com (trial)». A session
// with two Kedalio connectors tells them apart by it. The address and the label come from the
// deployment's configuration, never from the code.
func (s *Server) identity() string {
	id := "the Go core of Kedalio"
	if u := strings.TrimSpace(s.cfg.PublicURL); u != "" {
		if i := strings.Index(u, "://"); i >= 0 {
			u = u[i+3:]
		}
		id += " at " + strings.TrimRight(u, "/")
	}
	if l := strings.TrimSpace(s.cfg.Label); l != "" {
		id += " (" + l + ")"
	}
	return id
}

// instructions is what every client is told on connecting, under 512 characters: ChatGPT and Codex read only the
// first ones. The writing rules are the kedalio-memory skill's and the memory's own refusals'.
func (s *Server) instructions() string {
	who := s.cfg.Owner
	if who == "" {
		who = "the owner"
	}
	lang := s.cfg.Language
	if lang == "" {
		lang = "the owner's language"
	}
	return "This is " + s.identity() + ". It holds " + who + "'s memory, tasks and projects. " +
		"Its tools are named by what they do (memory_*, tasks_*); each describes its own arguments. " +
		"Recall the memory before assuming anything about " + who + ", their projects or tools. " +
		"When " + who + " states a task or a decision, save it at once (tasks_save, memory_write). " +
		"If the Kedalio plugin's skills are installed: the session-start skill at your first reply in a working folder, " +
		"session-end when the work ends. Answer in " + lang + "."
}

// Handler serves /mcp (behind the bearer token) and /health.
func (s *Server) Handler() http.Handler {
	mcp := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		if isCloud(r.Context()) {
			return s.cloudSDK
		}
		return s.sdk
	}, &sdk.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// The core sits behind a proxy that forwards the public Host to a loopback address, which the SDK's DNS
		// rebinding guard would refuse; the bearer token is what keeps a rebinding page out.
		DisableLocalhostProtection: true,
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", s.authorized(mcp))
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return mux
}

// Run serves the tools over one transport (stdio: a client that started this process) as the owner, until the
// client hangs up or ctx ends. There is no bearer token here: whoever started the process owns it.
func (s *Server) Run(ctx context.Context, t sdk.Transport) error { return s.sdk.Run(ctx, t) }

// authorized lets a request through when it carries the static token or, with OAuth on, an access token. The static
// token is compared as a hash with a constant-time compare, so neither the content nor the length of the token shows in
// the time a refusal takes. Browser-based MCP clients get CORS: the bearer token, never a cookie, is what authorizes.
func (s *Server) authorized(next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(s.cfg.Token))
	upWant := sha256.Sum256([]byte(s.cfg.UpkeepToken))
	hasUpkeep := s.cfg.UpkeepToken != "" // an empty token must never match an empty bearer
	challenge := `Bearer realm="kedalio"`
	if s.cfg.ResourceMetadata != "" {
		challenge += `, resource_metadata="` + s.cfg.ResourceMetadata + `"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Expose-Headers", "WWW-Authenticate, Mcp-Session-Id")
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "authorization, content-type, accept, mcp-protocol-version, mcp-session-id")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var ip string
		if s.cfg.ClientIP != nil {
			ip = s.cfg.ClientIP(r)
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		have := sha256.Sum256([]byte(got))
		// both compares always run, so the time says nothing about which token was close
		isOwner := subtle.ConstantTimeCompare(have[:], want[:]) == 1
		isUpkeep := subtle.ConstantTimeCompare(have[:], upWant[:]) == 1 && hasUpkeep
		var named *policy
		var namedBy string
		pass := isOwner || isUpkeep
		if ok && !pass {
			if strings.HasPrefix(got, namedPrefix) {
				// a token of this shape is a named one or nothing: it never goes to the OAuth checks
				if s.cfg.Named != nil {
					if n, pol, found := s.cfg.Named(r.Context(), got); found {
						if named = tokenPolicies[pol]; named != nil {
							namedBy, pass = namedAuthorPrefix+n, true
						}
					}
				}
			} else {
				pass = s.cfg.Verify != nil && s.cfg.Verify(r.Context(), got)
			}
		}
		if !ok || !pass {
			// only a wrong token counts, and a valid one always passes: behind a shared proxy address the owner must not
			// be locked out by strangers' failures. Wrong tokens from a blocked address are told apart by the 429.
			if ip != "" && s.cfg.AuthFailures != nil {
				if wait := s.cfg.AuthFailures.Blocked(ip, time.Now()); wait > 0 {
					h.Set("Retry-After", strconv.Itoa(wait))
					http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
					return
				}
				s.cfg.AuthFailures.Take(ip, time.Now())
			}
			h.Set("WWW-Authenticate", challenge)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if named != nil {
			// the author is marked «token:», so no token's name can read as the upkeep (upkeepAuthor); a cloud policy is
			// also cloud for everything that reads isCloud (the short tool list, remember, the inbox's mark)
			ctx := context.WithValue(r.Context(), authorKey{}, namedBy)
			ctx = context.WithValue(ctx, policyKey{}, named)
			if named == cloudPolicy {
				ctx = context.WithValue(ctx, cloudKey{}, true)
			}
			r = r.WithContext(ctx)
		} else if isUpkeep && !isOwner {
			r = r.WithContext(context.WithValue(r.Context(), authorKey{}, upkeepAuthor))
		} else if s.cfg.Client != nil && !isOwner {
			c, remote := s.cfg.Client(r.Context(), got)
			if c == "" {
				// an access token that passed but whose client could not be read (a busy database): it gets the
				// narrowest policy, never the owner's
				c, remote = "client", true
			}
			ctx := context.WithValue(r.Context(), authorKey{}, "oauth:"+c)
			if remote {
				ctx = context.WithValue(ctx, cloudKey{}, true)
			}
			r = r.WithContext(ctx)
		}
		// the caller's address travels with the call: clients_approve approves only a request that came from it
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerIPKey{}, ip)))
	})
}

type callerIPKey struct{}

// callerIP is the address the call came from, keyed as Config.ClientIP does; empty when the server cannot tell.
func callerIP(ctx context.Context) string {
	ip, _ := ctx.Value(callerIPKey{}).(string)
	return ip
}

// minUpkeepToken is the fewest characters the upkeep token may have.
const minUpkeepToken = 32

type authorKey struct{}

// by is who a write is recorded under: the OAuth client that made the call, else the configured author.
func (s *Server) by(ctx context.Context) string {
	if a, ok := ctx.Value(authorKey{}).(string); ok {
		return a
	}
	return s.cfg.By
}
