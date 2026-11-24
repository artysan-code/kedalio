package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"kedalio/core/vault"
)

// swordfishMask is the vault's mask with one value in it, «swordfish»: the shapes (tokens, addresses) and that word.
func swordfishMask(s string) string {
	return vault.MaskField("", strings.ReplaceAll(s, "swordfish", vault.Hidden))
}

// A task is a page of the memory read from every client: before this check, tasks_save kept a token in its notes, log
// or decisions as it was given.
func TestTasksSaveRefusesASecretInAnyTextField(t *testing.T) {
	r := newRig(t, swordfishMask)
	s := r.session(t)
	id := taskID(mustCall(t, s, "tasks_save", map[string]any{"title": "Rotate the router's password"}))
	for _, args := range []map[string]any{
		{"title": "Use swordfish"},
		{"id": id, "notes": "The word is swordfish."},
		{"id": id, "log": "set password: hunter2"},
		{"id": id, "decision": "keep DB_PASSWORD=s3cr3t-Value"},
		{"id": id, "find": "router", "replace": "router (token=abc123)"},
		{"id": id, "steps_add": []any{"log in", "type ghp_abcdefghijklmnop1234"}},
		{"id": id, "attach": "https://alice:pw123@example.com/admin"},
		{"id": id, "labels": []any{"swordfish"}},
	} {
		out, failed := call(t, s, "tasks_save", args)
		if !failed || !strings.Contains(problemOf(out), "looks like a secret") {
			t.Errorf("%v: %v", args, out)
		}
		if strings.Contains(strings.ToLower(problemOf(out)), "hunter2") {
			t.Errorf("the refusal quotes the secret: %v", out)
		}
	}
	d, _ := r.store.Get(ctx, "tasks/"+id+".md")
	for _, leak := range []string{"swordfish", "hunter2", "s3cr3t", "abc123", "ghp_", "pw123"} {
		if strings.Contains(d.Body, leak) {
			t.Errorf("the task kept %s:\n%s", leak, d.Body)
		}
	}
	// a pair made of the old notes and a replace: «DB_PASSWORD is set» → «DB_PASSWORD=…»
	pid := taskID(mustCall(t, s, "tasks_save", map[string]any{"title": "Set up the database", "notes": "The DB_PASSWORD is set."}))
	if out, failed := call(t, s, "tasks_save", map[string]any{"id": pid, "find": " is set", "replace": "=Hx7kQ29zLmP4"}); !failed {
		t.Errorf("a replace made a pair: %v", out)
	}
	for _, args := range []map[string]any{{"id": id, "for": "swordfish"}, {"id": id, "ref": "swordfish"}} {
		if out, failed := call(t, s, "tasks_save", args); !failed {
			t.Errorf("%v: %v", args, out)
		}
	}
	// prose about secrets is no secret
	mustCall(t, s, "tasks_save", map[string]any{"id": id, "log": "password: vedi il pannello del router; auth: OAuth",
		"notes": "Il token sta in `KEDALIO_CORE_TOKEN`, nel pannello di Coolify."})
}

func TestMemoryWritesRefuseANewCredentialPairButAPageThatHoldsOneStaysEditable(t *testing.T) {
	r := newRig(t, nil)
	s := r.session(t)
	for _, args := range []map[string]any{
		{"op": "write", "path": "notes/router.md", "base_rev": 0, "body": "# Router\n\nThe router at home.\n\npassword: hunter2\n"},
		{"op": "append", "text": "set DB_PASSWORD=s3cr3t-Value on venus"},
		{"op": "append", "where": "inbox", "text": "the api_key = \"a long phrase of words\""},
		{"op": "remember", "text": "the router's password: Rx7!kz"},
	} {
		if out := mustCall(t, s, "memory_write", args); out["refused"] != true {
			t.Errorf("%v = %v", args, out)
		}
	}
	// prose stays writable
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/login.md", "base_rev": 0,
		"body": "# Login\n\nHow the core signs in.\n\nauth: OAuth 2.1 with a passkey; token: la sezione dei segreti.\n"})
	// a page that got in before the check: listed by the health scan, path only, and still editable
	if _, err := r.store.Write(ctx, "notes/old.md", "# Old\n\nAn old note.\n\ntoken=abc123\n", "test"); err != nil {
		t.Fatal(err)
	}
	mustCall(t, s, "memory_write", map[string]any{"op": "edit", "path": "notes/old.md", "find": "An old note.", "replace": "An old note, to clean."})
	if out := mustCall(t, s, "memory_write", map[string]any{"op": "edit", "path": "notes/old.md", "find": "to clean.", "replace": "to clean. secret=zz99Kq7x"}); out["refused"] != true {
		t.Errorf("a second pair was added: %v", out)
	}
	h := mustCall(t, s, "memory_list", map[string]any{"what": "health"})
	if got := h["secrets"].([]any); len(got) != 1 || got[0] != "notes/old.md" {
		t.Errorf("secrets = %v", got)
	}
	if got := h["secret_revisions"].([]any); len(got) != 1 || got[0] != "notes/old.md@1" {
		t.Errorf("secret_revisions = %v", got)
	}
	if k := h["secret_kinds"].(map[string]any)["notes/old.md"].([]any); len(k) != 1 || k[0] != "name = value: token" {
		t.Errorf("secret_kinds = %v", h["secret_kinds"])
	}
	if b, _ := json.Marshal(h); strings.Contains(string(b), "abc123") {
		t.Errorf("the health answer carries the value: %s", b)
	}
}

