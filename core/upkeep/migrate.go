// Package upkeep is the bookkeeping of the memory's upkeep runs: which run is open, what was last done and what the
// owner turned down, plus the pure checks that keep a run from losing what the memory said.
package upkeep

import (
	"context"
	"database/sql"

	"kedalio/core/db"
)

// steps are the tables of the package, appended to and never edited (db.Migrate).
var steps = []string{
	`create table upkeep_runs (
  id text primary key, job text not null, started text not null, ended text, status text, report text not null default '');
create table upkeep_dismissed (kind text not null, key text not null, at text not null, primary key (kind, key));`,
}

// Migrate brings the upkeep's tables up to date.
func Migrate(ctx context.Context, d *sql.DB) error {
	return db.Migrate(ctx, d, "upkeep", steps)
}
