package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"kedalio/core/clip"
	"kedalio/protocol"
)

// The operations behind the tasks_brief, tasks_list, tasks_get and tasks_save tools, as Go values that marshal to the
// answers the tools give. The MCP layer masks and serialises them; nothing here knows MCP.

// Clock is the day as the owner has it, at the head of the debrief and of the session's start: the other answers
// leave it out, a client reads it from those.
type Clock struct {
	Today   string `json:"today"`
	Now     string `json:"now"`
	Weekday string `json:"weekday"`
}

// Clock is the day now in the account's zone.
func (b *Book) Clock() Clock {
	now := b.now()
	return Clock{Today: DayOf(now, b.Zone), Now: HHMM(now, b.Zone), Weekday: now.In(b.Zone).Weekday().String()}
}

// Line is a task as a line: easy to read back to the owner, the id kept for the next call. all (the whole list) lets it
// say when the task waits for another still open.
func (b *Book) Line(t Task, all []Task) string {
	var closed func(string) bool
	if all != nil {
		closed = ClosedIn(all)
	}
	var parts []string
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if t.Due != DayOf(b.now(), b.Zone) {
		add(t.Due)
	}
	add(t.Time)
	add(t.Ref)
	add(t.Title)
	if t.Project != "" {
		add("[" + t.Project + "]")
	}
	if t.Owner != "" && t.Owner != b.Me {
		add("(" + t.Owner + ")")
	}
	if f := ForOf(t); f != nil {
		add("for " + lastPart(f.Target))
	}
	if t.Status == Doing {
		add("in progress")
	}
	add(t.Stage)
	if all != nil && IsBlocked(all, t) {
		add("blocked")
	}
	if p := ProgressOf(t.Notes, closed); p != nil {
		add(fmt.Sprintf("%d/%d", p.Done, p.Total))
	}
	if t.Priority == 1 {
		add("!")
	}
	add("#" + t.ID)
	return strings.Join(parts, " · ")
}

func (b *Book) lines(ts []Task, all []Task) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, b.Line(t, all))
	}
	return out
}

// ---------------------------------------------------------------- tasks_brief

// BriefAnswer is the owner's debrief. The long lists (no date, waiting, stalled) show their first briefCap lines and a
// total: in full they made the debrief 20k characters, and tasks_list gives any of them whole. Each line ends with the
// task's #id.
type BriefAnswer struct {
	Clock
	Moment             Moment   `json:"moment"`
	Overdue            []string `json:"overdue,omitempty"`
	Missed             []string `json:"missed,omitempty"`
	LaterToday         []string `json:"later_today,omitempty"`
	Tomorrow           []string `json:"tomorrow,omitempty"`
	NextDays           []string `json:"next_days,omitempty"`
	NoDate             []string `json:"no_date,omitempty"`
	NoDateTotal        int      `json:"no_date_total,omitempty"`
	WaitingOnOthers    []string `json:"waiting_on_others,omitempty"`
	WaitingTotal       int      `json:"waiting_total,omitempty"`
	InProgressByAgents []string `json:"in_progress_by_agents,omitempty"`
	ForOthers          []string `json:"for_others,omitempty"`
	Stalled            []string `json:"stalled,omitempty"`
	StalledTotal       int      `json:"stalled_total,omitempty"`
	DoneToday          int      `json:"done_today,omitempty"`
	More               string   `json:"more,omitempty"`
}

// briefTitle is how many characters of a title a debrief line keeps: the line is there to recognise the task, and
// long titles were most of the debrief's size.
const briefTitle = 60

// cutTitle is a title cut to briefTitle characters.
func cutTitle(s string) string {
	if r := []rune(s); len(r) > briefTitle {
		return strings.TrimSpace(string(r[:briefTitle-1])) + "…"
	}
	return s
}

// briefLine is a task as the debrief shows it: when, the title cut, the project's last part, the id. tasks_get and
// tasks_list have the rest (ref, stage, progress, who).
func (b *Book) briefLine(t Task) string {
	var parts []string
	if t.Due != "" && t.Due != DayOf(b.now(), b.Zone) {
		parts = append(parts, t.Due)
	}
	if t.Time != "" {
		parts = append(parts, t.Time)
	}
	parts = append(parts, cutTitle(t.Title))
	if t.Project != "" {
		parts = append(parts, "["+lastPart(t.Project)+"]")
	}
	if f := ForOf(t); f != nil {
		parts = append(parts, "for "+lastPart(f.Target))
	}
	if t.Priority == 1 {
		parts = append(parts, "!")
	}
	parts = append(parts, "#"+t.ID)
	return strings.Join(parts, " · ")
}

func (b *Book) briefLines(ts []Task) []string {
	var out []string
	for _, t := range ts {
		out = append(out, b.briefLine(t))
	}
	return out
}

// briefCap is how many lines a long list of the debrief shows.
const briefCap = 10

