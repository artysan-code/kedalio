// Command kedalio-core runs the Kedalio core: the MCP connector over the memory, the tasks and the projects, in one
// encrypted SQLite database (docs/architecture.md).
//
//	KEDALIO_CORE_TOKEN=… kedalio-core -data /var/lib/kedalio
//
// Flags with an environment twin (KEDALIO_CORE_<NAME>): -data, -listen, -public-url, -client-ip-header, -owner,
// -language, -timezone (-zone), -embed-url, -embed-model, -backup-dir, -backup-every, -backup-keep,
// -backup-s3-endpoint, -backup-s3-bucket, -backup-s3-region, -backup-s3-prefix, -backup-s3-keep. The bearer token is only ever read from
// KEDALIO_CORE_TOKEN: a flag would end in the shell history.
//
// `kedalio-core clients [-data dir] list | revoke <client-id>` lists the OAuth clients that were given access and takes
// one away (all its tokens, and the client itself); it reads the same data folder and environment.
//
// `kedalio-core revisions redact [-data dir] [-dry-run] <path>@<rev> | <path> ...` takes the values the secret scan finds
// out of stored revisions (or the live page), in place, and prints only how many it replaced.
//
// With -public-url the core is also an OAuth authorization server for MCP clients such as claude.ai: the owner signs in
// with a passkey, and the first start with no passkey prints the one-time link that makes it. The static token keeps
// working beside it.
//
// The database is encrypted at rest with a key derived from the instance key, a file in the data folder (<data>/kek). The
// first start makes it and prints the recovery code, once, on stderr. A data folder without it (a restored volume)
// starts locked: the core answers /health and POST /unlock only, and opens when the code arrives.
//
// With -backup-dir the core keeps consistent, still-encrypted copies of the database there (see core/db/backup.go); the
// folder should be on another machine's disk, and the copies need the recovery code to be read after the key file is lost.
// With -backup-s3-endpoint each copy is also sent to an S3-compatible store (core/offsite); its keys are the environment
// variables BACKUP_S3_ACCESS_KEY and BACKUP_S3_SECRET_KEY (like the bearer token, never a flag).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"kedalio/core/db"
	"kedalio/core/identity"
	"kedalio/core/instance"
	"kedalio/core/offsite"
)

// env is the default of a flag: its environment twin when set.
func env(name, def string) string {
	if v := os.Getenv("KEDALIO_CORE_" + name); v != "" {
		return v
	}
	return def
}

// envDuration and envInt read the environment twin of a numeric flag. A value that does not parse is not replaced by the
// default: the owner set it on purpose and would run with something else without knowing, so the problem is added to
// bad and the caller refuses to start once the flags are defined.
func envDuration(name string, def time.Duration, bad *error) time.Duration {
	v := os.Getenv("KEDALIO_CORE_" + name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		*bad = errors.Join(*bad, fmt.Errorf("KEDALIO_CORE_%s=%q is not a duration (for example 24h or 30m)", name, v))
		return def
	}
	return d
}

func envInt(name string, def int, bad *error) int {
	v := os.Getenv("KEDALIO_CORE_" + name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*bad = errors.Join(*bad, fmt.Errorf("KEDALIO_CORE_%s=%q is not a whole number", name, v))
		return def
	}
	return n
}

