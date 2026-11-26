package mcp

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kedalio/core/db"
	"kedalio/core/projects"
)

// namedStub is Config.Named for a test: live tokens by text, with the policy each is bound to, and a count of what
// reached Verify.
type namedStub struct {
	mu       sync.Mutex
	tokens   map[string]string // token -> policy
	revoked  map[string]bool
	verified []string
}

const (
	testNamed       = "kdl_alice-token-not-a-secret"
	testNamedOddity = "kdl_odd-token-not-a-secret"
)

func namedRig(t *testing.T) (*rig, *namedStub) {
	t.Helper()
	stub := &namedStub{tokens: map[string]string{testNamed: "cloud", testNamedOddity: "nonesuch"}, revoked: map[string]bool{}}
	r := newRigWith(t, nil, func(c *Config) {
		d, err := db.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		if err := projects.Migrate(ctx, d); err != nil {
			t.Fatal(err)
		}
		c.Ops = &Ops{Projects: projects.New(d)}
		c.UpkeepToken = testUpkeepToken
		c.Named = func(_ context.Context, tok string) (string, string, bool) {
			stub.mu.Lock()
			defer stub.mu.Unlock()
			pol, ok := stub.tokens[tok]
			if !ok || stub.revoked[tok] {
				return "", "", false
			}
			if tok == testNamedOddity {
				return "odd", pol, true
			}
			return "alice", pol, true
		}
		c.Verify = func(_ context.Context, tok string) bool {
			stub.mu.Lock()
			defer stub.mu.Unlock()
			stub.verified = append(stub.verified, tok)
			return false
		}
		c.Client = func(_ context.Context, tok string) (string, bool) {
			stub.mu.Lock()
			defer stub.mu.Unlock()
			stub.verified = append(stub.verified, tok)
			return "", false
		}
	})
	return r, stub
}

func status(t *testing.T, r *rig, token string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.http.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json, text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestNamedCloudTokenListsTheCloudToolsAndWritesAsItself(t *testing.T) {
	r, _ := namedRig(t)
	s, err := r.connect(t, testNamed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	var want []string
	for name := range cloudMay {
		want = append(want, name)
	}
	slices.Sort(want)
	if got := listedTools(t, s); !slices.Equal(got, want) {
		t.Errorf("lists %v, want %v", got, want)
	}
	id := taskID(mustCall(t, s, "tasks_save", map[string]any{"title": "Buy tea"}))
	if hist, _ := r.store.History(ctx, "tasks/"+id+".md"); len(hist) == 0 || hist[len(hist)-1].By != "token:alice" && hist[0].By != "token:alice" {
		t.Errorf("the task is not recorded under the token: %+v", hist)
	}
}

func TestNamedCloudTokenRemembersIntoTheInboxMarkedWithItsName(t *testing.T) {
	r, _ := namedRig(t)
	s, err := r.connect(t, testNamed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if out := mustCall(t, s, "memory_write", map[string]any{"op": "remember", "text": "Alice takes sugar in her tea"}); out["how"] != "inbox" {
		t.Errorf("remember = %v", out)
	}
	entries, doc, err := r.srv.inboxEntries(ctx)
	if err != nil || len(entries) != 1 || !entries[0].cloud {
		t.Fatalf("%v %v", entries, err)
	}
	if !strings.Contains(doc.Body, "why: cloud: from token alice, waits for the owner") {
		t.Errorf("the mark is missing:\n%s", doc.Body)
	}
}

func TestNamedCloudTokenCannotDeleteOrRewriteAPage(t *testing.T) {
	r, _ := namedRig(t)
	if _, err := r.store.Write(ctx, "notes/tea.md", teaPage, "owner"); err != nil {
		t.Fatal(err)
	}
	s, err := r.connect(t, testNamed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if res, err := s.CallTool(ctx, &sdk.CallToolParams{Name: "memory_delete", Arguments: map[string]any{"what": "page", "path": "notes/tea.md"}}); err == nil && !res.IsError {
		t.Error("memory_delete ran")
	}
	if out, failed := call(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/tea.md", "base_rev": 1, "body": "# Tea\n\nGone.\n"}); !denied(out, failed) {
		t.Errorf("a page rewrite was allowed: %v", out)
	}
	if d, _ := r.store.Get(ctx, "notes/tea.md"); d == nil || d.Rev != 1 {
		t.Errorf("the page changed: %+v", d)
	}
}

func TestNamedTokenIsRefusedOnceRevokedOrBoundToAnUnknownPolicy(t *testing.T) {
	r, stub := namedRig(t)
	if got := status(t, r, testNamed); got == http.StatusUnauthorized {
		t.Fatalf("a live token got %d", got)
	}
	stub.mu.Lock()
	stub.revoked[testNamed] = true
	stub.mu.Unlock()
	if got := status(t, r, testNamed); got != http.StatusUnauthorized {
		t.Errorf("revoked: %d", got)
	}
	if got := status(t, r, testNamedOddity); got != http.StatusUnauthorized {
		t.Errorf("unknown policy: %d", got)
	}
	if got := status(t, r, "kdl_never-issued"); got != http.StatusUnauthorized {
		t.Errorf("unknown token: %d", got)
	}
}

func TestAKdlBearerNeverReachesVerifyOrClient(t *testing.T) {
	r, stub := namedRig(t)
	for _, tok := range []string{testNamed, "kdl_never-issued"} {
		status(t, r, tok)
	}
	if len(stub.verified) != 0 {
		t.Errorf("reached the OAuth checks: %v", stub.verified)
	}
}

func TestNoNamedTokenCanBeTheUpkeepAuthor(t *testing.T) {
	// a name that would read as the upkeep still carries the token: prefix, which upkeepAuthor never has
	if "token:"+upkeepAuthor == upkeepAuthor || strings.TrimPrefix("token:"+upkeepAuthor, "token:") != upkeepAuthor {
		t.Fatal("the author of a token is not marked")
	}
	r := newRigWith(t, nil, func(c *Config) {
		c.UpkeepToken = testUpkeepToken
		c.Named = func(_ context.Context, tok string) (string, string, bool) {
			return upkeepAuthor, "cloud", tok == testNamed
		}
	})
	s, err := r.connect(t, testNamed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	id := taskID(mustCall(t, s, "tasks_save", map[string]any{"title": "Buy tea"}))
	hist, _ := r.store.History(ctx, "tasks/"+id+".md")
	for _, h := range hist {
		if h.By == upkeepAuthor {
			t.Errorf("a write is recorded as the upkeep: %+v", h)
		}
	}
}

func TestKnownPolicy(t *testing.T) {
	if !KnownPolicy("cloud") || KnownPolicy("owner") || KnownPolicy("") {
		t.Error("KnownPolicy")
	}
}
