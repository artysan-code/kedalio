package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"text/tabwriter"

	"kedalio/core/identity"
	"kedalio/core/mcp"
)

const tokensUsage = "usage: kedalio-core tokens [-data dir] create <name> -policy cloud | list | revoke <name>"

// runTokens is `kedalio-core tokens [-data dir] create <name> -policy <policy> | list | revoke <name>`: the operator's
// way to make the static bearer tokens headless agents call /mcp with, each bound to a policy and to its own author
// name, and to take one away. It runs beside a running core (openData). A token's text is printed once, by create; the
// database keeps its hash only, and no log line carries it.
func runTokens(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("tokens", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	data := fs.String("data", env("DATA", "data"), "folder of the database (core.db) and the instance key (kek)")
	if err := fs.Parse(args); err != nil {
		return errors.New(tokensUsage)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New(tokensUsage)
	}
	cmd, rest := rest[0], rest[1:]

	// flag stops at the first argument that is not a flag, so create reads its own flags after the name
	var name, policy string
	switch cmd {
	case "create":
		cf := flag.NewFlagSet("tokens create", flag.ContinueOnError)
		cf.SetOutput(io.Discard)
		p := cf.String("policy", "", "what the token may do: cloud")
		if len(rest) == 0 {
			return errors.New(tokensUsage)
		}
		name = rest[0]
		if err := cf.Parse(rest[1:]); err != nil || cf.NArg() != 0 {
			return errors.New(tokensUsage)
		}
		if !mcp.KnownPolicy(*p) {
			return fmt.Errorf("unknown policy %q: the only one is cloud", *p)
		}
		policy = *p
	case "revoke":
		if len(rest) != 1 {
			return errors.New(tokensUsage)
		}
		name = rest[0]
	case "list":
		if len(rest) != 0 {
			return errors.New(tokensUsage)
		}
	default:
		return errors.New(tokensUsage)
	}

	d, err := openData(*data)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := identity.Migrate(ctx, d); err != nil {
		return err
	}
	tk := identity.NewTokens(d)

	switch cmd {
	case "create":
		token, err := tk.Create(ctx, name, policy)
		if err != nil {
			return err
		}
		slog.Info("token created", "name", name, "policy", policy)
		fmt.Fprintf(out, "token %s (policy %s). It will not be shown again; keep it now:\n%s\n", name, policy, token)
	case "revoke":
		if err := tk.Revoke(ctx, name); err != nil {
			return err
		}
		slog.Info("token revoked", "name", name)
		fmt.Fprintf(out, "revoked %s: the next call with it gets 401\n", name)
	default:
		list, err := tk.List(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tPOLICY\tCREATED\tLAST USED\tREVOKED")
		for _, e := range list {
			used, revoked := e.Used, e.Revoked
			if used == "" {
				used = "never"
			}
			if revoked == "" {
				revoked = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Name, e.Policy, e.Created, used, revoked)
		}
		return w.Flush()
	}
	return nil
}
