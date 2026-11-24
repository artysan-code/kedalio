// Package fakes3 is an in-memory path-style S3 for tests: it verifies SigV4 with code of its own, so a client whose
// signature is wrong is refused as the real store would.
package fakes3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// What a client must use to be accepted.
const (
	Access = "test-access-id"
	Secret = "test-secret-never-in-an-error"
	Region = "auto"
	Bucket = "kedalio-backups"
)

// Store is a path-style S3 that verifies SigV4 itself: it rebuilds the signature from the request it received, with
// its own code, and answers 403 SignatureDoesNotMatch when it differs. Its listing gives two keys a page, so that the
// client has to follow continuation tokens.
type Store struct {
	mu      sync.Mutex
	objects map[string][]byte // key inside the bucket
	srv     *httptest.Server
	// URL is its address.
	URL string
}

// New starts a store that stops with the test.
func New(t testing.TB) *Store {
	f := &Store{objects: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	f.URL = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

func fail(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code></Error>", code)
}

func (f *Store) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if !f.verify(r, body) {
		fail(w, http.StatusForbidden, "SignatureDoesNotMatch")
		return
	}
	bucketPath, ok := strings.CutPrefix(r.URL.Path, "/"+Bucket)
	if !ok {
		fail(w, http.StatusNotFound, "NoSuchBucket")
		return
	}
	key := strings.TrimPrefix(bucketPath, "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPut:
		f.objects[key] = body
	case r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && key != "":
		b, ok := f.objects[key]
		if !ok {
			fail(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Write(b)
	case r.Method == http.MethodGet:
		prefix := r.URL.Query().Get("prefix")
		var keys []string
		for k := range f.objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		start := 0
		if tok := r.URL.Query().Get("continuation-token"); tok != "" {
			start = sort.SearchStrings(keys, tok)
		}
		type item struct {
			Key  string
			Size int
		}
		var page struct {
			XMLName               xml.Name `xml:"ListBucketResult"`
			Contents              []item
			IsTruncated           bool
			NextContinuationToken string
		}
		for i := start; i < len(keys) && i < start+2; i++ {
			page.Contents = append(page.Contents, item{keys[i], len(f.objects[keys[i]])})
		}
		if start+2 < len(keys) {
			page.IsTruncated, page.NextContinuationToken = true, keys[start+2]
		}
		xml.NewEncoder(w).Encode(page)
	default:
		fail(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

// verify recomputes the signature the way the S3 documentation lays it out, from what arrived.
func (f *Store) verify(r *http.Request, body []byte) bool {
	rest, ok := strings.CutPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ")
	if !ok {
		return false
	}
	fields := map[string]string{}
	for _, p := range strings.Split(rest, ", ") {
		k, v, _ := strings.Cut(p, "=")
		fields[k] = v
	}
	scope, ok := strings.CutPrefix(fields["Credential"], Access+"/")
	if !ok {
		return false
	}
	sum := sha256.Sum256(body)
	if r.Header.Get("x-amz-content-sha256") != hex.EncodeToString(sum[:]) {
		return false
	}
	var canon strings.Builder
	for _, h := range strings.Split(fields["SignedHeaders"], ";") {
		v := r.Header.Get(h)
		if h == "host" {
			v = r.Host
		}
		canon.WriteString(h + ":" + strings.TrimSpace(v) + "\n")
	}
	creq := strings.Join([]string{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, canon.String(), fields["SignedHeaders"], r.Header.Get("x-amz-content-sha256")}, "\n")
	h := sha256.Sum256([]byte(creq))
	toSign := "AWS4-HMAC-SHA256\n" + r.Header.Get("x-amz-date") + "\n" + scope + "\n" + hex.EncodeToString(h[:])
	parts := strings.Split(scope, "/")
	k := []byte("AWS4" + Secret)
	for _, p := range parts {
		m := hmac.New(sha256.New, k)
		m.Write([]byte(p))
		k = m.Sum(nil)
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte(toSign))
	return hex.EncodeToString(m.Sum(nil)) == fields["Signature"] && parts[1] == Region && parts[2] == "s3"
}

// Keys are the stored keys inside the bucket.
func (f *Store) Keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.objects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Put stores an object as it is.
func (f *Store) Put(key string, b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = b
}
