package instance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kedalio/core/identity"
)

type bearerAs string

func (b bearerAs) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

func TestTheOwnersTokenApprovesALocalClientsCodeThroughTheToolAndACloudCodeIsRefused(t *testing.T) {
	o := testOptions(t.TempDir())
	o.PublicURL = "https://core.example.com"
	ctx, stop := context.WithCancel(context.Background())
	inst, err := o.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// the context ends first: Close waits for what runs under it
	defer func() {
		stop()
		inst.Close()
	}()
	srv := httptest.NewServer(inst.Handler)
	defer srv.Close()

	auth := identity.NewAuth(inst.DB, o.PublicURL)
	start := func(redirect string) string {
		r, err := auth.Register(ctx, map[string]any{"client_name": "Claude Code", "redirect_uris": []any{redirect}})
		if err != nil || r.Status != 201 {
			t.Fatalf("register: %v %v", r, err)
		}
		code, _, _, err := auth.StartApproval(ctx, "oauth", url.Values{"client_id": {r.JSON["client_id"].(string)},
			"redirect_uri": {redirect}, "response_type": {"code"}, "code_challenge": {strings.Repeat("a", 43)},
			"code_challenge_method": {"S256"}}, "127.0.0.1", "Firefox/1")
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	c := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	s, err := c.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: srv.URL + "/mcp", DisableStandaloneSSE: true,
		HTTPClient: &http.Client{Transport: bearerAs(o.Token)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	say := func(code string, confirm bool, host string) (map[string]any, bool) {
		res, err := s.CallTool(ctx, &sdk.CallToolParams{Name: "clients_approve", Arguments: map[string]any{"code": code, "confirm": confirm, "host": host}})
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.Unmarshal([]byte(res.Content[0].(*sdk.TextContent).Text), &out)
		return out, res.IsError
	}

	local := start("http://127.0.0.1:5000/cb")
	if out, failed := say(local, false, ""); failed || out["approved"] != false || out["kind"] != "local program" {
		t.Fatalf("show: %v", out)
	}
	if v, _ := auth.ShowApproval(ctx, local); v.State != "pending" {
		t.Fatalf("shown, state %s", v.State)
	}
	if out, failed := say(local, true, "127.0.0.1"); failed || out["approved"] != true {
		t.Fatalf("approve: %v", out)
	}
	if v, _ := auth.ShowApproval(ctx, local); v.State != "approved" {
		t.Errorf("state after approve: %s", v.State)
	}
	if _, failed := say(local, true, "127.0.0.1"); !failed {
		t.Error("approving twice")
	}
	if _, failed := say("0000-0000", true, "x"); !failed {
		t.Error("a non-code was approved")
	}
	// a local client started from another address is not approved from this one, however local its redirect is
	asked := start("http://127.0.0.1:5001/cb")
	auth.Decide(ctx, asked, false, "x") // free the slot; the start helper asks from 127.0.0.1, so change the row's address
	other := start("http://127.0.0.1:5002/cb")
	if _, err := inst.DB.Exec("update approvals set ip = '203.0.113.7' where state = 'pending'"); err != nil {
		t.Fatal(err)
	}
	if _, failed := say(other, true, "127.0.0.1"); !failed {
		t.Error("a request from another address was approved")
	}
	cloud := start("https://claude.ai/api/mcp/auth_callback")
	if _, failed := say(cloud, true, "claude.ai"); !failed {
		t.Error("a cloud client's code was approved from a tool")
	}
	if v, _ := auth.ShowApproval(ctx, cloud); v.State != "pending" {
		t.Errorf("cloud code state %s", v.State)
	}
}
