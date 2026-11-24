package offsite

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"kedalio/core/offsite/fakes3"
)

func clientFor(f *fakes3.Store) *Client {
	return &Client{
		Endpoint: f.URL, Region: fakes3.Region, Bucket: fakes3.Bucket, Prefix: DefaultPrefix,
		Credentials: func(context.Context) (string, string, error) { return fakes3.Access, fakes3.Secret, nil },
	}
}

// The example of AWS's SigV4 documentation (a GET with a Range header): the signature must be the published one.
func TestSigV4MatchesTheDocumentedExample(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	now := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	// the documentation's example key, split so that a secret scanner does not take it for a real one
	access := "AKIA" + "IOSFODNN7EXAMPLE"
	docSecret := strings.Join([]string{"wJalrXUtnFEMI", "K7MDENG", "bPxRfiCYEXAMPLEKEY"}, "/")
	sign(req, emptyHash, access, docSecret, "us-east-1", now)
	want := "AWS4-HMAC-SHA256 Credential=" + access + "/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, " +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("got %s", got)
	}
}

func TestPutGetListDeleteAgainstAFakeThatChecksTheSignature(t *testing.T) {
	f := fakes3.New(t)
	c := clientFor(f)
	ctx := context.Background()
	// a name with characters SigV4 must encode, sent through a path-style address
	c.Prefix = "my backups/core 1/"
	path := filepath.Join(t.TempDir(), "x")
	body := bytes.Repeat([]byte("encrypted pages "), 100_000)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(ctx, "core-a+b.db", path); err != nil {
		t.Fatal(err)
	}
	if got := f.Keys(); !slices.Equal(got, []string{"my backups/core 1/core-a+b.db"}) {
		t.Fatalf("keys %v", got)
	}
	objs, err := c.List(ctx)
	if err != nil || len(objs) != 1 || objs[0].Name != "core-a+b.db" || objs[0].Size != int64(len(body)) {
		t.Fatalf("list %v %v", objs, err)
	}
	var got bytes.Buffer
	if n, err := c.Get(ctx, "core-a+b.db", &got); err != nil || n != int64(len(body)) || !bytes.Equal(got.Bytes(), body) {
		t.Fatalf("get %d %v", n, err)
	}
	if err := c.Delete(ctx, "core-a+b.db"); err != nil || len(f.Keys()) != 0 {
		t.Fatalf("delete %v %v", err, f.Keys())
	}
}

func TestAWrongSecretIsRefusedAndTheErrorNamesNeitherKey(t *testing.T) {
	c := clientFor(fakes3.New(t))
	c.Credentials = func(context.Context) (string, string, error) { return fakes3.Access, "a-wrong-secret-value", nil }
	_, err := c.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("%v", err)
	}
	for _, s := range []string{fakes3.Access, fakes3.Secret, "a-wrong-secret-value"} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("the error says %q: %v", s, err)
		}
	}
}

// R2 is addressed as https://<account>.r2.cloudflarestorage.com/<bucket>/<key>: the bucket is in the path, never in the host.
func TestTheBucketIsInThePathAndNotInTheHost(t *testing.T) {
	var gotPath, gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotHost = r.URL.Path, r.Host
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := clientFor(fakes3.New(t))
	c.Endpoint = srv.URL + "/"
	if err := c.Delete(context.Background(), "core-x.db"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/kedalio-backups/kedalio-core/core-x.db" || strings.Contains(gotHost, fakes3.Bucket) {
		t.Fatalf("%s on %s", gotPath, gotHost)
	}
}

