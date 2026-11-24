package identity

import (
	"bytes"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"kedalio/core/db"
)

func TestNormalizeCodeAcceptsTheCodeInAnyCaseWithDashOrSpacesAndNothingElse(t *testing.T) {
	for in, want := range map[string]string{
		"4F7K-92QD":   "4F7K92QD",
		"4f7k92qd":    "4F7K92QD",
		" 4f7k 92qd ": "4F7K92QD",
		"4F7K-92Q":    "",
		"4F7K-92QDX":  "",
		"4F7K-92Q0":   "", // 0, O, 1, I and L are not in the alphabet: a typo must not land on another code
		"4F7K-92QO":   "",
		"4F7K-92Q1":   "",
		"4F7K-92QI":   "",
		"4F7K-92QL":   "",
		"4F7K_92QD":   "",
		"":            "",
	} {
		got, ok := normalizeCode(in)
		if got != want || ok != (want != "") {
			t.Errorf("normalizeCode(%q) = %q, %v", in, got, ok)
		}
	}
}

func TestANewCodeIsEightSymbolsOfTheAlphabetShownInTwoGroups(t *testing.T) {
	seen := map[string]bool{}
	for n := 0; n < 200; n++ {
		c := newApprovalCode()
		if len(c) != 9 || c[4] != '-' {
			t.Fatalf("code %q is not XXXX-XXXX", c)
		}
		if _, ok := normalizeCode(c); !ok {
			t.Fatalf("code %q does not normalize", c)
		}
		seen[c] = true
	}
	if len(seen) < 199 {
		t.Errorf("%d distinct codes of 200: not random", len(seen))
	}
}

const claudeRedirect = "https://claude.ai/api/mcp/auth_callback"

// approvalParams registers a client with the redirect and returns the authorization request it would send.
func approvalParams(t *testing.T, a *Auth, name, redirect string) url.Values {
	t.Helper()
	r, err := a.Register(ctx, map[string]any{"client_name": name, "redirect_uris": []any{redirect}})
	if err != nil || r.Status != 201 {
		t.Fatalf("register: %v %v", r, err)
	}
	return url.Values{"client_id": {r.JSON["client_id"].(string)}, "redirect_uri": {redirect}, "response_type": {"code"},
		"code_challenge": {strings.Repeat("a", 43)}, "code_challenge_method": {"S256"}, "state": {"st"}}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func testAuth(t *testing.T) (*Auth, *clock) {
	t.Helper()
	a := NewAuth(testDB(t), testURL)
	if err := Migrate(ctx, a.db); err != nil {
		t.Fatal(err)
	}
	c := &clock{time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)}
	a.Now = c.now
	return a, c
}

func TestAnApprovalGoesFromPendingToApprovedToDoneAndIssuesOneCode(t *testing.T) {
	a, _ := testAuth(t)
	p := approvalParams(t, a, "Claude", claudeRedirect)
	code, waiter, _, err := a.StartApproval(ctx, "oauth", p, "203.0.113.7", "agent/1")
	if err != nil {
		t.Fatal(err)
	}
	if st, to, err := a.PollApproval(ctx, waiter); err != nil || st != "pending" || to != "" {
		t.Fatalf("poll before: %q %q %v", st, to, err)
	}
	v, err := a.ShowApproval(ctx, strings.ToLower(code))
	if err != nil {
		t.Fatal(err)
	}
	if v.ClientName != "Claude" || v.Kind != "cloud: claude.ai" || v.RedirectHost != "claude.ai" || v.IP != "203.0.113.7" ||
		v.Agent != "agent/1" || v.State != "pending" || v.Purpose != "oauth" || v.Local {
		t.Errorf("view = %+v", v)
	}
	if v, err = a.Decide(ctx, code, true, "cli"); err != nil || v.State != "approved" {
		t.Fatalf("decide: %+v %v", v, err)
	}
	st, to, err := a.PollApproval(ctx, waiter)
	if err != nil || st != "approved" {
		t.Fatalf("poll after: %q %q %v", st, to, err)
	}
	u, _ := url.Parse(to)
	if u.Host != "claude.ai" || u.Query().Get("code") == "" || u.Query().Get("state") != "st" || u.Query().Get("iss") != testURL {
		t.Errorf("redirect = %s", to)
	}
	// single use: the second poll does not issue another code, and the code is spent for show and decide
	if st, to, _ := a.PollApproval(ctx, waiter); st != "expired" || to != "" {
		t.Errorf("second poll: %q %q", st, to)
	}
	if _, err := a.ShowApproval(ctx, code); !errors.Is(err, ErrNoApproval) {
		t.Errorf("show of a spent code: %v", err)
	}
	if _, err := a.Decide(ctx, code, true, "cli"); !errors.Is(err, ErrNoApproval) {
		t.Errorf("decide of a spent code: %v", err)
	}
	var n int
	if err := a.db.QueryRow("select count(*) from oauth_codes").Scan(&n); err != nil || n != 1 {
		t.Errorf("%d oauth codes", n)
	}
}

