package instance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kedalio/core/identity"
)

// With no public URL the identity is off, yet a named token still opens /mcp until it is revoked.
func TestNamedTokenWorksWithoutAPublicURLUntilRevoked(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	inst, err := testOptions(t.TempDir()).Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop(); inst.Close() })
	srv := httptest.NewServer(inst.Handler)
	t.Cleanup(srv.Close)

	tk := identity.NewTokens(inst.DB)
	token, err := tk.Create(ctx, "alice", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	post := func(tok string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("content-type", "application/json")
		req.Header.Set("accept", "application/json, text/event-stream")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if got := post(token); got != http.StatusOK {
		t.Fatalf("a live token: %d", got)
	}
	if err := tk.Revoke(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if got := post(token); got != http.StatusUnauthorized {
		t.Errorf("a revoked token: %d", got)
	}
}
