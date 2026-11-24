// Package memory is the account's memory: Markdown pages with a path, in the core's SQLite database.
//
// Every write keeps the version it replaces (revisions), so any page can be read as it was and put back; nothing is
// ever removed for good, a deletion is a revision that marks it gone. The links between pages ([[target]] or
// [[target|label]], as Obsidian writes them) are kept per page for the graph and the backlinks, with the name of the
// relation when the page names one. Every write of a page goes to the search index (Indexer) in the same transaction.
//
// No structure is imposed: a path is a folder/name.md the client chooses. One prefix is reserved, tasks/, where the
// tasks keep one page each. Every other page carries its kind in its front matter, written here when the writer left
// it out.
package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"

	"kedalio/core/db"
)

// Migrations are the memory's tables, one step each, only ever appended to.
var Migrations = []string{`
create table docs (
  path text primary key, title text not null, body text not null, rev integer not null,
  created text not null, updated text not null, by text not null, deleted integer not null default 0,
  kind text);
create index docs_live on docs(path) where deleted = 0;
create table revisions (
  path text not null, rev integer not null, at text not null, by text not null, op text not null, body text not null,
  primary key (path, rev));
create index revisions_at on revisions(at);
create table links (src text not null, dst text not null, rel text not null default '', primary key (src, dst, rel));
create index links_dst on links (dst);
create table memory_meta (k text primary key, v text not null);
insert into memory_meta (k, v) values ('writes_all', '0'), ('writes_pages', '0');`,
	// the pages near each other by meaning (similar.go): a page's vector, and its nearest pages with the cosine
	`create table page_vectors (path text primary key, rev integer not null, model text not null, vec blob not null);
create table similar (src text not null, dst text not null, w real not null, primary key (src, dst));
create index similar_dst on similar (dst);`,
	// facts in time (conflicts.go): the pairs of lines that may say the same fact twice, and the rev of each page whose
	// new lines were last compared; pages that exist now start as compared, so only later writes are looked at
	`create table fact_conflicts (path text not null, old text not null, new text not null, day text not null, primary key (path, old, new));
create table fact_checked (path text primary key, rev integer not null);
insert into fact_checked select path, rev from docs where deleted = 0;`}

// Migrate brings the memory's tables up to date.
func Migrate(ctx context.Context, sqldb *sql.DB) error {
	return db.Migrate(ctx, sqldb, "memory", Migrations)
}

// Indexer keeps the search index in step with the pages, inside the page's own write transaction.
type Indexer interface {
	IndexPage(ctx context.Context, tx *sql.Tx, path, title, body string) error
	RemovePage(ctx context.Context, tx *sql.Tx, path string) error
}

// TaskIndexer keeps the tasks' derived index in step with the task pages (`tasks/t-….md`), inside the page's own write
// transaction. A task is not in the search index; the task book finds tasks by its own query.
type TaskIndexer interface {
	IndexTask(ctx context.Context, tx *sql.Tx, path, body string) error
	DropTask(ctx context.Context, tx *sql.Tx, path string) error
}

// Op is what a revision did.
type Op string

const (
	OpWrite   Op = "write"
	OpDelete  Op = "delete"
	OpRestore Op = "restore"
)

// Info is what a listing says of a page.
type Info struct {
	Path string
	// Title is the page's front matter title, else its first heading, else its name.
	Title string
	// Kind is "" for a task or a page never given one.
	Kind    Kind
	Rev     int
	Created string
	Updated string
	By      string
}

// Doc is a page.
type Doc struct {
	Info
	Body string
}

// Revision is a version of a page; a deletion's body is empty.
type Revision struct {
	Path string
	Rev  int
	At   string
	By   string
	Op   Op
	Body string
}

// HistoryEntry is a revision without its body.
type HistoryEntry struct {
	Path string
	Rev  int
	At   string
	By   string
	Op   Op
}

// ErrChanged is returned by WriteAt when the page is newer than the revision the writer read.
var ErrChanged = errors.New("changed meanwhile")

// Resolver resolves a [[target]] to a page path, "" when it points nowhere.
type Resolver func(target string) string

