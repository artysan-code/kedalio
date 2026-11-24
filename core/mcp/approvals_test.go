package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kedalio/core/db"
	"kedalio/core/projects"
	"kedalio/core/upkeep"
)

// fakeApprovals knows one code, "AAAA-AAAA", with the view given, and records who decided.
type fakeApprovals struct {
	view    ApprovalView
	decided []string
}

func (f *fakeApprovals) Show(_ context.Context, code string) (ApprovalView, error) {
	if code != "AAAA-AAAA" || f.view.State != "pending" {
		return ApprovalView{}, ErrNoApproval
	}
	return f.view, nil
}

func (f *fakeApprovals) Approve(ctx context.Context, code, by string) error {
	if _, err := f.Show(ctx, code); err != nil {
		return err
	}
	f.decided = append(f.decided, by)
	f.view.State = "approved"
	return nil
}

func localView() ApprovalView {
	return ApprovalView{Purpose: "oauth", ClientName: "Claude Code", Kind: "local program", RedirectHost: "localhost",
		Local: true, IP: "127.0.0.1", Age: 90 * time.Second, State: "pending"}
}

// approvalRig is cloudRig with the approvals on.
func approvalRig(t *testing.T, f *fakeApprovals) (r *rig, cloud, local, up *sdk.ClientSession) {
	t.Helper()
	r = newRigWith(t, nil, func(c *Config) {
		d, err := db.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		if err := projects.Migrate(ctx, d); err != nil {
			t.Fatal(err)
		}
		if err := upkeep.Migrate(ctx, d); err != nil {
			t.Fatal(err)
		}
		c.Ops = &Ops{Projects: projects.New(d)}
		c.Upkeep = &Upkeep{DB: d}
		c.UpkeepToken = testUpkeepToken
		c.Approvals = f
		// the test server is reached from 127.0.0.1: that is the caller's address
		c.ClientIP = func(r *http.Request) string {
			h, _, _ := net.SplitHostPort(r.RemoteAddr)
			return h
		}
		c.Verify = func(_ context.Context, tok string) bool { return tok == testCloudToken || tok == testLocalToken }
		c.Client = func(_ context.Context, tok string) (string, bool) {
			switch tok {
			case testCloudToken:
				return "Claude", true
			case testLocalToken:
				return "Claude Code", false
			}
			return "", false
		}
	})
	var ss []*sdk.ClientSession
	for _, tok := range []string{testCloudToken, testLocalToken, testUpkeepToken} {
		s, err := r.connect(t, tok)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		ss = append(ss, s)
	}
	return r, ss[0], ss[1], ss[2]
}

func TestClientsApproveShowsWhoAsksAndApprovesOnlyOnConfirmAndNeverEchoesTheCode(t *testing.T) {
	f := &fakeApprovals{view: localView()}
	r, _, local, _ := approvalRig(t, f)
	owner := r.session(t)

	for _, s := range []*sdk.ClientSession{owner, local} {
		f.view, f.decided = localView(), nil
		out, failed := call(t, s, "clients_approve", map[string]any{"code": "AAAA-AAAA"})
		if failed || out["approved"] != false || out["client"] != "Claude Code" || out["kind"] != "local program" ||
			out["host"] != "localhost" || out["age_seconds"] != float64(90) {
			t.Fatalf("first call: %v", out)
		}
		if len(f.decided) != 0 {
			t.Fatal("a call without confirm approved")
		}
		if out["browser"] != nil || out["asked_from"] != nil {
			t.Errorf("the requester's browser or address is in the result: %v", out)
		}
		if next, _ := out["next"].(string); !strings.Contains(next, "confirm") {
			t.Errorf("no instruction for the second call: %v", out)
		}
		out, failed = call(t, s, "clients_approve", map[string]any{"code": "AAAA-AAAA", "confirm": true, "host": "localhost"})
		if failed || out["approved"] != true || len(f.decided) != 1 {
			t.Fatalf("second call: %v %v", out, f.decided)
		}
		for _, v := range out {
			if str, ok := v.(string); ok && strings.Contains(str, "AAAA") {
				t.Errorf("the code came back: %v", out)
			}
		}
	}
	if f.decided[0] != "oauth:Claude Code" {
		t.Errorf("decided by %q", f.decided[0])
	}
	// the owner's static token is recorded as the server's owner author
	f.view, f.decided = localView(), nil
	mustCall(t, owner, "clients_approve", map[string]any{"code": "AAAA-AAAA", "confirm": true, "host": "LocalHost"})
	if f.decided[0] != "owner" {
		t.Errorf("owner decided as %q", f.decided[0])
	}
}

