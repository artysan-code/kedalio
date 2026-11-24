package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"kedalio/core/identity"
)

// geteuid is a variable so a test can be root.
var geteuid = os.Geteuid

// runApprove is `kedalio-core approve [-data dir] [-deny] [-yes] <code>`: the owner approves the code a new client's
// sign-in page shows (ADR 0007). It shows who asks (the page's claims, not facts) and waits for a yes, so that a code
// somebody else asked the owner to approve is looked at once more. The code is never printed back.
func runApprove(ctx context.Context, args []string, in io.Reader, out io.Writer, tty bool) error {
	fs := flag.NewFlagSet("approve", flag.ContinueOnError)
	data := fs.String("data", env("DATA", "data"), "folder of the database (core.db) and the instance key (kek)")
	deny := fs.Bool("deny", false, "refuse the request instead of approving it")
	yes := fs.Bool("yes", false, "approve without asking (for a script; it takes away the look at who asks)")
	list := fs.Bool("list", false, "list the requests waiting, never their codes")
	denyAll := fs.Bool("deny-all", false, "refuse every request waiting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *list || *denyAll {
		if *list && *denyAll || fs.NArg() != 0 {
			return errors.New("usage: kedalio-core approve [-data dir] -list | -deny-all")
		}
	} else if fs.NArg() != 1 {
		return errors.New("usage: kedalio-core approve [-data dir] [-deny] [-yes] <code> | -list | -deny-all")
	}
	// the database is shared with the running core: files made by root could not be opened by the core after
	if geteuid() == 0 {
		return errors.New("do not run this as root (docker compose exec -u root): the core's own user owns the database files")
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
	switch {
	case *list:
		return listApprovals(ctx, auth, out)
	case *denyAll:
		n, err := auth.DenyAllPending(ctx, "cli")
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "denied %d waiting request(s)\n", n)
		return nil
	}
	code := fs.Arg(0)
	v, err := auth.ShowApproval(ctx, code)
	if err != nil {
		return err
	}
	printApproval(out, v)
	if !*deny && !*yes {
		if !tty {
			return errors.New("approving needs a terminal to ask you: run it through `ssh -t` and `docker compose exec` (not -T), or add -yes")
		}
		fmt.Fprint(out, "Approve? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(out, "\nnot approved; the request waits until it expires")
			return nil
		}
	}
	if _, err := auth.Decide(ctx, code, !*deny, "cli"); err != nil {
		return err
	}
	if *deny {
		fmt.Fprintln(out, "denied")
	} else if v.Purpose == identity.PurposePasskey {
		fmt.Fprintln(out, "approved: the page makes the passkey in a moment")
	} else {
		fmt.Fprintln(out, "approved: the client connects on its own in a moment")
	}
	return nil
}

func listApprovals(ctx context.Context, auth *identity.Auth, out io.Writer) error {
	list, err := auth.PendingApprovals(ctx)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(out, "no waiting requests")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tKIND\tCLIENT\tHOST\tFROM\tAGE")
	for _, p := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", p.ID, p.Kind, orDash(p.ClientName), orDash(p.Host), orDash(p.IP), p.Age.Round(time.Second))
	}
	return w.Flush()
}

func printApproval(out io.Writer, v identity.ApprovalView) {
	if v.Purpose == identity.PurposePasskey {
		fmt.Fprintln(out, "Request:   add a passkey to this Kedalio")
	} else {
		fmt.Fprintf(out, "Client:    %s (self-declared name, not verified)\n", identity.CleanName(v.ClientName))
		fmt.Fprintf(out, "Kind:      %s\n", v.Kind)
		fmt.Fprintf(out, "Goes to:   %s\n", v.RedirectHost)
	}
	fmt.Fprintf(out, "Asked:     %s ago, from %s\n", v.Age.Round(time.Second), orDash(v.IP))
	fmt.Fprintf(out, "Browser:   %s\n", orDash(v.Agent))
	fmt.Fprintln(out, "Approve only a code you started yourself, just now: a code someone sent you is how an attacker asks to get in.")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
