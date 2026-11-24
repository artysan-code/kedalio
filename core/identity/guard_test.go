package identity

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientIPComesFromTheNamedHeaderItsLastEntryOrTheConnection(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:5555"
	r.Header.Set("Cf-Connecting-Ip", "203.0.113.9")
	r.Header.Set("X-Forwarded-For", "1.1.1.1, 198.51.100.2")
	for header, want := range map[string]string{"cf-connecting-ip": "203.0.113.9", "x-forwarded-for": "198.51.100.2",
		"": "10.0.0.1", "x-missing": "10.0.0.1"} {
		if got := (IPSource{Header: header}).ClientIP(r); got != want {
			t.Errorf("header %q: %s, want %s", header, got, want)
		}
	}
}

func TestClientIPWithTrustedProxiesTakesTheRightMostAddressThatIsNotOne(t *testing.T) {
	trusted, err := ParseTrustedProxies("10.0.0.0/8, 192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, conn, xff, want string }{
		{"client behind two trusted hops", "10.0.0.1:1", "6.6.6.6, 203.0.113.9, 10.0.0.7", "203.0.113.9"},
		{"a forged entry on the left is ignored", "10.0.0.1:1", "1.2.3.4, 203.0.113.9", "203.0.113.9"},
		{"connection not from a proxy: the header is a client's", "198.51.100.5:1", "1.2.3.4", "198.51.100.5"},
		{"all trusted: the connection", "10.0.0.1:1", "10.0.0.2, 192.0.2.1", "10.0.0.1"},
		{"garbage: the connection", "10.0.0.1:1", "not-an-ip", "10.0.0.1"},
		{"mapped v6 is the v4", "10.0.0.1:1", "::ffff:203.0.113.9", "203.0.113.9"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.conn
		r.Header.Set("X-Forwarded-For", c.xff)
		if got := (IPSource{Header: "x-forwarded-for", Trusted: trusted}).ClientIP(r); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if _, err := ParseTrustedProxies("10.0.0.0/8, nope"); err == nil {
		t.Error("a bad entry must be refused")
	}
}

func TestClientIPKeysAnIPv6ClientByItsSlash64(t *testing.T) {
	for conn, want := range map[string]string{"[2001:db8:1:2:aaaa::1]:5": "2001:db8:1:2::/64", "[2001:db8:1:2:bbbb::9]:5": "2001:db8:1:2::/64",
		"[2001:db8:1:3::1]:5": "2001:db8:1:3::/64", "198.51.100.5:5": "198.51.100.5"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = conn
		if got := (IPSource{}).ClientIP(r); got != want {
			t.Errorf("%s: %s, want %s", conn, got, want)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1"
	r.Header.Set("X-Real-Ip", "2001:db8:9:9:1:2:3:4")
	if got := (IPSource{Header: "x-real-ip"}).ClientIP(r); got != "2001:db8:9:9::/64" {
		t.Errorf("from a header: %s", got)
	}
}

func TestBucketsNeverHoldMoreThanTheCap(t *testing.T) {
	b := NewBuckets(1, time.Hour)
	now := time.Unix(1000, 0)
	for i := 0; i < maxBuckets*3; i++ {
		b.Take(fmt.Sprintf("ip%d", i), now)
		if len(b.b) > maxBuckets {
			t.Fatalf("%d keys after %d requests, cap %d", len(b.b), i+1, maxBuckets)
		}
	}
}

func TestBucketLetsABurstThroughThenOneRequestPerInterval(t *testing.T) {
	b := NewBuckets(3, time.Second)
	at := time.Unix(1000, 0)
	for i := 0; i < 3; i++ {
		if w := b.Take("a", at); w != 0 {
			t.Fatalf("burst %d waits %d", i, w)
		}
	}
	if w := b.Take("a", at); w != 1 {
		t.Errorf("fourth waits %d, want 1", w)
	}
	if w := b.Take("b", at); w != 0 {
		t.Error("another address has its own")
	}
	if w := b.Take("a", at.Add(999*time.Millisecond)); w != 1 {
		t.Errorf("not yet refilled: %d", w)
	}
	if w := b.Take("a", at.Add(1999*time.Millisecond)); w != 0 {
		t.Errorf("refilled after the interval: %d", w)
	}
	if w := b.Take("a", at.Add(1999*time.Millisecond)); w != 1 {
		t.Errorf("and only one: %d", w)
	}
}

func TestAFormActsOnlyWhenSentFromTheServicesOwnPages(t *testing.T) {
	const origin = "https://core.example.com"
	for _, c := range []struct {
		h    map[string]string
		want bool
	}{
		{map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		{map[string]string{"Sec-Fetch-Site": "none"}, true},
		{map[string]string{"Sec-Fetch-Site": "same-site"}, false},
		{map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{map[string]string{"Origin": origin}, true},
		{map[string]string{"Origin": "https://evil.example"}, false},
		{map[string]string{}, true},
		{map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": origin}, false},
	} {
		h := http.Header{}
		for k, v := range c.h {
			h.Set(k, v)
		}
		if got := fromOwnPages(h, origin); got != c.want {
			t.Errorf("%v: %v, want %v", c.h, got, c.want)
		}
	}
}

func TestWarnIPSourceNamesTheTwoUnsafeSettings(t *testing.T) {
	trusted, _ := ParseTrustedProxies("10.0.0.0/8")
	cases := map[string]struct {
		url  string
		src  IPSource
		want string // empty: no warning at all
	}{
		"public without header":  {"https://core.example.com", IPSource{}, "no -client-ip-header"},
		"header without trusted": {"https://core.example.com", IPSource{Header: "x-forwarded-for"}, "trusted from any connection"},
		"header and trusted":     {"https://core.example.com", IPSource{Header: "x-forwarded-for", Trusted: trusted}, ""},
		"local, nothing set":     {"", IPSource{}, ""},
	}
	for name, tc := range cases {
		var buf bytes.Buffer
		WarnIPSource(slog.New(slog.NewTextHandler(&buf, nil)), tc.url, tc.src)
		if tc.want == "" && buf.Len() > 0 {
			t.Errorf("%s: unexpected warning: %s", name, buf.String())
		}
		if tc.want != "" && !strings.Contains(buf.String(), tc.want) {
			t.Errorf("%s: no warning with %q in %q", name, tc.want, buf.String())
		}
	}
}
