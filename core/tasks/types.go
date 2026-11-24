// Package tasks is the account's tasks: one Markdown page each under tasks/ in the memory, with refs, relations,
// recurrence, stalled detection and saved views.
//
// The page is the truth. The tables of index.go are derived from it inside the page's own write transaction (the
// memory.TaskIndexer hook), so filters and views never parse hundreds of pages.
package tasks

// Status is where a task stands; its column on the owner's board.
type Status string

const (
	Todo    Status = "todo"
	Doing   Status = "doing"
	Waiting Status = "waiting"
	Done    Status = "done"
	Dropped Status = "dropped"
)

// Statuses are the five statuses in board order.
var Statuses = []Status{Todo, Doing, Waiting, Done, Dropped}

// ValidStatus reports whether s is one of the five.
func ValidStatus(s Status) bool {
	for _, x := range Statuses {
		if x == s {
			return true
		}
	}
	return false
}

// Group is what the statuses belong to: views and stale detection speak of groups.
type Group string

const (
	GroupOpen   Group = "open"
	GroupActive Group = "active"
	GroupPaused Group = "paused"
	GroupClosed Group = "closed"
)

// Groups are the four groups.
var Groups = []Group{GroupOpen, GroupActive, GroupPaused, GroupClosed}

// GroupOf is the group a status belongs to.
func GroupOf(s Status) Group {
	switch s {
	case Todo:
		return GroupOpen
	case Doing:
		return GroupActive
	case Waiting:
		return GroupPaused
	}
	return GroupClosed
}

// Closed reports whether a status ends the task.
func (s Status) Closed() bool { return s == Done || s == Dropped }

// RelKind is what a relation points at. Pages and tasks are [[links]] and edges of the graph; a run, an engine, an
// agent or a date is not a page, so it is written plain (`worked_by run:r-1`); a name is a person's name with no page
// (a role, a company: `assigned_to name:lead`) and lives in the task index only.
type RelKind string

const (
	KindPage   RelKind = "page"
	KindTask   RelKind = "task"
	KindRun    RelKind = "run"
	KindEngine RelKind = "engine"
	KindAgent  RelKind = "agent"
	KindDate   RelKind = "date"
	KindName   RelKind = "name"
)

// Rel is one edge of a task.
type Rel struct {
	Rel  string  `json:"rel"`
	Kind RelKind `json:"kind"`
	// Target is a page's path or name as written (no .md), a task id, a run's id, an engine's or agent's name, a day.
	Target string `json:"target"`
	// Note is the text in brackets after it: `(since 2025)`.
	Note string `json:"note,omitempty"`
}

// Field is a front matter line the task book does not know (Obsidian's tags, say), kept and written back.
type Field struct {
	Key   string
	Value any
}

// Task is a task as its page says it.
type Task struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   Status `json:"status"`
	Due      string `json:"due,omitempty"`  // YYYY-MM-DD
	Time     string `json:"time,omitempty"` // HH:MM
	Remind   *int   `json:"remind,omitempty"`
	Priority int    `json:"priority,omitempty"` // 1 high, 2, 3; 0 unset
	// Repeat is a rule (`FREQ=WEEKLY;BYDAY=MO`, rrule.go) or one of the four old words.
	Repeat string `json:"repeat,omitempty"`
	// RepeatFrom is what the next day is counted from: "due" (default) or "done".
	RepeatFrom string `json:"repeat_from,omitempty"`
	Ref        string `json:"ref,omitempty"`   // the project's own name for it: TASK-495
	Stage      string `json:"stage,omitempty"` // spec, discovery, release pending…: free, lower case
	// Labels is empty when the task has none.
	Labels  []string `json:"labels,omitempty"`
	Detail  string   `json:"detail,omitempty"` // where the full detail lives: a path, a path#anchor, a URL, [[page]]
	Created string   `json:"created"`
	Updated string   `json:"updated"`
	Done    string   `json:"done,omitempty"`
	// Snooze is «looked at, fine until» this day: the task is not called stalled until then, or until it sees activity.
	Snooze string `json:"snooze,omitempty"`
	// SnoozedAt is the Updated the snooze was set at: a later write to the task ends it early.
	SnoozedAt string `json:"snoozed_at,omitempty"`
	Notes     string `json:"notes,omitempty"`
	// Rels is the `## Relations` section, the truth for who, what about, what it is part of. Nil means «not read yet»
	// (a task built from the old fields); an empty list means none.
	Rels []Rel `json:"rels,omitempty"`
	// RelExtra are lines of `## Relations` that name no known target: kept as written.
	RelExtra []string `json:"relExtra,omitempty"`

	// Derived from Rels and never written to the file, for what reads the old shape:
	Owner     string   `json:"owner,omitempty"`   // assigned_to's name, else the agent that works on it (claude)
	Project   string   `json:"project,omitempty"` // the page it is part_of
	Parent    string   `json:"parent,omitempty"`  // the task it is part_of
	BlockedBy []string `json:"blocked_by,omitempty"`

	// Extra is every other front matter line of the page, in the order it had them.
	Extra []Field `json:"-"`
}

// Input is a change to a task. A nil pointer leaves the field; a pointer to "" (or an empty list, Priority 0, a
// negative Remind) clears it. Lists and relations are given whole.
type Input struct {
	Title      string
	Status     Status
	Due        *string // YYYY-MM-DD
	Time       *string // HH:MM
	Remind     *int    // minutes before Time; negative clears
	Priority   *int    // 1 high, 2, 3; 0 clears
	Repeat     *string
	RepeatFrom *string // "due" or "done"
	Notes      *string
	Ref        *string
	Stage      *string
	Labels     *[]string
	Detail     *string
	// Snooze is a day: the task is not called stalled until then, or until it sees activity.
	Snooze *string

	// The relations:
	About     *[]string // the concepts, entities and people it concerns (page names)
	PartOf    *[]string // what it is a part of: a parent task (id) and/or project pages
	BlockedBy *[]string
	Related   *[]string
	// AssignedTo is a person (never an agent: an agent is WorkedBy).
	AssignedTo *string
	// WorkedBy is an agent, an engine or a run: `agent:name`, `engine:claude`, `run:r-…`.
	WorkedBy *string
	// WaitingOn is, with status waiting, a person, a run, a day or a task.
	WaitingOn *string
	// For is the human it is for; the account when none.
	For *string
	// Project and Parent are the old arguments, mapped to part_of.
	Project *string
	Parent  *string
}

// S makes the pointer an Input takes.
func S(s string) *string { return &s }
