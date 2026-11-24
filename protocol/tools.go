package protocol

import "encoding/json"

// The Kedalio MCP tools: the same for every client, claude.ai and Claude Code. The names and arguments are the contract
// that skills written for the tools rely on, so a change to them also updates plugin/skills. Each tool's answer is JSON;
// its shape is set by the code that builds it.

// Tool names.
const (
	ToolMemoryRecall  = "memory_recall"
	ToolMemoryRead    = "memory_read"
	ToolMemoryList    = "memory_list"
	ToolMemoryWrite   = "memory_write"
	ToolMemoryDelete  = "memory_delete"
	ToolMemorySources = "memory_sources"
	ToolMemoryUpkeep  = "memory_upkeep"
	ToolTasksBrief    = "tasks_brief"
	ToolTasksList     = "tasks_list"
	ToolTasksGet      = "tasks_get"
	ToolTasksSave     = "tasks_save"

	// The session, project and settings tools: not part of the memory and tasks set above.
	ToolSessionStart = "session_start"
	ToolProjectsLink = "projects_link"
	ToolSettingsGet  = "settings_get"
	ToolSettingsSet  = "settings_set"

	// The owner's approval of a new client's sign-in code (ADR 0007).
	ToolClientsApprove = "clients_approve"
)

// MemoryRecall: what the memory holds about a subject, in one call. An empty Query gives the core (who the owner is,
// what changed lately). Mode "search" lists matching pages and passages instead.
type MemoryRecall struct {
	Query        string `json:"query,omitempty"`
	Mode         string `json:"mode,omitempty"`   // recall (default) or search
	Budget       int    `json:"budget,omitempty"` // tokens of answer, 300–4000, default 2000
	Limit        int    `json:"limit,omitempty"`  // search only, 1–50
	IncludeTasks bool   `json:"include_tasks,omitempty"`
	// Version, with Mode search, reads the documents of the collections as they were at that version (the superseded
	// ones included); by default the current ones.
	Version string `json:"version,omitempty"`
}

// MemoryRead: a page in full, its rev (the Base of a later write) and its links both ways.
type MemoryRead struct {
	Path    string `json:"path,omitempty"` // folder/name.md
	Card    string `json:"card,omitempty"` // a project's path: its card page
	Rev     int    `json:"rev,omitempty"`  // an older version
	History bool   `json:"history,omitempty"`
	// Offset and Section read a long page in pieces: the body from that character, or the one section under that
	// heading. MaxChars caps the body (default 4000 characters; 0 is all of it).
	Offset   int    `json:"offset,omitempty"`
	Section  string `json:"section,omitempty"`
	MaxChars *int   `json:"max_chars,omitempty"`
	// Links true lists the pages that link to this one and the related ones; by default they are only counted (the
	// outgoing links are always listed).
	Links bool `json:"links,omitempty"`
}