var (
	linkRe    = regexp.MustCompile(`\[\[([^\]|#]+)(?:#[^\]|]*)?(?:\|[^\]]*)?\]\]`)
	mdExt     = regexp.MustCompile(`(?i)\.md$`)
	fmBlock   = regexp.MustCompile(`^---\n(?s:.*?)\n---\n?`)
	fmTitle   = regexp.MustCompile(`(?m)^title:\s*["']?(.+?)["']?\s*$`)
	heading   = regexp.MustCompile(`(?m)^#\s+(.+)$`)
	labelLink = regexp.MustCompile(`\[\[([^\]|#]+)(?:#[^\]|]*)?\|([^\]]*)\]\]`)
	plainLink = regexp.MustCompile(`\[\[([^\]|#]+)[^\]]*\]\]`)
	badChars  = regexp.MustCompile(`[<>:"|?*]`)
	taskIDRe  = regexp.MustCompile(`^tasks/(t-[\w-]+)\.md$`)
	logPathRe = regexp.MustCompile(`^tasks/t-[\w-]+/log\.md$`)
	slashes   = regexp.MustCompile(`/{2,}`)
)

func stripMD(s string) string { return mdExt.ReplaceAllString(s, "") }

func lastSegment(p string) string { return p[strings.LastIndex(p, "/")+1:] }

// isLogPath reports whether a path is a task's log episode, which is a page, not a task.
func isLogPath(p string) bool { return logPathRe.MatchString(p) }

// isTaskPage reports whether the task book owns the page.
func isTaskPage(p string) bool { return strings.HasPrefix(p, "tasks/") && !isLogPath(p) }

// MakeResolver returns a resolver over a set of live paths: the exact path, else the shallowest page with that name
// (ties by path order). Built once, it answers each target without touching the database.
func MakeResolver(paths []string) Resolver {
	exact := map[string]bool{}
	byName := map[string]string{}
	depth := func(p string) int { return strings.Count(p, "/") }
	for _, p := range paths {
		exact[p] = true
		name := strings.ToLower(lastSegment(p))
		cur, ok := byName[name]
		if !ok || depth(p) < depth(cur) || (depth(p) == depth(cur) && p < cur) {
			byName[name] = p
		}
	}
	return func(target string) string {
		t := stripMD(target)
		if exact[t+".md"] {
			return t + ".md"
		}
		return byName[strings.ToLower(lastSegment(t))+".md"]
	}
}

// CleanPath returns a path the memory accepts: folders and a name ending in .md, no way out of the tree.
func CleanPath(p string) (string, error) {
	s := strings.TrimSpace(p)
	s = strings.ReplaceAll(s, "\\", "/")
	s = strings.TrimLeft(s, "/")
	s = slashes.ReplaceAllString(s, "/")
	withExt := s
	if !mdExt.MatchString(s) {
		withExt = s + ".md"
	}
	bad := s == "" || len(utf16.Encode([]rune(withExt))) > 300 || badChars.MatchString(withExt)
	for _, r := range withExt {
		if r < 32 {
			bad = true
		}
	}
	for _, seg := range strings.Split(withExt, "/") {
		if seg == "" || seg == "." || seg == ".." {
			bad = true
		}
	}
	if bad {
		return "", fmt.Errorf("not a document path: %s", p)
	}
	return withExt, nil
}

// TitleOf returns the title of a page: its front matter `title`, else its first heading, else its name.
func TitleOf(path, body string) string {
	if m := fmRe.FindStringSubmatch(body); m != nil {
		if t := fmTitle.FindStringSubmatch(m[1]); t != nil {
			return strings.TrimSpace(t[1])
		}
	}
	if h := heading.FindStringSubmatch(fmBlock.ReplaceAllString(body, "")); h != nil {
		return strings.TrimSpace(h[1])
	}
	return strings.TrimSpace(stripMD(lastSegment(path)))
}

