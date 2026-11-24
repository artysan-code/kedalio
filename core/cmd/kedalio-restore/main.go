// Command kedalio-restore fetches a copy of the core's database from the S3-compatible store the core sends its backups
// to, into a new data folder (see «Backups and restore» in the README).
//
//	BACKUP_S3_ACCESS_KEY=… BACKUP_S3_SECRET_KEY=… kedalio-restore -endpoint https://<account>.r2.cloudflarestorage.com \
//	    -bucket kedalio-backups -to /data/core
//
// The settings are the core's own flags and their KEDALIO_CORE_BACKUP_S3_* twins. The two keys are read from the
// environment because the vault that held them was on the lost server: take them from where you keep them. The
// copy is still encrypted; start kedalio-core on the folder (it comes up locked) and POST the recovery code to /unlock.
// The copy's tag is checked before it is kept: the recovery code comes in KEDALIO_RECOVERY_CODE. -list shows what the store
// has, newest last; -name picks a copy and the newest is the default. A copy older than the newest verified one needs
// -allow-older, a copy with no tag needs -allow-unsigned.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"kedalio/core/db"
	"kedalio/core/offsite"
	"kedalio/core/vault"
)

func env(name, def string) string {
	if v := os.Getenv("KEDALIO_CORE_BACKUP_S3_" + name); v != "" {
		return v
	}
	return def
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "kedalio-restore:", err)
		os.Exit(1)
	}
}

func run() error {
	c := &offsite.Client{}
	flag.StringVar(&c.Endpoint, "endpoint", env("ENDPOINT", ""), "the store's address (https://<account>.r2.cloudflarestorage.com)")
	flag.StringVar(&c.Bucket, "bucket", env("BUCKET", ""), "the bucket")
	flag.StringVar(&c.Region, "region", env("REGION", "auto"), "the store's region (auto for Cloudflare R2)")
	flag.StringVar(&c.Prefix, "prefix", env("PREFIX", offsite.DefaultPrefix), "the folder in the bucket the core writes to")
	to := flag.String("to", "", "the new data folder; it must have no core.db")
	name := flag.String("name", "", "the copy to fetch (core-<time>.db); the newest when empty")
	allowUnsigned := flag.Bool("allow-unsigned", false, "accept a copy that has no tag (made before copies were signed); it is not checked")
	allowOlder := flag.Bool("allow-older", false, "accept a copy older than the newest one the store holds a valid tag for (a possible rollback)")
	list := flag.Bool("list", false, "list the copies in the store and stop")
	flag.Parse()
	c.Prefix = db.NormalizeS3Prefix(c.Prefix)

	access, secret := os.Getenv("BACKUP_S3_ACCESS_KEY"), os.Getenv("BACKUP_S3_SECRET_KEY")
	if c.Endpoint == "" || c.Bucket == "" || access == "" || secret == "" {
		return errors.New("-endpoint, -bucket and the environment BACKUP_S3_ACCESS_KEY and BACKUP_S3_SECRET_KEY are needed")
	}
	c.Credentials = func(context.Context) (string, string, error) { return access, secret, nil }

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	if *list {
		objects, err := c.List(ctx)
		if err != nil {
			return err
		}
		for _, o := range objects {
			fmt.Printf("%s\t%d bytes\n", o.Name, o.Size)
		}
		return nil
	}
	if *to == "" {
		return errors.New("-to is the new data folder")
	}
	// the tags are signed under a key derived from the instance key, so the recovery code is needed to check a copy; like
	// the store's keys it comes from the environment, never from an argument
	kek, err := recoveryKey(os.Getenv("KEDALIO_RECOVERY_CODE"))
	if err != nil {
		return err
	}
	key, err := vault.DatabaseKey(kek)
	if err != nil {
		return err
	}
	got, err := offsite.Restore(ctx, c, *name, *to, offsite.RestoreOptions{
		Key: key, AllowUnsigned: *allowUnsigned, AllowOlder: *allowOlder,
		Warn: func(msg string) { fmt.Fprintln(os.Stderr, "kedalio-restore: warning:", msg) },
	})
	if err != nil {
		return err
	}
	fmt.Printf("%s is in %s/core.db, still encrypted. Start kedalio-core -data %s: it comes up locked; "+
		"POST {\"code\": \"<the recovery code>\"} to /unlock.\n", got, *to, *to)
	return nil
}

// recoveryKey tells a missing recovery code from a malformed one, so a mistyped code is not reported as absent.
func recoveryKey(code string) ([]byte, error) {
	if strings.TrimSpace(code) == "" {
		return nil, errors.New("the environment KEDALIO_RECOVERY_CODE (the recovery code) is needed to check the copy's tag")
	}
	kek, ok := vault.KeyFromCode(code)
	if !ok {
		return nil, errors.New("KEDALIO_RECOVERY_CODE is not a valid recovery code: check it against the one saved at setup")
	}
	return kek, nil
}