func TestADecidedApprovalCannotBeDecidedAgain(t *testing.T) {
	a, _ := testAuth(t)
	code, _, _, err := a.StartApproval(ctx, "oauth", approvalParams(t, a, "Claude", claudeRedirect), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Decide(ctx, code, false, "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Decide(ctx, code, true, "cli"); !errors.Is(err, ErrNoApproval) {
		t.Errorf("approving a denied request: %v", err)
	}
}

func TestADeniedApprovalSendsTheBrowserBackWithAccessDenied(t *testing.T) {
	a, _ := testAuth(t)
	code, waiter, _, _ := a.StartApproval(ctx, "oauth", approvalParams(t, a, "Claude", claudeRedirect), "", "")
	if _, err := a.Decide(ctx, code, false, "cli"); err != nil {
		t.Fatal(err)
	}
	st, to, err := a.PollApproval(ctx, waiter)
	if err != nil || st != "denied" {
		t.Fatalf("%q %v", st, err)
	}
	u, _ := url.Parse(to)
	if u.Query().Get("error") != "access_denied" || u.Query().Get("code") != "" {
		t.Errorf("redirect = %s", to)
	}
	var n int
	a.db.QueryRow("select count(*) from oauth_codes").Scan(&n)
	if n != 0 {
		t.Error("a denied request got a code")
	}
}

func TestAnApprovalExpiresAfterTenMinutesForShowDecideAndPoll(t *testing.T) {
	a, c := testAuth(t)
	code, waiter, exp, _ := a.StartApproval(ctx, "oauth", approvalParams(t, a, "Claude", claudeRedirect), "", "")
	if !exp.Equal(c.t.Add(10 * time.Minute)) {
		t.Errorf("expires %v", exp)
	}
	c.t = c.t.Add(10*time.Minute + time.Second)
	if _, err := a.ShowApproval(ctx, code); !errors.Is(err, ErrNoApproval) {
		t.Errorf("show: %v", err)
	}
	if _, err := a.Decide(ctx, code, true, "cli"); !errors.Is(err, ErrNoApproval) {
		t.Errorf("decide: %v", err)
	}
	if st, _, _ := a.PollApproval(ctx, waiter); st != "expired" {
		t.Errorf("poll: %q", st)
	}
}

func TestAnUnknownWaiterAndANonCodeAreRefused(t *testing.T) {
	a, _ := testAuth(t)
	if st, to, err := a.PollApproval(ctx, "nothing"); err != nil || st != "expired" || to != "" {
		t.Errorf("poll: %q %q %v", st, to, err)
	}
	if _, err := a.ShowApproval(ctx, "AAAA-AAAA"); !errors.Is(err, ErrNoApproval) {
		t.Errorf("unknown code: %v", err)
	}
	if _, err := a.ShowApproval(ctx, "0000-0000"); !errors.Is(err, ErrNotACode) {
		t.Errorf("non code: %v", err)
	}
}

func TestStartRefusesWhatCheckAuthorizeRefusesAndAnUnknownPurpose(t *testing.T) {
	a, _ := testAuth(t)
	p := approvalParams(t, a, "Claude", claudeRedirect)
	p.Set("redirect_uri", "https://evil.example/cb")
	var bad *InvalidRequestError
	if _, _, _, err := a.StartApproval(ctx, "oauth", p, "", ""); !errors.As(err, &bad) {
		t.Errorf("bad redirect: %v", err)
	}
	p = approvalParams(t, a, "Claude", claudeRedirect)
	p.Set("state", strings.Repeat("s", 3000))
	if _, _, _, err := a.StartApproval(ctx, "oauth", p, "", ""); !errors.As(err, &bad) {
		t.Errorf("huge state: %v", err)
	}
	if _, _, _, err := a.StartApproval(ctx, "other", p, "", ""); err == nil {
		t.Error("unknown purpose accepted")
	}
}

func TestThereAreAtMostTwentyLivePendingApprovalsThreeFromOneAddressAndTwoPasskeysAndADecidedOneFreesASlot(t *testing.T) {
	a, c := testAuth(t)
	p := approvalParams(t, a, "Claude", claudeRedirect)
	start := func(ip string) (string, error) {
		code, _, _, err := a.StartApproval(ctx, "oauth", p, ip, "")
		return code, err
	}
	// one address gets three, and the fourth does not cost anyone else a slot
	var first string
	for n := 0; n < MaxPendingPerIP; n++ {
		code, err := start("203.0.113.1")
		if err != nil {
			t.Fatalf("approval %d: %v", n, err)
		}
		if n == 0 {
			first = code
		}
	}
	if _, err := start("203.0.113.1"); !errors.Is(err, ErrTooManyFromAddress) {
		t.Fatalf("a fourth from one address: %v", err)
	}
	if _, err := a.Decide(ctx, first, false, "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := start("203.0.113.1"); err != nil {
		t.Errorf("after a decision: %v", err)
	}
	// other addresses fill the rest of the twenty, and one flooder never fills them alone
	for n := 3; n < MaxPendingApprovals; n++ {
		if _, err := start("198.51.100." + string(rune('0'+n%10)) + string(rune('0'+n/10))); err != nil {
			t.Fatalf("approval %d: %v", n, err)
		}
	}
	if _, err := start("192.0.2.1"); !errors.Is(err, ErrTooManyApprovals) {
		t.Fatalf("over the cap: %v", err)
	}
	c.t = c.t.Add(11 * time.Minute)
	if _, err := start("192.0.2.1"); err != nil {
		t.Errorf("after expiry: %v", err)
	}
}

func TestOnlyTwoPasskeyApprovalsWaitAtOnceWhoeverAsks(t *testing.T) {
	a, _ := testAuth(t)
	for n, ip := range []string{"203.0.113.1", "203.0.113.2"} {
		if _, _, _, err := a.StartApproval(ctx, PurposePasskey, nil, ip, ""); err != nil {
			t.Fatalf("passkey approval %d: %v", n, err)
		}
	}
	if _, _, _, err := a.StartApproval(ctx, PurposePasskey, nil, "203.0.113.3", ""); !errors.Is(err, ErrTooManyPasskeyApprovals) {
		t.Fatalf("a third passkey approval: %v", err)
	}
	// a client's approval is not held back by them
	if _, _, _, err := a.StartApproval(ctx, "oauth", approvalParams(t, a, "Claude", claudeRedirect), "203.0.113.3", ""); err != nil {
		t.Errorf("a client approval: %v", err)
	}
}

func TestPendingApprovalsAreListedWithoutTheCodeAndDenyAllRefusesThem(t *testing.T) {
	a, _ := testAuth(t)
	code, waiter, _, _ := a.StartApproval(ctx, "oauth", approvalParams(t, a, "Claude", claudeRedirect), "203.0.113.7", "x")
	decided, _, _, _ := a.StartApproval(ctx, "oauth", approvalParams(t, a, "Other", claudeRedirect), "203.0.113.8", "x")
	a.Decide(ctx, decided, true, "cli")
	a.StartApproval(ctx, PurposePasskey, nil, "203.0.113.9", "")
	list, err := a.PendingApprovals(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %v", list, err)
	}
	norm, _ := normalizeCode(code)
	for _, p := range list {
		if p.ID == "" || len(p.ID) > 8 || strings.Contains(toText(p), norm) || strings.Contains(toText(p), waiter) {
			t.Errorf("a listed request shows a secret or no id: %+v", p)
		}
	}
	if list[0].Kind == "" || list[0].Host == "" && list[0].Kind != "passkey" {
		t.Errorf("%+v", list[0])
	}
	n, err := a.DenyAllPending(ctx, "cli")
	if err != nil || n != 2 {
		t.Fatalf("deny all: %d %v", n, err)
	}
	if st, _, _ := a.PollApproval(ctx, waiter); st != "denied" {
		t.Errorf("poll after deny-all: %s", st)
	}
	if list, _ := a.PendingApprovals(ctx); len(list) != 0 {
		t.Errorf("still pending: %v", list)
	}
}

func toText(p PendingApproval) string {
	return p.ID + p.Purpose + p.Kind + p.ClientName + p.Host + p.IP
}

func TestACodeAndAWaiterAreStoredHashedNeverAsThemselves(t *testing.T) {
	a, _ := testAuth(t)
	code, waiter, _, _ := a.StartApproval(ctx, "oauth", approvalParams(t, a, "Claude", claudeRedirect), "", "")
	var h, w string
	if err := a.db.QueryRow("select hash, waiter from approvals").Scan(&h, &w); err != nil {
		t.Fatal(err)
	}
	norm, _ := normalizeCode(code)
	if h != hash(norm) || w != hash(waiter) || strings.Contains(h+w, norm) {
		t.Errorf("stored %q %q", h, w)
	}
}

func TestALocalClientIsShownAsALocalProgram(t *testing.T) {
	a, _ := testAuth(t)
	code, _, _, _ := a.StartApproval(ctx, "oauth", approvalParams(t, a, "Claude Code", "http://localhost:4567/callback"), "", "")
	v, err := a.ShowApproval(ctx, code)
	if err != nil || v.Kind != "local program" || !v.Local || v.RedirectHost != "localhost" {
		t.Errorf("%+v %v", v, err)
	}
}

func TestAClientWithALiveApprovalIsNotPrunedAndRevokingItDropsItsApprovals(t *testing.T) {
	a, c := testAuth(t)
	p := approvalParams(t, a, "Claude", claudeRedirect)
	if _, _, _, err := a.StartApproval(ctx, "oauth", p, "", ""); err != nil {
		t.Fatal(err)
	}
	// registered two hours ago and no token yet: only the live approval keeps it
	a.db.Exec("update oauth_clients set created = ?", c.t.Add(-2*time.Hour).UTC().Format(time.RFC3339))
	if _, err := a.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	a.db.QueryRow("select count(*) from oauth_clients").Scan(&n)
	if n != 1 {
		t.Fatal("the client of a live approval was pruned")
	}
	c.t = c.t.Add(11 * time.Minute)
	if _, err := a.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	a.db.QueryRow("select count(*) from oauth_clients").Scan(&n)
	var m int
	a.db.QueryRow("select count(*) from approvals").Scan(&m)
	if n != 0 || m != 0 {
		t.Errorf("after expiry: %d clients, %d approvals", n, m)
	}
	p = approvalParams(t, a, "Claude", claudeRedirect)
	a.StartApproval(ctx, "oauth", p, "", "")
	if ok, err := a.RevokeClient(ctx, p.Get("client_id")); err != nil || !ok {
		t.Fatal(ok, err)
	}
	a.db.QueryRow("select count(*) from approvals").Scan(&m)
	if m != 0 {
		t.Error("revoking left its approvals")
	}
}

func TestMigrationAddsApprovalsToADatabaseAtStepOne(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := db.Migrate(ctx, d, "identity", steps[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec("insert into oauth_clients values ('c', 'n', '[]', 'now'); insert into passkeys values ('p', 'n', 'k', 'now', null)"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow("select count(*) from approvals").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow("select count(*) from oauth_clients").Scan(&n); err != nil || n != 1 {
		t.Errorf("the old client is gone: %d %v", n, err)
	}
}

func TestTheCoreLogsWhoApprovedWhichClientButNeverTheCodeOrTheWaiter(t *testing.T) {
	var buf bytes.Buffer
	was := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(was)
	a, _ := testAuth(t)
	code, waiter, _, _ := a.StartApproval(ctx, "oauth", approvalParams(t, a, "Claude", claudeRedirect), "", "")
	a.Decide(ctx, code, true, "cli")
	a.PollApproval(ctx, waiter)
	norm, _ := normalizeCode(code)
	log := buf.String()
	if !strings.Contains(log, "client approved") || !strings.Contains(log, "name=Claude") || !strings.Contains(log, "by=cli") {
		t.Errorf("the approval is not logged: %q", log)
	}
	if strings.Contains(log, norm) || strings.Contains(log, code) || strings.Contains(log, waiter) {
		t.Errorf("a secret reached the log: %q", log)
	}
}
