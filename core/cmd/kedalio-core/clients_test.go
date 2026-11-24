package main

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kedalio/core/db"
	"kedalio/core/identity"
	"kedalio/core/vault"
)

// seedClients adds a cloud and a local client with a token each to the database of a data folder a core made.
func seedClients(t *testing.T, dir string) {
	t.Helper()
	raw, err := vault.HostKey(dir, false)
	if err != nil || raw == nil {
		t.Fatalf("key: %v %v", raw, err)
	}
	key, _ := vault.DatabaseKey(raw)
	d, err := db.OpenEncrypted(filepath.Join(dir, "core.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// the test core has no -public-url, so it never made the identity's tables
	if err := identity.Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`insert into oauth_clients values ('cid1', 'Claude', '["https://claude.ai/api/mcp/auth_callback"]', '2026-11-01T10:00:00Z')`,
		`insert into oauth_clients values ('cid3', 'Evil' || char(27) || '[2J' || char(10) || 'FAKE', '["http://127.0.0.1:5001/cb"]', '2026-11-02T11:00:00Z')`,
		`insert into oauth_clients values ('cid2', 'Claude Code', '["http://127.0.0.1:5000/cb"]', '2026-11-02T10:00:00Z')`,
		`insert into tokens (hash, kind, client, family, expires, created, used) values ('secret-hash-1', 'access', 'cid1', 'f', 99999999999999, '2026-11-01T10:00:00Z', '2026-11-03T08:00:00Z')`,
		`insert into tokens (hash, kind, client, family, expires, created) values ('secret-hash-2', 'refresh', 'cid1', 'f', 99999999999999, '2026-11-01T10:00:00Z')`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClientsListShowsEachClientWithoutTokenValuesAndRevokeRemovesOne(t *testing.T) {
	dir := t.TempDir()
	started(t, testOptions(dir))
	seedClients(t, dir)
	ctx := context.Background()

	var out bytes.Buffer
	if err := runClients(ctx, []string{"-data", dir, "list"}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"cid1", "Claude", "claude.ai", "cloud", "2026-11-03T08:00:00Z", "cid2", "local", "127.0.0.1", "never"} {
		if !strings.Contains(got, want) {
			t.Errorf("the list lacks %q:\n%s", want, got)
		}
	}
	if strings.ContainsAny(got, "\x1b\r") || strings.Count(got, "\n") != 4 {
		t.Errorf("a client name's control characters reached the terminal:\n%q", got)
	}
	if strings.Contains(got, "secret-hash") {
		t.Errorf("the list shows a token value:\n%s", got)
	}

	out.Reset()
	if err := runClients(ctx, []string{"-data", dir, "revoke", "cid1"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runClients(ctx, []string{"-data", dir, "list"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "cid1") || !strings.Contains(out.String(), "cid2") {
		t.Errorf("after revoking cid1:\n%s", out.String())
	}
	if err := runClients(ctx, []string{"-data", dir, "revoke", "cid1"}, &out); err == nil {
		t.Error("revoking an unknown client succeeded")
	}
}

func TestClientsRefusesBadUsageAndAFolderWithoutAKey(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{{"list", "x"}, {"revoke"}, {"nope"}, {}} {
		if err := runClients(context.Background(), append([]string{"-data", t.TempDir()}, args...), &out); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if err := runClients(context.Background(), []string{"-data", t.TempDir(), "list"}, &out); err == nil {
		t.Error("a folder with no key opened")
	}
}

// With a key but no database (a wrong -data, a volume mid-restore) opening would create an empty core.db that makes
// the next start skip the restore and the unlock, so the command refuses and creates nothing.
func TestClientsRefusesAFolderWithAKeyButNoDatabase(t *testing.T) {
	dir := t.TempDir()
	if _, err := vault.HostKey(dir, true); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runClients(context.Background(), []string{"-data", dir, "list"}, &out); err == nil {
		t.Error("a folder with no database was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "core.db")); err == nil {
		t.Error("the command created core.db")
	}
}

// pendingApproval registers a client and starts an approval for it on the database of a data folder, as the sign-in
// page does, and returns the code.
func pendingApproval(t *testing.T, dir, name, redirect string) (code string, a *identity.Auth) {
	t.Helper()
	d, err := openData(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ctx := context.Background()
	if err := identity.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	a = identity.NewAuth(d, "https://core.example.com")
	r, err := a.Register(ctx, map[string]any{"client_name": name, "redirect_uris": []any{redirect}})
	if err != nil || r.Status != 201 {
		t.Fatalf("register: %v %v", r, err)
	}
	code, _, _, err = a.StartApproval(ctx, "oauth", url.Values{"client_id": {r.JSON["client_id"].(string)}, "redirect_uri": {redirect},
		"response_type": {"code"}, "code_challenge": {strings.Repeat("a", 43)}, "code_challenge_method": {"S256"}}, "203.0.113.7", "Firefox/1")
	if err != nil {
		t.Fatal(err)
	}
	return code, a
}

// CI runs the tests as root, which approve refuses: the tests run as an ordinary user unless one says otherwise.
func init() { geteuid = func() int { return 1000 } }

func approveWith(dir string, in string, tty bool, args ...string) (string, error) {
	var out bytes.Buffer
	err := runApprove(context.Background(), append([]string{"-data", dir}, args...), strings.NewReader(in), &out, tty)
	return out.String(), err
}

func TestApproveShowsTheRequestAsksAndApprovesOnYes(t *testing.T) {
	dir := t.TempDir()
	started(t, testOptions(dir))
	code, a := pendingApproval(t, dir, "Evil"+string(rune(27))+"[2J Claude", "https://claude.ai/api/mcp/auth_callback")

	// anything but yes leaves it pending
	got, err := approveWith(dir, "n\n", true, strings.ToLower(code))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cloud: claude.ai", "claude.ai", "203.0.113.7", "Firefox/1", "self-declared", "Approve? [y/N]"} {
		if !strings.Contains(got, want) {
			t.Errorf("the request is shown without %q:\n%s", want, got)
		}
	}
	if strings.ContainsAny(got, "\x1b\r") || strings.Contains(got, code) {
		t.Errorf("a control character or the code reached the output:\n%q", got)
	}
	if v, _ := a.ShowApproval(context.Background(), code); v.State != "pending" {
		t.Fatalf("a no approved it: %v", v.State)
	}

	got, err = approveWith(dir, "y\n", true, code)
	if err != nil || !strings.Contains(got, "approved") || strings.Contains(got, code) {
		t.Fatalf("yes: %q %v", got, err)
	}
	if v, _ := a.ShowApproval(context.Background(), code); v.State != "approved" {
		t.Errorf("state after yes: %v", v.State)
	}
	// once: the code is of no use again
	if _, err := approveWith(dir, "y\n", true, code); err == nil || !strings.Contains(err.Error(), "no such approval") {
		t.Errorf("second approval: %v", err)
	}
}

func TestApproveWithoutATerminalNeedsYesAndDenyNeedsNoConfirmation(t *testing.T) {
	dir := t.TempDir()
	started(t, testOptions(dir))
	code, a := pendingApproval(t, dir, "Claude Code", "http://127.0.0.1:5000/cb")
	if _, err := approveWith(dir, "y\n", false, code); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Errorf("no terminal and no -yes: %v", err)
	}
	if v, _ := a.ShowApproval(context.Background(), code); v.State != "pending" {
		t.Fatal("approved with no terminal and no -yes")
	}
	if _, err := approveWith(dir, "", false, "-yes", code); err != nil {
		t.Fatal(err)
	}
	if v, _ := a.ShowApproval(context.Background(), code); v.State != "approved" {
		t.Error("-yes did not approve")
	}
	code2, a2 := pendingApproval(t, dir, "Other", "http://127.0.0.1:5000/cb")
	if out, err := approveWith(dir, "", false, "-deny", code2); err != nil || !strings.Contains(out, "denied") {
		t.Fatalf("deny: %q %v", out, err)
	}
	if _, err := a2.Decide(context.Background(), code2, true, "x"); err == nil {
		t.Error("a denied code was approved afterwards")
	}
}

func TestApproveRefusesUnknownCodesBadUsageAndRoot(t *testing.T) {
	dir := t.TempDir()
	started(t, testOptions(dir))
	seedClients(t, dir)
	for _, c := range []struct{ arg, want string }{{"AAAA-AAAA", "no such approval"}, {"0000-0000", "not an approval code"}} {
		if _, err := approveWith(dir, "y\n", true, c.arg); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.arg, err)
		}
	}
	for _, args := range [][]string{{}, {"A", "B"}} {
		if _, err := approveWith(dir, "", true, args...); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("%v: %v", args, err)
		}
	}
	was := geteuid
	geteuid = func() int { return 0 }
	defer func() { geteuid = was }()
	if _, err := approveWith(dir, "y\n", true, "AAAA-AAAA"); err == nil || !strings.Contains(err.Error(), "root") {
		t.Errorf("as root: %v", err)
	}
}

func TestApproveListShowsWaitingRequestsWithoutTheirCodesAndDenyAllRefusesThem(t *testing.T) {
	dir := t.TempDir()
	started(t, testOptions(dir))
	code, a := pendingApproval(t, dir, "Claude Code", "http://127.0.0.1:5000/cb")
	out, err := approveWith(dir, "", false, "-list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "Claude Code", "local program", "127.0.0.1", "203.0.113.7"} {
		if !strings.Contains(out, want) {
			t.Errorf("the list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, code) || strings.Contains(out, strings.ReplaceAll(code, "-", "")) {
		t.Errorf("the list shows the code:\n%s", out)
	}
	if out, err := approveWith(dir, "", false, "-deny-all"); err != nil || !strings.Contains(out, "denied 1") {
		t.Fatalf("deny-all: %q %v", out, err)
	}
	if _, err := a.Decide(context.Background(), code, true, "x"); err == nil {
		t.Error("a code approved after deny-all")
	}
	if out, _ := approveWith(dir, "", false, "-list"); !strings.Contains(out, "no waiting") {
		t.Errorf("empty list: %q", out)
	}
	if _, err := approveWith(dir, "", false, "-list", "AAAA-AAAA"); err == nil {
		t.Error("-list took a code")
	}
}

func TestPasskeysListShowsThemAndRemoveTakesOneAwayAndSignsEveryoneOut(t *testing.T) {
	dir := t.TempDir()
	started(t, testOptions(dir))
	seedClients(t, dir)
	d, err := openData(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`insert into passkeys (id, name, public_key, created) values ('credential-id-one-aaaaaaaaaaaaaaaaaaaaaaaa', 'laptop', 'pk1', '2026-11-01T10:00:00Z')`,
		`insert into passkeys (id, name, public_key, created) values ('credential-id-two-bbbbbbbbbbbbbbbbbbbbbbbb', 'phone' || char(27) || '[2J', 'pk2', '2026-11-02T10:00:00Z')`,
		`insert into sessions (id, hash, created, used, expires, ends) values ('s', 'h', 'x', 'x', 1, 1)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()
	var out bytes.Buffer
	ctx := context.Background()
	if err := runPasskeys(ctx, []string{"-data", dir, "list"}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"credential-id-one", "laptop", "phone", "2026-11-02T10:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("the list lacks %q:\n%s", want, got)
		}
	}
	if strings.ContainsAny(got, "\x1b\r") || strings.Contains(got, "pk1") {
		t.Errorf("a control character or key material reached the terminal:\n%q", got)
	}
	out.Reset()
	if err := runPasskeys(ctx, []string{"-data", dir, "remove", "credential-id-one"}, &out); err != nil || !strings.Contains(out.String(), "browser out") {
		t.Fatalf("remove: %q %v", out.String(), err)
	}
	out.Reset()
	runPasskeys(ctx, []string{"-data", dir, "list"}, &out)
	if strings.Contains(out.String(), "laptop") || !strings.Contains(out.String(), "phone") {
		t.Errorf("after the removal:\n%s", out.String())
	}
	if err := runPasskeys(ctx, []string{"-data", dir, "remove", "credential-id-one"}, &out); err == nil {
		t.Error("removing a removed passkey succeeded")
	}
	for _, args := range [][]string{{}, {"list", "x"}, {"remove"}, {"nope"}} {
		if err := runPasskeys(ctx, append([]string{"-data", dir}, args...), &out); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("%v: %v", args, err)
		}
	}
}
