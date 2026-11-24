package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"kedalio/core/memory"
	"kedalio/core/memory/search"
	"kedalio/core/tasks"
	"kedalio/core/vault"
)

const revisionsUsage = "usage: kedalio-core revisions redact [-data dir] [-dry-run] (<path>@<rev> | <path>)+"

// target is one revision named on the command line: rev 0 is the live page.
type target struct {
	path string
	rev  int
}

// parseTargets reads path@rev (rev a positive number) and path (the live page). All are read before any is touched, so
// a typo in the last one does not leave the first ones done.
func parseTargets(args []string) ([]target, error) {
	if len(args) == 0 {
		return nil, errors.New(revisionsUsage)
	}
	out := make([]target, 0, len(args))
	for _, a := range args {
		t := target{path: a}
		if i := strings.LastIndex(a, "@"); i >= 0 {
			rev, err := strconv.Atoi(a[i+1:])
			if err != nil || rev < 1 {
				return nil, fmt.Errorf("%q: the revision after @ is a number from 1", a)
			}
			t = target{path: a[:i], rev: rev}
		}
		p, err := memory.CleanPath(t.path)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", a, err)
		}
		t.path = p
		out = append(out, t)
	}
	return out, nil
}

// runRevisions is `kedalio-core revisions redact [-data dir] [-dry-run] <path>@<rev> | <path> ...`: it takes the values
// the secret scan finds (vault.Redact) out of stored revisions, in place, keeping each revision and everything else in
// it as it was; a path alone is the live page, rewritten with its index. It prints, per revision, how many values it
// replaced, never a value. With -dry-run it prints the counts and writes nothing. It sees what the scan's shapes and
// name = value pairs see, not a value only the running core's mask knows; the backups made before it keep the old text
// until they rotate out, and the page's vectors (no text) until its next write: a leaked credential is rotated anyway.
func runRevisions(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "redact" {
		return errors.New(revisionsUsage)
	}
	fs := flag.NewFlagSet("revisions redact", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	data := fs.String("data", env("DATA", "data"), "folder of the database (core.db) and the instance key (kek)")
	dry := fs.Bool("dry-run", false, "print the counts, change nothing")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w; %s", err, revisionsUsage)
	}
	targets, err := parseTargets(fs.Args())
	if err != nil {
		return err
	}

	d, err := openData(*data)
	if err != nil {
		return err
	}
	defer d.Close()
	// one connection, so that secure_delete holds for every write: SQLite then zeroes the bytes a rewrite frees instead
	// of leaving the old text in the file's free space
	d.SetMaxOpenConns(1)
	if _, err := d.ExecContext(ctx, "pragma secure_delete = on"); err != nil {
		return err
	}
	if err := memory.Migrate(ctx, d); err != nil {
		return err
	}
	index, err := search.New(ctx, d)
	if err != nil {
		return err
	}
	if err := tasks.Migrate(ctx, d); err != nil {
		return err
	}
	store := memory.New(d, index)
	// as the running core indexes tasks (instance: tasks.NewBook(…, "owner", …))
	store.SetTaskIndexer(tasks.Index{Me: "owner"})

	verb := "replaced"
	if *dry {
		verb = "would replace"
	}
	total, live := 0, false
	for _, t := range targets {
		r, err := store.Redact(ctx, t.path, t.rev, vault.Redact, *dry)
		if err != nil {
			return err
		}
		line := fmt.Sprintf("%s@%d: %s %d", r.Path, r.Rev, verb, r.Replaced)
		if r.Live {
			line += " (live page)"
		}
		if r.Removed > 0 {
			line += fmt.Sprintf(", %d in the removed page's kept text", r.Removed)
		}
		if r.Conflicts > 0 {
			line += fmt.Sprintf(", %d fact rows", r.Conflicts)
		}
		fmt.Fprintln(out, line)
		total += r.Replaced + r.Removed
		live = live || (r.Live && r.Replaced > 0)
	}
	fmt.Fprintf(out, "total: %s %d in %d revisions\n", verb, total, len(targets))
	if *dry {
		return nil
	}
	if live {
		if err := index.Optimize(ctx); err != nil {
			return err
		}
	}
	// the WAL holds the rewritten pages until a checkpoint; the running core's readers may keep it from finishing now,
	// and it does it later on its own
	var busy, logged, done int
	if err := d.QueryRowContext(ctx, "pragma wal_checkpoint(truncate)").Scan(&busy, &logged, &done); err != nil {
		return err
	}
	if busy != 0 {
		fmt.Fprintln(out, "checkpoint: busy, the running core finishes it")
	}
	return nil
}
