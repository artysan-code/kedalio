package vault

import (
	"context"
	"database/sql"

	"kedalio/core/db"
)

// steps are the tables of the package, appended to and never edited (db.Migrate). There is one owner, so nothing here
// names an account.
//
// The secrets store, the uses of secrets, the enrolled devices and the audit of them were cut. Their steps stay as
// no-ops so the numbers do not move: a database that ran them keeps their tables, unused and never dropped, and every
// database ends at the same version. A new one only gets what is still used.
var steps = []string{
	// the instance key's id and whether its recovery code was shown. Once it also made the
	// data key's wrap (data_keys), the secrets (secrets) and their audit (vault_audit): an old database has them still,
	// and Mask reads the secrets it holds to keep their values out of answers
	`create table vault_settings (k text primary key, v text not null);`,
	// once: secret_uses, secret_seen, secret_rules, secret_grants
	`select 1;`,
	// once: device_codes, devices, device_nonces, device_leases
	`select 1;`,
}

// Migrate brings the vault's tables up to date.
func Migrate(ctx context.Context, d *sql.DB) error {
	return db.Migrate(ctx, d, "vault", steps)
}
