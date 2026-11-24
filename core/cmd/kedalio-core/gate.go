package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"kedalio/core/identity"
	"kedalio/core/instance"
)

// gate is the HTTP side of an instance that may still be locked. While it waits for the recovery code (no key file,
// ADR 0002 decision 11) it answers /health and POST /unlock and nothing else; once unlocked it hands every request to
// the instance and /unlock is gone.
//
// Unlock is an HTTP route and not a prompt on stdin: the core runs as a service on a host the owner is not sitting at,
// and the owner unlocks it with a request after a restart. The code travels like the bearer token does, over the TLS of
// the proxy in front, and is never logged.
type gate struct {
	open atomic.Pointer[instance.Instance]
	// unlock opens the instance with a code; it is serialized, so two requests cannot open the database twice.
	unlock func(ctx context.Context, code string) (*instance.Instance, error)
	// ctx is the core's, not the request's: the instance's background work lives as long as the core.
	ctx    context.Context
	mu     sync.Mutex
	limits *identity.Buckets
	ip     identity.IPSource
	now    func() time.Time
}

func newGate(ctx context.Context, o instance.Options) *gate {
	ip := identity.IPSource{Header: o.ClientIPHeader, Trusted: o.TrustedProxies}
	// at startup, before the instance is unlocked: the unlock route is rate-limited by the same address
	identity.WarnIPSource(slog.Default(), o.PublicURL, ip)
	return &gate{
		ctx: ctx, unlock: o.Unlock, ip: ip, now: time.Now,
		// a few typos, then one try each ten seconds: the code is 256 random bits, so this is about not being a free oracle
		limits: identity.NewBuckets(5, 10*time.Second),
	}
}

// Instance is the running instance, nil while locked.
func (g *gate) Instance() *instance.Instance { return g.open.Load() }

func (g *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if i := g.open.Load(); i != nil {
		i.Handler.ServeHTTP(w, r)
		return
	}
	switch {
	case r.URL.Path == "/health":
		writeJSON(w, http.StatusOK, map[string]string{"status": "locked"})
	case r.URL.Path == "/unlock" && r.Method == http.MethodPost:
		g.serveUnlock(w, r)
	case r.URL.Path == "/unlock":
		w.Header().Set("allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST the recovery code to /unlock"})
	default:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "locked: the owner unlocks with the recovery code"})
	}
}

func (g *gate) serveUnlock(w http.ResponseWriter, r *http.Request) {
	if wait := g.limits.Take(g.ip.ClientIP(r), g.now()); wait > 0 {
		w.Header().Set("retry-after", strconv.Itoa(wait))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many tries"})
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.Code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"code": "<the recovery code>"}`})
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.open.Load() != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already unlocked"})
		return
	}
	i, err := g.unlock(g.ctx, body.Code)
	switch {
	case errors.Is(err, instance.ErrWrongCode):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	case err != nil:
		// the error may name paths, never the code: nothing here has it
		slog.Error("unlock failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the instance did not open: see the core's log"})
		return
	}
	g.open.Store(i)
	slog.Info("unlocked")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
