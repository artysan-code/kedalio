package identity

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// approvedPasskey starts a passkey approval, has it approved as the CLI would, and returns the waiter and the creation
// options the poll gave.
func approvedPasskey(t *testing.T, i *Identity) (waiter string, options map[string]any) {
	t.Helper()
	code, waiter, _, err := i.Auth.StartApproval(ctx, PurposePasskey, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.Auth.Decide(ctx, code, true, "cli"); err != nil {
		t.Fatal(err)
	}
	state, to, err := i.Auth.PollApproval(ctx, waiter)
	if err != nil || state != "approved" || to != "" {
		t.Fatalf("poll: %q %q %v", state, to, err)
	}
	options, err = i.Passkeys.CreationOptions(ctx, i.opt.Name)
	if err != nil {
		t.Fatal(err)
	}
	return waiter, options
}

func TestAPasskeyIsMadeOnlyAfterAnApprovalAndTheApprovalIsSpent(t *testing.T) {
	i := testIdentity(t)
	phone := newAuthenticator()
	code, waiter, _, _ := i.Auth.StartApproval(ctx, PurposePasskey, nil, "", "")
	opts, _ := i.Passkeys.CreationOptions(ctx, "Alice")
	reg := phone.register(opts["challenge"].(string), regOver{})
	// not approved yet, or not the waiter of any approval
	if err := i.passkeyAccept(ctx, waiter, reg, "x"); err == nil {
		t.Fatal("a passkey was made with the approval still pending")
	}
	if err := i.passkeyAccept(ctx, "nothing", reg, "x"); err == nil {
		t.Fatal("a passkey was made with no approval")
	}
	if n, _ := i.Passkeys.Count(ctx); n != 0 {
		t.Fatal("a passkey exists")
	}
	if _, err := i.Auth.Decide(ctx, code, true, "cli"); err != nil {
		t.Fatal(err)
	}
	// a registration the server did not ask for does not spend the approval
	if err := i.passkeyAccept(ctx, waiter, phone.register("not-asked", regOver{}), "x"); err == nil {
		t.Error("a registration over an unknown challenge was accepted")
	}
	opts, _ = i.Passkeys.CreationOptions(ctx, "Alice")
	if err := i.passkeyAccept(ctx, waiter, phone.register(opts["challenge"].(string), regOver{}), "laptop"); err != nil {
		t.Fatal(err)
	}
	if n, _ := i.Passkeys.Count(ctx); n != 1 {
		t.Errorf("%d passkeys", n)
	}
	opts, _ = i.Passkeys.CreationOptions(ctx, "Alice")
	if err := i.passkeyAccept(ctx, waiter, newAuthenticator().register(opts["challenge"].(string), regOver{}), "again"); err == nil {
		t.Error("the spent approval made a second passkey")
	}
	// an approval for a client is no approval for a passkey
	i2 := testIdentity(t)
	p := approvalParams(t, i2.Auth, "Claude", claudeRedirect)
	c2, w2, _, _ := i2.Auth.StartApproval(ctx, PurposeOAuth, p, "", "")
	i2.Auth.Decide(ctx, c2, true, "cli")
	opts, _ = i2.Passkeys.CreationOptions(ctx, "Alice")
	if err := i2.passkeyAccept(ctx, w2, newAuthenticator().register(opts["challenge"].(string), regOver{}), "x"); err == nil {
		t.Error("a client's approval made a passkey")
	}
}

func TestASecondPasskeyNeedsItsOwnApproval(t *testing.T) {
	i := testIdentity(t)
	for n := 0; n < 2; n++ {
		w, opts := approvedPasskey(t, i)
		if err := i.passkeyAccept(ctx, w, newAuthenticator().register(opts["challenge"].(string), regOver{}), "p"); err != nil {
			t.Fatalf("passkey %d: %v", n+1, err)
		}
	}
	if n, _ := i.Passkeys.Count(ctx); n != 2 {
		t.Errorf("%d passkeys", n)
	}
}

func TestTwoRegistrationsWithTheSameApprovalCannotBothWin(t *testing.T) {
	i := testIdentity(t)
	waiter, _ := approvedPasskey(t, i)
	var regs []Registration
	for n := 0; n < 4; n++ {
		opts, err := i.Passkeys.CreationOptions(ctx, "Alice")
		if err != nil {
			t.Fatal(err)
		}
		regs = append(regs, newAuthenticator().register(opts["challenge"].(string), regOver{}))
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for _, reg := range regs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i.passkeyAccept(ctx, waiter, reg, "p") == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if n, _ := i.Passkeys.Count(ctx); won != 1 || n != 1 {
		t.Errorf("won %d, passkeys %d", won, n)
	}
}

func TestAChallengeIsSpentByItsFirstUseAndFitsItsPurposeOnly(t *testing.T) {
	i := testIdentity(t)
	c, _ := i.Passkeys.challenge(ctx, purposeRegister)
	if !i.Passkeys.consume(ctx, c, purposeRegister) || i.Passkeys.consume(ctx, c, purposeRegister) {
		t.Error("a challenge is spent once")
	}
	c, _ = i.Passkeys.challenge(ctx, purposeRegister)
	if i.Passkeys.consume(ctx, c, purposeSignIn) {
		t.Error("a registration challenge signed someone in")
	}
}

// The setup page asks for a code only when the owner presses the button: opening /setup (a prefetch, a scanner) makes none.
func TestTheSetupPageStartsNothingByItself(t *testing.T) {
	p := setupPage("en")
	for _, want := range []string{`data-view="setup"`, `id="create"`, `id="code"`} {
		if !strings.Contains(p, want) {
			t.Errorf("the setup page lacks %s", want)
		}
	}
}

func TestPasskeysAreListedAndRemovedByIdPrefixAndRemovingOneEndsTheSessions(t *testing.T) {
	i := testIdentity(t)
	var ids []string
	for n := 0; n < 2; n++ {
		w, opts := approvedPasskey(t, i)
		a := newAuthenticator()
		if err := i.passkeyAccept(ctx, w, a.register(opts["challenge"].(string), regOver{}), "laptop"+string(rune('a'+n))); err != nil {
			t.Fatal(err)
		}
	}
	list, err := i.Passkeys.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %v", list, err)
	}
	for _, p := range list {
		ids = append(ids, p.ID)
		if p.Name == "" || p.Created == "" {
			t.Errorf("%+v", p)
		}
	}
	if _, err := i.Auth.NewSession(ctx, "agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := i.Passkeys.Remove(ctx, ""); err == nil {
		t.Error("an empty id removed passkeys")
	}
	if _, err := i.Passkeys.Remove(ctx, "x-no-such"); !errors.Is(err, ErrNoPasskey) {
		t.Errorf("unknown id: %v", err)
	}
	if _, err := i.Passkeys.Remove(ctx, ids[0][:8]); err != nil {
		t.Fatal(err)
	}
	if n, _ := i.Passkeys.Count(ctx); n != 1 {
		t.Errorf("%d passkeys after a removal", n)
	}
	var sessions int
	i.Auth.db.QueryRow("select count(*) from sessions").Scan(&sessions)
	if sessions != 0 {
		t.Errorf("%d sessions survived the removal of a passkey", sessions)
	}
}

func TestAddingAPasskeyIsLoggedWithoutSecrets(t *testing.T) {
	var buf bytes.Buffer
	was := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(was)
	i := testIdentity(t)
	w, opts := approvedPasskey(t, i)
	a := newAuthenticator()
	reg := a.register(opts["challenge"].(string), regOver{})
	if err := i.passkeyAccept(ctx, w, reg, "my\x1b[2J phone"); err != nil {
		t.Fatal(err)
	}
	log := buf.String()
	if !strings.Contains(log, "passkey added") || !strings.Contains(log, "name=") || strings.ContainsRune(log, 0x1b) {
		t.Errorf("log: %q", log)
	}
	if strings.Contains(log, w) || strings.Contains(log, reg.ID) {
		t.Errorf("a secret or the full id reached the log: %q", log)
	}
}