// BriefOf is the debrief: overdue, today by time, tomorrow, what waits, what agents do, the stalled ones.
func (b *Book) BriefOf(ctx context.Context) (*BriefAnswer, error) {
	all, err := b.List(ctx)
	if err != nil {
		return nil, err
	}
	now := b.now()
	d := Brief(all, now, b.Me, b.Zone)
	stalled, err := b.StalledNow(ctx, now)
	if err != nil {
		return nil, err
	}
	a := &BriefAnswer{
		Clock: b.Clock(), Moment: d.Moment,
		Overdue: b.briefLines(d.Overdue), Missed: b.briefLines(d.Missed), LaterToday: b.briefLines(d.Today),
		Tomorrow: b.briefLines(d.Tomorrow), NextDays: b.briefLines(d.Upcoming),
		NoDate: b.briefLines(d.Undated[:min(briefCap, len(d.Undated))]), NoDateTotal: len(d.Undated),
		WaitingOnOthers: b.briefLines(d.Waiting[:min(briefCap, len(d.Waiting))]), WaitingTotal: len(d.Waiting),
		InProgressByAgents: b.briefLines(d.Agents),
		ForOthers:          b.briefLines(d.ForOthers), StalledTotal: len(stalled), DoneToday: d.DoneToday,
	}
	for _, s := range stalled[:min(briefCap, len(stalled))] {
		var why []string
		for _, r := range s.Reasons {
			why = append(why, ReasonText(r))
		}
		a.Stalled = append(a.Stalled, fmt.Sprintf("%s: %s #%s", cutTitle(s.Title), strings.Join(why[:min(1, len(why))], "; "), s.ID))
	}
	if len(d.Undated) > briefCap || len(d.Waiting) > briefCap || len(stalled) > briefCap {
		a.More = "the long lists are cut at " + fmt.Sprint(briefCap) + ": tasks_list with status waiting, stalled true, or a project gives them whole"
	}
	return a, nil
}

// ---------------------------------------------------------------- tasks_list

