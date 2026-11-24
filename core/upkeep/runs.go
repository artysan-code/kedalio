package upkeep

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// OpenFor is how long an unfinished run blocks a new one: a run that died without End must not block the upkeep for
// good.
const OpenFor = 2 * time.Hour

// ErrOpen is Start's refusal while a recent run has not ended.
var ErrOpen = errors.New("an upkeep run is already open")

// Run is one upkeep run.
type Run struct {
	ID      string
	Job     string
	Started string
	Ended   string // "" while open
	Status  string
	Report  string
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Start opens a run of job and returns its id, or ErrOpen (wrapped, with the open run's id) while a run younger than
// OpenFor has not ended.
func Start(ctx context.Context, d *sql.DB, job string, now time.Time) (string, error) {
	var open string
	err := d.QueryRowContext(ctx, `select id from upkeep_runs where ended is null and started > ? order by started desc limit 1`,
		stamp(now.Add(-OpenFor))).Scan(&open)
	if err == nil {
		return "", fmt.Errorf("%w: %s", ErrOpen, open)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := "u-" + hex.EncodeToString(b)
	_, err = d.ExecContext(ctx, `insert into upkeep_runs (id, job, started) values (?, ?, ?)`, id, job, stamp(now))
	return id, err
}

// End closes a run with its status and report. A run that has ended is not changed again: its report is the record.
func End(ctx context.Context, d *sql.DB, id, status, report string, now time.Time) error {
	res, err := d.ExecContext(ctx, `update upkeep_runs set ended = ?, status = ?, report = ? where id = ? and ended is null`,
		stamp(now), status, report, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := d.QueryRowContext(ctx, `select 1 from upkeep_runs where id = ?`, id).Scan(&one); err == nil {
			return fmt.Errorf("upkeep run %q has ended already", id)
		}
		return fmt.Errorf("no upkeep run %q", id)
	}
	return nil
}

// Last is the most recently started run, nil when there is none.
func Last(ctx context.Context, d *sql.DB) (*Run, error) {
	rs, err := Recent(ctx, d, 1)
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	return &rs[0], nil
}

// Recent is the n most recently started runs, newest first; an open run has no Ended and no Status.
func Recent(ctx context.Context, d *sql.DB, n int) ([]Run, error) {
	rows, err := d.QueryContext(ctx, `select id, job, started, ended, status, report from upkeep_runs order by started desc, rowid desc limit ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var ended, status sql.NullString
		if err := rows.Scan(&r.ID, &r.Job, &r.Started, &ended, &status, &r.Report); err != nil {
			return nil, err
		}
		r.Ended, r.Status = ended.String, status.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastStarted is when each job last started a run; a job that never ran is absent.
func LastStarted(ctx context.Context, d *sql.DB) (map[string]string, error) {
	rows, err := d.QueryContext(ctx, `select job, max(started) from upkeep_runs group by job`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var job, at string
		if err := rows.Scan(&job, &at); err != nil {
			return nil, err
		}
		out[job] = at
	}
	return out, rows.Err()
}

// Dismiss records that the owner turned down a proposal (kind "duplicates" or "conflicts", its key), so it is not
// proposed again.
func Dismiss(ctx context.Context, d *sql.DB, kind, key string, now time.Time) error {
	_, err := d.ExecContext(ctx, `insert or replace into upkeep_dismissed (kind, key, at) values (?, ?, ?)`, kind, key, stamp(now))
	return err
}

// Dismissed is the set of keys turned down for kind.
func Dismissed(ctx context.Context, d *sql.DB, kind string) (map[string]bool, error) {
	rows, err := d.QueryContext(ctx, `select key from upkeep_dismissed where kind = ?`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}