func TestClientsApproveRefusesACloudClientsCodeAPasskeyAndAWrongCodeWithoutDeciding(t *testing.T) {
	f := &fakeApprovals{view: localView()}
	r, _, _, _ := approvalRig(t, f)
	owner := r.session(t)
	for name, c := range map[string]struct {
		view ApprovalView
		code string
		want string
	}{
		"cloud":   {ApprovalView{Purpose: "oauth", ClientName: "Claude", Kind: "cloud: claude.ai", RedirectHost: "claude.ai", State: "pending"}, "AAAA-AAAA", "kedalio-core approve"},
		"passkey": {ApprovalView{Purpose: "passkey", Kind: "passkey", State: "pending"}, "AAAA-AAAA", "kedalio-core approve"},
		"unknown": {localView(), "BBBB-BBBB", "no such code"},
		"spent":   {ApprovalView{Purpose: "oauth", Local: true, State: "approved"}, "AAAA-AAAA", "no such code"},
	} {
		f.view, f.decided = c.view, nil
		for _, confirm := range []bool{false, true} {
			out, failed := call(t, owner, "clients_approve", map[string]any{"code": c.code, "confirm": confirm})
			if !failed || !strings.Contains(strings.ToLower(out["errors"].([]any)[0].(map[string]any)["problem"].(string)+out["errors"].([]any)[0].(map[string]any)["fix"].(string)), c.want) {
				t.Errorf("%s (confirm %v): %v", name, confirm, out)
			}
			if strings.Contains(strings.ToLower(toJSON(out)), strings.ToLower(c.code)) {
				t.Errorf("%s: the refusal quotes the code: %v", name, out)
			}
		}
		if len(f.decided) != 0 {
			t.Errorf("%s: approved", name)
		}
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestClientsApproveIsNotListedToACloudClientAndNotCallableByItOrTheUpkeep(t *testing.T) {
	f := &fakeApprovals{view: localView()}
	r, cloud, local, up := approvalRig(t, f)
	for name, s := range map[string]*sdk.ClientSession{"cloud": cloud, "upkeep": up} {
		// the upkeep token is shown every tool, as for the others, and refused when it calls one it may not
		if name == "cloud" && slices.Contains(listedTools(t, s), "clients_approve") {
			t.Errorf("%s lists clients_approve", name)
		}
		if res, err := s.CallTool(ctx, &sdk.CallToolParams{Name: "clients_approve", Arguments: map[string]any{"code": "AAAA-AAAA", "confirm": true}}); err == nil && !res.IsError {
			t.Errorf("%s approved a code", name)
		}
	}
	for _, p := range []*policy{cloudPolicy, upkeepPolicy} {
		if no := p.allowed("clients_approve"); no == nil {
			t.Errorf("%s: the policy allows it", p.who)
		}
	}
	if !slices.Contains(listedTools(t, local), "clients_approve") || !slices.Contains(listedTools(t, r.session(t)), "clients_approve") {
		t.Error("a local client or the owner does not list clients_approve")
	}
	if len(f.decided) != 0 {
		t.Error("something was decided")
	}
}

func TestClientsApproveIsAbsentWithoutApprovalsAndAnnotatedDestructive(t *testing.T) {
	if slices.Contains(listedTools(t, newRig(t, nil).session(t)), "clients_approve") {
		t.Error("a server with no OAuth offers clients_approve")
	}
	r, _, _, _ := approvalRig(t, &fakeApprovals{view: localView()})
	res, err := r.session(t).ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if tl.Name == "clients_approve" {
			a := tl.Annotations
			if a == nil || a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint {
				t.Errorf("annotations %+v", a)
			}
			if !strings.Contains(tl.Description, "owner said yes") {
				t.Errorf("description: %q", tl.Description)
			}
			return
		}
	}
	t.Error("clients_approve not listed")
}

func TestAnUnexpectedApprovalsFailureIsTheCoresAndNotTheClients(t *testing.T) {
	f := &failingApprovals{}
	r, _, _, _ := approvalRig(t, &fakeApprovals{view: localView()})
	r.srv.cfg.Approvals = f
	out, failed := call(t, r.session(t), "clients_approve", map[string]any{"code": "AAAA-AAAA"})
	if !failed || strings.Contains(toJSON(out), "disk") {
		t.Errorf("%v", out)
	}
}

type failingApprovals struct{}

func (failingApprovals) Show(context.Context, string) (ApprovalView, error) {
	return ApprovalView{}, errors.New("disk is on fire")
}
func (failingApprovals) Approve(context.Context, string, string) error { return nil }

func TestClientsApproveRefusesARequestFromAnotherAddressAndAConfirmWithoutTheHost(t *testing.T) {
	f := &fakeApprovals{view: localView()}
	r, _, _, _ := approvalRig(t, f)
	owner := r.session(t)
	// a local program somebody else runs: its redirect is loopback too, but it asked from another address
	for name, ip := range map[string]string{"another address": "203.0.113.7", "no address": ""} {
		f.view, f.decided = localView(), nil
		f.view.IP = ip
		for _, confirm := range []bool{false, true} {
			out, failed := call(t, owner, "clients_approve", map[string]any{"code": "AAAA-AAAA", "confirm": confirm, "host": "localhost"})
			if !failed || !strings.Contains(toJSON(out), "kedalio-core approve") {
				t.Errorf("%s (confirm %v): %v", name, confirm, out)
			}
			if strings.Contains(toJSON(out), "203.0.113.7") {
				t.Errorf("%s: the refusal shows the requester's address: %v", name, out)
			}
		}
		if len(f.decided) != 0 {
			t.Errorf("%s: approved", name)
		}
	}
	// the same address, but the confirm does not repeat the host the prompt must show
	f.view, f.decided = localView(), nil
	for _, host := range []string{"", "evil.example"} {
		out, failed := call(t, owner, "clients_approve", map[string]any{"code": "AAAA-AAAA", "confirm": true, "host": host})
		if !failed || !strings.Contains(toJSON(out), "host") {
			t.Errorf("host %q: %v", host, out)
		}
	}
	if len(f.decided) != 0 {
		t.Error("approved with a wrong host")
	}
}
