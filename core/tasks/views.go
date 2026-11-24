package tasks

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"kedalio/core/memory"
)

// Saved views of the tasks: a page `views/<name>` whose front matter is a small declarative query; `tasks_list {view}`
// runs it here. Flat on purpose, so a person can edit it in any editor and the memory's own tools can write it: one
// `key: value` per line, lists with commas.
//
//	---
//	status: todo, doing        any of the statuses, or `all`; unset: the open ones
//	to: today                  a day, `today`, `today+7`, `today-3`
//	for: alice                 the filters of tasks_list: group project from to ref stage label parent for
//	stalled: true              assigned_to worked_by waiting_on about related blocked_by blocks; stalled: true|false
//	others: true               not the owner's to move: waiting on someone, assigned to someone else, or for someone
//	group_by: status           project | status | about | due-week | for | day | person
//	                           (day: overdue | today | tomorrow | later | undated, by the task's due day)
//	sort: -priority            due (default) | priority | updated | created | title | status; `-` reverses
//	layout: board              list (default) | board
//	limit: 50
//	---
//	# Title
//	One sentence saying what the view shows.

// viewTitle is the first heading of a view page, its title.
var viewTitle = regexp.MustCompile(`(?m)^#\s+(.+)$`)

// GroupBys, Sorts and Layouts are what a view page may say.
var (
	GroupBys = []string{"project", "status", "about", "due-week", "for", "day", "person"}
	Sorts    = []string{"due", "priority", "updated", "created", "title", "status"}
	Layouts  = []string{"list", "board"}
)

// ViewError is a view that cannot be run: the message says which line of the page is wrong.
type ViewError struct{ Msg string }

func (e *ViewError) Error() string { return e.Msg }

func viewErr(format string, a ...any) error { return &ViewError{fmt.Sprintf(format, a...)} }

// ViewFilter holds the filters a view takes, as written (days may be `today+N`).
type ViewFilter struct {
	Status []Status
	// AllStatuses is `status: all`.
	AllStatuses bool
	Group       []Group
	Project     string
	From, To    string
	Ref, Stage  string
	Label       string
	Parent      string
	For         string
	AssignedTo  string
	WorkedBy    string
	WaitingOn   string
	About       string
	Related     string
	BlockedBy   string
	Blocks      string
	Stalled     *bool
	Others      *bool
}

// SortBy is how a view orders its tasks.
type SortBy struct {
	By   string
	Desc bool
}

// ViewQuery is a view page as its query.
type ViewQuery struct {
	Name    string
	Title   string
	Layout  string
	GroupBy string
	Sort    SortBy
	Limit   int
	Filter  ViewFilter
}

// textKeys are the keys of a view that filter by a word.
var textKeys = []string{"project", "from", "to", "ref", "stage", "label", "parent", "for", "assigned_to", "worked_by",
	"waiting_on", "about", "related", "blocked_by", "blocks"}

// viewKeys are the keys a view page may hold; `kind` and `is` are the memory's own.
var viewKeys = append(slices.Clone(textKeys), "status", "group", "stalled", "others", "group_by", "sort", "layout", "limit", "kind", "is")

var (
	// ViewName is what a view's name looks like.
	viewNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	todayRe    = regexp.MustCompile(`^today(?:([+-])(\d{1,4}))?$`)
)

// ViewPath is the page of a view.
func ViewPath(name string) string { return "views/" + name + ".md" }

