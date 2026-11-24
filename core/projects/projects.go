// Package projects keeps what the core knows about a project besides its card page: the places that make a folder or a
// repository part of it, the folders the owner said are no project, and the settings a project (or a path above it) has.
// A project itself is a page in the memory (core/mcp/card.go); this is the data a session start needs to find it from
// the folder it runs in (a session does not say which computer it is on, so a place belongs to the instance).
package projects

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Migrate makes the tables.
func Migrate(ctx context.Context, d *sql.DB) error {
	_, err := d.ExecContext(ctx, `
create table if not exists project_places (
  kind text not null check (kind in ('folder', 'repo')),
  value text not null,
  path text not null,
  branch text not null default '',
  created text not null,
  primary key (kind, value));
create index if not exists project_places_path on project_places (path);
create table if not exists folder_ignores (dir text primary key, created text not null);
create table if not exists project_settings (
  path text not null, name text not null, value text not null, primary key (path, name));`)
	return err
}

// Store is the projects' data.
type Store struct {
	DB  *sql.DB
	Now func() time.Time
}

// New makes the store.
func New(d *sql.DB) *Store { return &Store{DB: d, Now: time.Now} }

// Name is what a project path looks like: lower case segments of letters, digits, dots, dashes and underscores.
var Name = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*)*$`)

// CleanPath is a project path as it is compared: one kind of separator, none at the ends, lower case.
func CleanPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	return strings.ToLower(strings.Trim(p, "/"))
}

// SameDir is a folder as places compare it: one kind of separator, none at the end.
func SameDir(d string) string {
	d = strings.ReplaceAll(strings.TrimSpace(d), "\\", "/")
	for len(d) > 1 && strings.HasSuffix(d, "/") {
		d = d[:len(d)-1]
	}
	return d
}

var (
	scpRemote = regexp.MustCompile(`^[\w.-]+@([\w.-]+):([^/].*)$`)
	scheme    = regexp.MustCompile(`(?i)^[a-z+]+://`)
	userinfo  = regexp.MustCompile(`^[^@/]*@`)
	port      = regexp.MustCompile(`^([^/:]+):\d+(/)`)
	hostPath  = regexp.MustCompile(`^[a-z0-9.-]+(:\d+)?/[^\s/][^\s]*$`)
)

// SameRemote is a repository address as one string to compare, whatever its form (`git@host:o/r.git`, `https://host/o/r`).
func SameRemote(url string) string {
	u := strings.TrimSpace(url)
	u = strings.TrimRight(u, "/")
	u = strings.TrimSuffix(u, ".git")
	if m := scpRemote.FindStringSubmatch(u); m != nil {
		return strings.ToLower(m[1] + "/" + m[2])
	}
	u = scheme.ReplaceAllString(u, "")
	u = userinfo.ReplaceAllString(u, "")
	u = port.ReplaceAllString(u, "$1$2")
	return strings.ToLower(u)
}

// RepoAddress is an https address for a remote as a git client reports it, or "" when it does not look like one.
func RepoAddress(remote string) string {
	n := SameRemote(remote)
	if n == "" || !hostPath.MatchString(n) {
		return ""
	}
	return "https://" + n
}

// The kinds of a place.
const (
	KindFolder = "folder"
	KindRepo   = "repo"
)

// Place is a folder or a repository that belongs to a project.
type Place struct {
	Kind   string `json:"kind"` // KindFolder or KindRepo
	Value  string `json:"value"`
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
}

