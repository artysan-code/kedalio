package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"kedalio/protocol"
)

// The tasks' tools. The task book does the work; these decode the arguments and
// shape nothing: the book's answers are the JSON the tools have always had.

var (
	statuses = []string{"todo", "doing", "waiting", "done", "dropped"}
	groups   = []string{"open", "active", "paused", "closed"}
)

func (s *Server) taskTools() []*tool {
	return []*tool{
		{
			name: protocol.ToolTasksBrief, kind: read, upFront: true,
			description: "Debrief: overdue, today, tomorrow, waiting, agents' work, stalled. For the day's plan.",
			schema:      object(nil),
			run:         func(ctx context.Context, _ []byte) (any, error) { return s.cfg.Tasks.BriefOf(ctx) },
		},
		{
			name: protocol.ToolTasksList, kind: read, upFront: true,
			description: "Search tasks, open unless status or group given. Cut at ~6000 chars (says how many are missing); notes/full add more. stalled: stopped moving. view: a saved view; views lists them.",
			schema: object(nil,
				choice("status", append([]string{"all"}, statuses...)),
				choice("group", groups),
				str("project"),
				str("text"),
				str("from", "due from, YYYY-MM-DD"),
				str("to", "due until"),
				str("ref"),
				str("stage"),
				str("label"),
				str("parent"),
				str("for"),
				str("assigned_to"),
				str("worked_by", "engine:claude, agent:<name>, run:<id>"),
				str("waiting_on"),
				str("about", "page name"),
				str("related"),
				str("blocked_by", "tasks waiting for this id"),
				str("blocks", "tasks this id waits for"),
				boolean("stalled"),
				str("view", "replaces the other filters"),
				boolean("views", "list them"),
				boolean("notes"),
				boolean("full"),
				integer("limit", 1, 500),
				integer("max_chars", 0, 0, "default 6000; 0 all")),
			run: func(ctx context.Context, raw []byte) (any, error) {
				q, err := decode[protocol.TasksList](raw)
				if err != nil {
					return nil, err
				}
				if q.Views {
					return s.cfg.Tasks.Views(ctx)
				}
				return s.cfg.Tasks.ListOf(ctx, q)
			},
		},
		{
			name: protocol.ToolTasksGet, kind: read,
			description: "One task in full: fields, notes, last 3 log lines, progress, relations, open parts. Read once per session (tasks_save answers with what changed). Notes cut at max_chars, says how to read on.",
			schema: object([]string{"id"},
				str("id", "id (t-…) or ref"),
				str("project", "for a ref"),
				integer("offset", 0, 0, "notes from character"),
				str("section", "notes section by heading"),
				integer("log", 1, 0, "default 3"),
				integer("max_chars", 0, 0, "notes, default 4000; 0 all")),
			run: func(ctx context.Context, raw []byte) (any, error) {
				q, err := decode[protocol.TasksGet](raw)
				if err != nil {
					return nil, err
				}
				return s.cfg.Tasks.GetOf(ctx, q)
			},
		},
		{
			name: protocol.ToolTasksSave, kind: write,
			description: "Add a task (no id, needs title) or change one (id); log, decision, notes edit, steps, attach in the same call; null clears a field. done is refused while steps are open (steps_check or drop); a repeating task moves to its next day.",
			schema: object(nil,
				str("id", "id (t-…) or ref"),
				str("title", "max 80 chars"),
				choice("status", statuses),
				nullable(str("project", "folder under $HOME, else a short name")),
				nullable(str("ref", "project's name for it (TASK-495)")),
				nullable(str("parent", "id or ref")),
				nullable(str("notes", "whole body (`## Steps`, `## Decisions`, `## Attachments`), replaces it (the steps stay if it has none; change them with steps_*); for a part use find/replace, log, steps_add, attach")),
				nullable(str("due", "YYYY-MM-DD; resolve relative days from `today` in session_start or tasks_brief")),
				nullable(str("time", "HH:MM")),
				nullable(integer("priority", 1, 3, "1 high, 2 normal, 3 low")),
				nullable(str("stage", "project flow step, max 40 chars")),
				nullable(strings_("labels")),
				nullable(strings_("about", "page names, whole list")),
				nullable(strings_("related")),
				nullable(strings_("blocked_by", "ids or refs, whole list")),
				nullable(str("assigned_to", "a person, default the owner")),
				nullable(str("worked_by", "engine:claude, agent:<name>, run:<id>")),
				nullable(str("waiting_on", "name, run:<id>, YYYY-MM-DD or task id")),
				nullable(str("for", "human it is for, default the owner")),
				nullable(str("repeat", "iCal subset: FREQ=DAILY|WEEKLY|MONTHLY|YEARLY, INTERVAL, BYDAY (MO,1MO), BYMONTHDAY (-1 last), UNTIL; no COUNT. Needs `due`. done unticks steps, clears blocked_by, moves to the next day; closes after UNTIL")),
				nullable(str("snooze", "YYYY-MM-DD: not stalled until then")),
				str("log", "log line"),
				str("decision", "decision line"),
				str("find", "notes: passage occurring once"),
				str("replace", "notes: replacement"),
				strings_("steps_add"),
				ints("steps_check", "indexes"),
				ints("steps_uncheck"),
				ints("steps_promote", "indexes to make tasks"),
				str("attach", "link, project path or [[page]]"),
				str("attach_label")),
			run: func(ctx context.Context, raw []byte) (any, error) {
				q, err := decode[protocol.TasksSave](clearNulls(raw))
				if err != nil {
					return nil, err
				}
				if err := s.checkTask(ctx, q); err != nil {
					return nil, err
				}
				if err := s.taskSecrets(ctx, q); err != nil {
					return nil, err
				}
				if q.ID == "" && q.Title == "" {
					return nil, needs("a new task", field{"title", true})
				}
				a, err := s.cfg.Tasks.Save(ctx, q)
				if err != nil {
					return nil, err
				}
				if a == nil {
					return nil, refusal("(arguments)", "nothing to do", "give a title to add a task, or an id with what to change")
				}
				return a, nil
			},
		},
	}
}

