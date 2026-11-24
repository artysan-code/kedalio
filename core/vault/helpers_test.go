package vault

import (
	"context"
	"database/sql"
	"testing"

	"kedalio/core/db"
)

var ctx = context.Background()

// newDB is a migrated in-memory database.
func newDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	return d
}

// newVault is an unlocked vault over a fresh data folder, with its database and folder.
func newVault(t *testing.T) (*Vault, *sql.DB, string) {
	t.Helper()
	d, dir := newDB(t), t.TempDir()
	v, err := Open(ctx, d, dir)
	if err != nil {
		t.Fatal(err)
	}
	if v.Locked() {
		t.Fatal("a new instance starts unlocked")
	}
	return v, d, dir
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// legacyTables is what the first steps made before the secrets store was cut: the sealed secrets and the data key they
// are sealed with. A fresh database has neither, so a test that needs an old one makes them.
const legacyTables = `create table if not exists data_keys (kind text primary key, wrapped text not null, created text not null);
create table if not exists secrets (
  name text primary key, kind text not null, sealed text not null, backend text not null default 'kedalio',
  created text not null, updated text not null, used text);`

// seedSecret stores a secret the way the cut secrets store did, so Mask has an old database's value to hide.
func seedSecret(t *testing.T, d *sql.DB, v *Vault, name, value string) {
	t.Helper()
	_, err := d.ExecContext(ctx, legacyTables)
	must(t, err)
	var wrapped string
	if err := d.QueryRowContext(ctx, "select wrapped from data_keys where kind = 'kek'").Scan(&wrapped); err != nil {
		wrapped = v.kek.wrap(random(keyBytes), dataKeyAAD)
		_, err := d.ExecContext(ctx, "insert into data_keys (kind, wrapped, created) values ('kek', ?, 'x')", wrapped)
		must(t, err)
	}
	raw, err := v.kek.unwrap(wrapped, dataKeyAAD)
	must(t, err)
	key, err := newAEAD(raw)
	must(t, err)
	_, err = d.ExecContext(ctx, "insert into secrets (name, kind, sealed, created, updated) values (?, 'env', ?, 'x', 'x')",
		name, seal(key, []byte(value), secretAAD(name)))
	must(t, err)
	v.mu.Lock()
	v.values = nil
	v.mu.Unlock()
}