// A placeholder the mask left in a page («Authorization: Bearer ‹hidden…›») is no secret: the scan flagged such pages,
// and a write of one was refused, because the mask changed the placeholder again.
func TestAPlaceholderIsNoSecretAndTheScanSaysWhyAPageIsFlagged(t *testing.T) {
	r := newRig(t, swordfishMask)
	s := r.session(t)
	masked := "# Call\n\nHow the API is called.\n\nAuthorization: Bearer " + vault.Hidden + ", token: " + vault.Hidden + "\n"
	mustCall(t, s, "memory_write", map[string]any{"op": "write", "path": "notes/call.md", "base_rev": 0, "body": masked})
	for path, body := range map[string]string{
		"notes/raw.md":   "# Raw\n\nA raw one.\n\nAuthorization: Bearer abcdefgh1234ijklmnop.qrstuv\n",
		"notes/vault.md": "# Vault\n\nThe word.\n\nIt is swordfish.\n",
		"notes/url.md":   "# URL\n\nAn address.\n\npostgres://alice:pw-12345@db.example.com/x\n",
	} {
		if _, err := r.store.Write(ctx, path, body, "test"); err != nil {
			t.Fatal(err)
		}
	}
	h := mustCall(t, s, "memory_list", map[string]any{"what": "health"})
	kinds := h["secret_kinds"].(map[string]any)
	want := map[string]string{"notes/raw.md": "Bearer-header credential", "notes/vault.md": "a value the vault holds", "notes/url.md": "credentials in an address"}
	for path, kind := range want {
		if k, _ := kinds[path].([]any); len(k) != 1 || k[0] != kind {
			t.Errorf("%s: %v, want %s", path, kinds[path], kind)
		}
	}
	if _, flagged := kinds["notes/call.md"]; flagged || len(kinds) != 3 {
		t.Errorf("secret_kinds = %v", kinds)
	}
}

// A task's log that got a secret-like line in before the scan stays summable: the upkeep adds a summary above the
// entries and the line is not new. What the edit brings is still judged.
func TestUpkeepSummaryOnALogHoldingAnOldSecretLikeLinePassesButANewSecretIsRefused(t *testing.T) {
	r, _ := upkeepRig(t, false)
	r.srv.cfg.Mask = swordfishMask
	log := "tasks/t-20261007-aaaaaa/log.md"
	old := "# Plan\n\n## Log\n- 10:00 Opened the panel, password swordfish\n- 11:00 Set GITHUB_TOKEN=" + "ghp_abcdefghijklmnop1234\n- 12:00 Done\n"
	if _, err := r.store.Write(ctx, log, old, "owner"); err != nil {
		t.Fatal(err)
	}
	up := r.upkeepSession(t)
	summary := func(text string) string {
		return "# Plan\n\n## Summary\n" + text + "\n\n" + strings.TrimPrefix(old, "# Plan\n\n")
	}
	if out, failed := call(t, up, "memory_write", map[string]any{"op": "write", "path": log, "body": summary("The panel was opened and the job is done.")}); failed || out["refused"] == true {
		t.Fatalf("a summary above an old secret-like line = %v", out)
	}
	if d, _ := r.store.Get(ctx, log); d == nil || !strings.Contains(d.Body, "## Summary") {
		t.Fatalf("the log = %+v", d)
	}
	for _, bad := range []string{"The new token is ghp_zyxwvutsrqponmlkji9876.", "The password is swordfish again, and swordfish twice."} {
		if out, failed := call(t, up, "memory_write", map[string]any{"op": "write", "path": log, "body": summary(bad)}); !failed && out["refused"] != true {
			t.Errorf("a new secret %q was taken: %v", bad, out)
		}
	}
}

// Three tasks_save log lines were refused on 23/11 for prose and references: a Bearer followed by a word, a sops key
// name after a variable. A real value in the same line is still refused.
func TestTasksSaveTakesReferencesAndProseButNotARealValue(t *testing.T) {
	r := newRig(t, swordfishMask)
	s := r.session(t)
	id := taskID(mustCall(t, s, "tasks_save", map[string]any{"title": "Wire the deploy"}))
	for _, line := range []string{
		"Il client manda il token Bearer in un passo; i Bearer non sono valori.",
		"mcp-env CLOUDFLARE_API_TOKEN=cloudflare.alice.token -- npx wrangler deploy",
		"SUPABASE_ACCESS_TOKEN=old.SUPABASE_ACCESS_TOKEN; SMTP_PASSWORD=chiave nel file sops",
		`N8N_API_KEY: "{secret}" nel template; Mappa dei token: kedalio-bot`,
	} {
		mustCall(t, s, "tasks_save", map[string]any{"id": id, "log": line})
	}
	rnd := "Xk92mPq7" + "Lw3nRt5vB8"
	for _, line := range []string{"Authorization: Bearer " + rnd, "CLOUDFLARE_API_TOKEN=" + rnd} {
		if out, failed := call(t, s, "tasks_save", map[string]any{"id": id, "log": line}); !failed {
			t.Errorf("%q: %v", line, out)
		}
	}
}
