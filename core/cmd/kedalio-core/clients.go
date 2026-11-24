package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"kedalio/core/db"
	"kedalio/core/identity"
	"kedalio/core/vault"
)

// runClients is `kedalio-core clients [-data dir] list | revoke <client-id>`: the owner's way to see the OAuth clients
// that were given access (claude.ai's connector, Claude Code) and to take one away. It opens the database with the
// instance key the data folder holds (openData). No token value is ever printed.
func runClients(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("clients", flag.ContinueOnError)
	data := fs.String("data", env("DATA", "data"), "folder of the database (core.db) and the instance key (kek)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 || (rest[0] != "list" && rest[0] != "revoke") || (rest[0] == "revoke") != (len(rest) == 2) || len(rest) > 2 {
		return errors.New("usage: kedalio-core clients [-data dir] list | revoke <client-id>")
	}

	d, err := openData(*data)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := identity.Migrate(ctx, d); err != nil {
		return err
	}
	auth := identity.NewAuth(d, "")

	if rest[0] == "revoke" {
		ok, err := auth.RevokeClient(ctx, rest[1])
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no client %q (see `clients list`)", rest[1])
		}
		fmt.Fprintf(out, "revoked %s: its tokens are gone and it must be approved again\n", rest[1])
		return nil
	}
	list, err := auth.Clients(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tHOST\tKIND\tCREATED\tLAST USED\tACTIVE TOKENS")
	for _, c := range list {
		kind, used := "local", c.LastUsed
		if c.Remote {
			kind = "cloud"
		}
		if used == "" {
			used = "never"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n", c.ID, identity.CleanName(c.Name), c.Host, kind, c.Created, used, c.Active)
	}
	return w.Flush()
}

// openData opens the encrypted database of a data folder with the instance key it holds, as the running core does (the
// two can run at once: the database is WAL), so a core still locked (no key file) cannot be reached
// this way. It refuses a folder without core.db: opening creates a missing file, and an empty core.db would make the
// next start skip the restore and the unlock.
func openData(data string) (*sql.DB, error) {
	raw, err := vault.HostKey(data, false)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("%s has no instance key (kek): the core there waits for its recovery code and this command cannot open the database", data)
	}
	key, err := vault.DatabaseKey(raw)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(data, "core.db")
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("%s has no database (core.db): wrong -data, or a restore not finished", data)
	}
	return db.OpenEncrypted(path, key)
}
