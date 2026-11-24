package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"kedalio/core/identity"
)

// runPasskeys is `kedalio-core passkeys [-data dir] list | remove <id>`: the owner's way to see the passkeys of an
// instance and take one away (a passkey is made at /setup with an approval code, so one the owner did not mean to approve
// must be findable). Removing one also signs every browser out. No key material is printed.
func runPasskeys(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("passkeys", flag.ContinueOnError)
	data := fs.String("data", env("DATA", "data"), "folder of the database (core.db) and the instance key (kek)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 || (rest[0] != "list" && rest[0] != "remove") || (rest[0] == "remove") != (len(rest) == 2) || len(rest) > 2 {
		return errors.New("usage: kedalio-core passkeys [-data dir] list | remove <id>")
	}
	d, err := openData(*data)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := identity.Migrate(ctx, d); err != nil {
		return err
	}
	keys := identity.NewPasskeys(d, identity.Site{})
	if rest[0] == "remove" {
		id, err := keys.Remove(ctx, rest[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "removed the passkey %s and signed every browser out\n", id)
		return nil
	}
	list, err := keys.List(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tCREATED\tLAST USED")
	for _, k := range list {
		used := k.Used
		if used == "" {
			used = "never"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", k.ID, k.Name, k.Created, used)
	}
	return w.Flush()
}
