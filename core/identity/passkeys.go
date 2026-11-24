package identity

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const challengeTTL = 5 * time.Minute

// Challenge purposes: the one-time values a passkey signs or registers over.
const (
	purposeRegister = "register"
	purposeSignIn   = "sign-in"
)

// Passkeys keeps the owner's passkeys and the one-time challenges they answer.
type Passkeys struct {
	db   *sql.DB
	site Site
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

// NewPasskeys makes the store over a database the identity's tables are in.
func NewPasskeys(d *sql.DB, site Site) *Passkeys { return &Passkeys{db: d, site: site} }

func (p *Passkeys) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Challenge is a new one-time challenge for a purpose.
func (p *Passkeys) challenge(ctx context.Context, purpose string) (string, error) {
	c := randomToken(32)
	now := p.now()
	if _, err := p.db.ExecContext(ctx, "delete from challenges where expires < ?", now.UnixMilli()); err != nil {
		return "", err
	}
	_, err := p.db.ExecContext(ctx, "insert into challenges (challenge, purpose, expires) values (?, ?, ?)",
		c, purpose, now.Add(challengeTTL).UnixMilli())
	return c, err
}

// consume spends a challenge: true once, when it was made for this purpose and has not expired.
func (p *Passkeys) consume(ctx context.Context, c, purpose string) bool {
	if c == "" {
		return false
	}
	var got string
	var expires int64
	err := p.db.QueryRowContext(ctx, "delete from challenges where challenge = ? returning purpose, expires", c).
		Scan(&got, &expires)
	return err == nil && got == purpose && expires >= p.now().UnixMilli()
}

// Count is how many passkeys there are.
func (p *Passkeys) Count(ctx context.Context) (int, error) {
	var n int
	err := p.db.QueryRowContext(ctx, "select count(*) from passkeys").Scan(&n)
	return n, err
}

// CreationOptions is what navigator.credentials.create takes, as JSON (PublicKeyCredential.parseCreationOptionsFromJSON).
func (p *Passkeys) CreationOptions(ctx context.Context, name string) (map[string]any, error) {
	c, err := p.challenge(ctx, purposeRegister)
	if err != nil {
		return nil, err
	}
	exclude := []any{}
	rows, err := p.db.QueryContext(ctx, "select id from passkeys")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		exclude = append(exclude, map[string]any{"type": "public-key", "id": id})
	}
	return map[string]any{
		"challenge":              c,
		"rp":                     map[string]any{"id": p.site.RPID, "name": "Kedalio"},
		"user":                   map[string]any{"id": randomToken(16), "name": name, "displayName": name},
		"pubKeyCredParams":       []any{map[string]any{"type": "public-key", "alg": -7}},
		"authenticatorSelection": map[string]any{"residentKey": "required", "userVerification": "required"},
		"attestation":            "none",
		"excludeCredentials":     exclude,
		"timeout":                challengeTTL.Milliseconds(),
	}, rows.Err()
}

// RequestOptions is what navigator.credentials.get takes: a discoverable credential, user verification required.
func (p *Passkeys) RequestOptions(ctx context.Context) (map[string]any, error) {
	c, err := p.challenge(ctx, purposeSignIn)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"challenge":        c,
		"rpId":             p.site.RPID,
		"userVerification": "required",
		"allowCredentials": []any{},
		"timeout":          challengeTTL.Milliseconds(),
	}, nil
}

// Check is a registration checked against a challenge it spends: the credential, or why not.
func (p *Passkeys) Check(ctx context.Context, reg Registration) (Credential, error) {
	c := clientChallenge(reg.Response.ClientDataJSON)
	if !p.consume(ctx, c, purposeRegister) {
		return Credential{}, errors.New("the challenge is unknown, used or expired")
	}
	return parseRegistration(reg, c, p.site)
}

// Add keeps a checked credential.
func (p *Passkeys) Add(ctx context.Context, cred Credential, name string) error {
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > 60 {
		name = string(r[:60])
	}
	if name == "" {
		name = "passkey"
	}
	_, err := p.db.ExecContext(ctx, "insert into passkeys (id, name, public_key, created) values (?, ?, ?, ?)",
		cred.ID, name, cred.PublicKey, p.now().UTC().Format(time.RFC3339))
	return err
}

// Verify is an assertion that signs the owner in, or why not: a sign-in challenge it spends, a known passkey, and a
// signature that holds.
func (p *Passkeys) Verify(ctx context.Context, a Assertion) error {
	if !a.clean() {
		return errors.New("the assertion lacks a part")
	}
	c := clientChallenge(a.ClientDataJSON)
	if !p.consume(ctx, c, purposeSignIn) {
		return errors.New("the challenge is unknown, used or expired")
	}
	var key string
	if err := p.db.QueryRowContext(ctx, "select public_key from passkeys where id = ?", a.Credential).Scan(&key); err != nil {
		return errors.New("not a known passkey")
	}
	ch, _ := unb64u(c)
	if err := verifyAssertion(a, key, ch, p.site); err != nil {
		return err
	}
	_, err := p.db.ExecContext(ctx, "update passkeys set used = ? where id = ?", p.now().UTC().Format(time.RFC3339), a.Credential)
	return err
}

// ErrNoPasskey is an id no passkey (or more than one) matches.
var ErrNoPasskey = errors.New("no passkey with that id")

// PasskeyInfo is a passkey as the owner sees it in `kedalio-core passkeys list`: never its key.
type PasskeyInfo struct {
	ID, Name, Created string
	// Used is empty when the passkey never signed anyone in.
	Used string
}

// List returns the passkeys, oldest first.
func (p *Passkeys) List(ctx context.Context) ([]PasskeyInfo, error) {
	rows, err := p.db.QueryContext(ctx, "select id, name, created, coalesce(used, '') from passkeys order by created, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PasskeyInfo
	for rows.Next() {
		var k PasskeyInfo
		if err := rows.Scan(&k.ID, &k.Name, &k.Created, &k.Used); err != nil {
			return nil, err
		}
		k.Name = CleanName(k.Name)
		out = append(out, k)
	}
	return out, rows.Err()
}

// Remove deletes the one passkey whose id starts with prefix, and ends every browser session: a passkey the owner did not
// make may already have signed someone in, and a session outlives its passkey. It is the id that was removed.
func (p *Passkeys) Remove(ctx context.Context, prefix string) (string, error) {
	if len(prefix) < 4 {
		return "", errors.New("give at least the first 4 characters of the id (see `passkeys list`)")
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	// substr, not LIKE: an id has underscores, which LIKE reads as wildcards
	rows, err := tx.QueryContext(ctx, "select id from passkeys where substr(id, 1, length(?1)) = ?1", prefix)
	if err != nil {
		return "", err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 1 {
		return "", ErrNoPasskey
	}
	if _, err := tx.ExecContext(ctx, "delete from passkeys where id = ?", ids[0]); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "delete from sessions"); err != nil {
		return "", err
	}
	return ids[0], tx.Commit()
}
