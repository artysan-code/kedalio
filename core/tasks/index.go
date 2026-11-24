package tasks

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"kedalio/core/db"
)

// The tasks' derived index: one row per task and one per relation, rebuilt on every write of a task page (the
// memory.TaskIndexer hook), so filters and views do not parse hundreds of pages. Derived: dropping the tables loses
// nothing, Reindex rebuilds them. Not a second truth: the page is.

// Migrations are the tasks' tables, one step each, only ever appended to.
var Migrations = []string{`
create table task_index (
  id text primary key, title text not null, status text not null, grp text not null,
  due text, time text, priority integer, ref text, stage text, labels text,
  project text, parent text,
  for_ text, assigned_to text, worked_by text, waiting_on text,
  created text, updated text, done text);
create index task_index_grp on task_index (grp, due);
create index task_index_project on task_index (project);
create table task_rels (
  id text not null, rel text not null, kind text not null, target text not null, key text not null,
  primary key (id, rel, kind, key));
create index task_rels_key on task_rels (rel, key);
create table task_settings (k text primary key, v text not null);`}

// Migrate brings the tasks' tables up to date.
func Migrate(ctx context.Context, sqldb *sql.DB) error {
	return db.Migrate(ctx, sqldb, "tasks", Migrations)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Index keeps the tables in step with the task pages. Me is the account's id, by whose lens the old `owner` is read.
type Index struct{ Me string }

// roleKey is the value a role is indexed by: a person by name, anything else as `kind:target`.
func roleKey(r *Rel) any {
	if r == nil {
		return nil
	}
	switch r.Kind {
	case KindPage, KindTask, KindDate, KindName:
		return relKey(*r)
	}
	return string(r.Kind) + ":" + relKey(*r)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// IndexTask puts a task page in the index, or takes it out when the body is no task. A file still in the old shape is
// indexed by what it says (its fields read as relations); its edges in `links` come when it is rewritten.
func (x Index) IndexTask(ctx context.Context, tx *sql.Tx, path, body string) error {
	return x.index(ctx, tx, path, body)
}

func (x Index) index(ctx context.Context, tx execer, path, body string) error {
	id := TaskIDOf(path)
	if id == "" {
		return nil
	}
	if err := dropTask(ctx, tx, id); err != nil {
		return err
	}
	t := FromFile(body, x.Me)
	if t == nil {
		return nil
	}
	var labels any
	if len(t.Labels) > 0 {
		labels = "," + strings.Join(t.Labels, ",") + ","
	}
	var priority any
	if t.Priority != 0 {
		priority = t.Priority
	}
	if _, err := tx.ExecContext(ctx,
		`insert into task_index (id, title, status, grp, due, time, priority, ref, stage, labels, project, parent,
		   for_, assigned_to, worked_by, waiting_on, created, updated, done)
		 values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Title, string(t.Status), string(GroupOf(t.Status)), nullable(t.Due), nullable(t.Time), priority,
		nullable(t.Ref), nullable(t.Stage), labels, nullable(t.Project), nullable(t.Parent),
		roleKey(ForOf(*t)), roleKey(AssignedOf(*t)), roleKey(WorkedByOf(*t)), roleKey(WaitingOnOf(*t)),
		nullable(t.Created), nullable(t.Updated), nullable(t.Done)); err != nil {
		return err
	}
	for _, r := range RelsOf(*t) {
		if _, err := tx.ExecContext(ctx,
			"insert or ignore into task_rels (id, rel, kind, target, key) values (?, ?, ?, ?, ?)",
			t.ID, r.Rel, string(r.Kind), r.Target, relKey(r)); err != nil {
			return err
		}
	}
	return nil
}

// DropTask takes a task page out of the index.
func (x Index) DropTask(ctx context.Context, tx *sql.Tx, path string) error {
	if id := TaskIDOf(path); id != "" {
		return dropTask(ctx, tx, id)
	}
	return nil
}

func dropTask(ctx context.Context, tx execer, id string) error {
	if _, err := tx.ExecContext(ctx, "delete from task_index where id = ?", id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "delete from task_rels where id = ?", id)
	return err
}

// taskPages is the range on the primary key (tasks/t-…) that holds the task pages, a task's log beside them left out.
const taskPages = "path >= 'tasks/t-' and path < 'tasks/t.' and path not like 'tasks/%/%' and deleted = 0"

// Reindex gives the task pages their kind and rebuilds the index from them: the way back if the tables are ever wrong,
// and what follows a write that does not go through the store (a restored copy, a page written straight into the database). It
// returns how many tasks it indexed.
func (x Index) Reindex(ctx context.Context, sqldb *sql.DB) (int, error) {
	tx, err := sqldb.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "update docs set kind = 'task' where "+taskPages); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "delete from task_index"); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "delete from task_rels"); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, "select path, body from docs where "+taskPages)
	if err != nil {
		return 0, err
	}
	type page struct{ path, body string }
	var pages []page
	for rows.Next() {
		var p page
		if err := rows.Scan(&p.path, &p.body); err != nil {
			rows.Close()
			return 0, err
		}
		pages = append(pages, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, p := range pages {
		if err := x.index(ctx, tx, p.path, p.body); err != nil {
			return 0, err
		}
	}
	return len(pages), tx.Commit()
}

// ---------------------------------------------------------------- queries

// Filter narrows the tasks Query finds. Empty fields do not filter.
type Filter struct {
	// Me is the account's id: a task with no `for` is for it, one with no `assigned_to` is its own.
	Me     string
	Status []Status
	Group  []Group
	// Project is a project page, and the folders under it.
	Project string
	From    string
	To      string
	Ref     string
	Stage   string
	Label   string
	Parent  string
	// For, AssignedTo are a person, by name.
	For        string
	AssignedTo string
	// WorkedBy is `run:r-…`, `engine:claude`, `agent:name`, or `any`.
	WorkedBy string
	// WaitingOn is a person, `run:…`, a day, a task id, or `any`.
	WaitingOn string
	// About, Related are a page, by name.
	About   string
	Related string
	// BlockedBy lists the tasks that wait for this task id (derived: `blocks` is the other side of `blocked_by`).
	BlockedBy string
	// Blocks lists the tasks this task id waits for.
	Blocks string
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// Query is the ids of the tasks that match, by day then time. Everything but the words in the notes is answered here.
func Query(ctx context.Context, sqldb *sql.DB, f Filter) ([]string, error) {
	var w []string
	var a []any
	eq := func(cond string, v ...any) { w = append(w, cond); a = append(a, v...) }
	rel := func(rel, key string, other bool) {
		if !other {
			eq("exists (select 1 from task_rels r where r.id = t.id and r.rel = ? and r.key = ?)", rel, strings.ToLower(key))
			return
		}
		eq("exists (select 1 from task_rels r where r.key = lower(t.id) and r.rel = ? and r.id = ?)", rel, key)
	}
	if len(f.Status) > 0 {
		eq("t.status in ("+marks(len(f.Status))+")", toAny(f.Status)...)
	}
	if len(f.Group) > 0 {
		eq("t.grp in ("+marks(len(f.Group))+")", toAny(f.Group)...)
	}
	if f.Project != "" {
		eq(`(t.project = ? or t.project like ? escape '\')`, f.Project, likeEscape(f.Project)+"/%")
	}
	if f.From != "" {
		eq("coalesce(t.due, '') >= ?", f.From)
	}
	if f.To != "" {
		eq("(t.due is not null and t.due <= ?)", f.To)
	}
	if f.Ref != "" {
		eq("lower(t.ref) = ?", strings.ToLower(f.Ref))
	}
	if f.Stage != "" {
		eq("t.stage = ?", strings.ToLower(f.Stage))
	}
	if f.Label != "" {
		eq(`t.labels like ? escape '\'`, "%,"+likeEscape(strings.ToLower(f.Label))+",%")
	}
	if f.Parent != "" {
		eq("t.parent = ?", f.Parent)
	}
	me := strings.ToLower(f.Me)
	if f.For != "" {
		eq("coalesce(t.for_, ?) = ?", me, strings.ToLower(f.For))
	}
	if f.AssignedTo != "" {
		eq("coalesce(t.assigned_to, ?) = ?", me, strings.ToLower(f.AssignedTo))
	}
	if f.WorkedBy == "any" {
		eq("t.worked_by is not null")
	} else if f.WorkedBy != "" {
		eq("t.worked_by = ?", strings.ToLower(f.WorkedBy))
	}
	if f.WaitingOn == "any" {
		eq("t.waiting_on is not null")
	} else if f.WaitingOn != "" {
		eq("t.waiting_on = ?", strings.ToLower(f.WaitingOn))
	}
	if f.About != "" {
		rel("about", f.About, false)
	}
	if f.Related != "" {
		rel("related", f.Related, false)
	}
	if f.BlockedBy != "" {
		rel("blocked_by", f.BlockedBy, false)
	}
	if f.Blocks != "" {
		rel("blocked_by", f.Blocks, true)
	}
	where := ""
	if len(w) > 0 {
		where = "where " + strings.Join(w, " and ")
	}
	return ids(ctx, sqldb, "select t.id from task_index t "+where+
		" order by coalesce(t.due, '9999'), coalesce(t.time, '99'), t.id", a...)
}

func toAny[T ~string](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	return out
}

func ids(ctx context.Context, sqldb *sql.DB, q string, args ...any) ([]string, error) {
	rows, err := sqldb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// BlockedIDs are the ids of the open tasks that wait for another still open (derived from `blocked_by`).
func BlockedIDs(ctx context.Context, sqldb *sql.DB) (map[string]bool, error) {
	list, err := ids(ctx, sqldb, `select distinct r.id from task_rels r join task_index b on lower(b.id) = r.key
		where r.rel = 'blocked_by' and r.kind = 'task' and b.grp != 'closed'`)
	set := map[string]bool{}
	for _, id := range list {
		set[id] = true
	}
	return set, err
}

// BlocksOf are the tasks that wait for this one: `blocks`, derived from the others' `blocked_by`.
func BlocksOf(ctx context.Context, sqldb *sql.DB, id string) ([]string, error) {
	return ids(ctx, sqldb, "select id from task_rels where rel = 'blocked_by' and kind = 'task' and key = ? order by id",
		strings.ToLower(id))
}

// GroupsByPath is each task's group by page path ("active", "closed"…), for the ranking of recall: the signature of
// memory.RecallOptions.TaskGroups, over a database.
func GroupsByPath(ctx context.Context, sqldb *sql.DB) (map[string]string, error) {
	rows, err := sqldb.QueryContext(ctx, "select id, grp from task_index")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, grp string
		if err := rows.Scan(&id, &grp); err != nil {
			return nil, err
		}
		out["tasks/"+id+".md"] = grp
		// the log of a closed task is as much history as the task; an open task's log stays out, since the core seeds
		// the "active" paths of this map as work in hand
		if grp == "closed" {
			out[LogPath(id)] = grp
		}
	}
	return out, rows.Err()
}

// The thresholds of stalled.go live beside the index, per account.

func setting(ctx context.Context, sqldb *sql.DB, k string) (string, error) {
	var v string
	err := sqldb.QueryRowContext(ctx, "select v from task_settings where k = ?", k).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func putSetting(ctx context.Context, sqldb *sql.DB, k string, v int) error {
	_, err := sqldb.ExecContext(ctx,
		"insert into task_settings (k, v) values (?, ?) on conflict (k) do update set v = excluded.v", k, fmt.Sprint(v))
	return err
}
