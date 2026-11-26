package instance

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kedalio/core/db"
	"kedalio/core/identity"
	"kedalio/core/mcp"
	"kedalio/core/memory"
	"kedalio/core/memory/search"
	"kedalio/core/offsite"
	"kedalio/core/projects"
	"kedalio/core/tasks"
	"kedalio/core/upkeep"
	"kedalio/core/vault"
)

// OffsiteOptions is where the copies go beyond the server's disk.
type OffsiteOptions struct {
	Endpoint, Bucket, Region, Prefix string
	// AccessKey and SecretKey are the store's credentials: kedalio-core reads them from the environment
	// (BACKUP_S3_ACCESS_KEY, BACKUP_S3_SECRET_KEY), never from a flag.
	AccessKey, SecretKey string
	Keep                 int
}

// Options are the instance's settings: kedalio-core fills them from flags and the environment.
type Options struct {
	Data, Token string
	// UpkeepToken is the second bearer token, the upkeep agent's: kedalio-core reads it from KEDALIO_UPKEEP_TOKEN, never
	// from a flag. Empty: there is none.
	UpkeepToken               string
	PublicURL, ClientIPHeader string
	// TrustedProxies: see identity.IPSource.
	TrustedProxies []netip.Prefix
	// Label is a word the deployment gives this core ("trial"), told to MCP clients with its address.
	Label                 string
	Owner, Language, Zone string
	EmbedURL, EmbedModel  string
	BackupDir             string
	BackupEvery           time.Duration
	BackupKeep            int
	// Stdio: the core is served over stdin and stdout to the one client that started it (see Instance.Serve), as the
	// owner: no HTTP, no token, no passkey.
	Stdio bool
	// Offsite sends each copy to an S3-compatible store (Endpoint empty: none).
	Offsite OffsiteOptions
}

var (
	// ErrLocked: the instance exists but its key is not here (a volume restored without its key file).
	ErrLocked = errors.New("the Instance is locked: it waits for the recovery code")
	// ErrWrongCode: the text is not a recovery code, or not this instance's. One answer for both, so a refusal tells
	// nothing.
	ErrWrongCode = errors.New("that is not this instance's recovery code")
)

// Instance is a running core: its open database, and what stops with it.
type Instance struct {
	DB      *sql.DB
	Handler http.Handler
	srv     *mcp.Server
	// Language and Zone are what the database settled on (see settleLocale): a language name, an IANA zone name.
	Language, Zone string
	wg             sync.WaitGroup
	store          *memory.Store
}

// Serve answers the MCP client on t (Options.Stdio) until it hangs up (stdin EOF) or ctx ends.
func (i *Instance) Serve(ctx context.Context, t sdk.Transport) error {
	return i.srv.Run(ctx, t)
}

// Close waits for the background work (it ends with the context) and closes the database.
func (i *Instance) Close() error {
	i.wg.Wait()
	if i.store != nil {
		i.store.Close()
	}
	return i.DB.Close()
}

// Start opens the instance of the data folder. The database key comes from the instance key, which is a file in the
// folder (the default) or the owner's recovery code (a restored volume). The first start, with no
// database yet, makes the key; with a database but no key it answers ErrLocked and the core waits for unlock.
func (o Options) Start(ctx context.Context) (*Instance, error) {
	_, err := os.Stat(filepath.Join(o.Data, "core.db"))
	raw, err2 := vault.HostKey(o.Data, errors.Is(err, os.ErrNotExist))
	if err2 != nil {
		return nil, err2
	}
	if raw == nil {
		return nil, ErrLocked
	}
	return o.open(ctx, raw, "")
}

// Unlock opens the instance with the recovery code the owner typed. In host-key mode the key file is put back, so the
// next start needs nothing.
func (o Options) Unlock(ctx context.Context, code string) (*Instance, error) {
	raw, ok := vault.KeyFromCode(code)
	if !ok {
		return nil, ErrWrongCode
	}
	// without this, a code would open (and so create) a new, empty database where the old one is missing
	if _, err := os.Stat(filepath.Join(o.Data, "core.db")); err != nil {
		return nil, errors.New("there is no database in the data folder to unlock")
	}
	return o.open(ctx, raw, code)
}

