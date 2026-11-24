package identity

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Options configure an Identity.
type Options struct {
	// PublicURL is the origin the core is reached at, as clients and browsers see it (https://core.example.com): the
	// OAuth issuer, the base of the redirects, and what a passkey is bound to. https, or http on a loopback address.
	PublicURL string
	// ClientIPHeader names the header a proxy in front sets with the client's address; empty: the connection's own.
	ClientIPHeader string
	// TrustedProxies are the proxies whose client-address header is believed (see IPSource); empty: any.
	TrustedProxies []netip.Prefix
	// Name is the owner's name, shown by the device that keeps the passkey; «Kedalio owner» when empty.
	Name string
	// Language is the instance's language setting (a name: English, Italian): the language of the pages; empty: the browser's.
	Language string
}

// Identity is the owner's sign-in: passkeys, sessions, and OAuth for MCP clients.
type Identity struct {
	Passkeys *Passkeys
	Auth     *Auth

	opt   Options
	url   string
	site  Site
	https bool

	forms, poll, token, register, registerAll *Buckets
}

// New migrates the tables and makes the identity.
func New(ctx context.Context, d *sql.DB, o Options) (*Identity, error) {
	u, err := url.Parse(strings.TrimRight(o.PublicURL, "/"))
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("the public URL %q is not an origin (scheme://host[:port])", o.PublicURL)
	}
	// a passkey works in a secure context only: https, or a loopback address for a trial on this machine
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return nil, fmt.Errorf("the public URL %q must be https (http only on a loopback address)", o.PublicURL)
	}
	site, err := SiteOf(u.String())
	if err != nil {
		return nil, err
	}
	if err := Migrate(ctx, d); err != nil {
		return nil, err
	}
	if o.Name == "" {
		o.Name = "Kedalio owner"
	}
	return &Identity{
		Passkeys: NewPasskeys(d, site), Auth: NewAuth(d, u.String()), opt: o, url: u.String(), site: site,
		https: u.Scheme == "https",
		forms: NewBuckets(10, 6*time.Second), poll: NewBuckets(60, time.Second), token: NewBuckets(30, 2*time.Second),
		register: NewBuckets(5, time.Minute), registerAll: NewBuckets(30, 30*time.Second),
	}, nil
}

// Verify is whether a bearer token is an OAuth access token this core issued (the shape mcp.Config.Verify takes).
func (i *Identity) Verify(ctx context.Context, token string) bool { return i.Auth.Bearer(ctx, token) }

// Client is the name of the OAuth client an access token was issued to ("" when it is none the core knows), the
// connector a write came through, kept on the revision; and whether that client is remote (Auth.ClientOf).
func (i *Identity) Client(ctx context.Context, token string) (string, bool) {
	return i.Auth.ClientOf(ctx, token)
}

// ResourceMetadataURL is where clients find the authorization server of /mcp, for the WWW-Authenticate of a 401.
func (i *Identity) ResourceMetadataURL() string { return i.Auth.ResourceMetadataURL() }

// PurgeEvery forgets the rows nothing will read again, once per interval, until the context ends.
func (i *Identity) PurgeEvery(ctx context.Context, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if _, err := i.Auth.Purge(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("identity: purging", "err", err)
			}
		}
	}
}
