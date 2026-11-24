package instance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"kedalio/core/db"
	"kedalio/core/memory"
	"kedalio/core/memory/search"
	"kedalio/core/offsite"
	"kedalio/core/offsite/fakes3"
	"kedalio/core/vault"
)

const marker = "okapi-marker-text"

func testOptions(dir string) Options {
	return Options{Data: dir, Token: "t", Language: "English", BackupEvery: time.Hour, BackupKeep: 3}
}

// started is a fresh instance with the marker in its database, stopped again; it gives the recovery code.
func started(t *testing.T, o Options) (code string) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	inst, err := o.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.DB.Exec("create table marks (body text)"); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.DB.Exec("insert into marks values (?)", marker); err != nil {
		t.Fatal(err)
	}
	stop()
	if err := inst.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := vault.HostKey(o.Data, false)
	if err != nil || raw == nil {
		t.Fatalf("no key file after the first start: %v", err)
	}
	return vault.RecoveryCode(raw)
}

func readMarker(t *testing.T, inst *Instance) {
	t.Helper()
	var got string
	if err := inst.DB.QueryRow("select body from marks").Scan(&got); err != nil || got != marker {
		t.Fatalf("marker %q %v", got, err)
	}
}

func TestHostKeyInstanceIsEncryptedAndReopensWithItsKeyFile(t *testing.T) {
	o := testOptions(t.TempDir())
	started(t, o)
	b, err := os.ReadFile(filepath.Join(o.Data, "core.db"))
	if err != nil || bytes.Contains(b, []byte(marker)) || bytes.Contains(b, []byte("SQLite format")) {
		t.Fatalf("core.db is readable: %v", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	inst, err := o.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	readMarker(t, inst)
	stop()
	inst.Close()

	// the key file of another instance does not open this database, and is not silently replaced
	other := testOptions(t.TempDir())
	started(t, other)
	k, _ := os.ReadFile(filepath.Join(other.Data, "kek"))
	if err := os.WriteFile(filepath.Join(o.Data, "kek"), k, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Start(context.Background()); err == nil || errors.Is(err, ErrLocked) {
		t.Fatalf("a foreign key file: %v", err)
	}
}

func TestLostKeyFileLeavesTheInstanceLockedUntilTheCode(t *testing.T) {
	o := testOptions(t.TempDir())
	code := started(t, o)
	if err := os.Remove(filepath.Join(o.Data, "kek")); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Start(context.Background()); !errors.Is(err, ErrLocked) {
		t.Fatalf("no key file: %v (a new instance must not be made over the old database)", err)
	}
	for _, bad := range []string{"nonsense", strings.Repeat("A", 52)} {
		if _, err := o.Unlock(context.Background(), bad); !errors.Is(err, ErrWrongCode) {
			t.Fatalf("code %q: %v", bad, err)
		}
	}
	ctx, stop := context.WithCancel(context.Background())
	inst, err := o.Unlock(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	readMarker(t, inst)
	stop()
	inst.Close()
	if _, err := os.Stat(filepath.Join(o.Data, "kek")); err != nil {
		t.Fatal("the key file was not put back, so the next start would need the code again")
	}
}

// The restore: only a copy from the backup folder and the recovery code are left.
func TestRestoreFromABackupCopyAndTheRecoveryCode(t *testing.T) {
	o := testOptions(t.TempDir())
	o.BackupDir = t.TempDir()
	ctx, stop := context.WithCancel(context.Background())
	inst, err := o.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inst.DB.Exec("create table marks (body text)")
	inst.DB.Exec("insert into marks values (?)", marker)
	raw, _ := vault.HostKey(o.Data, false)
	code := vault.RecoveryCode(raw)
	// the core made its first copy at start; wait for it
	var copies []string
	for range 100 {
		if copies, _ = db.Copies(o.BackupDir); len(copies) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(copies) != 1 {
		t.Fatalf("copies at start: %v", copies)
	}
	stop()
	inst.Close()

	// the copy made at start is older than the marker: take one more by hand
	dbKey, _ := vault.DatabaseKey(raw)
	d, err := db.OpenEncrypted(filepath.Join(o.Data, "core.db"), dbKey)
	if err != nil {
		t.Fatal(err)
	}
	path, err := db.Backup(context.Background(), d, o.BackupDir, dbKey, time.Now().Add(time.Hour))
	d.Close()
	if err != nil {
		t.Fatal(err)
	}

	fresh := testOptions(t.TempDir())
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(filepath.Join(fresh.Data, "core.db"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Start(context.Background()); !errors.Is(err, ErrLocked) {
		t.Fatalf("a restored copy without its key: %v", err)
	}
	ctx2, stop2 := context.WithCancel(context.Background())
	defer stop2()
	restored, err := fresh.Unlock(ctx2, code)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	defer stop2()
	readMarker(t, restored)
}

// constEmbedder gives every chunk the same vector: the pages are all near each other, which is not the point here.
type constEmbedder struct{}

func (constEmbedder) Model() string { return "const" }

func (constEmbedder) Embed(_ context.Context, input []string) ([][]float32, error) {
	out := make([][]float32, len(input))
	for i := range input {
		out[i] = []float32{1, 0, 0, 0}
	}
	return out, nil
}

// 150 pages need two refresh rounds (100, then 50). A round that refreshed pages goes straight on, so every page
// gets its vector while the idle interval (an hour here) is never waited for.
func TestBackgroundEmbeddingGoesOnWhileThereIsWork(t *testing.T) {
	old := embedIdle
	embedIdle = time.Hour
	t.Cleanup(func() { embedIdle = old })

	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqldb.Close() })
	bg := context.Background()
	if err := memory.Migrate(bg, sqldb); err != nil {
		t.Fatal(err)
	}
	ix, err := search.New(bg, sqldb)
	if err != nil {
		t.Fatal(err)
	}
	store := memory.New(sqldb, ix)
	e := constEmbedder{}
	for i := 0; i < 150; i++ {
		if _, err := store.Write(bg, fmt.Sprintf("note/%03d.md", i), "# Page\n\nsome words here", "t"); err != nil {
			t.Fatal(err)
		}
	}
	// the chunks are embedded first, so the loop starts with the refresh rounds
	for {
		n, err := ix.EmbedPending(bg, e, 50)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}

	ctx, stop := context.WithCancel(bg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		EmbedInBackground(ctx, ix, store, e)
	}()
	defer func() {
		stop()
		<-done
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := sqldb.QueryRow("select count(*) from page_vectors").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 150 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of 150 pages refreshed: the loop waited for the idle interval", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The whole way: a copy leaves for the store once the keys are in the vault (no restart), /health tells the failure before
// and the good time after, and with only the store, the key's recovery code and an empty folder the data comes back.
func TestOffsiteBackupGoesToTheStoreAndRestoresWithTheRecoveryCode(t *testing.T) {
	old := retryAfter
	retryAfter = 50 * time.Millisecond
	t.Cleanup(func() { retryAfter = old })

	o := testOptions(t.TempDir())
	code := started(t, o)
	store := fakes3.New(t)
	o.BackupDir = t.TempDir()
	o.Offsite = OffsiteOptions{Endpoint: store.URL, Bucket: fakes3.Bucket, Region: fakes3.Region, Prefix: "kedalio-core", Keep: 2,
		AccessKey: fakes3.Access, SecretKey: "a-wrong-secret-value"}
	health := func(inst *Instance) map[string]string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/health", nil)
		req.Header.Set("Authorization", "Bearer "+o.Token)
		inst.Handler.ServeHTTP(rec, req)
		var got map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err, rec.Body.String())
		}
		return got
	}
	badCtx, stopBad := context.WithCancel(context.Background())
	bad, err := o.Start(badCtx)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return health(bad)["offsite_backup"] == "failing" })
	if h := health(bad); h["status"] != "ok" || h["offsite_last_ok"] != "" || h["offsite_failing_since"] == "" {
		t.Fatalf("health with a wrong secret: %v", h)
	}
	anon := httptest.NewRecorder()
	bad.Handler.ServeHTTP(anon, httptest.NewRequest("GET", "/health", nil))
	if got := strings.TrimSpace(anon.Body.String()); got != `{"status":"ok"}` {
		t.Fatalf("health without the token tells the backup state: %s", got)
	}
	stopBad()
	bad.Close()

	o.Offsite.SecretKey = fakes3.Secret
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	inst, err := o.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	defer stop()
	waitFor(t, func() bool { return health(inst)["offsite_backup"] == "ok" })
	if h := health(inst); h["offsite_last_ok"] == "" || h["offsite_failing_since"] != "" {
		t.Fatalf("health with the keys: %v", h)
	}
	keys := store.Keys()
	if len(keys) != 2 || !strings.HasPrefix(keys[0], "kedalio-core/core-") || !strings.HasSuffix(keys[1], ".db.tag") {
		t.Fatalf("the store holds %v", keys)
	}

	// the server is lost: a fresh folder, the store's keys (from the owner) and the recovery code
	stop()
	inst.Close()
	fresh := testOptions(t.TempDir())
	c := &offsite.Client{
		Endpoint: store.URL, Region: fakes3.Region, Bucket: fakes3.Bucket, Prefix: "kedalio-core/",
		Credentials: func(context.Context) (string, string, error) { return fakes3.Access, fakes3.Secret, nil },
	}
	kek, _ := vault.KeyFromCode(code)
	dbKey, err := vault.DatabaseKey(kek)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := offsite.Restore(context.Background(), c, "", fresh.Data, offsite.RestoreOptions{Key: dbKey}); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Start(context.Background()); !errors.Is(err, ErrLocked) {
		t.Fatalf("a restored copy without its key: %v", err)
	}
	ctx2, stop2 := context.WithCancel(context.Background())
	defer stop2()
	restored, err := fresh.Unlock(ctx2, code)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	defer stop2()
	readMarker(t, restored)
}

func TestFailedStartLeavesNoBackgroundWorkRunning(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	o := testOptions(t.TempDir())
	o.EmbedURL, o.EmbedModel = "http://127.0.0.1:1", "m"
	o.PublicURL = "not an origin"
	if _, err := o.Start(ctx); err == nil {
		t.Fatal("an invalid public URL started")
	}
	buf := make([]byte, 1<<20)
	if stacks := string(buf[:runtime.Stack(buf, true)]); strings.Contains(stacks, "EmbedInBackground") {
		t.Errorf("the embedding loop is still running on a database the failed start closed:\n%s", stacks)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for range 200 {
		if ok() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the condition did not come true")
}

func TestStartSeedsTheDefaultViewsOnceADeletedOneStaysDeleted(t *testing.T) {
	o := testOptions(t.TempDir())
	started(t, o)
	names := func(inst *Instance) string {
		rows, err := inst.DB.Query("select path from docs where path like 'views/%' and deleted = 0 order by path")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				t.Fatal(err)
			}
			out = append(out, p)
		}
		return strings.Join(out, ",")
	}
	open := func() (*Instance, func()) {
		ctx, stop := context.WithCancel(context.Background())
		inst, err := o.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return inst, func() {
			stop()
			if err := inst.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	const all = "views/board.md,views/days.md,views/others.md,views/stalled.md,views/today.md"
	inst, done := open()
	if got := names(inst); got != all {
		t.Fatalf("fresh start: %s", got)
	}
	if _, err := inst.DB.Exec("update docs set deleted = 1 where path = 'views/board.md'"); err != nil {
		t.Fatal(err)
	}
	done()
	inst, done = open()
	defer done()
	if got := names(inst); got != strings.Replace(all, "views/board.md,", "", 1) {
		t.Fatalf("after restart: %s", got)
	}
	var n int
	if err := inst.DB.QueryRow("select count(*) from docs where path like 'views/%'").Scan(&n); err != nil || n != 5 {
		t.Fatalf("rows %d %v", n, err)
	}
}