func main() {
	if printVersion(os.Args[1:], os.Stdout) {
		return
	}
	// `kedalio-core clients …` manages the OAuth clients of an instance's data folder and does not start the core
	if len(os.Args) > 1 && os.Args[1] == "clients" {
		if err := runClients(context.Background(), os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "kedalio-core:", err)
			os.Exit(1)
		}
		return
	}
	// `kedalio-core passkeys list|remove` manages the owner's passkeys and does not start the core
	if len(os.Args) > 1 && os.Args[1] == "passkeys" {
		if err := runPasskeys(context.Background(), os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "kedalio-core:", err)
			os.Exit(1)
		}
		return
	}
	// `kedalio-core approve <code>` approves a new client's sign-in code and does not start the core
	if len(os.Args) > 1 && os.Args[1] == "approve" {
		st, _ := os.Stdin.Stat()
		tty := st != nil && st.Mode()&os.ModeCharDevice != 0
		if err := runApprove(context.Background(), os.Args[2:], os.Stdin, os.Stdout, tty); err != nil {
			fmt.Fprintln(os.Stderr, "kedalio-core:", err)
			os.Exit(1)
		}
		return
	}
	// `kedalio-core revisions redact …` takes secrets out of stored revisions in place and does not start the core
	if len(os.Args) > 1 && os.Args[1] == "revisions" {
		if err := runRevisions(context.Background(), os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "kedalio-core:", err)
			os.Exit(1)
		}
		return
	}
	// `kedalio-core stdio …` serves the same tools over stdin and stdout to a client on this computer (docs/local.md)
	if len(os.Args) > 1 && os.Args[1] == "stdio" {
		if err := runStdio(context.Background(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "kedalio-core:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "kedalio-core:", err)
		os.Exit(1)
	}
}

// What a request with a body may take to arrive and weigh, and how long its answer may take to be written. Variables so
// a test can lower them.
var (
	// maxRequestBody: the biggest legitimate body is a page of memory text; documents are fetched by the core from a URL,
	// never uploaded to it.
	maxRequestBody int64 = 8 << 20
	// bodyReadTimeout bounds the upload only (a slow sender holds a connection and a goroutine for as long as it likes
	// otherwise); the deadline is cleared once the body is read, because a read deadline that expires while the
	// handler works cancels the request's context.
	bodyReadTimeout = 60 * time.Second
	// responseWriteTimeout is how long after the body is read the answer must be written: the handler's work (a document fetch
	// and extraction, an upkeep run) and the write together. A write deadline is absolute, so it is set when the body ends.
	responseWriteTimeout = 5 * time.Minute
)

// bounded caps the body of a request and the time its upload and its answer may take. There is no server-wide
// ReadTimeout/WriteTimeout: they count from the headers and would cut a long tool call (and any stream) at their
// expiry, so the deadlines are set per request that has a body, and the upload's one is lifted when the body ends.
func bounded(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Now().Add(bodyReadTimeout))
			_ = rc.SetWriteDeadline(time.Now().Add(bodyReadTimeout + 10*time.Second)) // room for an error answer to a failed upload
			r.Body = &liftOnEOF{ReadCloser: http.MaxBytesReader(w, r.Body, maxRequestBody), lift: func() {
				_ = rc.SetReadDeadline(time.Time{})
				_ = rc.SetWriteDeadline(time.Now().Add(responseWriteTimeout))
			}}
		}
		next.ServeHTTP(w, r)
	})
}

// liftOnEOF runs lift when the body has been read to its end. A failed read (the deadline, the size cap) keeps the
// deadline: lifting it would let the server wait on the rest of a body that is not coming.
type liftOnEOF struct {
	io.ReadCloser
	lift func()
	once sync.Once
}

func (l *liftOnEOF) Read(p []byte) (int, error) {
	n, err := l.ReadCloser.Read(p)
	if err == io.EOF {
		l.once.Do(l.lift)
	}
	return n, err
}

// minToken is the fewest characters the owner's bearer token may have, as for the upkeep token: the token
// is what stands between the internet and the memory, so a guessable one is refused at start.
const minToken = 32

func checkToken(t string) error {
	if t == "" {
		return errors.New("KEDALIO_CORE_TOKEN is not set: the connector does not start without a bearer token")
	}
	if len(t) < minToken {
		return fmt.Errorf("KEDALIO_CORE_TOKEN is %d characters: at least %d (for example `openssl rand -hex 32`)", len(t), minToken)
	}
	return nil
}

