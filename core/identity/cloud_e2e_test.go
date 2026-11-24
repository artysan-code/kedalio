package identity

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectAs registers a client with one redirect, takes it through the owner's consent and the code exchange, and
// answers its access token: what claude.ai or Claude Code does to add the connector.
func (r *rig) connectAs(t *testing.T, name, redirect string) string {
	t.Helper()
	anon := &http.Client{CheckRedirect: r.owner.CheckRedirect}
	res, client := r.postJSON(t, anon, "/oauth/register", map[string]any{"redirect_uris": []string{redirect}, "client_name": name}, "Sec-Fetch-Site", "cross-site")
	id, _ := client["client_id"].(string)
	if res.StatusCode != 201 || id == "" {
		t.Fatalf("register %s: %d %v", name, res.StatusCode, client)
	}
	verifier := randomToken(32)
	agree := url.Values{"client_id": {id}, "redirect_uri": {redirect}, "response_type": {"code"}, "state": {"s"},
		"code_challenge": {pkce(t, verifier)}, "code_challenge_method": {"S256"}, "resource": {r.srv.URL + "/mcp"}, "decision": {"allow"}}
	res = r.postForm(t, r.owner, "/oauth/authorize", agree)
	back, _ := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != 302 || back.Scheme+"://"+back.Host+back.Path != redirect {
		t.Fatalf("consent for %s: %d %v", name, res.StatusCode, back)
	}
	res = r.postForm(t, anon, "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")},
		"client_id": {id}, "redirect_uri": {redirect}, "code_verifier": {verifier}}, "Sec-Fetch-Site", "cross-site")
	tok := readJSONBody(t, res)
	access, _ := tok["access_token"].(string)
	if res.StatusCode != 200 || access == "" {
		t.Fatalf("token for %s: %d %v", name, res.StatusCode, tok)
	}
	return access
}

func toolsOf(t *testing.T, s *sdk.ClientSession) []string {
	t.Helper()
	res, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	return names
}

// The connector added on claude.ai (its callback is the client's only redirect) gets the cloud policy over real
// requests: a short tools/list, its memory writes refused but for the inbox, marked there. Claude Code, through a
// loopback redirect, is the owner as before.
func TestAClientRegisteredWithClaudeAisCallbackGetsTheCloudPolicy(t *testing.T) {
	r := newRig(t)
	r.setupPasskey(t)
	cloud := r.connectAs(t, "Claude", "https://claude.ai/api/mcp/auth_callback")
	local := r.connectAs(t, "Claude Code", "http://127.0.0.1:43210/callback")

	cs, err := r.mcpClient(t, cloud)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	ls, err := r.mcpClient(t, local)
	if err != nil {
		t.Fatal(err)
	}
	defer ls.Close()

	// this rig has no settings or projects (no Ops): the upkeep's and the memory's own tools are the ones to hide
	cloudTools, localTools := toolsOf(t, cs), toolsOf(t, ls)
	for _, hidden := range []string{"memory_delete", "memory_sources", "memory_upkeep"} {
		if slices.Contains(cloudTools, hidden) || !slices.Contains(localTools, hidden) {
			t.Errorf("%s: listed to the cloud %v, to the local client %v", hidden, slices.Contains(cloudTools, hidden), slices.Contains(localTools, hidden))
		}
	}
	for _, c := range cloudTools {
		if !slices.Contains(localTools, c) {
			t.Errorf("the cloud lists %s, the local client does not", c)
		}
	}
	if len(localTools)-len(cloudTools) != 3 {
		t.Errorf("cloud lists %d tools, local %d", len(cloudTools), len(localTools))
	}

	write := func(s *sdk.ClientSession, args map[string]any) (string, bool) {
		res, err := s.CallTool(ctx, &sdk.CallToolParams{Name: "memory_write", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		text := res.Content[0].(*sdk.TextContent).Text
		return text, res.IsError || strings.Contains(text, `"refused":true`)
	}
	page := map[string]any{"op": "write", "path": "notes/from-cloud.md", "body": "# From cloud\n\nWritten in a claude.ai chat.\n"}
	if text, refused := write(cs, page); !refused || !strings.Contains(text, "not for a cloud client") {
		t.Errorf("a page write from the cloud: %s", text)
	}
	if d, _ := r.store.Get(ctx, "notes/from-cloud.md"); d != nil {
		t.Error("the cloud client wrote a page")
	}
	if text, refused := write(cs, map[string]any{"op": "append", "where": "inbox", "text": "Alice moved the meeting to Friday"}); refused {
		t.Fatalf("an inbox append from the cloud: %s", text)
	}
	inbox, _ := r.store.Get(ctx, "inbox/inbox.md")
	if inbox == nil || !strings.Contains(inbox.Body, "why: cloud: from Claude, waits for the owner") {
		t.Fatalf("the inbox entry is not marked: %+v", inbox)
	}
	if text, refused := write(ls, map[string]any{"op": "write", "path": "notes/from-cli.md", "body": "# From CLI\n\nWritten in Claude Code.\n"}); refused {
		t.Errorf("a page write from the local client: %s", text)
	}
	if h, _ := r.store.History(ctx, "notes/from-cli.md"); len(h) != 1 || h[0].By != "oauth:Claude Code" {
		t.Errorf("recorded by: %v", h)
	}
}
