package instance

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// A database has one language and one time zone, fixed when it is made: the language picks the memory's area names
// (so where its pages live), and a flag that changed it would silently move every path. They are kept in memory_meta
// (rows added, nothing altered).
const (
	metaLanguage = "language"
	metaZone     = "zone"

	// legacyZone is what the code before 1.0 used when no zone was asked for. Its language default was English, the
	// same as now, so only the zone differs.
	legacyZone = "Europe/Rome"
)

// languageName is the canonical name of a language setting: Italian for "it" and "Italian…", English for the rest.
func languageName(s string) string {
	if l := strings.ToLower(strings.TrimSpace(s)); l == "it" || strings.HasPrefix(l, "ital") {
		return "Italian"
	}
	return "English"
}

// hostZoneName is the IANA name of the host's zone: TZ, else /etc/localtime's target, else /etc/timezone, else UTC.
// time.Local cannot say its name ("Local"), and the name is what the database stores.
func hostZoneName() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(target, "zoneinfo/"); ok {
			if _, err := time.LoadLocation(name); err == nil {
				return name
			}
		}
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if name := strings.TrimSpace(string(b)); name != "" {
			if _, err := time.LoadLocation(name); err == nil {
				return name
			}
		}
	}
	return "UTC"
}

// hasMemory says whether the database was made and used by an earlier start; it must be asked before memory.Migrate.
// An empty set of tables (a first start that failed before it wrote anything) does not count: that database is new.
func hasMemory(ctx context.Context, sqldb *sql.DB) (bool, error) {
	var n int
	if err := sqldb.QueryRowContext(ctx, "select count(*) from sqlite_master where type = 'table' and name in ('memory_meta', 'docs')").Scan(&n); err != nil || n < 2 {
		return false, err
	}
	var any bool
	err := sqldb.QueryRowContext(ctx, "select exists(select 1 from docs)").Scan(&any)
	return any, err
}

func meta(ctx context.Context, sqldb *sql.DB, k string) (string, error) {
	var v string
	err := sqldb.QueryRowContext(ctx, "select v from memory_meta where k = ?", k).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// settleLocale gives the database's language and zone name, writing them when the database has none yet. A new
// database takes the asked language (English when none) and the asked zone, else the host's. A database from before
// 1.0 (used = true) takes what the code before 1.0 would have run with: the asked language (English when none) and the
// asked zone (Europe/Rome when none), so an instance that never set them does not change. A language asked against a
// stored one is told to the log and ignored. An asked zone wins for this run without being stored. Everything is
// checked before anything is written, and the two rows go in together.
func settleLocale(ctx context.Context, sqldb *sql.DB, used bool, o Options) (language, zone string, err error) {
	if o.Zone != "" {
		if _, err = time.LoadLocation(o.Zone); err != nil {
			return "", "", fmt.Errorf("time zone %q: %w", o.Zone, err)
		}
	}
	language, err = meta(ctx, sqldb, metaLanguage)
	if err != nil {
		return "", "", err
	}
	had := language != ""
	stored, err := meta(ctx, sqldb, metaZone)
	if err != nil {
		return "", "", err
	}
	if language == "" || stored == "" {
		if language == "" {
			language = languageName(o.Language)
		}
		if stored == "" {
			switch {
			case o.Zone != "":
				stored = o.Zone
			case used:
				stored = legacyZone
			default:
				stored = hostZoneName()
			}
		}
		tx, err := sqldb.BeginTx(ctx, nil)
		if err != nil {
			return "", "", err
		}
		defer tx.Rollback()
		for k, v := range map[string]string{metaLanguage: language, metaZone: stored} {
			if _, err = tx.ExecContext(ctx, "insert or ignore into memory_meta (k, v) values (?, ?)", k, v); err != nil {
				return "", "", err
			}
		}
		if err = tx.Commit(); err != nil {
			return "", "", err
		}
	}
	if had && o.Language != "" && languageName(o.Language) != language {
		slog.Warn("the language asked for is not this database's: it keeps its own, its pages are in its area names",
			"asked", o.Language, "database", language)
	}
	zone = stored
	if o.Zone != "" {
		zone = o.Zone
	}
	return language, zone, nil
}

// StoredLocale is the language and zone name of a database that is only read (a copy of one, for the recall
// evaluation): what it stores, a given language or zone taking its place; a database that stores nothing is read as
// the code before 1.0 ran it (English, Europe/Rome). Nothing is written.
func StoredLocale(ctx context.Context, sqldb *sql.DB, language, zone string) (string, string, error) {
	l, err := meta(ctx, sqldb, metaLanguage)
	if err != nil {
		return "", "", err
	}
	z, err := meta(ctx, sqldb, metaZone)
	if err != nil {
		return "", "", err
	}
	if language != "" {
		l = language
	}
	if zone != "" {
		z = zone
	}
	if z == "" {
		z = legacyZone
	}
	return languageName(l), z, nil
}