// open opens the database with the key derived from raw, brings the tables up to date and builds the handler. code is
// the recovery code when the key came from it.
func (o Options) open(ctx context.Context, raw []byte, code string) (_ *Instance, err error) {
	dbKey, err := vault.DatabaseKey(raw)
	if err != nil {
		return nil, err
	}
	sqldb, err := db.OpenEncrypted(filepath.Join(o.Data, "core.db"), dbKey)
	if errors.Is(err, db.ErrBadKey) {
		if code != "" {
			return nil, ErrWrongCode
		}
		return nil, fmt.Errorf("%s does not open core.db: restore the right key file, or remove it and unlock with the recovery code",
			filepath.Join(o.Data, "kek"))
	} else if err != nil {
		return nil, err
	}
	inst := &Instance{DB: sqldb}
	defer func() {
		if err != nil {
			sqldb.Close()
		}
	}()

	if err := vault.Migrate(ctx, sqldb); err != nil {
		return nil, err
	}
	v, err := vault.Open(ctx, sqldb, o.Data)
	if err != nil {
		return nil, err
	}
	if v.Locked() {
		if code == "" {
			// no key file: the vault waits for the recovery code
			return nil, ErrLocked
		}
		if ok, err := v.Unlock(ctx, code); err != nil {
			return nil, err
		} else if !ok {
			return nil, ErrWrongCode
		}
	}
	firstRun := newFirstRun(o.Data)
	firstRun.stdio = o.Stdio
	firstRun.dropOldLink()
	if shown, err := v.PeekRecoveryCode(ctx); err != nil {
		return nil, err
	} else if shown != "" {
		// to a file and not the terminal: in a container the terminal is the log, which is kept and shipped, and the code
		// is the only way back if the volume is lost. It counts as shown only once delivered; when the file cannot be
		// written the terminal is the lesser evil, the code is lost otherwise.
		if err := firstRun.setRecovery(shown); err != nil {
			slog.Warn("the recovery code could not be written to a file, printing it here instead: it is in the log", "file", firstRun.path, "err", err)
			fmt.Fprintf(os.Stderr, "kedalio-core: RECOVERY CODE (shown once; write it down and keep it away from this machine):\n\n  %s\n\n", shown)
		}
		if err := v.RecoveryShown(ctx); err != nil {
			return nil, err
		}
	}

	// the memory's tables first: search and tasks keep derived tables beside its pages
	used, err := hasMemory(ctx, sqldb)
	if err != nil {
		return nil, err
	}
	if err := memory.Migrate(ctx, sqldb); err != nil {
		return nil, err
	}
	language, zone, err := settleLocale(ctx, sqldb, used, o)
	if err != nil {
		return nil, err
	}
	loc, err := tasks.LoadZone(zone)
	if err != nil {
		return nil, err
	}
	inst.Language, inst.Zone = language, zone
	index, err := search.New(ctx, sqldb)
	if err != nil {
		return nil, err
	}
	if err := tasks.Migrate(ctx, sqldb); err != nil {
		return nil, err
	}
	store := memory.New(sqldb, index)
	inst.store = store
	store.Areas = memory.AreasEN
	if language == "Italian" {
		store.Areas = memory.AreasIT
	}
	book := tasks.NewBook(store, sqldb, "owner", loc, "owner")
	// on every start: a view the owner deleted keeps its page row, so only the ones never made are written
	if err := book.EnsureViews(ctx); err != nil {
		return nil, err
	}

	if err := projects.Migrate(ctx, sqldb); err != nil {
		return nil, err
	}
	if err := upkeep.Migrate(ctx, sqldb); err != nil {
		return nil, err
	}
	if o.Stdio && o.Token == "" {
		// the server insists on a token; nothing here ever sees it, as there is no HTTP side to present it to
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		o.Token = hex.EncodeToString(b[:])
	}
	cfg := mcp.Config{
		UpkeepToken: o.UpkeepToken, Upkeep: &mcp.Upkeep{DB: sqldb},
		Memory: store, Search: index, Tasks: book, Ops: &mcp.Ops{Projects: projects.New(sqldb)},
		Mask: v.Mask,
		// 20 wrong tokens at once, then one each 3 s per address: a guess of a 32-character token is hopeless either way,
		// this is about not being a free oracle and not letting the hash work be a load
		AuthFailures: identity.NewBuckets(20, 3*time.Second),
		ClientIP:     identity.IPSource{Header: o.ClientIPHeader, Trusted: o.TrustedProxies}.ClientIP,
		Token:        o.Token, Owner: o.Owner, Language: language, Zone: loc, PublicURL: o.PublicURL, Label: o.Label,
	}
	// background work starts only once nothing below can fail: a failed start closes the database, and a loop already
	// running would go on against it until the context ends
	var later []func()
	if o.EmbedURL != "" && o.EmbedModel != "" {
		query, background := search.Lanes(search.NewOpenAIEmbedder(search.EmbedConfig{URL: o.EmbedURL, Model: o.EmbedModel}))
		cfg.Embedder = query
		later = append(later, func() { EmbedInBackground(ctx, index, store, background) })
	}
	if !o.Stdio {
		// named tokens need only their table, not the public URL the rest of the identity is built on
		if err := identity.Migrate(ctx, sqldb); err != nil {
			return nil, err
		}
		cfg.Named = identity.NewTokens(sqldb).Lookup
	}
	var idn *identity.Identity
	if o.PublicURL != "" && !o.Stdio {
		if idn, err = identity.New(ctx, sqldb, identity.Options{PublicURL: o.PublicURL, ClientIPHeader: o.ClientIPHeader, TrustedProxies: o.TrustedProxies, Name: o.Owner, Language: language}); err != nil {
			return nil, err
		}
		cfg.Verify = idn.Verify
		cfg.Client = idn.Client
		cfg.Approvals = approvals{idn.Auth}
		cfg.ResourceMetadata = idn.ResourceMetadataURL()
		later = append(later, func() { idn.PurgeEvery(ctx, time.Hour) })
	}
	srv, err := mcp.New(cfg)
	if err != nil {
		return nil, err
	}
	inst.Handler = srv.Handler()
	inst.srv = srv
	if idn != nil {
		inst.Handler = idn.Wrap(inst.Handler)
	}
	var off *offsite.Uploader
	if o.BackupDir != "" {
		if o.Offsite.Endpoint != "" {
			off = o.newUploader()
			inst.Handler = withBackupHealth(inst.Handler, off, o.BackupEvery, o.Token)
		} else {
			slog.Warn("no -backup-s3-endpoint: the copies stay on this server's disk, which a lost disk takes with them")
		}
		inst.wg.Add(1)
		go func() {
			defer inst.wg.Done()
			backUpEvery(ctx, sqldb, dbKey, o.BackupDir, o.BackupEvery, o.BackupKeep, off)
		}()
	} else {
		slog.Warn("no -backup-dir: nothing copies the database off this disk")
	}
	for _, f := range later {
		inst.wg.Add(1)
		go func() {
			defer inst.wg.Done()
			f()
		}()
	}
	return inst, nil
}