// Places lists every place, by project.
func (s *Store) Places(ctx context.Context) ([]Place, error) {
	rows, err := s.DB.QueryContext(ctx, "select kind, value, path, branch from project_places order by path, kind, value")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Place
	for rows.Next() {
		var p Place
		if err := rows.Scan(&p.Kind, &p.Value, &p.Path, &p.Branch); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Known is every project path that has a place.
func (s *Store) Known(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, "select distinct path from project_places order by path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Resolve is the project of a session: the deepest folder place that holds its folder, else the project that has the
// repository of its remote; "" when none does.
func (s *Store) Resolve(ctx context.Context, dir, remote string) (string, error) {
	ps, err := s.Places(ctx)
	if err != nil {
		return "", err
	}
	if dir != "" {
		if p, ok := folderHolding(ps, dir); ok {
			return p.Path, nil
		}
	}
	if remote != "" {
		want := SameRemote(remote)
		for _, p := range ps {
			if p.Kind == KindRepo && SameRemote(p.Value) == want {
				return p.Path, nil
			}
		}
	}
	return "", nil
}

// folderHolding is the deepest folder place that is dir or holds it.
func folderHolding(ps []Place, dir string) (Place, bool) {
	d := SameDir(dir)
	var best Place
	found := false
	for _, p := range ps {
		if p.Kind != KindFolder {
			continue
		}
		base := SameDir(p.Value)
		in := d == base || strings.HasPrefix(d, strings.TrimRight(base, "/")+"/")
		if in && (!found || len(base) > len(best.Value)) {
			best, found = p, true
		}
	}
	return best, found
}

// ClaimedBy is the project that has the folder as a place or holds it, "" when none does.
func (s *Store) ClaimedBy(ctx context.Context, dir string) (string, error) {
	ps, err := s.Places(ctx)
	if err != nil {
		return "", err
	}
	p, _ := folderHolding(ps, dir)
	return p.Path, nil
}

// RepoHolder is the project that has this repository, "" when none does.
func (s *Store) RepoHolder(ctx context.Context, remote string) (string, error) {
	ps, err := s.Places(ctx)
	if err != nil {
		return "", err
	}
	want := SameRemote(remote)
	for _, p := range ps {
		if p.Kind == KindRepo && SameRemote(p.Value) == want {
			return p.Path, nil
		}
	}
	return "", nil
}

// AddPlace makes a folder or a repository a place of the project; the same place again is not an error.
func (s *Store) AddPlace(ctx context.Context, project, kind, value, branch string) error {
	_, err := s.DB.ExecContext(ctx, "insert or ignore into project_places (kind, value, path, branch, created) values (?, ?, ?, ?, ?)",
		kind, value, project, branch, s.Now().UTC().Format(time.RFC3339))
	return err
}

// RepoOf is the repository place of a project and its base branch; empty when it has none.
func (s *Store) RepoOf(ctx context.Context, path string) (repo, branch string, err error) {
	err = s.DB.QueryRowContext(ctx, "select value, branch from project_places where kind = 'repo' and path = ? order by created, value limit 1",
		CleanPath(path)).Scan(&repo, &branch)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return repo, branch, err
}

// RefusedError is a project call the caller has to change (a repository another project has), as against a failure of
// the database, which is not the caller's to fix.
type RefusedError struct{ Msg string }

func (e *RefusedError) Error() string { return e.Msg }

// SetRepo makes addr the project's repository, keeping its base branch; it refuses an address another project has.
func (s *Store) SetRepo(ctx context.Context, path, addr string) error {
	path = CleanPath(path)
	holder, err := s.RepoHolder(ctx, addr)
	if err != nil {
		return err
	}
	if holder != "" && holder != path {
		return &RefusedError{"that repository is a place of " + holder + " already"}
	}
	_, branch, err := s.RepoOf(ctx, path)
	if err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, "delete from project_places where kind = 'repo' and path = ?", path); err != nil {
		return err
	}
	return s.AddPlace(ctx, path, "repo", addr, branch)
}

// SetBranch sets the base branch of the project's repository; the project has to have one.
func (s *Store) SetBranch(ctx context.Context, path, branch string) error {
	r, err := s.DB.ExecContext(ctx, "update project_places set branch = ? where kind = 'repo' and path = ?", branch, CleanPath(path))
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return &RefusedError{"no repository for " + path + ": set its repo first"}
	}
	return nil
}

// Ignored reports whether the owner said that this folder is no project.
func (s *Store) Ignored(ctx context.Context, dir string) (bool, error) {
	var one int
	err := s.DB.QueryRowContext(ctx, "select 1 from folder_ignores where dir = ?", SameDir(dir)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Ignore keeps «no project» for a folder, so it is not asked about again.
func (s *Store) Ignore(ctx context.Context, dir string) error {
	_, err := s.DB.ExecContext(ctx, "insert or ignore into folder_ignores (dir, created) values (?, ?)",
		SameDir(dir), s.Now().UTC().Format(time.RFC3339))
	return err
}

// Setting is a project setting and where it comes from.
type Setting struct {
	Value  json.RawMessage `json:"value"`
	Origin string          `json:"origin"` // «own», a path above the project's, or «default»
}

// SettingOf is the setting of a path: its own, else the nearest path above it that has one; a missing setting is null
// with the origin «default».
func (s *Store) SettingOf(ctx context.Context, path, name string) (Setting, error) {
	path = CleanPath(path)
	for p := path; p != ""; {
		var raw string
		err := s.DB.QueryRowContext(ctx, "select value from project_settings where path = ? and name = ?", p, name).Scan(&raw)
		if err == nil {
			origin := "own"
			if p != path {
				origin = p
			}
			return Setting{Value: json.RawMessage(raw), Origin: origin}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Setting{}, err
		}
		i := strings.LastIndex(p, "/")
		if i < 0 {
			break
		}
		p = p[:i]
	}
	return Setting{Value: json.RawMessage("null"), Origin: "default"}, nil
}

// SetSetting keeps a setting on a path; null goes back to inheriting.
func (s *Store) SetSetting(ctx context.Context, path, name string, value json.RawMessage) error {
	path = CleanPath(path)
	if string(value) == "null" {
		_, err := s.DB.ExecContext(ctx, "delete from project_settings where path = ? and name = ?", path, name)
		return err
	}
	_, err := s.DB.ExecContext(ctx, "insert into project_settings (path, name, value) values (?, ?, ?) "+
		"on conflict (path, name) do update set value = excluded.value", path, name, string(value))
	return err
}

// Nearby is the projects near a path, for «another one»: its ancestors (nearest first), its siblings, its children, then
// whatever shares its first two segments; at most limit. The path itself is not in it.
func Nearby(path string, known []string, limit int) []string {
	parts := strings.Split(path, "/")
	group := strings.Join(parts[:len(parts)-1], "/")
	tier := func(k string) int {
		switch {
		case strings.HasPrefix(path, k+"/"):
			return 0
		case len(parts) > 1 && parentOf(k) == group:
			return 1
		case strings.HasPrefix(k, path+"/"):
			return 2
		case len(parts) > 2 && firstTwo(k) == firstTwo(path):
			return 3
		}
		return -1
	}
	type kt struct {
		k string
		t int
	}
	var ks []kt
	for _, k := range known {
		if k != path {
			if t := tier(k); t >= 0 {
				ks = append(ks, kt{k, t})
			}
		}
	}
	sort.SliceStable(ks, func(i, j int) bool {
		a, b := ks[i], ks[j]
		if a.t != b.t {
			return a.t < b.t
		}
		if a.t == 0 {
			return len(a.k) > len(b.k)
		}
		return a.k < b.k
	})
	var out []string
	for _, x := range ks {
		if len(out) == limit {
			break
		}
		out = append(out, x.k)
	}
	return out
}

func parentOf(k string) string {
	if i := strings.LastIndex(k, "/"); i > 0 {
		return k[:i]
	}
	return ""
}

func firstTwo(k string) string {
	p := strings.Split(k, "/")
	return strings.Join(p[:min(2, len(p))], "/")
}

// Roots are the folders a project path is filed under: a folder named after one starts the path (`~/work/acme/site`).
var Roots = []string{"work", "personal"}

var nonSegment = regexp.MustCompile(`[^a-z0-9._-]+`)

func segment(s string) string {
	s = nonSegment.ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(s, "-._")
}

// PathOf is a project path made of these segments, or "" when they do not make one.
func PathOf(parts ...string) string {
	var segs []string
	for _, p := range parts {
		if g := segment(p); g != "" {
			segs = append(segs, g)
		}
	}
	path := strings.Join(segs, "/")
	if path == "" || len(path) > 120 || !Name.MatchString(path) {
		return ""
	}
	return path
}

// Proposal is the project a folder may belong to.
type Proposal struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Why    string `json:"why"` // remote, path or name
}

// Propose is the project a folder may belong to, or nil when nothing in it says (no repository, and not under `work/`
// or `personal/`: a session in the home folder is not asked about). byRepo maps a repository (as SameRemote writes it)
// to the project that has it; known is every project path.
//
// Under a root the folder's own name is dropped from a path of four or more segments (a repository inside its
// project's folder: `work/acme/shop/shop-api` is `work/acme/shop`), else the path is the project; a project the memory
// already has at that depth or deeper wins over the guess.
func Propose(dir, remote string, known map[string]bool, byRepo map[string]string) *Proposal {
	rem := ""
	if remote != "" {
		rem = SameRemote(remote)
	}
	if held, ok := byRepo[rem]; rem != "" && ok {
		return &Proposal{held, known[held], "remote"}
	}
	var parts []string
	for _, p := range strings.Split(SameDir(dir), "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	root := -1
	for i, p := range parts {
		if p == Roots[0] || p == Roots[1] {
			root = i
			break
		}
	}
	if root >= 0 {
		tail := parts[root:]
		depth := len(tail)
		if depth >= 4 {
			depth--
		}
		for n := len(tail); n >= max(depth, 2); n-- {
			if path := PathOf(tail[:n]...); path != "" && known[path] {
				return &Proposal{path, true, "path"}
			}
		}
		if path := PathOf(tail[:depth]...); path != "" && len(tail) >= 2 {
			return &Proposal{path, known[path], "path"}
		}
		return nil
	}
	if rem == "" {
		return nil
	}
	name := PathOf(rem[strings.LastIndex(rem, "/")+1:])
	if name == "" {
		return nil
	}
	path := "personal/" + name
	return &Proposal{path, known[path], "name"}
}