// MemoryList: the pages under a folder, or of a kind.
type MemoryList struct {
	Folder     string `json:"folder,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Limit      int    `json:"limit,omitempty"` // 1–1000
	Collection string `json:"collection,omitempty"`
	// What picks the listing: pages (default), collections, documents or health.
	What string `json:"what,omitempty"`
}

// MemoryWrite changes the memory; Op picks the operation and the fields it needs.
type MemoryWrite struct {
	Op      string `json:"op"` // write, edit, append, remember, move, restore
	Path    string `json:"path,omitempty"`
	Card    string `json:"card,omitempty"`
	Body    string `json:"body,omitempty"`     // write: the whole Markdown
	BaseRev *int   `json:"base_rev,omitempty"` // write, edit: the rev it started from; 0 creates only, nil is not given
	Find    string `json:"find,omitempty"`     // edit
	Replace string `json:"replace,omitempty"`  // edit
	Where   string `json:"where,omitempty"`    // append: diary (default) or inbox
	Text    string `json:"text,omitempty"`     // append, remember
	About   string `json:"about,omitempty"`    // remember
	From    string `json:"from,omitempty"`     // move
	To      string `json:"to,omitempty"`       // move
	Rev     int    `json:"rev,omitempty"`      // restore
	// KeptIn names the pages the text a write or edit takes out of this one was written to; the upkeep's writes are
	// refused a sentence that is on neither this page nor those (write, edit).
	KeptIn []string `json:"kept_in,omitempty"`
	// Distinct says a page with a similar title exists and this is another subject (write).
	Distinct bool `json:"distinct,omitempty"`
	// Provenance is set by the core, never by the caller: "owner", or the outside source a page was written from
	// (0003: text from others).
	Provenance string `json:"-"`
}

// MemoryDelete: a page (a revision: it can be restored), a document or a collection.
type MemoryDelete struct {
	What       string `json:"what"` // page, document or collection
	Path       string `json:"path,omitempty"`
	Collection string `json:"collection,omitempty"`
	ID         string `json:"id,omitempty"`
	// KeptIn: the pages the removed page's text was written to (page; the upkeep's deletes are refused a sentence that
	// is on none of them).
	KeptIn []string `json:"kept_in,omitempty"`
}

// MemoryUpkeep: the nightly upkeep of the memory, run by an agent that holds the upkeep token (the owner's token may
// read the worklist and the runs). Op worklist counts what is due per job, or with Job lists that job's items, each
// with a key. Op start opens a run (refused while another is open), sweeps the tasks' shape and answers the run id and
// the job's items. Op end closes the run with a status and a short report. Op runs answers the last ten runs, newest
// first, with their reports. Op dismiss records rejected duplicates or conflicts (Items are their keys) so they are not
// offered again. Op inbox_placed removes the inbox entries named in Items once their sentences are on the pages in
// KeptIn.
type MemoryUpkeep struct {
	Op     string   `json:"op"`                // worklist, start, end, runs, dismiss or inbox_placed
	Job    string   `json:"job,omitempty"`     // weeks, logs, hubs, duplicates, conflicts or inbox
	Run    string   `json:"run,omitempty"`     // end: the id start gave
	Status string   `json:"status,omitempty"`  // end: ok, partial or failed
	Report string   `json:"report,omitempty"`  // end: what was done, at most 2000 characters
	Items  []string `json:"items,omitempty"`   // dismiss and inbox_placed: item keys
	KeptIn []string `json:"kept_in,omitempty"` // inbox_placed: the pages the entries' text was written to
}

// MemorySources: collections of documents (a stack's docs, a contract). Op save makes one, or changes it with ID; it
// has a hub page the core keeps. Op add_document adds a URL, or a file given as FileName with Text or Base64, to a
// collection; the same URL or name replaces it.
type MemorySources struct {
	Op          string  `json:"op"` // save or add_document
	ID          string  `json:"id,omitempty"`
	Name        string  `json:"name,omitempty"`
	Description string  `json:"description,omitempty"`
	Version     *string `json:"version,omitempty"` // free string; older versions are kept, superseded
	Important   *bool   `json:"important,omitempty"`
	Collection  string  `json:"collection,omitempty"` // add_document: the collection's id
	URL         string  `json:"url,omitempty"`
	FileName    string  `json:"file_name,omitempty"` // with its ending: guide.md, manual.pdf
	Text        *string `json:"text,omitempty"`
	Base64      *string `json:"base64,omitempty"`
	Title       string  `json:"title,omitempty"`
	// ImageDescription says what an image shows, in words, so it can be found.
	ImageDescription string `json:"image_description,omitempty"`
}

// TasksList: open tasks unless Status or Group says otherwise, by the filters given.
type TasksList struct {
	Status     string `json:"status,omitempty"` // all, todo, doing, waiting, done, dropped
	Group      string `json:"group,omitempty"`  // open, active, paused, closed
	Project    string `json:"project,omitempty"`
	Text       string `json:"text,omitempty"`
	From       string `json:"from,omitempty"` // due on or after, YYYY-MM-DD
	To         string `json:"to,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Stage      string `json:"stage,omitempty"`
	Label      string `json:"label,omitempty"`
	Parent     string `json:"parent,omitempty"`
	For        string `json:"for,omitempty"`
	AssignedTo string `json:"assigned_to,omitempty"`
	WorkedBy   string `json:"worked_by,omitempty"`
	WaitingOn  string `json:"waiting_on,omitempty"`
	About      string `json:"about,omitempty"`
	Related    string `json:"related,omitempty"`
	BlockedBy  string `json:"blocked_by,omitempty"`
	Blocks     string `json:"blocks,omitempty"`
	Stalled    bool   `json:"stalled,omitempty"`
	View       string `json:"view,omitempty"`
	Views      bool   `json:"views,omitempty"` // list the saved views instead
	Notes      bool   `json:"notes,omitempty"`
	Full       bool   `json:"full,omitempty"`
	Limit      int    `json:"limit,omitempty"` // 1–500
	// MaxChars caps the answer's tasks (default 6000 characters; 0 is no cap, and a full task keeps its whole notes).
	MaxChars *int `json:"max_chars,omitempty"`
}