func (o Options) newUploader() *offsite.Uploader {
	prefix := db.NormalizeS3Prefix(o.Offsite.Prefix)
	access, secret := o.Offsite.AccessKey, o.Offsite.SecretKey
	return &offsite.Uploader{Keep: o.Offsite.Keep, Client: &offsite.Client{
		Endpoint: o.Offsite.Endpoint, Bucket: o.Offsite.Bucket, Region: o.Offsite.Region, Prefix: prefix,
		Credentials: func(context.Context) (string, string, error) { return access, secret, nil },
	}}
}

// withBackupHealth adds the offsite copy's state to /health, which is how the owner sees that copies stopped leaving the
// server: "status" stays ok (a container that is up must not be restarted for a store that is down).
//
// The detail (how old the newest copy is, since when it fails) is for the owner: a caller without the owner's bearer
// token, which is anyone on the internet, gets the plain «ok» the container's health check needs.
func withBackupHealth(next http.Handler, off *offsite.Uploader, every time.Duration, token string) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			next.ServeHTTP(w, r)
			return
		}
		got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		have := sha256.Sum256([]byte(got))
		if token == "" || subtle.ConstantTimeCompare(have[:], want[:]) != 1 {
			next.ServeHTTP(w, r)
			return
		}
		body := offsiteHealth(off.State(), every, time.Now())
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
}