// clearNulls turns a null in the arguments into the empty value of its field: the book clears what it is given empty,
// and the tools have always cleared with null, which a pointer field cannot tell from a field left out.
func clearNulls(raw []byte) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	empty := map[string]string{"priority": "0", "labels": "[]", "about": "[]", "related": "[]", "blocked_by": "[]"}
	for k, v := range m {
		if !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			continue
		}
		if e, ok := empty[k]; ok {
			m[k] = json.RawMessage(e)
		} else {
			m[k] = json.RawMessage(`""`)
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// taskSecrets refuses a tasks_save whose text holds what the memory never keeps (looksSecret): a task is a page of the
// memory like any other, and its notes, log and decisions are read from every client. The whole notes are judged by
// what they bring, as a page is (newSecretPairs): a task that already holds a pair stays editable.
func (s *Server) taskSecrets(ctx context.Context, q protocol.TasksSave) error {
	fields := map[string]string{"title": q.Title, "log": q.Log, "decision": q.Decision, "replace": q.Replace,
		"attach": q.Attach, "attach_label": q.AttachLabel, "stage": derefString(q.Stage), "waiting_on": derefString(q.WaitingOn),
		"for": derefString(q.For), "assigned_to": derefString(q.AssignedTo), "ref": derefString(q.Ref)}
	for i, st := range q.StepsAdd {
		fields[fmt.Sprintf("steps_add[%d]", i)] = st
	}
	if q.Labels != nil {
		fields["labels"] = strings.Join(*q.Labels, "\n")
	}
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if s.looksSecret(fields[name]) {
			return secretIn(name)
		}
	}
	if q.Notes == nil && q.Find == "" {
		return nil
	}
	was := ""
	if q.ID != "" {
		if t, err := s.cfg.Tasks.Find(ctx, q.ID, derefString(q.Project)); err == nil {
			was = t.Notes
		}
	}
	// the notes as they will be: a replace is judged in place, for a pair can be made of the old text and the new
	notes, field := was, "replace"
	if q.Notes != nil {
		notes, field = *q.Notes, "notes"
	}
	if q.Find != "" {
		notes = strings.Replace(notes, q.Find, q.Replace, 1)
	}
	if s.newSecret(was, notes) || len(newSecretPairs(was, notes)) > 0 {
		return secretIn(field)
	}
	return nil
}

func secretIn(field string) error {
	return refusal(field, "it looks like a secret (a password, a token, a credential with its value): never in the memory",
		"say where the secret is kept (the service's panel, the secrets file), never the secret")
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
