package identity

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func testTokens(t *testing.T) (*Tokens, *sql.DB) {
	t.Helper()
	d := testDB(t)
	if err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	return NewTokens(d), d
}

func TestTokenNamesFollowTheRuleAndTheReservedAreRefused(t *testing.T) {
	tk, _ := testTokens(t)
	for _, n := range []string{"", "a", "Alice", "1alice", "al ice", "al_ice", "-alice", strings.Repeat("a", 33), "owner", "upkeep", "client"} {
		if _, err := tk.Create(ctx, n, "cloud"); err == nil {
			t.Errorf("name %q was accepted", n)
		}
	}
	for _, n := range []string{"al", "alice", "alice-2", strings.Repeat("a", 32)} {
		if _, err := tk.Create(ctx, n, "cloud"); err != nil {
			t.Errorf("name %q: %v", n, err)
		}
	}
}

func TestATokenHasThePrefixAndOnlyItsHashIsStored(t *testing.T) {
	tk, d := testTokens(t)
	token, err := tk.Create(ctx, "alice", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "kdl_") || len(token) < 4+40 {
		t.Errorf("token shape %q", token)
	}
	var h string
	if err := d.QueryRowContext(ctx, "select hash from named_tokens where name = 'alice'").Scan(&h); err != nil {
		t.Fatal(err)
	}
	if h != hash(token) {
		t.Error("the hash is not the SHA-256 of the token")
	}
	var n int
	if err := d.QueryRowContext(ctx, "select count(*) from named_tokens where name like ? or hash like ? or policy like ?",
		"%"+token+"%", "%"+token+"%", "%"+token+"%").Scan(&n); err != nil || n != 0 {
		t.Errorf("the token text is in the table (%d, %v)", n, err)
	}
}

func TestLookupFindsALiveTokenAndNotARevokedOrUnknownOne(t *testing.T) {
	tk, _ := testTokens(t)
	token, _ := tk.Create(ctx, "alice", "cloud")
	name, pol, ok := tk.Lookup(ctx, token)
	if !ok || name != "alice" || pol != "cloud" {
		t.Fatalf("lookup %q %q %v", name, pol, ok)
	}
	if _, _, ok := tk.Lookup(ctx, "kdl_nope"); ok {
		t.Error("an unknown token was found")
	}
	if _, _, ok := tk.Lookup(ctx, ""); ok {
		t.Error("an empty token was found")
	}
	if err := tk.Revoke(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := tk.Lookup(ctx, token); ok {
		t.Error("a revoked token was found")
	}
}

func TestRevokingAnUnknownNameIsAnError(t *testing.T) {
	tk, _ := testTokens(t)
	if err := tk.Revoke(ctx, "nobody"); err == nil {
		t.Error("revoking an unknown name passed")
	}
}

func TestARevokedNameCannotBeCreatedAgain(t *testing.T) {
	tk, _ := testTokens(t)
	if _, err := tk.Create(ctx, "alice", "cloud"); err != nil {
		t.Fatal(err)
	}
	if _, err := tk.Create(ctx, "alice", "cloud"); err == nil {
		t.Error("a live name was created twice")
	}
	if err := tk.Revoke(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := tk.Create(ctx, "alice", "cloud"); err == nil {
		t.Error("a revoked name was created again")
	}
}

func TestListShowsTheFactsAndNeverTheHash(t *testing.T) {
	tk, _ := testTokens(t)
	token, _ := tk.Create(ctx, "alice", "cloud")
	tk.Lookup(ctx, token)
	list, err := tk.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, err)
	}
	if e := list[0]; e.Name != "alice" || e.Policy != "cloud" || e.Created == "" || e.Used == "" || e.Revoked != "" {
		t.Errorf("%+v", e)
	}
}

func TestLastUsedIsWrittenAtMostEveryFiveMinutes(t *testing.T) {
	tk, d := testTokens(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	tk.Now = func() time.Time { return now }
	token, _ := tk.Create(ctx, "alice", "cloud")
	used := func() string {
		var u sql.NullString
		if err := d.QueryRowContext(ctx, "select used from named_tokens where name = 'alice'").Scan(&u); err != nil {
			t.Fatal(err)
		}
		return u.String
	}
	if used() != "" {
		t.Fatal("used before any call")
	}
	tk.Lookup(ctx, token)
	first := used()
	if first != now.Format(time.RFC3339) {
		t.Fatalf("first use %q", first)
	}
	now = now.Add(4 * time.Minute)
	tk.Lookup(ctx, token)
	if used() != first {
		t.Error("written again within five minutes")
	}
	now = now.Add(2 * time.Minute)
	tk.Lookup(ctx, token)
	if used() == first {
		t.Error("not written after five minutes")
	}
}

func TestIdentityMigrationToThreeKeepsOAuthRows(t *testing.T) {
	d := testDB(t)
	if _, err := d.ExecContext(ctx, "create table schema_versions (name text primary key, version integer not null)"); err != nil {
		t.Fatal(err)
	}
	for _, s := range steps[:2] {
		if _, err := d.ExecContext(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		"insert into schema_versions values ('identity', 2)",
		"insert into oauth_clients values ('c1', 'Example', '[]', '2026-01-01T00:00:00Z')",
		"insert into tokens values ('h1', 'access', 'c1', 'f1', 1, '2026-01-01T00:00:00Z', null, 0)",
	} {
		if _, err := d.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	var v, c, k, n int
	d.QueryRowContext(ctx, "select version from schema_versions where name = 'identity'").Scan(&v)
	d.QueryRowContext(ctx, "select count(*) from oauth_clients").Scan(&c)
	d.QueryRowContext(ctx, "select count(*) from tokens").Scan(&k)
	if err := d.QueryRowContext(ctx, "select count(*) from named_tokens").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if v != 3 || c != 1 || k != 1 {
		t.Errorf("version %d clients %d tokens %d", v, c, k)
	}
}

func TestLookupFailsClosedWhenTheDatabaseIsGone(t *testing.T) {
	tk, d := testTokens(t)
	token, err := tk.Create(ctx, "alice", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if name, policy, ok := tk.Lookup(ctx, token); ok || name != "" || policy != "" {
		t.Errorf("Lookup on a closed database = %q %q %v", name, policy, ok)
	}
}
