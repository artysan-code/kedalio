package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"kedalio/core/db"
	"kedalio/core/instance"
)

// defaultDataDir is where `kedalio-core stdio` keeps its database when -data is not given: the folder an application of
// the platform keeps its data in. Linux follows the XDG data home; macOS and Windows use the user's application folder
// (UserConfigDir: Application Support, %AppData%). The arguments are what the platform reports, so a test can pass them.
func defaultDataDir(goos, home, configDir, xdgDataHome string) (string, error) {
	switch goos {
	case "windows", "darwin":
		if configDir == "" {
			return "", errors.New("no user application folder: pass -data")
		}
		return filepath.Join(configDir, "Kedalio"), nil
	}
	if xdgDataHome != "" && filepath.IsAbs(xdgDataHome) {
		return filepath.Join(xdgDataHome, "kedalio"), nil
	}
	if home == "" {
		return "", errors.New("no home folder: pass -data")
	}
	return filepath.Join(home, ".local", "share", "kedalio"), nil
}

// runStdio is `kedalio-core stdio`: the core as an MCP server over stdin and stdout, for a client on the same computer
// (Claude Code, Claude Desktop) that starts it. Whoever runs the process is the owner, so there is no listener, token or
// sign-in. stdout carries the protocol and nothing else: logs and the first-run notice go to stderr.
func runStdio(ctx context.Context, args []string) error {
	var o instance.Options
	var badEnv error
	o.Stdio = true
	fs := flag.NewFlagSet("stdio", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&o.Data, "data", env("DATA", ""), "folder of the database and the instance key; empty: the platform's application data folder (docs/local.md)")
	fs.StringVar(&o.Owner, "owner", env("OWNER", ""), "the owner's name, for the instructions clients are given")
	fs.StringVar(&o.Language, "language", env("LANGUAGE", ""), "the language of a NEW database (English, Italian)")
	zone := env("TIMEZONE", env("ZONE", ""))
	fs.StringVar(&o.Zone, "timezone", zone, "the time zone (IANA name); empty: the computer's")
	fs.StringVar(&o.EmbedURL, "embed-url", env("EMBED_URL", ""), "base URL of an OpenAI-compatible embeddings server; empty: search by words alone")
	fs.StringVar(&o.EmbedModel, "embed-model", env("EMBED_MODEL", ""), "the embedding model")
	fs.StringVar(&o.BackupDir, "backup-dir", env("BACKUP_DIR", ""), "folder for the encrypted copies of the database, ideally another disk; empty: no backups")
	fs.DurationVar(&o.BackupEvery, "backup-every", envDuration("BACKUP_EVERY", 24*time.Hour, &badEnv), "how often a copy is made")
	fs.IntVar(&o.BackupKeep, "backup-keep", envInt("BACKUP_KEEP", db.DefaultKeep, &badEnv), "how many copies the folder keeps (the oldest go first)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if badEnv != nil {
		return badEnv
	}
	if o.BackupKeep < 1 || o.BackupEvery < time.Minute {
		return errors.New("-backup-keep is at least 1 and -backup-every at least a minute")
	}
	if o.Data == "" {
		home, _ := os.UserHomeDir()
		cfg, _ := os.UserConfigDir()
		var err error
		if o.Data, err = defaultDataDir(runtime.GOOS, home, cfg, os.Getenv("XDG_DATA_HOME")); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(o.Data, 0o700); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	inst, err := o.Start(ctx)
	if errors.Is(err, instance.ErrLocked) {
		return fmt.Errorf("%s has a database but not its key file (kek): put the key file back; the recovery code opens it only in server mode (docs/local.md)", o.Data)
	} else if err != nil {
		return err
	}
	// the background work ends with ctx: stop it before Close, which waits for it
	defer func() {
		stop()
		inst.Close()
	}()
	// the client hanging up (stdin EOF) ends Serve with a nil error or a closed pipe: both are a normal end
	if err := inst.Serve(ctx, stdioTransport(os.Stdin, os.Stdout)); err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