// ShortTask is a task short, as a list shows it: no notes, no timestamps, the roles as names.
type ShortTask struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Status     Status   `json:"status"`
	Due        string   `json:"due,omitempty"`
	Time       string   `json:"time,omitempty"`
	Remind     *int     `json:"remind,omitempty"`
	Priority   int      `json:"priority,omitempty"`
	Repeat     string   `json:"repeat,omitempty"`
	RepeatFrom string   `json:"repeat_from,omitempty"`
	Ref        string   `json:"ref,omitempty"`
	Stage      string   `json:"stage,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	Detail     string   `json:"detail,omitempty"`
	Done       string   `json:"done,omitempty"`
	Snooze     string   `json:"snooze,omitempty"`
	SnoozedAt  string   `json:"snoozed_at,omitempty"`
	Owner      string   `json:"owner,omitempty"`
	Project    string   `json:"project,omitempty"`
	Parent     string   `json:"parent,omitempty"`
	BlockedBy  []string `json:"blocked_by,omitempty"`
	For        string   `json:"for,omitempty"`
	AssignedTo string   `json:"assigned_to,omitempty"`
	WorkedBy   string   `json:"worked_by,omitempty"`
	WaitingOn  string   `json:"waiting_on,omitempty"`
	Notes      string   `json:"notes,omitempty"`
	Blocked    bool     `json:"blocked,omitempty"`
	// Stalled are the reasons, as sentences, when the task stalled.
	Stalled []string `json:"stalled,omitempty"`
}

// roleName is a role's target as a short string: a person by name, anything else as it is written.
func roleName(r *Rel) string {
	switch {
	case r == nil:
		return ""
	case r.Kind == KindPage || r.Kind == KindName:
		return lastPart(r.Target)
	case r.Kind == KindTask || r.Kind == KindDate:
		return r.Target
	}
	return string(r.Kind) + ":" + r.Target
}

// ShortOf is a task as a list shows it.
func ShortOf(t Task) ShortTask {
	return ShortTask{
		ID: t.ID, Title: t.Title, Status: t.Status, Due: t.Due, Time: t.Time, Remind: t.Remind, Priority: t.Priority,
		Repeat: t.Repeat, RepeatFrom: t.RepeatFrom, Ref: t.Ref, Stage: t.Stage, Labels: t.Labels, Detail: t.Detail,
		Done: t.Done, Snooze: t.Snooze, SnoozedAt: t.SnoozedAt, Owner: t.Owner, Project: t.Project, Parent: t.Parent,
		BlockedBy: t.BlockedBy, For: roleName(ForOf(t)), AssignedTo: roleName(AssignedOf(t)),
		WorkedBy: roleName(WorkedByOf(t)), WaitingOn: roleName(WaitingOnOf(t)),
	}
}

// FullTask is a task whole, with what a list says beside it.
type FullTask struct {
	Task
	Blocked bool     `json:"blocked,omitempty"`
	Stalled []string `json:"stalled,omitempty"`
}

// Counts is a count per key, kept in the order given (most first), as a JSON object.
type Counts []struct {
	Key string
	N   int
}

// MarshalJSON writes the counts as an object in their order.
func (c Counts) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, e := range c {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(e.Key)
		b.Write(k)
		fmt.Fprintf(&b, ":%d", e.N)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// ListSummary is what a list holds beyond the tasks it shows: how many per project (none: «-») and per status, how many
// overdue.
type ListSummary struct {
	ByProject Counts `json:"by_project"`
	ByStatus  Counts `json:"by_status"`
	Overdue   int    `json:"overdue"`
}

// SummaryOf counts a list.
func SummaryOf(tasks []Task, today string) ListSummary {
	count := func(key func(Task) string) Counts {
		n := map[string]int{}
		var order []string
		for _, t := range tasks {
			k := key(t)
			if _, ok := n[k]; !ok {
				order = append(order, k)
			}
			n[k]++
		}
		sort.SliceStable(order, func(i, j int) bool { return n[order[i]] > n[order[j]] })
		var c Counts
		for _, k := range order {
			c = append(c, struct {
				Key string
				N   int
			}{k, n[k]})
		}
		return c
	}
	s := ListSummary{
		ByProject: count(func(t Task) string {
			if t.Project == "" {
				return "-"
			}
			return t.Project
		}),
		ByStatus: count(func(t Task) string { return string(t.Status) }),
	}
	for _, t := range tasks {
		if t.Due != "" && t.Due < today && !t.Status.Closed() {
			s.Overdue++
		}
	}
	return s
}

// ListAnswer is tasks_list's answer.
type ListAnswer struct {
	Count     int  `json:"count"`
	Truncated bool `json:"truncated,omitempty"`
	// Shown and Hint are set when the answer stopped at listBudget before the limit.
	Shown   int          `json:"shown,omitempty"`
	Hint    string       `json:"hint,omitempty"`
	Summary *ListSummary `json:"summary,omitempty"`
	// Tasks are ShortTask, or FullTask when the list asked for them whole.
	Tasks []any `json:"tasks"`
}

// ViewInfo says which view an answer is of.
type ViewInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Layout  string `json:"layout"`
	GroupBy string `json:"group_by,omitempty"`
}

// ViewAnswerGroup is a group of a view's answer.
type ViewAnswerGroup struct {
	Group string      `json:"group"`
	Label string      `json:"label"`
	Tasks []ShortTask `json:"tasks"`
}

// ViewAnswer is a saved view run: its groups, each task short.
type ViewAnswer struct {
	Clock
	View   ViewInfo          `json:"view"`
	Count  int               `json:"count"`
	Groups []ViewAnswerGroup `json:"groups"`
}

// ViewsAnswer is the saved views.
type ViewsAnswer struct {
	Views []ListedView `json:"views"`
}

// Views are the saved views, and the format of one is in ParseView's comment.
func (b *Book) Views(ctx context.Context) (*ViewsAnswer, error) {
	v, err := b.ListViews(ctx)
	if err != nil {
		return nil, err
	}
	return &ViewsAnswer{v}, nil
}

// ViewOf runs a saved view by name and answers it.
func (b *Book) ViewOf(ctx context.Context, name string) (*ViewAnswer, error) {
	v, err := b.ReadView(ctx, name)
	if err != nil {
		return nil, err
	}
	r, err := b.RunView(ctx, v, b.now())
	if err != nil {
		return nil, err
	}
	a := &ViewAnswer{
		Clock: b.Clock(), Count: r.Count, Groups: []ViewAnswerGroup{},
		View: ViewInfo{r.View.Name, r.View.Title, r.View.Layout, r.View.GroupBy},
	}
	for _, g := range r.Group {
		out := ViewAnswerGroup{Group: g.Key, Label: g.Label, Tasks: []ShortTask{}}
		for _, t := range g.Tasks {
			s := ShortOf(t)
			for _, st := range r.Reasons[t.ID] {
				s.Stalled = append(s.Stalled, ReasonText(st))
			}
			out.Tasks = append(out.Tasks, s)
		}
		a.Groups = append(a.Groups, out)
	}
	return a, nil
}

// ListOf searches the tasks: open ones unless a status or group is given. A view in the query is run and answered as a
// *ViewAnswer; anything else is a *ListAnswer.
func (b *Book) ListOf(ctx context.Context, q protocol.TasksList) (any, error) {
	if q.View != "" {
		return b.ViewOf(ctx, q.View)
	}
	return b.Search(ctx, q)
}

// Search is tasks_list without a view.
func (b *Book) Search(ctx context.Context, q protocol.TasksList) (*ListAnswer, error) {
	all, err := b.List(ctx)
	if err != nil {
		return nil, err
	}
	id := func(k string) (string, error) {
		if k == "" {
			return "", nil
		}
		t, err := ResolveTask(all, k, q.Project)
		return t.ID, err
	}
	f := Filter{
		Project: q.Project, From: q.From, To: q.To, Ref: q.Ref, Stage: q.Stage, Label: q.Label, For: q.For,
		AssignedTo: q.AssignedTo, WorkedBy: q.WorkedBy, WaitingOn: q.WaitingOn, About: q.About, Related: q.Related,
	}
	// a misspelt filter is an error: `in_progress` used to answer an empty list that looked like "nothing there"
	if q.Group != "" {
		if !slices.Contains(Groups, Group(q.Group)) {
			return nil, refusef("group is one of open, active, paused, closed, not %q", q.Group)
		}
		f.Group = []Group{Group(q.Group)}
	}
	if q.Status != "" && q.Status != "all" && !ValidStatus(Status(q.Status)) {
		return nil, refusef("status is one of todo, doing, waiting, done, dropped or all, not %q", q.Status)
	}
	if q.Status != "all" && q.Group == "" {
		f.Status = []Status{Todo, Doing, Waiting}
		if q.Status != "" {
			f.Status = []Status{Status(q.Status)}
		}
	}
	if f.Parent, err = id(q.Parent); err != nil {
		return nil, err
	}
	if f.BlockedBy, err = id(q.BlockedBy); err != nil {
		return nil, err
	}
	if f.Blocks, err = id(q.Blocks); err != nil {
		return nil, err
	}
	hits, err := b.Query(ctx, f)
	if err != nil {
		return nil, err
	}
	if words := strings.ToLower(q.Text); words != "" {
		kept := hits[:0]
		for _, t := range hits {
			if strings.Contains(strings.ToLower(t.Ref+" "+t.Title+" "+t.Notes), words) {
				kept = append(kept, t)
			}
		}
		hits = kept
	}
	blocked, err := BlockedIDs(ctx, b.DB)
	if err != nil {
		return nil, err
	}
	var found map[string][]Stall
	now := b.now()
	if q.Stalled {
		stalled, err := b.StalledNow(ctx, now)
		if err != nil {
			return nil, err
		}
		found = map[string][]Stall{}
		for _, s := range stalled {
			found[s.ID] = s.Reasons
		}
		kept := hits[:0]
		for _, t := range hits {
			if _, ok := found[t.ID]; ok {
				kept = append(kept, t)
			}
		}
		hits = kept
	}
	limit := q.Limit
	if limit == 0 {
		limit = 50
	}
	a := &ListAnswer{Count: len(hits), Tasks: []any{}}
	summarise := func() {
		a.Truncated = true
		s := SummaryOf(hits, DayOf(b.now(), b.Zone))
		a.Summary = &s
	}
	if len(hits) > limit {
		summarise()
	}
	budget := charBudget(q.MaxChars, listBudget)
	size := 0
	for _, t := range hits[:min(limit, len(hits))] {
		item := b.listItem(t, q, blocked[t.ID], found[t.ID])
		// counted as it is sent, compact
		raw, _ := json.Marshal(item)
		// the answer goes whole into the reader's context: notes, full tasks or a high limit made one answer 25-40k
		// characters, so it stops at a budget and says how to see the rest
		if size += len(raw); budget > 0 && size > budget && len(a.Tasks) > 0 {
			a.Shown = len(a.Tasks)
			a.Hint = fmt.Sprintf("%d of %d shown, %d missing, to keep the answer under %d characters: narrow with "+
				"project, status, text or parent, drop notes or full, read one with tasks_get, or pass max_chars 0 "+
				"for all of them", a.Shown, a.Count, a.Count-a.Shown, budget)
			if !a.Truncated {
				summarise()
			}
			break
		}
		a.Tasks = append(a.Tasks, item)
	}
	return a, nil
}

// listBudget is the most characters of tasks one tasks_list answer carries unless max_chars says otherwise.
const listBudget = 6000

// listNotes is how much of a task's notes a list shows: tasks_get has the rest.
const listNotes = 300

// charBudget is the character cap a call asked for: max_chars when given (0 lifts the cap), else the default.
func charBudget(asked *int, def int) int {
	if asked == nil {
		return def
	}
	return max(*asked, 0)
}

// cutNotes is notes cut to listNotes characters, with where the rest is.
func cutNotes(notes string) string {
	if jsLen(notes) <= listNotes {
		return notes
	}
	return string(utf16Prefix(notes, listNotes)) + fmt.Sprintf("… (cut, %d characters in all: tasks_get has the rest)", jsLen(notes))
}

// listItem is one task as tasks_list shows it: short, or whole when the list asked for it.
func (b *Book) listItem(t Task, q protocol.TasksList, blocked bool, stalls []Stall) any {
	var why []string
	for _, r := range stalls {
		why = append(why, ReasonText(r))
	}
	if q.Full {
		// a list is read to choose: a full task keeps its fields, its notes are cut like the short form's, unless
		// max_chars 0 asked for everything
		if q.MaxChars == nil || *q.MaxChars > 0 {
			t.Notes = cutNotes(t.Notes)
		}
		// the relations are already in the task's role fields and in tasks_get: not twice in every row
		t.Rels, t.RelExtra = nil, nil
		return FullTask{Task: t, Blocked: blocked, Stalled: why}
	}
	// a list is read to choose: the notes and timestamps of fifty tasks made «what is there to do» too long for one
	// answer; tasks_get has each one whole
	s := ShortOf(t)
	if t.Status != Done {
		s.Done = ""
	}
	if q.Notes && t.Notes != "" {
		s.Notes = cutNotes(t.Notes)
	}
	s.Blocked, s.Stalled = blocked, why
	return s
}

// ---------------------------------------------------------------- tasks_get

// IndexedStep is a step with its index.
type IndexedStep struct {
	Index int `json:"index"`
	Step
}

// RelationLists are a task's relations by name, in the order they first appear, as a JSON object of lists.
type RelationLists []struct {
	Name    string
	Targets []string
}

// MarshalJSON writes the lists as an object in their order.
func (r RelationLists) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, e := range r {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(e.Name)
		v, _ := json.Marshal(e.Targets)
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// LogSummary is the end of a task's log: the rest is on its episode page.
type LogSummary struct {
	Path  string `json:"path"`
	Total int    `json:"total"`
	Last  []Note `json:"last"`
}

// logShown is how many lines of a task's log tasks_get shows unless log asks for more.
const logShown = 3

// partsShown is how many open parts tasks_get lists.
const partsShown = 10

// GetAnswer is one task in full.
type GetAnswer struct {
	Task        Task          `json:"task"`
	Relations   RelationLists `json:"relations,omitempty"`
	Blocks      []string      `json:"blocks,omitempty"`
	Steps       []IndexedStep `json:"steps,omitempty"`
	Progress    *Progress     `json:"progress,omitempty"`
	Log         LogSummary    `json:"log"`
	Attachments []Attachment  `json:"attachments,omitempty"`
	PartOf      string        `json:"part_of,omitempty"`
	Parts       []string      `json:"parts,omitempty"`
	PartsDone   string        `json:"parts_done,omitempty"`
	PartsMore   int           `json:"parts_more,omitempty"`
	WaitingFor  []string      `json:"waiting_for,omitempty"`
	Blocking    []string      `json:"blocking,omitempty"`
	// NotesCut says the notes are a piece of the whole (cut to the cap, from an offset or one section), and how to get
	// the rest.
	NotesCut *NotesCut `json:"notes_cut,omitempty"`
}

// NotesCut says which piece of the notes an answer carries.
type NotesCut struct {
	Note    string `json:"note"`
	Total   int    `json:"total_chars"`
	Offset  int    `json:"offset"`
	Next    int    `json:"next_offset,omitempty"`
	Section string `json:"section,omitempty"`
	// Sections are the notes' headings with their offsets, when the answer stopped short, to choose a section from.
	Sections []clip.Heading `json:"sections,omitempty"`
}

// getBudget is the most characters one tasks_get answer carries unless max_chars says otherwise.
const getBudget = 4000

// GetOf is one task in full: its fields, its notes, the steps with their index, the progress, and how it stands with
// the others: its parent, its parts, what it waits for, what waits for it.
func (b *Book) GetOf(ctx context.Context, q protocol.TasksGet) (*GetAnswer, error) {
	all, err := b.List(ctx)
	if err != nil {
		return nil, err
	}
	t, err := ResolveTask(all, q.ID, q.Project)
	if err != nil {
		return nil, err
	}
	st := Relations(all, t)
	log, err := b.LogOf(ctx, t)
	if err != nil {
		return nil, err
	}
	closed := ClosedIn(all)
	a := &GetAnswer{Task: t, Progress: ProgressOf(t.Notes, closed), Attachments: Attachments(t.Notes)}
	// each fact once: relations is the readable form of rels, and the steps are listed with their index below
	a.Task.Rels, a.Task.RelExtra = nil, nil
	a.Task.Notes = WithoutSteps(t.Notes)
	for _, r := range RelsOf(t) {
		line := strings.TrimPrefix(RelLine(r), "- "+r.Rel+" ")
		i := indexOfName(a.Relations, r.Rel)
		if i < 0 {
			a.Relations = append(a.Relations, struct {
				Name    string
				Targets []string
			}{Name: r.Rel})
			i = len(a.Relations) - 1
		}
		a.Relations[i].Targets = append(a.Relations[i].Targets, line)
	}
	if a.Blocks, err = BlocksOf(ctx, b.DB, t.ID); err != nil {
		return nil, err
	}
	if len(a.Blocks) == 0 {
		a.Blocks = nil
	}
	for i, s := range Steps(t.Notes, closed) {
		a.Steps = append(a.Steps, IndexedStep{i, s})
	}
	shown := logShown
	if q.Log > 0 {
		shown = q.Log
	}
	a.Log = LogSummary{Path: log.Path, Total: len(log.Lines), Last: log.Lines[max(0, len(log.Lines)-shown):]}
	if st.Parent != nil {
		a.PartOf = b.Line(*st.Parent, all)
	}
	if len(st.Parts) > 0 {
		// the open ones only, as parts_done counts the rest: a long list of closed parts is no news
		for _, x := range st.Parts {
			if x.Status.Closed() {
				continue
			}
			if len(a.Parts) < partsShown {
				a.Parts = append(a.Parts, string(x.Status)+" · "+b.Line(x, all))
			} else {
				a.PartsMore++ // not dropped silently: the answer says how many are left out
			}
		}
		a.PartsDone = fmt.Sprintf("%d/%d", st.PartsDone, len(st.Parts))
	}
	for _, x := range st.WaitingFor {
		a.WaitingFor = append(a.WaitingFor, b.Line(x, all))
	}
	for _, x := range st.Blocking {
		a.Blocking = append(a.Blocking, b.Line(x, all))
	}
	if err := fitNotes(a, q); err != nil {
		return nil, err
	}
	return a, nil
}

// maxSections is how many of the notes' headings a cut answer lists.
const maxSections = 40

// fitNotes cuts the notes so the answer stays within its cap: the notes are what makes a task long. A section or an
// offset reads another piece; max_chars 0 lifts the cap.
func fitNotes(a *GetAnswer, q protocol.TasksGet) error {
	whole := a.Task.Notes
	src := whole
	section := ""
	// the checklist is not in the notes of the answer: its section is the `steps` list
	if stepsHead.MatchString("## " + strings.TrimSpace(q.Section)) {
		a.Task.Notes = ""
		a.NotesCut = &NotesCut{Section: strings.TrimSpace(q.Section), Note: "the steps are listed in `steps`, with their index"}
		return nil
	}
	if q.Section != "" {
		body, _, ok := clip.Section(whole, q.Section)
		if !ok {
			var titles []string
			for _, h := range clip.Headings(whole) {
				titles = append(titles, strconv.Quote(h.Title))
			}
			return refusef("no section %q in the notes of %s (none, or more than one has that title): its sections are %s",
				q.Section, a.Task.ID, strings.Join(titles, ", "))
		}
		src, section = body, strings.TrimSpace(q.Section)
	}
	// the cap is for the notes alone: the parts and the log of a busy task used to take it, and the notes came out cut
	// at a third of it
	room := 0
	if budget := charBudget(q.MaxChars, getBudget); budget > 0 {
		room = max(budget, 500)
	}
	piece, c := clip.WindowJSON(src, q.Offset, room)
	a.Task.Notes = piece
	if !c.Cut && q.Offset == 0 && section == "" {
		return nil
	}
	nc := &NotesCut{Total: c.Total, Offset: c.Offset, Section: section}
	switch {
	case c.Cut:
		nc.Next = c.Next
		nc.Sections = clip.Headings(whole)
		nc.Sections = nc.Sections[:min(len(nc.Sections), maxSections)]
		nc.Note = fmt.Sprintf("truncated, %d characters in total, characters %d-%d shown: tasks_get with offset %d for the rest, "+
			"section for one part by its heading (Decisions…), max_chars 0 for everything", c.Total, c.Offset, c.Next, c.Next)
	case section != "":
		nc.Note = fmt.Sprintf("only the section %q, %d characters", section, c.Total)
	default:
		nc.Note = fmt.Sprintf("the notes from character %d to the end, %d characters in total", c.Offset, c.Total)
	}
	a.NotesCut = nc
	return nil
}

func indexOfName(r RelationLists, name string) int {
	for i, e := range r {
		if e.Name == name {
			return i
		}
	}
	return -1
}

// utf16Prefix is the first n UTF-16 units of s: the length the clients count a title's limit in.
func utf16Prefix(s string, n int) []rune {
	units := 0
	var out []rune
	for _, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if units+w > n {
			break
		}
		units += w
		out = append(out, r)
	}
	return out
}

// ---------------------------------------------------------------- tasks_save

// SaveAnswer is what tasks_save answers: who the task is (id, ref, project, title, status: the caller's hooks read them
// from the top level) and only what the call changed, never the notes the client just sent. The answers of each thing
// it did are merged, a later one overwriting the same key of an earlier.
type SaveAnswer struct {
	ID      string `json:"id"`
	Ref     string `json:"ref,omitempty"`
	Project string `json:"project,omitempty"`
	Title   string `json:"title"`
	Status  Status `json:"status"`
	// UpdatedAt is the task's own Updated; `updated` is the one-line summary of an update.
	UpdatedAt string `json:"updated_at"`
	Added     string `json:"added,omitempty"`
	Updated   string `json:"updated,omitempty"`
	Rearmed   bool   `json:"rearmed,omitempty"`
	NextDue   string `json:"next_due,omitempty"`
	// Log is the page of the task's log, when a log line was added.
	Log string `json:"log,omitempty"`
	// Steps are the steps this call added, ticked or unticked; Progress is the whole checklist, "3/8".
	Steps        []IndexedStep `json:"steps,omitempty"`
	Progress     string        `json:"progress,omitempty"`
	Promoted     []string      `json:"promoted,omitempty"`
	Attachment   *Attachment   `json:"attachment,omitempty"`
	Done         string        `json:"done,omitempty"`
	Unblocked    []string      `json:"unblocked,omitempty"`
	AllPartsDone string        `json:"all_parts_done,omitempty"`
	// The names kept as plain text.
	UnresolvedPeople []string `json:"unresolved_people,omitempty"`
	UnresolvedHint   string   `json:"unresolved_hint,omitempty"`
}

// noteUnresolved fills the people with no page.
func noteUnresolved(a *SaveAnswer, t Task) {
	// no page is made for a name nobody answers to: a role or a company stays a plain name, a person gets a page
	if people := UnresolvedPeople(t); len(people) > 0 {
		a.UnresolvedPeople = people
		a.UnresolvedHint = "kept as plain names, no page made: if one is a real person, memory_write their page " +
			"(`aka:` in its front matter for other names) and the task points at it on its next write"
	}
}

// inputOf is the fields of a save as an input.
func inputOf(s protocol.TasksSave) Input {
	return Input{
		Title: s.Title, Status: Status(s.Status), Due: s.Due, Time: s.Time, Priority: s.Priority, Repeat: s.Repeat,
		Notes: s.Notes, Ref: s.Ref, Stage: s.Stage, Labels: s.Labels, Snooze: s.Snooze, About: s.About,
		Related: s.Related, BlockedBy: s.BlockedBy, AssignedTo: s.AssignedTo, WorkedBy: s.WorkedBy,
		WaitingOn: s.WaitingOn, For: s.For, Project: s.Project, Parent: s.Parent,
	}
}

// Save adds or changes a task, and in the same call writes a log or decision line, edits one passage of the notes,
// adds, ticks, unticks or promotes steps, and attaches a link. Without an ID it adds one; Done closes it (a repeating
// task moves to its next day).
func (b *Book) Save(ctx context.Context, s protocol.TasksSave) (*SaveAnswer, error) {
	in := inputOf(s)
	toStatus := Input{}
	if in.Status != "" && in.Status != Done {
		toStatus.Status = in.Status
	}
	in.Status = ""
	project := ""
	if s.Project != nil {
		project = *s.Project
	}
	a := &SaveAnswer{}
	did := false
	id := s.ID
	switch {
	case id == "":
		if s.Title == "" {
			return nil, refusef("a new task needs: title")
		}
		all, err := b.List(ctx)
		if err != nil {
			return nil, err
		}
		resolved, err := ResolveRefs(all, in, nil)
		if err != nil {
			return nil, err
		}
		t, err := b.Add(ctx, resolved)
		if err != nil {
			return nil, err
		}
		a.Added = b.Line(t, nil)
		noteUnresolved(a, t)
		id, did = t.ID, true
		// the add has no status: a new task that starts doing is added, then moved
		if toStatus.Status != "" {
			if err := b.saveUpdate(ctx, a, id, project, toStatus); err != nil {
				return nil, err
			}
		}
	default:
		withStatus := in
		withStatus.Status = toStatus.Status
		onlyProject := withStatus
		onlyProject.Project = nil
		// `project` alone only says where to look for a ref, so it is not a change
		if !reflect.DeepEqual(onlyProject, Input{}) {
			if err := b.saveUpdate(ctx, a, id, project, withStatus); err != nil {
				return nil, err
			}
			did = true
		}
	}
	if s.Log != "" || s.Decision != "" {
		if s.Log != "" {
			if err := b.saveNote(ctx, a, id, project, SectionLog, s.Log); err != nil {
				return nil, err
			}
		}
		if s.Decision != "" {
			if err := b.saveNote(ctx, a, id, project, SectionDecisions, s.Decision); err != nil {
				return nil, err
			}
		}
		did = true
	}
	if s.Find != "" || s.Replace != "" {
		if s.Find == "" {
			return nil, refusef("editing the notes needs: find")
		}
		t, err := b.Find(ctx, id, project)
		if err != nil {
			return nil, err
		}
		r, err := b.UpdateWith(ctx, t.ID, func(cur Task) (Input, error) {
			n, err := EditNotes(cur.Notes, s.Find, s.Replace)
			return Input{Notes: S(n)}, err
		}, "")
		if err != nil {
			return nil, err
		}
		a.Updated, did = b.Line(r.Task, nil), true
	}
	if len(s.StepsAdd)+len(s.StepsCheck)+len(s.StepsUncheck)+len(s.StepsPromote) > 0 {
		if err := b.saveSteps(ctx, a, id, project, s); err != nil {
			return nil, err
		}
		did = true
	}
	if s.Attach != "" {
		t, err := b.Find(ctx, id, project)
		if err != nil {
			return nil, err
		}
		r, err := b.UpdateWith(ctx, t.ID, func(cur Task) (Input, error) {
			n, err := AddAttachment(cur.Notes, s.Attach, s.AttachLabel)
			return Input{Notes: S(n)}, err
		}, "")
		if err != nil {
			return nil, err
		}
		a.Updated, did = b.Line(r.Task, nil), true
		if all := Attachments(r.Task.Notes); len(all) > 0 {
			a.Attachment = &all[len(all)-1]
		}
	}
	if s.Status == string(Done) {
		if err := b.saveDone(ctx, a, id, project); err != nil {
			return nil, err
		}
		did = true
	}
	if !did {
		return nil, refusef("nothing to do: give a title to add a task, or an id with what to change")
	}
	t, err := b.Find(ctx, id, project)
	if err != nil {
		return nil, err
	}
	a.ID, a.Ref, a.Project, a.Title, a.Status, a.UpdatedAt = t.ID, t.Ref, t.Project, t.Title, t.Status, t.Updated
	return a, nil
}

// saveUpdate is the update tool: a change by id or ref.
func (b *Book) saveUpdate(ctx context.Context, a *SaveAnswer, id, project string, in Input) error {
	all, err := b.List(ctx)
	if err != nil {
		return err
	}
	cur, err := ResolveTask(all, id, project)
	if err != nil {
		return err
	}
	resolved, err := ResolveRefs(all, in, &cur)
	if err != nil {
		return err
	}
	// notes written whole may come from a tasks_get answer, which has no checklist: it is kept
	r, err := b.UpdateWith(ctx, cur.ID, func(now Task) (Input, error) {
		if resolved.Notes != nil {
			resolved.Notes = S(KeepSteps(now.Notes, *resolved.Notes))
		}
		return resolved, nil
	}, "")
	if err != nil {
		return err
	}
	a.Updated = b.Line(r.Task, all)
	if r.Rearmed {
		a.Rearmed, a.NextDue = true, r.Task.Due
	}
	noteUnresolved(a, r.Task)
	return nil
}

// saveNote is the note tool: a dated line to a task's log or decisions.
func (b *Book) saveNote(ctx context.Context, a *SaveAnswer, id, project string, section NoteSection, text string) error {
	t, err := b.Find(ctx, id, project)
	if err != nil {
		return err
	}
	day := DayOf(b.now(), b.Zone)
	var task Task
	if section == SectionLog {
		if task, err = b.Log(ctx, t.ID, text, day); err != nil {
			return err
		}
		a.Log = LogPath(t.ID)
	} else {
		r, err := b.UpdateWith(ctx, t.ID, func(cur Task) (Input, error) {
			n, err := AddNote(cur.Notes, section, text, day)
			return Input{Notes: S(n)}, err
		}, "")
		if err != nil {
			return err
		}
		task = r.Task
	}
	a.Updated = b.Line(task, nil)
	return nil
}

// saveSteps is the steps tool: add, tick and untick steps by index, then promote.
func (b *Book) saveSteps(ctx context.Context, a *SaveAnswer, id, project string, s protocol.TasksSave) error {
	t, err := b.Find(ctx, id, project)
	if err != nil {
		return err
	}
	taskID := t.ID
	if len(s.StepsCheck)+len(s.StepsUncheck)+len(s.StepsAdd) > 0 {
		r, err := b.UpdateWith(ctx, t.ID, func(cur Task) (Input, error) {
			notes := cur.Notes
			for _, i := range s.StepsCheck {
				notes = SetStep(notes, i, Ticked(true))
			}
			for _, i := range s.StepsUncheck {
				notes = SetStep(notes, i, Ticked(false))
			}
			for _, st := range s.StepsAdd {
				var err error
				if notes, err = AddStep(notes, st); err != nil {
					return Input{}, err
				}
			}
			return Input{Notes: S(notes)}, nil
		}, "")
		if err != nil {
			return err
		}
		taskID = r.Task.ID
	}
	var promoted []Task
	if len(s.StepsPromote) > 0 {
		p, err := b.Promote(ctx, taskID, s.StepsPromote)
		if err != nil {
			return err
		}
		promoted = p.Promoted
	}
	all, err := b.List(ctx)
	if err != nil {
		return err
	}
	now := t
	for _, x := range all {
		if x.ID == taskID {
			now = x
		}
	}
	a.Updated = b.Line(now, all)
	steps := Steps(now.Notes, ClosedIn(all))
	touched := map[int]bool{}
	for _, i := range slices.Concat(s.StepsCheck, s.StepsUncheck) {
		touched[i] = true
	}
	for i := len(steps) - len(s.StepsAdd); i < len(steps); i++ {
		touched[i] = true
	}
	for i, st := range steps {
		if touched[i] {
			a.Steps = append(a.Steps, IndexedStep{i, st})
		}
	}
	if p := ProgressOf(now.Notes, ClosedIn(all)); p != nil {
		a.Progress = fmt.Sprintf("%d/%d", p.Done, p.Total)
	}
	if len(promoted) > 0 {
		a.Promoted = b.lines(promoted, all)
	}
	return nil
}

// saveDone is the done tool: it closes a task and says what it unblocked, and when it was the last open part of a larger
// one.
func (b *Book) saveDone(ctx context.Context, a *SaveAnswer, id, project string) error {
	t, err := b.Find(ctx, id, project)
	if err != nil {
		return err
	}
	r, err := b.Update(ctx, t.ID, Input{Status: Done}, "")
	if err != nil {
		return err
	}
	all, err := b.List(ctx)
	if err != nil {
		return err
	}
	a.Done = b.Line(r.Task, nil)
	if r.Rearmed {
		a.Rearmed, a.NextDue = true, r.Task.Due
	}
	for _, x := range all {
		if !x.Status.Closed() && slices.Contains(x.BlockedBy, r.Task.ID) && !IsBlocked(all, x) {
			a.Unblocked = append(a.Unblocked, b.Line(x, all))
		}
	}
	if r.Task.Parent != "" {
		for _, p := range all {
			if p.ID == r.Task.Parent && !p.Status.Closed() {
				last := true
				for _, part := range Relations(all, p).Parts {
					if !part.Status.Closed() {
						last = false
					}
				}
				if last {
					a.AllPartsDone = b.Line(p, all)
				}
			}
		}
	}
	return nil
}