func csv(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.ToLower(strings.TrimSpace(x)); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// ResolveDay is a day as written in a view, `today`, `today+7`, or a date, as a date.
func ResolveDay(v, today, what string) (string, error) {
	if m := todayRe.FindStringSubmatch(strings.ToLower(strings.TrimSpace(v))); m != nil {
		if m[1] == "" {
			return today, nil
		}
		n, _ := strconv.Atoi(m[2])
		if m[1] == "-" {
			n = -n
		}
		return AddDays(today, n), nil
	}
	if d := strings.TrimSpace(v); ValidDay(d) {
		return d, nil
	}
	return "", viewErr(`%s is YYYY-MM-DD, today, today+N or today-N, not "%s"`, what, v)
}

// ParseView is a view page as its query. The error is a *ViewError saying which line is wrong.
func ParseView(name, body string) (ViewQuery, error) {
	fm := memory.FrontMatter(body)
	bad := func(format string, a ...any) (ViewQuery, error) {
		return ViewQuery{}, viewErr("view %s: %s", name, fmt.Sprintf(format, a...))
	}
	var keys []string
	for k := range fm {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !slices.Contains(viewKeys, k) {
			var known []string
			for _, x := range viewKeys {
				if x != "kind" && x != "is" {
					known = append(known, x)
				}
			}
			return bad(`unknown line "%s" (known: %s)`, k, strings.Join(known, ", "))
		}
	}
	var f ViewFilter
	for k, dst := range map[string]*string{
		"project": &f.Project, "from": &f.From, "to": &f.To, "ref": &f.Ref, "stage": &f.Stage, "label": &f.Label,
		"parent": &f.Parent, "for": &f.For, "assigned_to": &f.AssignedTo, "worked_by": &f.WorkedBy,
		"waiting_on": &f.WaitingOn, "about": &f.About, "related": &f.Related, "blocked_by": &f.BlockedBy, "blocks": &f.Blocks,
	} {
		*dst = fm[k]
	}
	if fm["status"] != "" {
		s := csv(fm["status"])
		if len(s) == 1 && s[0] == "all" {
			f.AllStatuses = true
		} else if !slices.ContainsFunc(s, func(x string) bool { return !ValidStatus(Status(x)) }) {
			for _, x := range s {
				f.Status = append(f.Status, Status(x))
			}
		} else {
			return bad("status is all or some of %s", joinStatuses())
		}
	}
	if fm["group"] != "" {
		for _, x := range csv(fm["group"]) {
			if !slices.Contains(Groups, Group(x)) {
				var gs []string
				for _, g := range Groups {
					gs = append(gs, string(g))
				}
				return bad("group is some of %s", strings.Join(gs, ", "))
			}
			f.Group = append(f.Group, Group(x))
		}
	}
	for k, dst := range map[string]**bool{"stalled": &f.Stalled, "others": &f.Others} {
		if v := fm[k]; v != "" {
			if v != "true" && v != "false" {
				return bad("%s is true or false", k)
			}
			b := v == "true"
			*dst = &b
		}
	}
	if len(f.Status) > 0 || f.AllStatuses {
		if len(f.Group) > 0 {
			return bad("status and group together: use one")
		}
	}
	for _, k := range []string{"from", "to"} {
		if fm[k] != "" {
			if _, err := ResolveDay(fm[k], "2000-01-01", k); err != nil {
				return bad("%s", err.Error())
			}
		}
	}
	layout := fm["layout"]
	if layout == "" {
		layout = "list"
	}
	if !slices.Contains(Layouts, layout) {
		return bad("layout is %s", strings.Join(Layouts, " or "))
	}
	if fm["group_by"] != "" && !slices.Contains(GroupBys, fm["group_by"]) {
		return bad("group_by is one of %s", strings.Join(GroupBys, ", "))
	}
	sortText := strings.TrimSpace(fm["sort"])
	if sortText == "" {
		sortText = "due"
	}
	by := strings.TrimPrefix(sortText, "-")
	if !slices.Contains(Sorts, by) {
		return bad("sort is one of %s, with - in front to reverse", strings.Join(Sorts, ", "))
	}
	limit := 0
	if fm["limit"] != "" {
		n, err := strconv.Atoi(fm["limit"])
		if err != nil || n < 1 || n > 500 {
			return bad("limit is a number from 1 to 500")
		}
		limit = n
	}
	title := name
	if m := viewTitle.FindStringSubmatch(frontStrip.ReplaceAllString(body, "")); m != nil {
		title = strings.TrimSpace(m[1])
	}
	return ViewQuery{
		Name: name, Title: title, Layout: layout, GroupBy: fm["group_by"],
		Sort: SortBy{by, strings.HasPrefix(sortText, "-")}, Limit: limit, Filter: f,
	}, nil
}

// ReadView is the view a page holds. The error is a *ViewError when there is none, naming the ones there are.
func (b *Book) ReadView(ctx context.Context, name string) (ViewQuery, error) {
	n := strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "views/"), ".md")
	if viewNameRe.MatchString(n) {
		doc, err := b.Store.Get(ctx, ViewPath(n))
		if err != nil {
			return ViewQuery{}, err
		}
		if doc != nil {
			return ParseView(n, doc.Body)
		}
	}
	known, err := b.ListViews(ctx)
	if err != nil {
		return ViewQuery{}, err
	}
	var names []string
	for _, v := range known {
		names = append(names, v.Name)
	}
	hint := ""
	if len(names) > 0 {
		hint = " (there are: " + strings.Join(names, ", ") + ")"
	}
	return ViewQuery{}, viewErr(`no view "%s"%s`, name, hint)
}

