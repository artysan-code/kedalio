package vault

import (
	"database/sql"
	"testing"

	"kedalio/core/db"
)

func tables(t *testing.T, d *sql.DB) map[string]bool {
	t.Helper()
	rows, err := d.QueryContext(ctx, "select name from sqlite_master where type = 'table'")
	must(t, err)
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		must(t, rows.Scan(&n))
		out[n] = true
	}
	return out
}

func version(t *testing.T, d *sql.DB) int {
	t.Helper()
	var v int
	must(t, d.QueryRowContext(ctx, "select version from schema_versions where name = 'vault'").Scan(&v))
	return v
}

// The old steps, as they ran before the secrets store, devices and uses were cut: a database that ran them keeps what
// they made, and a new one does not get it.
const oldStepOne = `create table vault_settings (k text primary key, v text not null);
create table data_keys (kind text primary key, wrapped text not null, created text not null);
create table secrets (name text primary key, kind text not null, sealed text not null, backend text not null default 'kedalio', created text not null, updated text not null, used text);
create table vault_audit (seq integer primary key autoincrement, at text not null, actor text not null, action text not null, subject text not null);`

func TestAFreshDatabaseHasNoTableOfTheCutParts(t *testing.T) {
	d := newDB(t)
	got := tables(t, d)
	for _, n := range []string{"secrets", "data_keys", "vault_audit", "secret_uses", "secret_seen", "secret_rules",
		"secret_grants", "device_codes", "devices", "device_nonces", "device_leases"} {
		if got[n] {
			t.Errorf("a new database has the table %s", n)
		}
	}
	if !got["vault_settings"] {
		t.Error("the key's settings are kept")
	}
	if v := version(t, d); v != len(steps) {
		t.Errorf("version %d", v)
	}
}

func TestADatabaseThatRanTheOldStepsKeepsItsTablesAndReachesTheSameVersion(t *testing.T) {
	d, err := db.Open(":memory:")
	must(t, err)
	t.Cleanup(func() { d.Close() })
	_, err = d.ExecContext(ctx, "create table schema_versions (name text primary key, version integer not null)")
	must(t, err)
	_, err = d.ExecContext(ctx, oldStepOne)
	must(t, err)
	_, err = d.ExecContext(ctx, "insert into schema_versions values ('vault', 1)")
	must(t, err)
	_, err = d.ExecContext(ctx, "insert into secrets (name, kind, sealed, created, updated) values ('A', 'env', 'x', 'x', 'x')")
	must(t, err)

	must(t, Migrate(ctx, d))

	if !tables(t, d)["secrets"] {
		t.Error("an old table is never dropped")
	}
	var n int
	must(t, d.QueryRowContext(ctx, "select count(*) from secrets").Scan(&n))
	if n != 1 {
		t.Errorf("rows %d", n)
	}
	if v := version(t, d); v != version(t, newDB(t)) {
		t.Errorf("version %d differs from a new database's", v)
	}
}