// LinksIn returns the targets a page links to, as written ([[a/b|label]] gives "a/b"), without headings, once each.
func LinksIn(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range linkRe.FindAllStringSubmatch(body, -1) {
		t := stripMD(strings.TrimSpace(m[1]))
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// LedeOf returns the sentence under the title that says what a page is, without Markdown links.
func LedeOf(body string) string {
	lines := strings.Split(strings.TrimSpace(fmBlock.ReplaceAllString(body, "")), "\n")
	if strings.HasPrefix(lines[0], "# ") {
		lines = lines[1:]
	}
	first := ""
	for _, l := range lines {
		// a heading says where the text is, not what it says
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			first = l
			break
		}
	}
	first = strings.TrimSpace(first)
	first = labelLink.ReplaceAllString(first, "$2")
	first = plainLink.ReplaceAllStringFunc(first, func(m string) string {
		return lastSegment(plainLink.FindStringSubmatch(m)[1])
	})
	// 240 UTF-16 units: the length the lede has always been cut to
	if u := utf16.Encode([]rune(first)); len(u) > 240 {
		first = string(utf16.Decode(u[:240]))
	}
	return first
}

// Store is the memory over a database migrated with Migrate.
type Store struct {
	db  *sql.DB
	idx Indexer
	// Areas are the account's area names; AreasEN unless set (the instance sets them from the database's language).
	Areas Areas
	tasks TaskIndexer
	now   func() time.Time
	// factFails counts, per page and revision, the rounds in which the embedder failed on it (CheckConflicts)
	factMu    sync.Mutex
	factFails map[string]int
	// graphVer counts the writes that can change the recall graph (pages, links, similarity); graphHit is the graph
	// built at some version, reused until a write moves it on.
	graphVer atomic.Uint64
	graphMu  sync.Mutex
	// verConn is one connection kept for `pragma data_version`, which counts the commits of any other connection:
	// another process's (redact, restore, a reindex) and this process's own writer connections.
	verMu       sync.Mutex
	verConn     *sql.Conn
	verGen      uint64
	graphHit    *cachedGraph
	resolverHit struct {
		ver dbState
		fn  Resolver
	}
}

// New returns a Store; idx may be nil, then nothing is indexed for search.
func New(sqldb *sql.DB, idx Indexer) *Store {
	return &Store{db: sqldb, idx: idx, Areas: AreasEN, now: time.Now}
}

// SetTaskIndexer makes every write and removal of a task page update the tasks' index in the same transaction.
func (s *Store) SetTaskIndexer(x TaskIndexer) { s.tasks = x }

func (s *Store) stamp() string { return s.now().UTC().Format("2006-01-02T15:04:05.000Z") }

const infoCols = "path, title, kind, rev, created, updated, by"

type scanner interface{ Scan(...any) error }

func scanInfo(r scanner, extra ...any) (Info, error) {
	var i Info
	var kind sql.NullString
	err := r.Scan(append([]any{&i.Path, &i.Title, &kind, &i.Rev, &i.Created, &i.Updated, &i.By}, extra...)...)
	i.Kind = Kind(kind.String)
	return i, err
}

// Get returns a live page, nil when there is none.
func (s *Store) Get(ctx context.Context, path string) (*Doc, error) {
	p, err := CleanPath(path)
	if err != nil {
		return nil, err
	}
	var d Doc
	d.Info, err = scanInfo(s.db.QueryRowContext(ctx,
		"select "+infoCols+", body from docs where path = ? and deleted = 0", p), &d.Body)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ListOpts narrows List.
type ListOpts struct {
	// Tasks includes the task pages (they are also included when Kind is episode, which task logs are).
	Tasks bool
	// Limit is 1000 when zero.
	Limit int
	// Kind lists the pages of that kind only, when set.
	Kind Kind
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func likePrefix(prefix string) string { return likeEscaper.Replace(prefix) + "%" }

// List returns the live pages under a folder (all of them for ""), newest first.
func (s *Store) List(ctx context.Context, prefix string, opts ListOpts) ([]Info, error) {
	q := "select " + infoCols + ` from docs where deleted = 0 and path like ? escape '\'`
	args := []any{likePrefix(prefix)}
	if !opts.Tasks && opts.Kind != KindEpisode {
		q += " and path not like 'tasks/%'"
	}
	if opts.Kind != "" {
		q += " and kind = ?"
		args = append(args, string(opts.Kind))
	}
	limit := opts.Limit
	if limit == 0 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, q+" order by updated desc, path limit ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Info
	for rows.Next() {
		i, err := scanInfo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// Write writes a page, whatever it replaces. A page that does not say its kind keeps the one it had, else takes its
// folder's: the body written carries it.
func (s *Store) Write(ctx context.Context, path, body, by string) (*Doc, error) {
	return s.write(ctx, path, body, by, -1, OpWrite)
}

// WriteAt is Write refusing (ErrChanged) to overwrite a page newer than base, the revision the writer read (0 for a
// page the writer saw absent).
func (s *Store) WriteAt(ctx context.Context, path, body, by string, base int) (*Doc, error) {
	return s.write(ctx, path, body, by, base, OpWrite)
}

func (s *Store) write(ctx context.Context, path, given, by string, base int, op Op) (*Doc, error) {
	p, err := CleanPath(path)
	if err != nil {
		return nil, err
	}
	now := s.stamp()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var curRev, curDeleted int
	var curCreated, curBody string
	var curKind sql.NullString
	found := true
	err = tx.QueryRowContext(ctx, "select rev, created, deleted, kind, body from docs where path = ?", p).
		Scan(&curRev, &curCreated, &curDeleted, &curKind, &curBody)
	if err == sql.ErrNoRows {
		found = false
	} else if err != nil {
		return nil, err
	}
	if base >= 0 {
		seen := curRev
		if curDeleted != 0 {
			seen = 0
		}
		if seen != base {
			return nil, fmt.Errorf("%s %w (now rev %d): read it again", p, ErrChanged, curRev)
		}
	}
	rev := curRev + 1
	created := now
	if found {
		created = curCreated
	}
	task := isTaskPage(p)
	// a task page is of kind task: the task book writes its front matter, so none is added here
	taskID := taskIDRe.MatchString(p)
	var was *KindIs
	if found && curDeleted == 0 && IsKind(curKind.String) && Kind(curKind.String) != KindTask {
		was = &KindIs{Kind(curKind.String), FrontMatter(curBody)["is"]}
	}
	body := given
	var kind sql.NullString
	if !task {
		var k KindIs
		k, body = WithKind(p, given, s.Areas, was)
		kind = sql.NullString{String: string(k.Kind), Valid: true}
	}
	if taskID {
		kind = sql.NullString{String: string(KindTask), Valid: true}
	}
	title := TitleOf(p, body)

	if _, err := tx.ExecContext(ctx,
		`insert into docs (path, title, body, rev, created, updated, by, deleted, kind) values (?, ?, ?, ?, ?, ?, ?, 0, ?)
		on conflict (path) do update set title = excluded.title, body = excluded.body, rev = excluded.rev,
		updated = excluded.updated, by = excluded.by, deleted = 0, kind = excluded.kind`,
		p, title, body, rev, created, now, by, kind); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "insert into revisions (path, rev, at, by, op, body) values (?, ?, ?, ?, ?, ?)",
		p, rev, now, by, string(op), body); err != nil {
		return nil, err
	}
	if err := setLinks(ctx, tx, p, body); err != nil {
		return nil, err
	}
	if taskID && s.tasks != nil {
		if err := s.tasks.IndexTask(ctx, tx, p, body); err != nil {
			return nil, err
		}
	}
	// tasks are found by their own query, not by the search index
	if !task && s.idx != nil {
		if err := s.idx.IndexPage(ctx, tx, p, title, body); err != nil {
			return nil, err
		}
	}
	err = tx.Commit()
	s.graphChanged()
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, p)
}

// Remove deletes a page as a revision: it leaves every list and search, its history stays.
func (s *Store) Remove(ctx context.Context, path, by string) error {
	p, err := CleanPath(path)
	if err != nil {
		return err
	}
	now := s.stamp()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rev int
	err = tx.QueryRowContext(ctx, "select rev from docs where path = ? and deleted = 0", p).Scan(&rev)
	if err == sql.ErrNoRows {
		return fmt.Errorf("no document %s", p)
	}
	if err != nil {
		return err
	}
	for _, st := range []struct {
		q    string
		args []any
	}{
		{"update docs set deleted = 1, rev = ?, updated = ?, by = ? where path = ?", []any{rev + 1, now, by, p}},
		{"insert into revisions (path, rev, at, by, op, body) values (?, ?, ?, ?, 'delete', '')", []any{p, rev + 1, now, by}},
		{"delete from links where src = ?", []any{p}},
	} {
		if _, err := tx.ExecContext(ctx, st.q, st.args...); err != nil {
			return err
		}
	}
	if (!strings.HasPrefix(p, "tasks/") || isLogPath(p)) && s.idx != nil {
		if err := s.idx.RemovePage(ctx, tx, p); err != nil {
			return err
		}
	}
	if taskIDRe.MatchString(p) && s.tasks != nil {
		if err := s.tasks.DropTask(ctx, tx, p); err != nil {
			return err
		}
	}
	err = tx.Commit()
	s.graphChanged()
	return err
}

// History returns a page's revisions, newest first.
func (s *Store) History(ctx context.Context, path string) ([]HistoryEntry, error) {
	p, err := CleanPath(path)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "select path, rev, at, by, op from revisions where path = ? order by rev desc", p)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryEntry
	for rows.Next() {
		var h HistoryEntry
		if err := rows.Scan(&h.Path, &h.Rev, &h.At, &h.By, &h.Op); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Revision returns one version of a page, nil when there is none.
func (s *Store) Revision(ctx context.Context, path string, rev int) (*Revision, error) {
	p, err := CleanPath(path)
	if err != nil {
		return nil, err
	}
	var r Revision
	err = s.db.QueryRowContext(ctx, "select path, rev, at, by, op, body from revisions where path = ? and rev = ?", p, rev).
		Scan(&r.Path, &r.Rev, &r.At, &r.By, &r.Op, &r.Body)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &r, err
}

// Restore puts an older version back, as a new revision on top.
func (s *Store) Restore(ctx context.Context, path string, rev int, by string) (*Doc, error) {
	r, err := s.Revision(ctx, path, rev)
	if err != nil {
		return nil, err
	}
	if r == nil || r.Op == OpDelete {
		return nil, fmt.Errorf("no version %d of %s to restore", rev, path)
	}
	return s.write(ctx, path, r.Body, by, -1, OpRestore)
}

// Resolver returns a resolver over the live pages, from one query: build it once per request, not per link. It is
// kept until a write changes the pages (the version is read before the query, so a racing write leaves it stale).
func (s *Store) Resolver(ctx context.Context) (Resolver, error) {
	ver := s.graphStamp(ctx)
	s.graphMu.Lock()
	hit := s.resolverHit
	s.graphMu.Unlock()
	if hit.fn != nil && hit.ver == ver {
		return hit.fn, nil
	}
	fn, err := s.resolver(ctx)
	if err != nil {
		return nil, err
	}
	s.graphMu.Lock()
	s.resolverHit.ver, s.resolverHit.fn = ver, fn
	s.graphMu.Unlock()
	return fn, nil
}

func (s *Store) resolver(ctx context.Context) (Resolver, error) {
	rows, err := s.db.QueryContext(ctx, "select path from docs where deleted = 0")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return MakeResolver(paths), rows.Err()
}

// Link is an outgoing link: its target as written, the path it resolves to ("" if none) and its relation ("" if plain).
type Link struct {
	Target string
	Path   string
	Rel    string
}

// Related is a page that names its relation to another.
type Related struct {
	From string
	Rel  string
}

// Links are a page's links both ways.
type Links struct {
	Out []Link
	// Back are the pages that link here, sorted.
	Back    []string
	Related []Related
}

// Links returns a page's links both ways, resolved to paths where they exist. A nil resolver is built from the
// database.
func (s *Store) Links(ctx context.Context, path string, resolve Resolver) (Links, error) {
	var res Links
	p, err := CleanPath(path)
	if err != nil {
		return res, err
	}
	if resolve == nil {
		if resolve, err = s.Resolver(ctx); err != nil {
			return res, err
		}
	}
	rows, err := s.db.QueryContext(ctx, "select dst, rel from links where src = ? order by rel = '', rel, dst", p)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.Target, &l.Rel); err != nil {
			rows.Close()
			return res, err
		}
		l.Path = resolve(l.Target)
		res.Out = append(res.Out, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	// a link reaches this page when it names it (by path or by name) and resolves to it
	name := strings.ToLower(lastSegment(strings.TrimSuffix(p, ".md")))
	cands, err := s.db.QueryContext(ctx,
		`select src, dst, rel from links where lower(dst) = ? or lower(dst) like ? escape '\'`,
		name, "%/"+likeEscaper.Replace(name))
	if err != nil {
		return res, err
	}
	defer cands.Close()
	back := map[string]bool{}
	for cands.Next() {
		var src, dst, rel string
		if err := cands.Scan(&src, &dst, &rel); err != nil {
			return res, err
		}
		if src == p || resolve(dst) != p {
			continue
		}
		back[src] = true
		if rel != "" {
			res.Related = append(res.Related, Related{From: src, Rel: rel})
		}
	}
	for src := range back {
		res.Back = append(res.Back, src)
	}
	sort.Strings(res.Back)
	return res, cands.Err()
}

// setLinks replaces a page's edges in `links`: its relations with their name, its other links with none.
func setLinks(ctx context.Context, tx *sql.Tx, path, body string) error {
	if _, err := tx.ExecContext(ctx, "delete from links where src = ?", path); err != nil {
		return err
	}
	for _, e := range EdgesOf(body) {
		if _, err := tx.ExecContext(ctx, "insert or ignore into links (src, dst, rel) values (?, ?, ?)",
			path, e.Target, e.Rel); err != nil {
			return err
		}
	}
	return nil
}