// ListedView is a saved view in a listing; a page that does not parse is listed with its error.
type ListedView struct {
	Name   string `json:"name"`
	Title  string `json:"title"`
	Layout string `json:"layout,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ListViews are the saved views: name, title and layout.
func (b *Book) ListViews(ctx context.Context) ([]ListedView, error) {
	rows, err := b.DB.QueryContext(ctx, "select path, title, body from docs where path like 'views/%' and deleted = 0 order by path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ListedView{}
	for rows.Next() {
		var path, title, body string
		if err := rows.Scan(&path, &title, &body); err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(strings.TrimPrefix(path, "views/"), ".md")
		if !viewNameRe.MatchString(name) {
			continue
		}
		if v, err := ParseView(name, body); err != nil {
			out = append(out, ListedView{Name: name, Title: title, Error: err.Error()})
		} else {
			out = append(out, ListedView{Name: name, Title: v.Title, Layout: v.Layout})
		}
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- running

// ViewGroup is a group of a view's answer.
type ViewGroup struct {
	Key   string
	Label string
	Tasks []Task
}

// ViewResult is a view run.
type ViewResult struct {
	View  ViewQuery
	Today string
	Count int
	Group []ViewGroup
	// Reasons are the reasons of each stalled task, when the view asks for the stalled ones.
	Reasons map[string][]Stall
}

func nameOf(t string) string { return mdSuffix.ReplaceAllString(lastPart(t), "") }

// WeekOf is the ISO week of a day, `2026-W44`.
func WeekOf(day string) string {
	y, w := parseDay(day).ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

// DayBuckets are the buckets of `group_by: day`, in the order they are shown; the app translates the keys.
var DayBuckets = []string{"overdue", "today", "tomorrow", "later", "undated"}

// DayBucket is the bucket of a due day (undated: none) against today.
func DayBucket(due, today string) string {
	switch {
	case due == "":
		return "undated"
	case due < today:
		return "overdue"
	case due == today:
		return "today"
	case due == AddDays(today, 1):
		return "tomorrow"
	}
	return "later"
}

// isOwner reports whether a person reference is the owner: the account itself, or a page of the owner's own area
// (`io/`, `me/`).
func isOwner(who, me, own string) bool {
	return strings.EqualFold(who, me) || strings.HasPrefix(strings.TrimSuffix(strings.TrimPrefix(who, "[["), "]]"), own+"/")
}

// OtherOf is who a task waits on or belongs to when it is not the owner's to move (waiting on someone, assigned to
// someone else, or for someone), else "". own is the owner's area.
func OtherOf(t Task, me, own string) string {
	for _, r := range []*Rel{WaitingOnOf(t), AssignedOf(t), ForOf(t)} {
		if r != nil && r.Target != "" && !isOwner(r.Target, me, own) {
			return nameOf(r.Target)
		}
	}
	return ""
}

// GroupKeys are the groups a task falls in for a group_by (a task about two pages is in two). today is for `day`; own,
// the owner's area, is for `person`.
func GroupKeys(t Task, by, me, today, own string) []string {
	switch by {
	case "project":
		return []string{t.Project}
	case "status":
		return []string{string(t.Status)}
	case "about":
		var a []string
		for _, r := range RelsOf(t) {
			if r.Rel == "about" {
				a = append(a, nameOf(r.Target))
			}
		}
		if len(a) == 0 {
			return []string{""}
		}
		return a
	case "due-week":
		if t.Due == "" {
			return []string{""}
		}
		return []string{WeekOf(t.Due)}
	case "for":
		if f := ForOf(t); f != nil {
			return []string{nameOf(f.Target)}
		}
		return []string{me}
	case "day":
		return []string{DayBucket(t.Due, today)}
	case "person":
		if o := OtherOf(t, me, own); o != "" {
			return []string{o}
		}
		return []string{me}
	}
	return []string{""}
}

// localeCmp orders text the way a person reads it: case-insensitively first.
func localeCmp(a, b string) int {
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

func dueKey(t Task) string {
	if t.Due == "" {
		return "9999"
	}
	return t.Due
}

func byDue(a, b Task) int {
	if c := localeCmp(dueKey(a), dueKey(b)); c != 0 {
		return c
	}
	return localeCmp(timeKey(a), timeKey(b))
}

var sorters = map[string]func(a, b Task) int{
	"due": byDue,
	"priority": func(a, b Task) int {
		if prio(a) != prio(b) {
			return prio(a) - prio(b)
		}
		return byDue(a, b)
	},
	"updated": func(a, b Task) int { return localeCmp(a.Updated, b.Updated) },
	"created": func(a, b Task) int { return localeCmp(a.Created, b.Created) },
	"title":   func(a, b Task) int { return localeCmp(a.Title, b.Title) },
	"status": func(a, b Task) int {
		if ia, ib := slices.Index(Statuses, a.Status), slices.Index(Statuses, b.Status); ia != ib {
			return ia - ib
		}
		return byDue(a, b)
	},
}

// RunView runs a view over the account's tasks at now. The error is a *ViewError when a filter names what does not exist.
func (b *Book) RunView(ctx context.Context, v ViewQuery, now time.Time) (ViewResult, error) {
	today, f := DayOf(now, b.Zone), v.Filter
	id := func(k string) (string, error) {
		if k == "" {
			return "", nil
		}
		all, err := b.List(ctx)
		if err != nil {
			return "", err
		}
		t, err := ResolveTask(all, k, f.Project)
		if err != nil {
			return "", viewErr("view %s: %s", v.Name, err)
		}
		return t.ID, nil
	}
	day := func(k, val string) (string, error) {
		if val == "" {
			return "", nil
		}
		return ResolveDay(val, today, k)
	}
	q := Filter{
		Group: f.Group, Project: f.Project, Ref: f.Ref, Stage: f.Stage, Label: f.Label, For: f.For,
		AssignedTo: f.AssignedTo, WorkedBy: f.WorkedBy, WaitingOn: f.WaitingOn, About: f.About, Related: f.Related,
	}
	if !f.AllStatuses && len(f.Group) == 0 {
		q.Status = f.Status
		if q.Status == nil {
			q.Status = []Status{Todo, Doing, Waiting}
		}
	}
	var err error
	if q.From, err = day("from", f.From); err != nil {
		return ViewResult{}, err
	}
	if q.To, err = day("to", f.To); err != nil {
		return ViewResult{}, err
	}
	if q.Parent, err = id(f.Parent); err != nil {
		return ViewResult{}, err
	}
	if q.BlockedBy, err = id(f.BlockedBy); err != nil {
		return ViewResult{}, err
	}
	if q.Blocks, err = id(f.Blocks); err != nil {
		return ViewResult{}, err
	}
	tasks, err := b.Query(ctx, q)
	if err != nil {
		return ViewResult{}, err
	}
	var reasons map[string][]Stall
	if f.Stalled != nil {
		stalled, err := b.StalledNow(ctx, now)
		if err != nil {
			return ViewResult{}, err
		}
		found := map[string][]Stall{}
		for _, s := range stalled {
			found[s.ID] = s.Reasons
		}
		tasks = slices.DeleteFunc(tasks, func(t Task) bool { _, ok := found[t.ID]; return ok != *f.Stalled })
		if *f.Stalled {
			reasons = map[string][]Stall{}
			for _, t := range tasks {
				reasons[t.ID] = found[t.ID]
			}
		}
	}
	own := b.Store.Areas.Self
	if f.Others != nil {
		tasks = slices.DeleteFunc(tasks, func(t Task) bool { return (OtherOf(t, b.Me, own) != "") != *f.Others })
	}
	cmp := sorters[v.Sort.By]
	sort.SliceStable(tasks, func(i, j int) bool {
		c := cmp(tasks[i], tasks[j])
		if v.Sort.Desc {
			c = -c
		}
		return c < 0
	})
	if v.Limit > 0 && len(tasks) > v.Limit {
		tasks = tasks[:v.Limit]
	}
	groups := map[string][]Task{}
	// a board shows its columns even when empty
	if v.GroupBy == "status" && v.Layout == "board" {
		for _, s := range []string{"todo", "doing", "waiting"} {
			groups[s] = []Task{}
		}
	}
	for _, t := range tasks {
		keys := []string{""}
		if v.GroupBy != "" {
			keys = GroupKeys(t, v.GroupBy, b.Me, today, own)
		}
		for _, k := range keys {
			groups[k] = append(groups[k], t)
		}
	}
	order := func(k string) int {
		switch v.GroupBy {
		case "status":
			return slices.Index(Statuses, Status(k))
		case "day":
			return slices.Index(DayBuckets, k)
		}
		return 0
	}
	var keys []string
	for k := range groups {
		keys = append(keys, k)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		a, c := keys[i], keys[j]
		if (a == "") != (c == "") {
			return c == ""
		}
		if order(a) != order(c) {
			return order(a) < order(c)
		}
		return localeCmp(a, c) < 0
	})
	res := ViewResult{View: v, Today: today, Count: len(tasks), Reasons: reasons}
	for _, k := range keys {
		label := k
		if v.GroupBy != "day" && k == "" {
			label = "(none)"
		}
		res.Group = append(res.Group, ViewGroup{Key: k, Label: label, Tasks: groups[k]})
	}
	return res, nil
}

// ---------------------------------------------------------------- the views that ship

func viewPage(front []string, title, what string) string {
	return "---\nkind: concept\n" + strings.Join(front, "\n") + "\n---\n\n# " + title + "\n\n" + what + "\n"
}

// DefaultViews are the views every account starts with, by name. Others are made by writing a page.
var DefaultViews = []struct{ Name, Body string }{
	{"today", viewPage([]string{"status: todo, doing", "to: today", "sort: due", "layout: list"},
		"Today", "The open tasks due today or earlier, oldest first.")},
	// the owner's default list: by day, overdue first, then today, tomorrow, the rest, the undated last
	{"days", viewPage([]string{"status: todo, doing, waiting", "group_by: day", "sort: due", "layout: list"},
		"By day", "The open tasks in a list by day: overdue, today, tomorrow, later, then the ones with no day.")},
	{"board", viewPage([]string{"status: todo, doing, waiting", "group_by: status", "sort: priority", "layout: board"},
		"Board", "The open tasks in a column for each status.")},
	{"stalled", viewPage([]string{"stalled: true", "group_by: project", "sort: due", "layout: list"},
		"Stalled", "The tasks that stopped moving, with the reason for each.")},
	// what is not theirs to move, by the person it waits on or belongs to
	{"others", viewPage([]string{"status: todo, doing, waiting", "others: true", "group_by: person", "sort: due", "layout: list"},
		"For others", "The open tasks waiting on someone, assigned to someone else or done for someone, by person.")},
}

// EnsureViews writes the shipped views, each only when its page has never existed: the owner's own version of one is
// kept, and one the owner deleted stays deleted. A delete is a revision, so the page's row outlives it (deleted = 1);
// that row is the mark that the view was once there, and no separate "seeded" flag is needed.
//
// A view's name follows the account's language: the Italian ones keep the name they were seeded under ("others" is
// "per-altri" there), and a database that has either name is not given the other.
func (b *Book) EnsureViews(ctx context.Context) error {
	for _, v := range DefaultViews {
		names := []string{v.Name}
		if v.Name == "others" {
			names = []string{"others", "per-altri"}
		}
		var n int
		for _, name := range names {
			var c int
			if err := b.DB.QueryRowContext(ctx, "select count(*) from docs where path = ?", ViewPath(name)).Scan(&c); err != nil {
				return err
			}
			n += c
		}
		if n > 0 {
			continue
		}
		name := v.Name
		if name == "others" && b.Store.Areas == memory.AreasIT {
			name = "per-altri"
		}
		if _, err := b.Store.Write(ctx, ViewPath(name), v.Body, "kedalio"); err != nil {
			return err
		}
	}
	return nil
}
