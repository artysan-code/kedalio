// Package offsite sends the core's encrypted database copies to an S3-compatible store (Cloudflare R2, MinIO, AWS) and
// brings one back. The copies are uploaded as they lie on disk, already encrypted: nothing here decrypts or reads a
// file into memory. The client is the standard library's, with SigV4 signing written out: a store needs four calls
// (PUT, GET, LIST, DELETE), which is less code than a dependency.
package offsite

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Credentials gives the access key and its secret when a call needs them: the core reads them from the vault each time,
// so adding them needs no restart and a rotated key is used at once.
type Credentials func(ctx context.Context) (access, secret string, err error)

// Client talks to one bucket, path-style (https://host/bucket/key): the form R2 and every S3 clone accept, and the only
// one that needs no DNS name per bucket.
type Client struct {
	Endpoint    string // https://<account>.r2.cloudflarestorage.com
	Region      string // "auto" for R2
	Bucket      string
	Prefix      string // a folder in the bucket, "" or ending in "/"; calls never see outside it
	Credentials Credentials
	HTTP        *http.Client
	now         func() time.Time
}

// Object is a file in the store, named relative to the client's prefix.
type Object struct {
	Name string
	Size int64
}

// Put uploads the file at path as name. The file is read twice from disk, once for its hash (SigV4 signs the payload)
// and once as the request body, so memory holds a buffer and not the copy.
func (c *Client) Put(ctx context.Context, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	size, hash, err := hashOf(f)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	req, err := c.request(ctx, http.MethodPut, name, nil, f)
	if err != nil {
		return err
	}
	req.ContentLength = size
	resp, err := c.do(req, hash)
	if err != nil {
		return fmt.Errorf("upload %s: %w", name, err)
	}
	return resp.Body.Close()
}

func hashOf(f *os.File) (int64, string, error) {
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}

// Get streams the object into w and says how many bytes came.
func (c *Client) Get(ctx context.Context, name string, w io.Writer) (int64, error) {
	return c.GetUpTo(ctx, name, w, math.MaxInt64-1)
}

// GetUpTo is Get that gives up when the body is longer than max bytes: the store has listed a size, and a hostile or
// broken one must not be able to fill the disk by sending more.
func (c *Client) GetUpTo(ctx context.Context, name string, w io.Writer, max int64) (int64, error) {
	req, err := c.request(ctx, http.MethodGet, name, nil, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.do(req, emptyHash)
	if err != nil {
		return 0, fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()
	n, err := io.Copy(w, io.LimitReader(resp.Body, max))
	if err != nil {
		return n, err
	}
	// one more byte means the body is longer than listed; it is read, not written
	if extra, _ := io.CopyN(io.Discard, resp.Body, 1); extra > 0 {
		return n, fmt.Errorf("download %s: more than the %d bytes expected", name, max)
	}
	return n, nil
}

// Delete removes the object; one that is not there is not an error (S3 answers 204 either way).
func (c *Client) Delete(ctx context.Context, name string) error {
	req, err := c.request(ctx, http.MethodDelete, name, nil, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req, emptyHash)
	if err != nil {
		return fmt.Errorf("delete %s: %w", name, err)
	}
	return resp.Body.Close()
}

// List is the objects directly under the prefix, in name order.
func (c *Client) List(ctx context.Context) ([]Object, error) {
	var out []Object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {c.Prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		req, err := c.request(ctx, http.MethodGet, "", q, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.do(req, emptyHash)
		if err != nil {
			return nil, fmt.Errorf("list: %w", err)
		}
		var page struct {
			Contents []struct {
				Key  string
				Size int64
			}
			IsTruncated           bool
			NextContinuationToken string
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("list: the answer is not a listing: %w", err)
		}
		for _, o := range page.Contents {
			if name := strings.TrimPrefix(o.Key, c.Prefix); name != "" && !strings.Contains(name, "/") {
				out = append(out, Object{Name: name, Size: o.Size})
			}
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
			return out, nil
		}
		token = page.NextContinuationToken
	}
}

func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(h)
	return err == nil && ip.IsLoopback()
}

const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // sha256 of nothing

func (c *Client) request(ctx context.Context, method, name string, query url.Values, body io.Reader) (*http.Request, error) {
	base, err := url.Parse(strings.TrimRight(c.Endpoint, "/"))
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return nil, errors.New("the endpoint is not an http(s) address")
	}
	// the requests carry signed headers and the copies' bytes: plain http only to this machine (a local MinIO)
	if base.Scheme == "http" && !loopbackHost(base.Hostname()) {
		return nil, errors.New("the endpoint must be https (http only on a loopback address)")
	}
	path := "/" + escape(c.Bucket)
	if name != "" {
		path += "/" + escapePath(c.Prefix+name)
	}
	u, err := url.Parse(base.Scheme + "://" + base.Host + path)
	if err != nil {
		return nil, err
	}
	u.RawQuery = canonicalQuery(query)
	return http.NewRequestWithContext(ctx, method, u.String(), body)
}

// do signs and sends; a status outside 2xx is an error that names the store's code and never the request.
func (c *Client) do(req *http.Request, payloadHash string) (*http.Response, error) {
	access, secret, err := c.Credentials(req.Context())
	if err != nil {
		return nil, err
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	sign(req, payloadHash, access, secret, c.Region, now())
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		// a url.Error repeats the address, which is not a secret; the headers are never in it
		return nil, err
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer resp.Body.Close()
	var e struct{ Code string }
	_ = xml.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&e)
	if e.Code == "" {
		return nil, fmt.Errorf("the store answered %s", resp.Status)
	}
	return nil, fmt.Errorf("the store answered %s (%s)", resp.Status, e.Code)
}

// sign adds the SigV4 headers: every header already on the request, and the host, are signed.
func sign(req *http.Request, payloadHash, access, secret, region string, now time.Time) {
	now = now.UTC()
	date := now.Format("20060102")
	stamp := now.Format("20060102T150405Z")
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("x-amz-date", stamp)

	hdr := map[string]string{"host": req.URL.Host}
	for k, v := range req.Header {
		hdr[strings.ToLower(k)] = strings.Join(v, ",")
	}
	names := make([]string, 0, len(hdr))
	for k := range hdr {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + strings.TrimSpace(hdr[k]) + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{
		req.Method, req.URL.EscapedPath(), req.URL.RawQuery, canonHeaders.String(), signed, payloadHash,
	}, "\n")

	scope := date + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hexHash(canonical)
	key := mac([]byte("AWS4"+secret), date)
	for _, part := range []string{region, "s3", "aws4_request"} {
		key = mac(key, part)
	}
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+access+"/"+scope+
		", SignedHeaders="+signed+", Signature="+hex.EncodeToString(mac(key, toSign)))
}

func mac(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}

func hexHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// escape is RFC 3986's encoding, which SigV4 requires: only unreserved characters stay.
func escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch ch := s[i]; {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = escape(parts[i])
	}
	return strings.Join(parts, "/")
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, escape(k)+"="+escape(v))
		}
	}
	return strings.Join(parts, "&")
}
