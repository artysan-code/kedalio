package identity

import (
	"context"
	"database/sql"

	"kedalio/core/db"
)

// steps are the tables of the package, appended to and never edited (db.Migrate). There is one owner, so nothing here
// names an account.
var steps = []string{
	`create table passkeys (
  id text primary key, name text not null, public_key text not null, created text not null, used text);
create table challenges (challenge text primary key, purpose text not null, expires integer not null);
create table sessions (
  id text primary key, hash text not null unique, agent text not null default '', created text not null,
  used text not null, expires integer not null, ends integer not null);
create table oauth_clients (id text primary key, name text not null, redirects text not null, created text not null);
create table oauth_codes (
  hash text primary key, client text not null, redirect text not null, challenge text not null,
  resource text, scope text, expires integer not null);
create table tokens (
  hash text primary key, kind text not null, client text not null, family text not null, expires integer not null,
  created text not null, used text, revoked integer not null default 0);
create index tokens_family on tokens(family);`,
	// a new client or a passkey is approved with a short code (ADR 0007): hash is that of the normalized code, waiter
	// that of the secret only the requesting browser knows; params is the JSON of the checked OAuth request.
	`create table approvals (
  hash text primary key, waiter text not null unique, purpose text not null, params text not null default '{}',
  client text not null default '', ip text not null default '', agent text not null default '',
  created integer not null, expires integer not null, state text not null default 'pending',
  decided_by text, decided integer);
create index approvals_client on approvals(client);`,
}

// Migrate brings the identity's tables up to date.
func Migrate(ctx context.Context, d *sql.DB) error {
	return db.Migrate(ctx, d, "identity", steps)
}