// TasksGet: one task in full.
type TasksGet struct {
	ID      string `json:"id"` // t-… or a ref
	Project string `json:"project,omitempty"`
	// Offset and Section read long notes in pieces: from that character, or the one section under that heading
	// (Decisions…; Steps answers with the steps list). Log is how many of the log's last lines to show (default 3). MaxChars caps the notes
	// (default 4000 characters; 0 is all of it).
	Offset   int    `json:"offset,omitempty"`
	Section  string `json:"section,omitempty"`
	Log      int    `json:"log,omitempty"`
	MaxChars *int   `json:"max_chars,omitempty"`
}

// TasksSave adds a task (no ID) or changes one. Pointer fields distinguish "leave it" (nil) from "clear it".
type TasksSave struct {
	ID           string    `json:"id,omitempty"`
	Title        string    `json:"title,omitempty"` // required to add, at most 80 characters
	Status       string    `json:"status,omitempty"`
	Project      *string   `json:"project,omitempty"`
	Ref          *string   `json:"ref,omitempty"`
	Parent       *string   `json:"parent,omitempty"`
	Notes        *string   `json:"notes,omitempty"`
	Due          *string   `json:"due,omitempty"`
	Time         *string   `json:"time,omitempty"`
	Priority     *int      `json:"priority,omitempty"`
	Stage        *string   `json:"stage,omitempty"`
	Labels       *[]string `json:"labels,omitempty"`
	About        *[]string `json:"about,omitempty"`
	Related      *[]string `json:"related,omitempty"`
	BlockedBy    *[]string `json:"blocked_by,omitempty"`
	AssignedTo   *string   `json:"assigned_to,omitempty"`
	WorkedBy     *string   `json:"worked_by,omitempty"`
	WaitingOn    *string   `json:"waiting_on,omitempty"`
	For          *string   `json:"for,omitempty"`
	Repeat       *string   `json:"repeat,omitempty"`
	Snooze       *string   `json:"snooze,omitempty"`
	Log          string    `json:"log,omitempty"`
	Decision     string    `json:"decision,omitempty"`
	Find         string    `json:"find,omitempty"`
	Replace      string    `json:"replace,omitempty"`
	StepsAdd     []string  `json:"steps_add,omitempty"`
	StepsCheck   []int     `json:"steps_check,omitempty"`
	StepsUncheck []int     `json:"steps_uncheck,omitempty"`
	StepsPromote []int     `json:"steps_promote,omitempty"`
	Attach       string    `json:"attach,omitempty"`
	AttachLabel  string    `json:"attach_label,omitempty"`
}

// SessionStart: what a session learns when it starts in a folder, in one call. Only reads.
type SessionStart struct {
	Dir     string `json:"dir,omitempty"`     // the session's folder
	Remote  string `json:"remote,omitempty"`  // its git remote
	Project string `json:"project,omitempty"` // the project, when known
}

// ProjectsLink answers the folder question session_start asks (it says `folder: {ask}`).
type ProjectsLink struct {
	Op      string `json:"op"`                // link, create or ignore
	Dir     string `json:"dir"`               // the session's folder, an absolute path
	Project string `json:"project,omitempty"` // link: the project's path
	Name    string `json:"name,omitempty"`    // create: the new project's path, lower case, a/b/c
	Repo    string `json:"repo,omitempty"`    // the folder's git remote; it becomes the project's repository
	Branch  string `json:"branch,omitempty"`  // the repository's default branch
}

// SettingsGet: a setting of the owner's account. Without Key, the list of keys and the shape of each value.
type SettingsGet struct {
	Key string `json:"key,omitempty"`
}

// SettingsSet changes a setting; Value has the shape the key's entry in settings_get gives.
type SettingsSet struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// ClientsApprove approves the code a new local client's sign-in page shows. Without Confirm it only shows who asks;
// with Confirm and the same Host it approves, and only a request that came from the caller's own address. A cloud client's
// code, and a passkey's, are approved on the server, never here.
type ClientsApprove struct {
	Code    string `json:"code"`
	Confirm bool   `json:"confirm,omitempty"`
	// Host is the redirect host the first call named, repeated with Confirm so that it shows in the owner's permission prompt.
	Host string `json:"host,omitempty"`
}