// offsiteHealth is /health's body. «ok» needs a recent copy on the store: a sync that goes through only proves the keys. A copy
// is due each `every`, so one older than two of those means copies stopped reaching the store.
func offsiteHealth(st offsite.State, every time.Duration, now time.Time) map[string]string {
	body := map[string]string{"status": "ok", "offsite_backup": "waiting"}
	if !st.LastOK.IsZero() {
		body["offsite_backup"] = "ok"
		body["offsite_last_ok"] = st.LastOK.UTC().Format(time.RFC3339)
		if st.NewestCopy.IsZero() || now.Sub(st.NewestCopy) > 2*every {
			body["offsite_backup"] = "stale"
		}
	}
	if !st.NewestCopy.IsZero() {
		body["offsite_newest_copy"] = st.NewestCopy.UTC().Format(time.RFC3339)
	}
	if !st.FailingSince.IsZero() {
		body["offsite_backup"] = "failing"
		body["offsite_failing_since"] = st.FailingSince.UTC().Format(time.RFC3339)
	}
	return body
}

// retryAfter is how soon a copy or an upload that failed is tried again; a test shortens it.
var retryAfter = 15 * time.Minute

// backUpEvery keeps a copy at most `every` old in dir, the newest `keep` of them: one when the core starts if the last is
// older than that, then one each `every`. With an offsite uploader the copies on disk are then sent to the store. A
// failure is logged once per try and tried again soon, and the copies already there stay.
func backUpEvery(ctx context.Context, d *sql.DB, key []byte, dir string, every time.Duration, keep int, off *offsite.Uploader) {
	for {
		var wait time.Duration
		if copies, err := db.Copies(dir); err == nil && len(copies) > 0 {
			if t, ok := db.CopyTime(copies[len(copies)-1]); ok {
				wait = max(0, every-time.Since(t))
			}
		}
		if wait == 0 {
			path, err := db.Backup(ctx, d, dir, key, time.Now())
			if err == nil {
				err = db.Rotate(dir, keep)
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Warn("backup failed", "err", err)
				wait = min(retryAfter, every)
			} else {
				slog.Info("backup made", "file", filepath.Base(path), "keep", keep)
				wait = every
			}
		}
		if off != nil {
			wasFailing := !off.State().FailingSince.IsZero()
			n, err := off.Sync(ctx, dir)
			switch {
			case ctx.Err() != nil:
				return
			case err != nil:
				// one line when it starts to fail; the retries that follow are seen in /health, not 96 lines a day
				if !wasFailing {
					slog.Warn("offsite backup failed, retrying", "err", err)
				}
				wait = min(wait, retryAfter)
			case n > 0 || wasFailing:
				slog.Info("offsite backup made", "copies", n, "keep", off.Keep)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// embedIdle is how long the background embedding waits when a round found nothing to do; a test makes it long.
var embedIdle = 10 * time.Second

// EmbedInBackground embeds the chunks the model has not seen, a batch at a time, then brings the pages' neighbours by
// meaning up to date: a write never waits for the model, and a model that is down only delays this. A round that did
// a full batch of work goes straight on to the next one.
func EmbedInBackground(ctx context.Context, index *search.Index, store *memory.Store, e search.Embedder) {
	const batch, conflictPages = 32, 5
	for ctx.Err() == nil {
		n, err := index.EmbedPending(ctx, e, batch)
		work := n
		if err == nil && n == 0 {
			work, err = store.RefreshSimilar(ctx, e.Model())
		}
		if err == nil && work == 0 {
			// a few pages a round, so a query that comes in waits for at most their one embedder call
			work, err = store.CheckConflicts(ctx, e, conflictPages)
			if work == conflictPages {
				work = batch // a full round: there may be more
			}
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("embedding in the background", "err", err)
		}
		if work < batch || err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(embedIdle):
			}
		}
	}
}
