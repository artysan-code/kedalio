package identity

import (
	"context"
	"net/url"
	"strings"
)

// ClientInfo is a registered OAuth client as the owner sees it in `kedalio-core clients list`. Never a token value.
type ClientInfo struct {
	ID, Name string
	// Host is the host of the client's first redirect that is not on this machine, else its first redirect's.
	Host string
	// Remote is a cloud client (see ClientOf).
	Remote bool
	// Created and LastUsed are RFC 3339 times; LastUsed is empty when no token of the client was ever used.
	Created, LastUsed string
	// Active is its tokens that are neither revoked nor expired.
	Active int
}

// Clients lists the registered clients, newest first.
func (a *Auth) Clients(ctx context.Context) ([]ClientInfo, error) {
	rows, err := a.db.QueryContext(ctx,
		`select c.id, c.name, c.redirects, c.created,
		        coalesce((select max(used) from tokens t where t.client = c.id), ''),
		        (select count(*) from tokens t where t.client = c.id and t.revoked = 0 and t.expires > ?)
		   from oauth_clients c order by c.created desc, c.id`, a.now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClientInfo
	for rows.Next() {
		var c ClientInfo
		var redirects string
		if err := rows.Scan(&c.ID, &c.Name, &redirects, &c.Created, &c.LastUsed, &c.Active); err != nil {
			return nil, err
		}
		c.Name = strings.Map(func(r rune) rune {
			if unsafeRune(r) {
				return -1
			}
			return r
		}, c.Name)
		var list []string
		if fromJSON(redirects, &list) != nil {
			list = nil
		}
		c.Remote = isRemote(list)
		c.Host = redirectHost(list)
		out = append(out, c)
	}
	return out, rows.Err()
}

// redirectHost is the host of the first redirect that is not on this machine, else of the first redirect.
func redirectHost(redirects []string) string {
	host := ""
	for _, r := range redirects {
		if u, err := url.Parse(r); err == nil && (host == "" || !isLoopbackHost(host)) {
			host = u.Hostname()
			if !isLoopbackHost(host) {
				break
			}
		}
	}
	return host
}

// RevokeClient ends all of a client's access at once and forgets it: its tokens and codes are deleted, so an access
// token is refused on the next call and a refresh token buys nothing, and the client has to register again and be
// approved again. It is whether the client existed.
func (a *Auth) RevokeClient(ctx context.Context, id string) (bool, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for _, q := range []string{"delete from tokens where client = ?", "delete from oauth_codes where client = ?",
		"delete from approvals where client = ?"} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return false, err
		}
	}
	res, err := tx.ExecContext(ctx, "delete from oauth_clients where id = ?", id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, tx.Commit()
}