func copyFile(t *testing.T, dir string, at time.Time, body string) {
	t.Helper()
	name := "core-" + at.UTC().Format("20060102T150405Z") + ".db"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Sync sends what the store lacks, keeps the newest Keep, removes the rest under the prefix, and leaves alone anything
// that is not a copy or sits outside the prefix.
func TestSyncUploadsWhatIsMissingAndPrunesOnlyCopiesUnderThePrefix(t *testing.T) {
	f := fakes3.New(t)
	u := &Uploader{Client: clientFor(f), Keep: 3}
	dir := t.TempDir()
	base := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	f.Put("kedalio-core/notes.txt", []byte("the owner's"))
	f.Put("other/core-20200101T000000Z.db", []byte("another folder's copy"))
	for i := range 5 {
		copyFile(t, dir, base.AddDate(0, 0, i), "copy "+string(rune('a'+i)))
	}
	n, err := u.Sync(context.Background(), dir)
	if err != nil || n != 3 {
		t.Fatalf("uploaded %d %v", n, err)
	}
	want := []string{"kedalio-core/core-20261103T030000Z.db", "kedalio-core/core-20261104T030000Z.db", "kedalio-core/core-20261105T030000Z.db",
		"kedalio-core/notes.txt", "other/core-20200101T000000Z.db"}
	if got := f.Keys(); !slices.Equal(got, want) {
		t.Fatalf("keys %v", got)
	}
	// one more copy: one upload, and the oldest in the store goes
	copyFile(t, dir, base.AddDate(0, 0, 5), "copy f")
	if n, err := u.Sync(context.Background(), dir); err != nil || n != 1 {
		t.Fatalf("uploaded %d %v", n, err)
	}
	if got := f.Keys(); slices.Contains(got, "kedalio-core/core-20261103T030000Z.db") || len(got) != 5 {
		t.Fatalf("keys %v", got)
	}
	if n, _ := u.Sync(context.Background(), dir); n != 0 {
		t.Fatalf("nothing is new but %d were sent", n)
	}
}

func TestStateGivesTheNewestCopyOnTheStoreAndNoneWhenThereIsNothingToSend(t *testing.T) {
	u := &Uploader{Client: clientFor(fakes3.New(t)), Keep: 5}
	dir := t.TempDir()
	if _, err := u.Sync(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if st := u.State(); st.LastOK.IsZero() || !st.NewestCopy.IsZero() {
		t.Fatalf("an empty store: %+v", st)
	}
	for _, name := range []string{"core-20261105T124647Z.db", "core-20261106T124647Z.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("copy"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := u.Sync(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if got, want := u.State().NewestCopy, time.Date(2026, 11, 6, 12, 46, 47, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("newest %v, want %v", got, want)
	}
}

func TestAFailingStoreKeepsEveryCopyAndShowsSinceWhenAndTheLastGoodTime(t *testing.T) {
	f := fakes3.New(t)
	c := clientFor(f)
	u := &Uploader{Client: c, Keep: 2}
	dir := t.TempDir()
	base := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	copyFile(t, dir, base, "one")
	if _, err := u.Sync(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	good := u.State()
	if good.LastOK.IsZero() || !good.FailingSince.IsZero() {
		t.Fatalf("%+v", good)
	}
	// the secret is rotated in the store and not in the vault
	c.Credentials = func(context.Context) (string, string, error) { return fakes3.Access, "stale", nil }
	copyFile(t, dir, base.AddDate(0, 0, 1), "two")
	copyFile(t, dir, base.AddDate(0, 0, 2), "three")
	if _, err := u.Sync(context.Background(), dir); err == nil {
		t.Fatal("a refused signature must be an error")
	}
	if st := u.State(); st.FailingSince.IsZero() || !st.LastOK.Equal(good.LastOK) {
		t.Fatalf("%+v", st)
	}
	if len(f.Keys()) != 1 {
		t.Fatalf("nothing is removed on a failure: %v", f.Keys())
	}
	// the key is back: it catches up on its own, in one run, and the failure ends
	c.Credentials = func(context.Context) (string, string, error) { return fakes3.Access, fakes3.Secret, nil }
	if n, err := u.Sync(context.Background(), dir); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if st := u.State(); !st.FailingSince.IsZero() || len(f.Keys()) != 2 {
		t.Fatalf("%+v %v", st, f.Keys())
	}
}

func TestRestoreFetchesACopyIntoAnEmptyFolderAndRefusesAUsedOne(t *testing.T) {
	f := fakes3.New(t)
	f.Put("kedalio-core/core-20261101T030000Z.db", []byte("old"))
	f.Put("kedalio-core/core-20261102T030000Z.db", []byte("new"))
	f.Put("kedalio-core/core-20261103T030000Z.db", []byte("newest"))
	c := clientFor(f)
	ctx := context.Background()
	to := filepath.Join(t.TempDir(), "core")
	if name, err := Restore(ctx, c, "core-20261102T030000Z.db", to, unsignedOK); err != nil || name != "core-20261102T030000Z.db" {
		t.Fatalf("%s %v", name, err)
	}
	if b, _ := os.ReadFile(filepath.Join(to, "core.db")); string(b) != "new" {
		t.Fatalf("%q", b)
	}
	if _, err := Restore(ctx, c, "", to, unsignedOK); err == nil {
		t.Fatal("a folder with a database is refused")
	}
	if name, err := Restore(ctx, c, "", t.TempDir(), unsignedOK); err != nil || name != "core-20261103T030000Z.db" {
		t.Fatalf("%s %v", name, err)
	}
	if _, err := Restore(ctx, c, "core-19990101T000000Z.db", t.TempDir(), unsignedOK); err == nil {
		t.Fatal("a copy the store lacks")
	}
}

func TestAnEndpointOverPlainHTTPIsRefusedUnlessItIsLoopback(t *testing.T) {
	for endpoint, ok := range map[string]bool{
		"http://s3.example.com":  false,
		"http://10.0.0.5:9000":   false,
		"https://s3.example.com": true,
		"http://127.0.0.1:9000":  true,
		"http://localhost:9000":  true,
		"http://[::1]:9000":      true,
		"ftp://s3.example.com":   false,
	} {
		c := clientFor(fakes3.New(t))
		c.Endpoint = endpoint
		_, err := c.request(context.Background(), http.MethodGet, "", nil, nil)
		if (err == nil) != ok {
			t.Errorf("%s: err %v, want accepted %v", endpoint, err, ok)
		}
	}
}

func TestGetUpToStopsAtTheListedSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("x"), 1000))
	}))
	defer srv.Close()
	c := clientFor(fakes3.New(t))
	c.Endpoint = srv.URL
	var got bytes.Buffer
	if _, err := c.GetUpTo(context.Background(), "core-x.db", &got, 100); err == nil {
		t.Fatal("a store that sends more than it listed was accepted")
	}
	if got.Len() > 100 {
		t.Errorf("%d bytes written past the cap of 100", got.Len())
	}
	got.Reset()
	if n, err := c.GetUpTo(context.Background(), "core-x.db", &got, 1000); err != nil || n != 1000 {
		t.Errorf("an exact-size object: %d, %v", n, err)
	}
}
