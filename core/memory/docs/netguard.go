// Package docs reads what a collection is made of: a file given to the core, or one URL fetched by it, once. The
// reading is here (PDF, Markdown, HTML, text, CSV, XLSX, images by their description); the cutting and indexing are the
// search package's.
package docs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// What a request to fetch a document may take, and how big its answer may be.
const (
	fetchTimeout = 15 * time.Second
	// MaxFetch is the most a fetched URL may weigh.
	MaxFetch = 10_000_000
	// maxHops is how many redirects are followed, each checked again.
	maxHops = 5
)

// specialPurpose is what the standard library's predicates miss of the IANA special-purpose registries (IPv4 and IPv6):
// ranges that are not globally reachable, or that carry an address of another kind (NAT64, 6to4) to somewhere inside.
// Blocking a whole registry entry also blocks the few globally reachable addresses in it (192.0.0.9, 2001:1::1), which a
// fetch for a document never needs.
var specialPurpose = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "100.64.0.0/10" /* carrier-grade NAT */, "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32",
		"64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20", "5f00::/16",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// IsPrivateIP says whether an address is loopback, private, link-local, shared, unspecified, multicast or reserved,
// or an IPv4 address mapped into IPv6 that is one of those: the addresses a fetch for a collection never reaches.
func IsPrivateIP(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() {
		return true
	}
	for _, p := range specialPurpose {
		if p.Contains(a) {
			return true
		}
	}
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified()
}

// RefusedHost gives the reason a host name is refused without asking the DNS, or "".
func RefusedHost(host string) string {
	h := strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if a, err := netip.ParseAddr(h); err == nil {
		if IsPrivateIP(a) {
			return host + " is a private address"
		}
		return ""
	}
	if !strings.Contains(h, ".") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") ||
		strings.HasSuffix(h, ".localhost") {
		return host + " is not a public host"
	}
	return ""
}

// CleanURL gives an address in the form it is kept, or the reason it cannot be fetched: http or https, no credentials,
// a public host.
func CleanURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", errors.New("origin is the URL of a page or a file")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("origin is an http or https URL")
	}
	if u.User != nil {
		return "", errors.New("an origin carries no credentials")
	}
	if why := RefusedHost(u.Hostname()); why != "" {
		return "", errors.New("origin: " + why)
	}
	return u.String(), nil
}

// Fetcher gets documents from the network, never from the core's own: the address behind every hop is checked, and the
// check is made on the address the connection goes to, so a name that answers differently the second time (DNS
// rebinding) reaches nothing private either.
type Fetcher struct {
	// Client replaces the guarded client; for tests that serve from the loopback. Left nil, the guard is on.
	Client *http.Client
}

// clean is CleanURL, unless the fetcher has a client of its own (tests): then the address is the caller's to trust.
func (f *Fetcher) clean(raw string) (string, error) {
	if f == nil || f.Client == nil {
		return CleanURL(raw)
	}
	return strings.TrimSpace(raw), nil
}

// guarded is the client that checks every connection.
func guarded() *http.Client {
	d := &net.Dialer{
		Timeout: fetchTimeout,
		// Control runs with the address the connection is about to be made to, after the name is resolved
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			a, err := netip.ParseAddr(host)
			if err != nil || IsPrivateIP(a) {
				return fmt.Errorf("%s points to a private address", host)
			}
			return nil
		},
	}
	return &http.Client{
		Timeout:   fetchTimeout,
		Transport: &http.Transport{DialContext: d.DialContext, Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxHops {
				return fmt.Errorf("more than %d redirects", maxHops)
			}
			if _, err := CleanURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
}

// Body is an answer: its content type and its bytes.
type Body struct {
	Type  string
	Bytes []byte
}

// Get is the answer of an HTTP GET without credentials, read up to max bytes; the error says why it is not usable.
func (f *Fetcher) Get(ctx context.Context, rawURL string, max int) (Body, error) {
	if _, err := f.clean(rawURL); err != nil {
		return Body{}, err
	}
	client := guarded()
	if f != nil && f.Client != nil {
		client = f.Client
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Body{}, err
	}
	req.Header.Set("User-Agent", "Kedalio/1 (+collection fetcher)")
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return Body{}, fmt.Errorf("%s: %w", plainURL(rawURL), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Body{}, fmt.Errorf("%d from %s", resp.StatusCode, plainURL(rawURL))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(max)+1))
	if err != nil {
		return Body{}, err
	}
	if len(b) > max {
		return Body{}, fmt.Errorf("%s is over %d MB", plainURL(rawURL), max/1_000_000)
	}
	return Body{Type: strings.ToLower(resp.Header.Get("Content-Type")), Bytes: b}, nil
}

// plainURL is rawURL as an error message may carry it: scheme, host and path only. The query (and any user info or
// fragment) can hold a token, and the message ends in logs and tool results.
func plainURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "the address"
	}
	return u.Scheme + "://" + u.Host + u.Path
}
