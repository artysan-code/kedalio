package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kedalio/core/instance"
	"kedalio/core/vault"
)

func testOptions(dir string) instance.Options {
	return instance.Options{Data: dir, Token: "t", Language: "English", BackupEvery: time.Hour, BackupKeep: 3}
}

// started makes a fresh instance in o's folder, stops it again and gives the recovery code.
func started(t *testing.T, o instance.Options) (code string) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	inst, err := o.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if err := inst.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := vault.HostKey(o.Data, false)
	if err != nil || raw == nil {
		t.Fatalf("no key file after the first start: %v", err)
	}
	return vault.RecoveryCode(raw)
}

func TestGateServesOnlyHealthAndUnlockWhileLockedAndLimitsTries(t *testing.T) {
	o := testOptions(t.TempDir())
	code := started(t, o)
	os.Remove(filepath.Join(o.Data, "kek"))
	ctx, stop := context.WithCancel(context.Background())
	g := newGate(ctx, o)
	defer func() {
		stop()
		if i := g.Instance(); i != nil {
			i.Close()
		}
	}()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "192.0.2.1:1234"
		g.ServeHTTP(w, r)
		return w
	}

	if w := do("GET", "/health", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "locked") {
		t.Fatalf("health: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/mcp", "{}"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("mcp while locked: %d", w.Code)
	}
	if w := do("GET", "/unlock", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /unlock: %d", w.Code)
	}
	if w := do("POST", "/unlock", "{}"); w.Code != http.StatusBadRequest {
		t.Fatalf("no code: %d", w.Code)
	}
	if w := do("POST", "/unlock", `{"code":"AAAA"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong code: %d", w.Code)
	}
	w := do("POST", "/unlock", `{"code":"`+code+`"}`)
	if w.Code != 200 || g.Instance() == nil {
		t.Fatalf("unlock: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), code) {
		t.Fatal("the code is echoed")
	}
	if w := do("GET", "/health", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok"`) {
		t.Fatalf("health unlocked: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/unlock", `{"code":"`+code+`"}`); w.Code == 200 {
		t.Fatal("/unlock works on an unlocked core")
	}

	// the limiter: after the burst, a wrong code is not even looked at
	locked := newGate(context.Background(), o)
	locked.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	var last int
	for range 8 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/unlock", strings.NewReader(`{"code":"AAAA"}`))
		r.RemoteAddr = "192.0.2.9:1"
		locked.ServeHTTP(w, r)
		last = w.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after 8 tries: %d", last)
	}
}
