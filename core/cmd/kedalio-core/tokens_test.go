package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestTokensCreatePrintsTheTokenOnceAndListNeverShowsItOrItsHash(t *testing.T) {
	dir := t.TempDir()
	started(t, testOptions(dir))
	ctx := context.Background()

	var out bytes.Buffer
	if err := runTokens(ctx, []string{"-data", dir, "create", "alice", "-policy", "cloud"}, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	token := lines[len(lines)-1]
	if !strings.HasPrefix(token, "kdl_") || !strings.Contains(out.String(), "will not be shown again") {
		t.Fatalf("output %q", out.String())
	}

	out.Reset()
	if err := runTokens(ctx, []string{"-data", dir, "list"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), token) || strings.Contains(out.String(), "kdl_") {
		t.Errorf("list shows the token: %s", out.String())
	}
	if !strings.Contains(out.String(), "alice") || !strings.Contains(out.String(), "cloud") || !strings.Contains(out.String(), "never") {
		t.Errorf("list: %s", out.String())
	}
	if got := revokedColumn(t, out.String(), "alice"); got != "-" {
		t.Errorf("list before revoke: REVOKED is %q, want -", got)
	}

	out.Reset()
	if err := runTokens(ctx, []string{"-data", dir, "revoke", "alice"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), token) {
		t.Error("revoke prints the token")
	}
	out.Reset()
	if err := runTokens(ctx, []string{"-data", dir, "list"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := revokedColumn(t, out.String(), "alice"); got == "-" || got == "" {
		t.Errorf("list after revoke shows no revocation time for alice: %q", out.String())
	}
	if err := runTokens(ctx, []string{"-data", dir, "revoke", "nobody"}, &out); err == nil {
		t.Error("revoking an unknown name passed")
	}
}

func TestTokensRefusesAnUnknownPolicyReservedAndBadNamesAndBadUsage(t *testing.T) {
	dir := t.TempDir()
	started(t, testOptions(dir))
	for _, args := range [][]string{
		{"create", "alice", "-policy", "owner"},
		{"create", "alice"},
		{"create", "upkeep", "-policy", "cloud"},
		{"create", "owner", "-policy", "cloud"},
		{"create", "client", "-policy", "cloud"},
		{"create", "Alice", "-policy", "cloud"},
		{"create"},
		{"list", "extra"},
		{"revoke"},
		{},
	} {
		var out bytes.Buffer
		if err := runTokens(context.Background(), append([]string{"-data", dir}, args...), &out); err == nil {
			t.Errorf("%v was accepted: %s", args, out.String())
		} else if strings.Contains(out.String(), "kdl_") {
			t.Errorf("%v printed a token", args)
		}
	}
}

// revokedColumn is the last field of the list row of name: the tabwriter turned the tabs into spaces, and every other
// field of a row is a single word, so the last one is the REVOKED column.
func revokedColumn(t *testing.T, list, name string) string {
	t.Helper()
	for _, l := range strings.Split(list, "\n") {
		if f := strings.Fields(l); len(f) > 0 && f[0] == name {
			return f[len(f)-1]
		}
	}
	t.Fatalf("no row for %s in %s", name, list)
	return ""
}
