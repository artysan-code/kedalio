package identity

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// bodyLimit is the most a request to the sign-in and OAuth routes may send: their forms are small.
const bodyLimit = 64 << 10

// IPSource says where a request's client address is read from. Behind a proxy the connection is the proxy's, so the
// deployment names the header that carries the client (KEDALIO_CORE_CLIENT_IP_HEADER: cf-connecting-ip or x-real-ip
// hold one address, x-forwarded-for a list) and, optionally, the proxies it trusts (KEDALIO_CORE_TRUSTED_PROXIES, CIDRs).
//
// Trust model: a header is only as good as the proxy that writes it. With Trusted set, the header counts only when the
// connection itself comes from a trusted proxy (a client reaching the service directly can write any value in it), and
// in a list the right-most entry that is not a trusted proxy is the client: what is left of it was written by the client
// or by something it controls. Without Trusted the right-most entry counts (one proxy hop). A header that holds no
// address falls back to the connection's own.
type IPSource struct {
	Header  string
	Trusted []netip.Prefix
}

// ClientIP is the address a request came from, as a key for the limiters: an IPv6 client is keyed by its /64, the
// smallest network an ISP hands out, or one host with a /64 (or more) of its own would have 2^64 buckets to spend.
func (s IPSource) ClientIP(r *http.Request) string {
	a := s.addr(r)
	if ip, err := netip.ParseAddr(a); err == nil && ip.Is6() && !ip.Is4In6() {
		return netip.PrefixFrom(ip, 64).Masked().String()
	}
	return a
}

func (s IPSource) addr(r *http.Request) string {
	conn := r.RemoteAddr
	if host, _, err := net.SplitHostPort(conn); err == nil {
		conn = host
	}
	if s.Header == "" || (len(s.Trusted) > 0 && !s.trusts(conn)) {
		return conn
	}
	parts := strings.Split(strings.Join(r.Header.Values(s.Header), ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			return conn // not an address: not what a proxy writes
		}
		if ip = ip.Unmap(); !s.trusts(ip.String()) {
			return ip.String()
		}
	}
	return conn
}

func (s IPSource) trusts(addr string) bool {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	for _, p := range s.Trusted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// WarnIPSource logs what a deployment's client-address setting gets wrong, once at startup. It only warns: a deployment
// that runs today has to keep starting.
func WarnIPSource(log *slog.Logger, publicURL string, s IPSource) {
	if publicURL != "" && s.Header == "" {
		log.Warn("rate limits: -public-url is set but no -client-ip-header: behind a proxy or tunnel every client shares the " +
			"proxy's address, so one outsider can use up the sign-in, token and unlock limits for everyone; set the header the proxy writes")
	}
	if s.Header != "" && len(s.Trusted) == 0 {
		log.Warn("rate limits: the client-ip header is trusted from any connection (no -trusted-proxies): anyone who reaches the "+
			"core directly can pick the address the limits see, so the core must only be reachable through the proxy", "header", s.Header)
	}
}

// ParseTrustedProxies reads a comma-separated list of CIDRs or single addresses.
func ParseTrustedProxies(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(list, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		if p, err := netip.ParsePrefix(f); err == nil {
			out = append(out, p.Masked())
		} else if a, err := netip.ParseAddr(f); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		} else {
			return nil, fmt.Errorf("trusted proxy %q is neither an address nor a CIDR", f)
		}
	}
	return out, nil
}

// Buckets limits requests per key (an address) as a token bucket: `burst` at once, then one every `every`. In memory:
// a restart forgets, which costs an attacker nothing they could not do anyway.
type Buckets struct {
	burst float64
	every time.Duration
	mu    sync.Mutex
	b     map[string]bucket
	swept time.Time
}

// maxBuckets is the most keys a limiter keeps: a flood from many addresses must not grow the map without bound.
const maxBuckets = 10_000

type bucket struct {
	tokens float64
	at     time.Time
}

// NewBuckets makes a limiter.
func NewBuckets(burst int, every time.Duration) *Buckets {
	return &Buckets{burst: float64(burst), every: every, b: map[string]bucket{}}
}

// Blocked is what Take would answer, without taking a token.
func (b *Buckets) Blocked(key string, now time.Time) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	cur, ok := b.b[key]
	if !ok {
		return 0
	}
	tokens := min(b.burst, cur.tokens+float64(now.Sub(cur.at))/float64(b.every))
	if tokens < 1 {
		return int((1-tokens)*float64(b.every)/float64(time.Second) + 0.999999)
	}
	return 0
}

// Take is 0 when the request may go, otherwise how many seconds until it may.
func (b *Buckets) Take(key string, now time.Time) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.b) >= maxBuckets && now.Sub(b.swept) >= b.every {
		b.sweep(now)
		b.swept = now
	}
	cur, ok := b.b[key]
	if !ok {
		if len(b.b) >= maxBuckets {
			// still full after the sweep, so every key is recent: drop some to hold the cap, which only gives those
			// addresses a fresh burst
			for k := range b.b {
				delete(b.b, k)
				if len(b.b) < maxBuckets*9/10 {
					break
				}
			}
		}
		cur = bucket{b.burst, now}
	}
	tokens := min(b.burst, cur.tokens+float64(now.Sub(cur.at))/float64(b.every))
	if tokens < 1 {
		b.b[key] = bucket{tokens, now}
		return int((1-tokens)*float64(b.every)/float64(time.Second) + 0.999999)
	}
	b.b[key] = bucket{tokens - 1, now}
	return 0
}

// sweep drops the keys that have filled up again: they are the same as never seen.
func (b *Buckets) sweep(now time.Time) {
	for k, v := range b.b {
		if v.tokens+float64(now.Sub(v.at))/float64(b.every) >= b.burst {
			delete(b.b, k)
		}
	}
}

// fromOwnPages is whether a browser's request comes from this server's own pages. The session cookie is SameSite=Lax,
// which a sibling subdomain still satisfies (it is the same site, not the same origin), so every state-changing
// request that acts with the cookie also checks where it was sent from: Sec-Fetch-Site when the browser says it, the
// Origin otherwise. A request with neither is not from a browser that would carry the cookie on its own.
func fromOwnPages(h http.Header, origin string) bool {
	if site := h.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}
	o := h.Get("Origin")
	return o == "" || o == origin
}
