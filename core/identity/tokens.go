package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Tokens are the named bearer tokens of headless agents: static, each bound to a policy by name, revocable one by one.
// They live apart from OAuth (table named_tokens) and need no public URL, so the store is built from the database alone.
type Tokens struct {
	db *sql.DB
	// Now pins the clock; nil is the current time.
	Now func() time.Time
}

func NewTokens(d *sql.DB) *Tokens { return &Tokens{db: d} }

func (t *Tokens) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// TokenPrefix starts every named token: the mask and the server tell it from an OAuth token by it.
const TokenPrefix = "kdl_"

var tokenName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// reservedNames are authors the core already gives meaning to, so no token may be taken for them.
var reservedNames = map[string]bool{"owner": true, "upkeep": true, "client": true}

// Create makes a token for name under policy and returns it: this is the only time its text exists, only its hash is
// kept. A name is never reused, a revoked one included, so an author on a revision always means the same token.
func (t *Tokens) Create(ctx context.Context, name, policy string) (string, error) {
	if !tokenName.MatchString(name) {
		return "", fmt.Errorf("the name %q must be 2 to 32 characters: a lowercase letter, then lowercase letters, digits or -", name)
	}
	if reservedNames[name] {
		return "", fmt.Errorf("the name %q is reserved", name)
	}
	token := TokenPrefix + randomToken(32)
	_, err := t.db.ExecContext(ctx, "insert into named_tokens (name, hash, policy, created) values (?, ?, ?, ?)",
		name, hash(token), policy, t.now().UTC().Format(time.RFC3339))
	if err != nil {
		var n int
		if t.db.QueryRowContext(ctx, "select count(*) from named_tokens where name = ?", name).Scan(&n) == nil && n > 0 {
			return "", fmt.Errorf("the name %q was used before (names are never reused, revoked or not)", name)
		}
		return "", err
	}
	return token, nil
}

// TokenInfo is what List tells of a token: never the hash.
type TokenInfo struct {
	Name, Policy, Created, Used, Revoked string
}

// List is every token, the revoked ones too, oldest first.
func (t *Tokens) List(ctx context.Context) ([]TokenInfo, error) {
	rows, err := t.db.QueryContext(ctx, "select name, policy, created, used, revoked from named_tokens order by created, name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenInfo
	for rows.Next() {
		var e TokenInfo
		var used, revoked sql.NullString
		if err := rows.Scan(&e.Name, &e.Policy, &e.Created, &used, &revoked); err != nil {
			return nil, err
		}
		e.Used, e.Revoked = used.String, revoked.String
		out = append(out, e)
	}
	return out, rows.Err()
}

// Revoke marks a token revoked (the row stays, so the name stays taken); revoking twice keeps the first time.
func (t *Tokens) Revoke(ctx context.Context, name string) error {
	res, err := t.db.ExecContext(ctx, "update named_tokens set revoked = coalesce(revoked, ?) where name = ?",
		t.now().UTC().Format(time.RFC3339), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("no token named " + name + " (see `tokens list`)")
	}
	return nil
}

// Lookup is the name and policy of a live token; a revoked or unknown one is not ok.
func (t *Tokens) Lookup(ctx context.Context, token string) (name, policy string, ok bool) {
	if token == "" {
		return "", "", false
	}
	h, now := hash(token), t.now()
	var revoked sql.NullString
	if t.db.QueryRowContext(ctx, "select name, policy, revoked from named_tokens where hash = ?", h).Scan(&name, &policy, &revoked) != nil ||
		revoked.Valid {
		return "", "", false
	}
	// «last used» is a courtesy to the owner: written at most every five minutes, as for an OAuth token
	_, _ = t.db.ExecContext(ctx, "update named_tokens set used = ? where hash = ? and (used is null or used < ?)",
		now.UTC().Format(time.RFC3339), h, now.Add(-5*time.Minute).UTC().Format(time.RFC3339))
	return name, policy, true
}