func run() error {
	var o instance.Options
	var badEnv error
	flag.StringVar(&o.Data, "data", env("DATA", "data"), "folder of the database (core.db) and the instance key (kek)")
	listen := flag.String("listen", env("LISTEN", "127.0.0.1:8787"), "address to listen on")
	flag.StringVar(&o.PublicURL, "public-url", env("PUBLIC_URL", ""), "origin the core is reached at (https://core.example.com): turns OAuth for MCP clients on; empty: the static token only")
	flag.StringVar(&o.Label, "label", env("LABEL", ""), "a word told to MCP clients beside the address (trial), so that a session tells this core from another Kedalio")
	flag.StringVar(&o.ClientIPHeader, "client-ip-header", env("CLIENT_IP_HEADER", ""), "header a proxy in front sets with the client's address (x-forwarded-for), for the rate limits")
	trusted := flag.String("trusted-proxies", env("TRUSTED_PROXIES", ""), "comma-separated addresses or CIDRs of the proxies in front: with them set, -client-ip-header counts only on a connection from one, and in x-forwarded-for the right-most address that is not one of them is the client")
	flag.StringVar(&o.Owner, "owner", env("OWNER", ""), "the owner's name, for the instructions clients are given")
	flag.StringVar(&o.Language, "language", env("LANGUAGE", ""), "the language of a NEW database (English, Italian): it picks the memory's area names (io, projects… or io, progetti…) and the answers; fixed when the database is made, English when empty")
	zone := env("TIMEZONE", env("ZONE", ""))
	flag.StringVar(&o.Zone, "timezone", zone, "the time zone (IANA name): of a NEW database, else its own; for this run when it is set. Empty: the host's (TZ)")
	flag.StringVar(&o.Zone, "zone", zone, "the same as -timezone")
	flag.StringVar(&o.EmbedURL, "embed-url", env("EMBED_URL", ""), "base URL of an OpenAI-compatible embeddings server; empty: search by words alone")
	flag.StringVar(&o.EmbedModel, "embed-model", env("EMBED_MODEL", ""), "the embedding model")
	flag.StringVar(&o.BackupDir, "backup-dir", env("BACKUP_DIR", ""), "folder for the encrypted copies of the database, ideally another disk or machine; empty: no backups")
	flag.DurationVar(&o.BackupEvery, "backup-every", envDuration("BACKUP_EVERY", 24*time.Hour, &badEnv), "how often a copy is made")
	flag.IntVar(&o.BackupKeep, "backup-keep", envInt("BACKUP_KEEP", db.DefaultKeep, &badEnv), "how many copies the folder keeps (the oldest go first)")
	flag.StringVar(&o.Offsite.Endpoint, "backup-s3-endpoint", env("BACKUP_S3_ENDPOINT", ""), "an S3-compatible store the copies are also sent to (https://<account>.r2.cloudflarestorage.com); its keys are the environment variables BACKUP_S3_ACCESS_KEY and BACKUP_S3_SECRET_KEY; empty: copies stay on this disk")
	flag.StringVar(&o.Offsite.Bucket, "backup-s3-bucket", env("BACKUP_S3_BUCKET", ""), "the bucket of the store")
	flag.StringVar(&o.Offsite.Region, "backup-s3-region", env("BACKUP_S3_REGION", "auto"), "the store's region (auto for Cloudflare R2)")
	flag.StringVar(&o.Offsite.Prefix, "backup-s3-prefix", env("BACKUP_S3_PREFIX", offsite.DefaultPrefix), "the folder of the bucket the copies go in; nothing outside it is listed or deleted")
	flag.IntVar(&o.Offsite.Keep, "backup-s3-keep", envInt("BACKUP_S3_KEEP", offsite.DefaultKeep, &badEnv), "how many copies the store keeps (the oldest go first)")
	// -version is answered by main before the flags are parsed, so it is told here only for -h
	defaultUsage := flag.Usage
	flag.Usage = func() {
		defaultUsage()
		fmt.Fprintln(flag.CommandLine.Output(), "\nkedalio-core -version prints the version and exits (given alone).")
	}
	flag.Parse()
	if badEnv != nil {
		return badEnv
	}

	o.Offsite.AccessKey, o.Offsite.SecretKey = os.Getenv("BACKUP_S3_ACCESS_KEY"), os.Getenv("BACKUP_S3_SECRET_KEY")
	o.Token = os.Getenv("KEDALIO_CORE_TOKEN")
	o.UpkeepToken = os.Getenv("KEDALIO_UPKEEP_TOKEN")
	if err := checkToken(o.Token); err != nil {
		return err
	}
	var err error
	if o.TrustedProxies, err = identity.ParseTrustedProxies(*trusted); err != nil {
		return fmt.Errorf("-trusted-proxies: %w", err)
	}
	if o.BackupKeep < 1 || o.BackupEvery < time.Minute {
		return errors.New("-backup-keep is at least 1 and -backup-every at least a minute")
	}

	if o.Offsite.Endpoint != "" {
		switch {
		case o.BackupDir == "":
			return errors.New("-backup-s3-endpoint sends the copies -backup-dir makes: set -backup-dir too")
		case o.Offsite.Bucket == "":
			return errors.New("-backup-s3-endpoint needs -backup-s3-bucket")
		case o.Offsite.Keep < 1:
			return errors.New("-backup-s3-keep is at least 1")
		case o.Offsite.AccessKey == "" || o.Offsite.SecretKey == "":
			return errors.New("-backup-s3-endpoint needs the environment variables BACKUP_S3_ACCESS_KEY and BACKUP_S3_SECRET_KEY")
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(o.Data, 0o700); err != nil {
		return err
	}
	g := newGate(ctx, o)
	inst, err := o.Start(ctx)
	switch {
	case errors.Is(err, instance.ErrLocked):
		slog.Warn("locked: POST the recovery code to /unlock", "data", o.Data)
	case err != nil:
		return err
	default:
		g.open.Store(inst)
	}
	defer func() {
		// the background work ends with ctx, which a failed listen has not cancelled yet: Close would wait for it forever.
		// The instance may be the one the unlock opened.
		stop()
		if i := g.Instance(); i != nil {
			i.Close()
		}
	}()

	// no server-wide ReadTimeout or WriteTimeout: a deadline counts from the headers and, once it expires, cancels the
	// request, an answer that is still being worked on. bounded sets them per request with a body instead; the headers
	// and the idle wait between requests are bounded here.
	httpSrv := &http.Server{Addr: *listen, Handler: bounded(g), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.ListenAndServe() }()
	slog.Info("kedalio-core listening", "addr", *listen, "data", o.Data, "locked", g.Instance() == nil,
		"oauth", o.PublicURL != "", "backups", o.BackupDir != "")
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdown)
}
