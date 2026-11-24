package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"kedalio/core/memory"
)

// MaxTitle: a task's title names the thing, like a commit's subject: what is to be done, not how or why, which go in
// the notes. Longer titles crowd the owner's board and stop reading as a name.
const MaxTitle = 80

// jsLen is a string's length in UTF-16 units, how the limits below were counted.
func jsLen(s string) int { return len(utf16.Encode([]rune(s))) }

func oneLine(s string, max int) bool { return !strings.Contains(s, "\n") && jsLen(s) <= max }

// words keeps each word once, in order, empty ones out, spaces collapsed.
func words(xs []string, lower bool) []string {
	out := []string{}
	for _, x := range xs {
		if lower {
			x = strings.ToLower(x)
		}
		x = strings.TrimSpace(spacesRe.ReplaceAllString(x, " "))
		if x != "" && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// relationInputs reports whether an input names a relation.
func relationInputs(in Input) bool {
	return in.About != nil || in.PartOf != nil || in.BlockedBy != nil || in.Related != nil || in.AssignedTo != nil ||
		in.WorkedBy != nil || in.WaitingOn != nil || in.For != nil || in.Project != nil || in.Parent != nil
}

// ApplyInput is an input checked and applied onto a task. The error says what is wrong. me is the account's id: the
// person a task is for and assigned to unless it says otherwise, so never written.
func ApplyInput(base Task, in Input, now time.Time, me string) (Task, error) {
	t := base
	if in.Title != "" {
		title := strings.TrimSpace(spacesRe.ReplaceAllString(in.Title, " "))
		if title == "" {
			return t, refusef("a task needs a title")
		}
		if n := jsLen(title); n > MaxTitle {
			return t, refusef("a task's title is %d characters, at most %d: name the thing in a few words "+
				"(what is to be done) and put the how, the why and the details in the notes", n, MaxTitle)
		}
		t.Title = title
	}
	if in.Status != "" {
		if !ValidStatus(in.Status) {
			return t, refusef("status is one of %s", joinStatuses())
		}
		t.Status = in.Status
		if in.Status == Done {
			t.Done = stamp(now)
		} else {
			t.Done = ""
		}
	}
	// text sets a string field; the clear and the check are the same for all of them
	text := func(dst *string, v *string, ok func(string) bool, what string) error {
		if v == nil {
			return nil
		}
		if *v != "" && !ok(*v) {
			return refusef("%s", what)
		}
		*dst = *v
		return nil
	}
	anything := func(string) bool { return true }
	trimmed := func(v *string) *string {
		if v == nil {
			return nil
		}
		return S(strings.TrimSpace(*v))
	}
	for _, c := range []struct {
		dst  *string
		v    *string
		ok   func(string) bool
		what string
	}{
		{&t.Due, in.Due, ValidDay, "due is a day, YYYY-MM-DD"},
		{&t.Time, in.Time, ValidTime, "time is HH:MM"},
		{&t.Repeat, trimmed(in.Repeat), anything, ""},
		{&t.RepeatFrom, in.RepeatFrom, func(s string) bool { return s == "due" || s == "done" }, `repeat_from is "due" or "done"`},
		{&t.Notes, in.Notes, anything, ""},
		{&t.Ref, trimmed(in.Ref), func(s string) bool { return oneLine(s, 40) },
			"ref is the project's name for the task, one line, at most 40 characters"},
		{&t.Detail, trimmed(in.Detail), func(s string) bool { return oneLine(s, 500) },
			"detail is one link or path, at most 500 characters"},
		{&t.Snooze, in.Snooze, ValidDay, "snooze is a day, YYYY-MM-DD"},
	} {
		if err := text(c.dst, c.v, c.ok, c.what); err != nil {
			return t, err
		}
	}
	if in.Remind != nil {
		switch r := *in.Remind; {
		case r < 0:
			t.Remind = nil
		case r > 1440:
			return t, refusef("remind is minutes, 0 to 1440")
		default:
			t.Remind = &r
		}
	}
	if in.Priority != nil {
		if p := *in.Priority; p < 0 || p > 3 {
			return t, refusef("priority is 1 (high), 2 or 3")
		} else {
			t.Priority = p
		}
	}
	if in.Stage != nil {
		s := strings.TrimSpace(*in.Stage)
		if s != "" && !oneLine(s, 40) {
			return t, refusef("stage is a word or two, at most 40 characters")
		}
		t.Stage = strings.ToLower(spacesRe.ReplaceAllString(s, " "))
	}
	if in.Labels != nil {
		ls := words(*in.Labels, true)
		if len(ls) > 12 {
			return t, refusef("labels: at most 12, each at most 30 characters")
		}
		for _, l := range ls {
			if !oneLine(l, 30) {
				return t, refusef("labels: at most 12, each at most 30 characters")
			}
		}
		t.Labels = ls
		if len(ls) == 0 {
			t.Labels = nil
		}
	}
	if in.Snooze != nil && *in.Snooze == "" {
		t.SnoozedAt = ""
	}
	if relationInputs(in) {
		rels, err := EditRels(RelsOf(base), in, me, base.ID)
		if err != nil {
			return t, err
		}
		t.Rels = rels
	}
	// the old fields follow the relations; with none left Rels is an empty list while they are derived, never a gap
	// that would read them back from the fields about to be cleared
	if t.Rels == nil {
		t.Rels = RelsOf(t)
	}
	derive(&t)
	if len(t.Rels) == 0 {
		t.Rels = nil
	}
	if t.Time != "" && t.Due == "" {
		return t, refusef("a time needs a day: set due too")
	}
	if t.Repeat == "" {
		t.RepeatFrom = ""
	} else {
		if t.Due == "" {
			return t, refusef("a repeating task needs a first day: set due")
		}
		if _, err := RuleFor(t.Repeat, t.RepeatFrom, t.Due); err != nil {
			return t, refusef("repeat: %s", err)
		}
	}
	// `updated` is the version an editor sends back as `base`: two writes in the same millisecond must still differ
	at := now
	if prev, ok := parseStamp(base.Updated); ok && !prev.Add(time.Millisecond).Before(now) {
		at = prev.Add(time.Millisecond)
	}
	t.Updated = stamp(at)
	if in.Snooze != nil && *in.Snooze != "" {
		t.SnoozedAt = t.Updated
	}
	return t, nil
}

func joinStatuses() string {
	var s []string
	for _, x := range Statuses {
		s = append(s, string(x))
	}
	return strings.Join(s, ", ")
}

// StaleError is returned by Update when an editor read the task at a version that is not the current one.
type StaleError struct{ Current Task }

func (e *StaleError) Error() string { return "the task changed meanwhile: reload it" }

// peopleRels are the relations that name a person, whose page is found when a task is written.
var peopleRels = []string{"assigned_to", "for", "waiting_on"}

// meDefault are the relations whose value is the account itself unless said otherwise (or, for waiting_on, is
// nothing): not written.
var meDefault = []string{"for", "assigned_to", "waiting_on"}

// slugOf is a page's file name from a person's name: `Nina Petrova` → `nina-petrova`.
func slugOf(s string) string { return strings.ReplaceAll(memory.RelName(s), "_", "-") }

// UnresolvedPeople are the names a task keeps as plain text where a person is meant: no page of a person answered to
// them.
func UnresolvedPeople(t Task) []string {
	var out []string
	for _, r := range RelsOf(t) {
		if r.Kind == KindName && slices.Contains(peopleRels, r.Rel) && !slices.Contains(out, r.Target) {
			out = append(out, r.Target)
		}
	}
	return out
}

// Book is the tasks of one account, in its zone, written in the name of By (the caller's label).
type Book struct {
	Store *memory.Store
	DB    *sql.DB
	// Me is the account's id: who a new task belongs to unless it says otherwise.
	Me string
	// Zone is the account's time zone.
	Zone *time.Location
	By   string
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Diary writes the line the server puts in the diary when a task closes: its link and a sentence (not the agent's
	// to write), joined with the closures just before it into one line. Nil: none. The diary refusing it does not undo
	// the close.
	Diary func(ctx context.Context, c Closed, now time.Time) error

	// Every change reads, checks and writes under this lock: two requests to one account cannot interleave.
	mu sync.Mutex
}

// NewBook returns the tasks of an account over a store and the database it lives in, and makes the store keep the
// tasks' index in step with every task page it writes. Migrate must have run on db, after the memory's.
func NewBook(store *memory.Store, sqldb *sql.DB, me string, zone *time.Location, by string) *Book {
	store.SetTaskIndexer(Index{Me: me})
	if zone == nil {
		zone, _ = LoadZone("")
	}
	return &Book{Store: store, DB: sqldb, Me: me, Zone: zone, By: by}
}

type authorKey struct{}

// WithAuthor makes the writes of the calls made with ctx be recorded under by, not under the book's own By: how a
// request made with an agent's own token leaves its name on the tasks it saves.
func WithAuthor(ctx context.Context, by string) context.Context {
	return context.WithValue(ctx, authorKey{}, by)
}

// by is who a write is recorded under: the author of the request, else the book's.
func (b *Book) by(ctx context.Context) string {
	if a, ok := ctx.Value(authorKey{}).(string); ok && a != "" {
		return a
	}
	return b.By
}

func (b *Book) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Book) newID(now time.Time) string {
	var r [3]byte
	_, _ = rand.Read(r[:])
	return "t-" + strings.ReplaceAll(DayOf(now, b.Zone), "-", "") + "-" + hex.EncodeToString(r[:])
}

// projectResolver finds the page of each project a relation names, built once for a batch: a project's slug
// (`work/acme/site`) is the page `<projects area>/work/acme/site` when there is one (the account's projects live in
// their area), else a page of that exact path, else the only page under the projects area that ends with the slug.
// None found: the slug as it was. A page list that fails is an error, not "none found".
func (b *Book) projectResolver(ctx context.Context) func(target string) (string, error) {
	area := b.Store.Areas.Projects
	var paths []string
	loaded := false
	return func(target string) (string, error) {
		t := mdSuffix.ReplaceAllString(target, "")
		if strings.HasPrefix(t, area+"/") {
			return t, nil
		}
		if !loaded {
			infos, err := b.Store.List(ctx, area+"/", memory.ListOpts{Limit: 100000})
			if err != nil {
				return "", err
			}
			loaded = true
			for _, i := range infos {
				paths = append(paths, i.Path)
			}
		}
		if slices.Contains(paths, area+"/"+t+".md") {
			return area + "/" + t, nil
		}
		if d, err := b.Store.Get(ctx, t); err == nil && d != nil {
			return t, nil
		}
		var ends []string
		for _, p := range paths {
			if strings.HasSuffix(p, "/"+t+".md") {
				ends = append(ends, p)
			}
		}
		if len(ends) == 1 {
			return strings.TrimSuffix(ends[0], ".md"), nil
		}
		return t, nil
	}
}

// peopleResolver finds the pages a name can mean, built once per write and only when a name needs it: the people
// area, and any other page that says it is a person or an organization (`is:` in its front matter). Each with the
// names it answers to. A page list that fails is an error: a name must not turn into a plain `name:` relation
// because the list was unavailable for a moment.
func (b *Book) peopleResolver(ctx context.Context) (func(name string) string, error) {
	area := b.Store.Areas.People
	type cand struct {
		path, slug string
		aka        []string
		inArea     bool
	}
	var cands []cand
	infos, err := b.Store.List(ctx, "", memory.ListOpts{Limit: 100000})
	if err != nil {
		return nil, err
	}
	for _, d := range infos {
		inArea := strings.HasPrefix(d.Path, area+"/")
		if !inArea && d.Kind != memory.KindEntity {
			continue
		}
		doc, err := b.Store.Get(ctx, d.Path)
		if err != nil || doc == nil {
			continue
		}
		fm := memory.FrontMatter(doc.Body)
		if !inArea && fm["is"] != "person" && fm["is"] != "organization" {
			continue
		}
		path := strings.TrimSuffix(d.Path, ".md")
		c := cand{path: path, slug: lastPart(path), inArea: inArea}
		for _, a := range strings.Split(fm["aka"], ",") {
			if s := slugOf(a); s != "" {
				c.aka = append(c.aka, s)
			}
		}
		cands = append(cands, c)
	}
	// the people area first, then the shallowest, so a name two pages share reads the same way every time
	sort.SliceStable(cands, func(i, j int) bool {
		a, c := cands[i], cands[j]
		if a.inArea != c.inArea {
			return a.inArea
		}
		if da, dc := strings.Count(a.path, "/"), strings.Count(c.path, "/"); da != dc {
			return da < dc
		}
		return a.path < c.path
	})
	return func(name string) string {
		s := slugOf(name)
		if s == "" {
			return ""
		}
		for _, c := range cands {
			if c.slug == s {
				return c.path
			}
		}
		for _, c := range cands {
			if slices.Contains(c.aka, s) {
				return c.path
			}
		}
		// the first name (`anna` for `anna-cruz`) or a prefix, when only one page has it
		var pre []cand
		for _, c := range cands {
			if strings.HasPrefix(c.slug, s+"-") || (len(s) >= 3 && strings.HasPrefix(c.slug, s)) {
				pre = append(pre, c)
			}
		}
		if len(pre) == 1 {
			return pre[0].path
		}
		return ""
	}, nil
}

// write writes a task in the new shape, after its people are found: a name that is a person's page (by slug, first
// name, prefix or `aka`) points at it. A name that is none stays a plain name (kind `name`: a role, a company, someone
// without a page yet): no page is made for it, UnresolvedPeople lists it so the agent can make the page if it is a
// person. The account itself is the default for `for` and `assigned_to`, so it is not written. The notes are tidied
// (the steps in `## Steps`, once each) and the log leaves them for the task's episode, addLog after it. Returns the
// task as written.
func (b *Book) write(ctx context.Context, given Task, addLog []string) (Task, error) {
	var people func(string) string
	project := b.projectResolver(ctx)
	rels := []Rel{}
	for _, r := range RelsOf(given) {
		if r.Kind == KindPage && slices.Contains(meDefault, r.Rel) && isMe(r.Target, b.Me) {
			continue
		}
		switch {
		case r.Rel == "part_of" && r.Kind == KindPage:
			target, err := project(r.Target)
			if err != nil {
				return given, err
			}
			r.Target = target
		case slices.Contains(peopleRels, r.Rel) && (r.Kind == KindPage || r.Kind == KindName) &&
			!(r.Kind == KindPage && (strings.Contains(r.Target, "/") || isMe(r.Target, b.Me))):
			if people == nil {
				var err error
				if people, err = b.peopleResolver(ctx); err != nil {
					return given, err
				}
			}
			if found := people(r.Target); found != "" {
				r.Kind, r.Target = KindPage, found
			} else {
				slug := slugOf(r.Target)
				if slug == "" {
					slug = "unknown"
				}
				r.Kind, r.Target = KindName, slug
			}
		}
		rels = append(rels, r)
	}
	t := given
	t.Rels = rels
	tidied, moved := Tidy(given.Notes)
	t.Notes = ""
	if strings.TrimSpace(tidied) != "" {
		t.Notes = tidied
	}
	if err := b.moveLog(ctx, t, moved, addLog); err != nil {
		return t, err
	}
	derive(&t)
	if len(rels) == 0 {
		t.Rels = nil
	}
	if _, err := b.Store.Write(ctx, "tasks/"+t.ID+".md", ToFile(t), b.by(ctx)); err != nil {
		return t, err
	}
	return t, nil
}

// moveLog puts the log lines that left the notes, and the new ones, onto the task's episode; a line it has already is
// not added again, so a move that is done twice adds nothing.
func (b *Book) moveLog(ctx context.Context, t Task, moved, added []string) error {
	if len(moved) == 0 && len(added) == 0 {
		return nil
	}
	doc, err := b.Store.Get(ctx, LogPath(t.ID))
	if err != nil {
		return err
	}
	body := ""
	if doc != nil {
		body = doc.Body
	}
	have := logLines(body)
	lines := append(joinLines(have, moved), added...)
	// a summary written above the entries stays there
	if len(lines) == len(have) {
		return nil
	}
	page, err := logBody(t.ID, t.Title, lines, summaryOf(body))
	if err != nil {
		return err
	}
	_, err = b.Store.Write(ctx, LogPath(t.ID), page, b.by(ctx))
	return err
}

// LogView is a task's log.
type LogView struct {
	// Path is the page it lives on.
	Path  string `json:"path"`
	Lines []Note `json:"lines"`
}

// LogOf is a task's log, oldest first, and the page it lives on; the lines a task not yet moved still has in its
// notes come with it.
func (b *Book) LogOf(ctx context.Context, t Task) (LogView, error) {
	doc, err := b.Store.Get(ctx, LogPath(t.ID))
	if err != nil {
		return LogView{}, err
	}
	body := ""
	if doc != nil {
		body = doc.Body
	}
	_, moved := Tidy(t.Notes)
	v := LogView{Path: LogPath(t.ID), Lines: []Note{}}
	for _, l := range joinLines(logLines(body), moved) {
		v.Lines = append(v.Lines, lineOf(l))
	}
	return v, nil
}

// Log adds a dated line to the task's log (its episode), the task touched so its `updated` says it was worked on.
func (b *Book) Log(ctx context.Context, id, text, day string) (Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.log(ctx, id, text, day)
}

func (b *Book) log(ctx context.Context, id, text, day string) (Task, error) {
	t := strings.TrimSpace(lineBreakRe.ReplaceAllString(strings.TrimSpace(text), " "))
	if t == "" {
		return Task{}, refusef("a note needs some text")
	}
	cur, err := b.get(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if cur == nil {
		return Task{}, refusef("no task %s", id)
	}
	applied, err := ApplyInput(*cur, Input{}, b.now(), b.Me)
	if err != nil {
		return Task{}, err
	}
	return b.write(ctx, applied, []string{day + " · " + t})
}

// taskRows are the task pages' paths and bodies.
func (b *Book) taskRows(ctx context.Context) ([]struct{ Path, Body string }, error) {
	rows, err := b.DB.QueryContext(ctx, "select path, body from docs where "+taskPages+" order by path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct{ Path, Body string }
	for rows.Next() {
		var r struct{ Path, Body string }
		if err := rows.Scan(&r.Path, &r.Body); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// List is every task.
func (b *Book) List(ctx context.Context) ([]Task, error) {
	rows, err := b.taskRows(ctx)
	if err != nil {
		return nil, err
	}
	out := []Task{}
	for _, r := range rows {
		if t := FromFile(r.Body, b.Me); t != nil {
			out = append(out, *t)
		}
	}
	return out, nil
}

// Get is a task by id, nil when there is none.
func (b *Book) Get(ctx context.Context, id string) (*Task, error) { return b.get(ctx, id) }

func (b *Book) get(ctx context.Context, id string) (*Task, error) {
	if !IsID(id) {
		return nil, nil
	}
	doc, err := b.Store.Get(ctx, "tasks/"+id+".md")
	if err != nil || doc == nil {
		return nil, err
	}
	return FromFile(doc.Body, b.Me), nil
}

// Find is the task a key names (id, #id or ref), read fresh; project says where to look for a ref.
func (b *Book) Find(ctx context.Context, key, project string) (Task, error) {
	id := strings.TrimSpace(key)
	if strings.HasPrefix(id, "#t-") {
		id = id[1:]
	}
	if IsID(id) {
		t, err := b.get(ctx, id)
		if err != nil {
			return Task{}, err
		}
		if t == nil {
			return Task{}, refusef("no task %s", id)
		}
		return *t, nil
	}
	all, err := b.List(ctx)
	if err != nil {
		return Task{}, err
	}
	return ResolveTask(all, id, project)
}

// Query is the tasks the index finds for a filter, by day and time: no page is parsed but the answers'.
func (b *Book) Query(ctx context.Context, f Filter) ([]Task, error) {
	f.Me = b.Me
	ids, err := Query(ctx, b.DB, f)
	if err != nil {
		return nil, err
	}
	out := []Task{}
	for _, id := range ids {
		if t, err := b.get(ctx, id); err != nil {
			return nil, err
		} else if t != nil {
			out = append(out, *t)
		}
	}
	return out, nil
}

// Add adds a task.
func (b *Book) Add(ctx context.Context, in Input) (Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.add(ctx, in)
}

func (b *Book) add(ctx context.Context, in Input) (Task, error) {
	now := b.now()
	base := Task{ID: b.newID(now), Status: Todo, Created: stamp(now), Updated: stamp(now)}
	t, err := ApplyInput(base, in, now, b.Me)
	if err != nil {
		return Task{}, err
	}
	if t.Title == "" {
		return Task{}, refusef("a task needs a title")
	}
	return b.write(ctx, t, nil)
}

// Result is a task after an update.
type Result struct {
	Task Task
	// Rearmed is true when completing a repeating task moved it to its next day instead of closing it.
	Rearmed bool
}

// Update changes a task. Completing a repeating one does not close it: it is re-armed (same id, `due` moved to the next
// day of its rule, steps unticked, `blocked_by` cleared, a line in its log) and Rearmed is true; past its UNTIL it closes
// for good. An editor that read the task at base (its `updated`) does not overwrite a change made since: *StaleError.
func (b *Book) Update(ctx context.Context, id string, in Input, base string) (Result, error) {
	return b.UpdateWith(ctx, id, func(Task) (Input, error) { return in, nil }, base)
}

// UpdateWith is Update with the change a function of the task as it is now: the way to edit its notes.
func (b *Book) UpdateWith(ctx context.Context, id string, change func(cur Task) (Input, error), base string) (Result, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.update(ctx, id, change, base)
}

func (b *Book) update(ctx context.Context, id string, change func(Task) (Input, error), base string) (Result, error) {
	cur, err := b.get(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if cur == nil {
		return Result{}, refusef("no task %s", id)
	}
	if base != "" && cur.Updated != base {
		return Result{}, &StaleError{*cur}
	}
	in, err := change(*cur)
	if err != nil {
		return Result{}, err
	}
	now := b.now()
	applied, err := ApplyInput(*cur, in, now, b.Me)
	if err != nil {
		return Result{}, err
	}
	closing := in.Status.Closed() && !cur.Status.Closed()
	// a task is done when its steps are, never because the last one was ticked; dropping it is another matter
	if in.Status == Done && closing {
		tidied, _ := Tidy(applied.Notes)
		var open []Step
		for _, s := range Steps(tidied, b.closedFn(ctx)) {
			if !s.Done {
				open = append(open, s)
			}
		}
		if len(open) > 0 {
			var first []string
			for _, s := range open[:min(3, len(open))] {
				first = append(first, s.Text)
			}
			more, verb := "", "steps are"
			if len(open) > 3 {
				more = "; …"
			}
			if len(open) == 1 {
				verb = "step is"
			}
			return Result{}, refusef("%d %s still open (%s%s): tick them, or drop the task",
				len(open), verb, strings.Join(first, "; "), more)
		}
	}
	day := DayOf(now, b.Zone)
	if in.Status == Done && closing {
		if again := b.rearm(applied, day); again != nil {
			t, err := b.write(ctx, *again, []string{day + " · done on " + day})
			if err != nil {
				return Result{}, err
			}
			closed := t
			closed.Status = Done
			b.diaryClosed(ctx, closed, now)
			return Result{Task: t, Rearmed: true}, nil
		}
	}
	t, err := b.write(ctx, applied, nil)
	if err != nil {
		return Result{}, err
	}
	if closing {
		b.diaryClosed(ctx, t, now)
	}
	return Result{Task: t}, nil
}

// rearm is the task as it starts its next round: nil when it does not repeat or its rule has run out (UNTIL). A legacy
// word is stored as the rule it meant, so the old `monthly` on the 31st keeps its last-day behaviour.
func (b *Book) rearm(done Task, day string) *Task {
	if done.Repeat == "" || done.Due == "" {
		return nil
	}
	r, err := RuleFor(done.Repeat, done.RepeatFrom, done.Due)
	if err != nil {
		return nil
	}
	due, ok, err := NextDate(r, done.Due, day)
	if err != nil || !ok {
		return nil
	}
	next := done
	next.Status, next.Due, next.Repeat, next.RepeatFrom = Todo, due, FormatRule(r), r.From
	// a run that did the last round is not doing this one; what blocked it does not block the next
	if done.Rels != nil {
		next.Rels = []Rel{}
		for _, x := range done.Rels {
			if !(x.Rel == "worked_by" && x.Kind == KindRun) && x.Rel != "blocked_by" {
				next.Rels = append(next.Rels, x)
			}
		}
	}
	next.Done, next.Snooze, next.SnoozedAt = "", "", ""
	if done.Notes != "" {
		next.Notes = UntickSteps(done.Notes)
	}
	// `owner` and the other old fields follow the relations
	return derive(&next)
}

// closedFn says whether a task is done or dropped (a promoted step counts as done by it).
func (b *Book) closedFn(ctx context.Context) func(id string) bool {
	return func(id string) bool {
		t, err := b.get(ctx, id)
		return err == nil && t != nil && t.Status.Closed()
	}
}

// Closed is a task that has just closed, as the diary hears of it.
type Closed struct {
	ID, Title string
	// Done is false for a task dropped.
	Done bool
}

// diaryClosed tells the diary a task closed. The diary refusing it must not undo the close.
func (b *Book) diaryClosed(ctx context.Context, t Task, now time.Time) {
	if b.Diary == nil {
		return
	}
	// a diary that refuses the line leaves the task closed all the same
	_ = b.Diary(ctx, Closed{ID: t.ID, Title: t.Title, Done: t.Status == Done}, now)
}

// Promoted is what Promote made.
type Promoted struct {
	Task     Task
	Promoted []Task
}

// Promote turns steps of a task into tasks of their own: each is a task part_of this one (and of its project) titled
// with the step, the step staying in the list as `- [ ] text → [[t-…]]`, counted done when that task closes.
func (b *Book) Promote(ctx context.Context, id string, indexes []int) (Promoted, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out Promoted
	idx := slices.Clone(indexes)
	sort.Ints(idx)
	for _, i := range slices.Compact(idx) {
		cur, err := b.get(ctx, id)
		if err != nil {
			return out, err
		}
		if cur == nil {
			return out, refusef("no task %s", id)
		}
		steps := Steps(cur.Notes, nil)
		if i < 0 || i >= len(steps) {
			return out, refusef("no step %d", i)
		}
		s := steps[i]
		if s.Promoted != "" {
			return out, refusef("step %d is already %s", i, s.Promoted)
		}
		if jsLen(s.Text) > MaxTitle {
			return out, refusef("step %d is too long for a title (%d characters): shorten it, then promote it", i, MaxTitle)
		}
		partOf := []string{id}
		for _, r := range RelsOf(*cur) {
			if r.Rel == "part_of" && r.Kind == KindPage {
				partOf = append(partOf, r.Target)
			}
		}
		in := Input{Title: s.Text, PartOf: &partOf}
		if f := ForOf(*cur); f != nil {
			in.For = S(f.Target)
		}
		sub, err := b.add(ctx, in)
		if err != nil {
			return out, err
		}
		notes, err := PromoteStep(cur.Notes, i, sub.ID)
		if err != nil {
			return out, err
		}
		if _, err := b.update(ctx, id, func(Task) (Input, error) { return Input{Notes: S(notes)}, nil }, ""); err != nil {
			return out, err
		}
		out.Promoted = append(out.Promoted, sub)
	}
	t, err := b.get(ctx, id)
	if t != nil {
		out.Task = *t
	}
	return out, err
}

// StalledNow is the account's stalled tasks at now, from its database alone.
func (b *Book) StalledNow(ctx context.Context, now time.Time) ([]StalledTask, error) {
	rows, err := stallRows(ctx, b.DB)
	if err != nil {
		return nil, err
	}
	th, err := LoadThresholds(ctx, b.DB)
	if err != nil {
		return nil, err
	}
	return Stalled(rows, now, th, b.Zone), nil
}

// TaskGroups is each task page's group, the signature of memory.RecallOptions.TaskGroups.
func (b *Book) TaskGroups(ctx context.Context) (map[string]string, error) {
	return GroupsByPath(ctx, b.DB)
}

// Reindex rebuilds the tasks' index from the task pages.
func (b *Book) Reindex(ctx context.Context) (int, error) { return Index{Me: b.Me}.Reindex(ctx, b.DB) }
