// Package db opens the core's SQLite database and applies each package's migrations.
package db

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ncruces/go-sqlite3"
	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"
)

func init() {
	sqlite3.AutoExtension(fts5.Register)
}

// Open opens (or creates) the database at path in WAL mode with foreign keys on. Writes take the lock when the
// transaction starts, so two writers wait for each other instead of failing halfway. ":memory:" gives a private
// database for tests. The file is plain: the core uses OpenEncrypted.
func Open(path string) (*sql.DB, error) {
	return open(path, nil)
}

// ErrBadKey is what OpenEncrypted answers when the file is not a database that this key opens.
var ErrBadKey = errors.New("the database does not open with this key")

// OpenEncrypted opens (or creates) the database at path encrypted at rest with key, 32 bytes (vault.DatabaseKey): the
// file, its WAL and any temporary file hold nothing readable (ADR 0002 decision 11). A wrong key, or a file that was
// never encrypted, is ErrBadKey.
func OpenEncrypted(path string, key []byte) (*sql.DB, error) {
	if len(key) != 32 {
		return nil, errors.New("the database key is 32 bytes")
	}
	return open(path, key)
}

// ping opens the first connection. Two processes opening a new file at once (two processes of the owner's)
// race to switch it to WAL, which takes the lock without waiting on busy_timeout: the loser retries instead of
// failing — and, on an encrypted file, instead of being taken for a wrong key.
func ping(db *sql.DB) error {
	var err error
	for wait := 10 * time.Millisecond; ; wait *= 2 {
		if err = db.Ping(); err == nil || !errors.Is(err, sqlite3.BUSY) || wait > 2*time.Second {
			return err
		}
		time.Sleep(wait)
	}
}

// MaxConns is how many connections a file database opens at once: one is the memory store's data_version probe.
const MaxConns = 5

func open(path string, key []byte) (*sql.DB, error) {
	dsn := "file:" + uriPath.Replace(path) + "?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	if key != nil {
		// temp tables and sort spills stay in memory instead of going through the cipher
		dsn += "&vfs=adiantum&hexkey=" + hex.EncodeToString(key) + "&_pragma=temp_store(memory)"
	}
	if path != ":memory:" {
		dsn += "&_pragma=journal_mode(wal)&_pragma=synchronous(normal)"
		// The driver creates files with the umask's mode, usually 0644: the database is created first at 0600 and
		// modeof gives the WAL the same mode. The shm file keeps the umask's; it holds only the WAL's page index.
		if err := createPrivate(path); err != nil {
			return nil, err
		}
		dsn += "&modeof=" + uriParam.Replace(path)
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		// Every connection to ":memory:" is a database of its own.
		db.SetMaxOpenConns(1)
	} else {
		// Each connection is a SQLite instance with its own memory: left unbounded, a burst of readers grows the
		// process by about a hundred MB. Writes are serialised by the database lock anyway, so a few connections
		// serve the readers and the one writer.
		db.SetMaxOpenConns(MaxConns)
		db.SetMaxIdleConns(MaxConns)
		db.SetConnMaxIdleTime(time.Minute)
	}
	if err := ping(db); err != nil {
		db.Close()
		return nil, openError(err, key)
	}
	if key != nil {
		// a connection can open before anything is read; a wrong key shows at the first page
		var n int
		if err := db.QueryRow("select count(*) from sqlite_master").Scan(&n); err != nil {
			db.Close()
			return nil, openError(err, key)
		}
	}
	return db, nil
}

// createPrivate creates path empty with mode 0600 when it does not exist; an existing file is left as it is.
func createPrivate(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	} else if err != nil {
		return err
	}
	return f.Close()
}

// openError tells a wrong key from any other failure: only a file that is not a database (or reads as corrupt, which
// is how the cipher shows a wrong key) is ErrBadKey. Permissions or a full disk must not make the owner replace a good
// key. Any other error is kept as text with the key masked, since some sqlite errors quote the DSN.
func openError(err error, key []byte) error {
	if errors.Is(err, sqlite3.NOTADB) || errors.Is(err, sqlite3.CORRUPT) {
		return ErrBadKey
	}
	return errors.New(redact(err, key))
}

// redact is err's text with the key masked.
func redact(err error, key []byte) string {
	msg := err.Error()
	if key != nil {
		msg = strings.ReplaceAll(msg, hex.EncodeToString(key), "***")
	}
	return msg
}

// ErrNewerDatabase: the file was migrated by a newer Kedalio than this binary.
var ErrNewerDatabase = errors.New("database was created by a newer Kedalio; upgrade the binary")

// Migrate brings the tables of one package (its name, as in "memory") up to date: steps[i] is version i+1, applied
// once, in order, each in its own transaction. Steps are only ever appended.
func Migrate(ctx context.Context, db *sql.DB, name string, steps []string) error {
	if _, err := db.ExecContext(ctx,
		"create table if not exists schema_versions (name text primary key, version integer not null)"); err != nil {
		return err
	}
	var have int
	err := db.QueryRowContext(ctx, "select version from schema_versions where name = ?", name).Scan(&have)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if have > len(steps) {
		return fmt.Errorf("%s: %w (schema %d, this binary knows %d)", name, ErrNewerDatabase, have, len(steps))
	}
	for v := have; v < len(steps); v++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		// Another process may have applied this step since the read above: the transaction holds the write lock
		// (_txlock=immediate), so the version read inside it is the one that counts.
		var now int
		err = tx.QueryRowContext(ctx, "select version from schema_versions where name = ?", name).Scan(&now)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			tx.Rollback()
			return err
		}
		if now > v {
			tx.Rollback()
			v = now - 1
			continue
		}
		if _, err := tx.ExecContext(ctx, steps[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s migration %d: %w", name, v+1, err)
		}
		if _, err := tx.ExecContext(ctx,
			"insert into schema_versions (name, version) values (?, ?) on conflict (name) do update set version = excluded.version",
			name, v+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
