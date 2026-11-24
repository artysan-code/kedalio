package tasks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// A task as its Markdown document, and back.
//
// Two shapes are read, one is written. The new shape: `kind: task` and scalars in the front matter, the relations in a
// `## Relations` section at the end. The old shape: `owner`, `project`, `parent` and `blocked_by` in the front matter.
// Reading either gives the same task; a task is written in the new shape whenever it is written. Pages in the old shape
// still exist in a live database, so reading them must keep working.

// notFront are the keys a task holds that are not front matter lines.
var notFront = []string{"notes", "rels", "relExtra"}

var (
	frontRe    = regexp.MustCompile(`^---\n((?s:.*?))\n---\n?((?s:.*))$`)
	kvRe       = regexp.MustCompile(`^(\w+):\s*(.*)$`)
	crlfRe     = regexp.MustCompile(`\r\n?`)
	titleLine  = regexp.MustCompile(`^\s*#\s.*\n?`)
	unicodeSep = strings.NewReplacer(` `, " ", ` `, " ")
)

// jsonValue is how JSON.stringify writes a value: no escaping of <, > and & that Go adds for HTML.
func jsonValue(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // strings, numbers and lists of them cannot fail
	return unicodeSep.Replace(strings.TrimRight(b.String(), "\n"))
}

// ToFile is a task as its file, in the new shape. Values are JSON-quoted where YAML could misread them.
func ToFile(t Task) string {
	lines := []string{"---"}
	put := func(k string, v any) { lines = append(lines, k+": "+jsonValue(v)) }
	put("id", t.ID)
	put("kind", "task")
	put("title", t.Title)
	put("status", t.Status)
	for _, f := range []struct{ k, v string }{{"due", t.Due}, {"time", t.Time}} {
		if f.v != "" {
			put(f.k, f.v)
		}
	}
	if t.Remind != nil {
		put("remind", *t.Remind)
	}
	if t.Priority != 0 {
		put("priority", t.Priority)
	}
	for _, f := range []struct{ k, v string }{
		{"repeat", t.Repeat}, {"repeat_from", t.RepeatFrom}, {"ref", t.Ref}, {"stage", t.Stage},
	} {
		if f.v != "" {
			put(f.k, f.v)
		}
	}
	if len(t.Labels) > 0 {
		put("labels", t.Labels)
	}
	for _, f := range []struct{ k, v string }{
		{"detail", t.Detail}, {"created", t.Created}, {"updated", t.Updated}, {"done", t.Done},
		{"snooze", t.Snooze}, {"snoozed_at", t.SnoozedAt},
	} {
		if f.v != "" {
			put(f.k, f.v)
		}
	}
	// whatever else the file had (Obsidian's tags, say), after the known fields
	for _, f := range t.Extra {
		switch v := f.Value.(type) {
		case nil:
			continue
		case string:
			if v == "" {
				continue
			}
		case []any:
			if len(v) == 0 {
				continue
			}
		}
		put(f.Key, f.Value)
	}
	lines = append(lines, "---", "", "# "+t.Title, "")
	if strings.TrimSpace(t.Notes) != "" {
		lines = append(lines, strings.TrimSpace(t.Notes), "")
	}
	if rels := sectionOf(RelsOf(t), t.RelExtra); rels != "" {
		lines = append(lines, rels, "")
	}
	return strings.Join(lines, "\n")
}

func frontOf(text string) []string {
	return frontRe.FindStringSubmatch(crlfRe.ReplaceAllString(text, "\n"))
}

func str(v any) (string, bool) { s, ok := v.(string); return s, ok }

func asInt(v any) (int, bool) {
	f, ok := v.(float64)
	return int(f), ok
}

// FromFile is a file back to a task, from either shape; nil when it is not one. Notes are the body after the title,
// without the relations section, which becomes Rels. With me, the account's id, the old `owner` is read by the
// account's lens (legacyRels): the account itself is nothing, a person on a waiting task is waited on.
func FromFile(text, me string) *Task {
	m := frontOf(text)
	if m == nil {
		return nil
	}
	vals := map[string]any{}
	var order []string
	for _, line := range strings.Split(m[1], "\n") {
		kv := kvRe.FindStringSubmatch(line)
		if kv == nil {
			continue
		}
		raw := strings.TrimSpace(kv[2])
		var v any = raw
		var parsed any
		if json.Unmarshal([]byte(raw), &parsed) == nil {
			v = parsed
		}
		if _, seen := vals[kv[1]]; !seen {
			order = append(order, kv[1])
		}
		vals[kv[1]] = v
	}
	id, okID := str(vals["id"])
	title, okTitle := str(vals["title"])
	if !okID || !okTitle {
		return nil
	}
	t := Task{ID: id, Title: title}
	known := map[string]func(v any){
		"status": func(v any) {
			if s, ok := str(v); ok && ValidStatus(Status(s)) {
				t.Status = Status(s)
			}
		},
		"due":         func(v any) { t.Due, _ = str(v) },
		"time":        func(v any) { t.Time, _ = str(v) },
		"repeat":      func(v any) { t.Repeat, _ = str(v) },
		"repeat_from": func(v any) { t.RepeatFrom, _ = str(v) },
		"ref":         func(v any) { t.Ref, _ = str(v) },
		"stage":       func(v any) { t.Stage, _ = str(v) },
		"detail":      func(v any) { t.Detail, _ = str(v) },
		"created":     func(v any) { t.Created, _ = str(v) },
		"updated":     func(v any) { t.Updated, _ = str(v) },
		"done":        func(v any) { t.Done, _ = str(v) },
		"snooze":      func(v any) { t.Snooze, _ = str(v) },
		"snoozed_at":  func(v any) { t.SnoozedAt, _ = str(v) },
		"remind": func(v any) {
			if n, ok := asInt(v); ok {
				t.Remind = &n
			}
		},
		"priority": func(v any) { t.Priority, _ = asInt(v) },
		"labels": func(v any) {
			if xs, ok := v.([]any); ok {
				for _, x := range xs {
					if s, ok := str(x); ok {
						t.Labels = append(t.Labels, s)
					}
				}
			}
		},
	}
	var old oldFields
	for _, k := range order {
		switch {
		case k == "id" || k == "title" || k == "kind" || slices.Contains(notFront, k):
		case k == "owner":
			old.Owner, _ = str(vals[k])
		case k == "project":
			old.Project, _ = str(vals[k])
		case k == "parent":
			old.Parent, _ = str(vals[k])
		case k == "blocked_by":
			if xs, ok := vals[k].([]any); ok {
				for _, x := range xs {
					old.BlockedBy = append(old.BlockedBy, fmt.Sprint(x))
				}
			}
		case known[k] != nil:
			known[k](vals[k])
		default:
			t.Extra = append(t.Extra, Field{k, vals[k]})
		}
	}
	if t.Status == "" {
		t.Status = Todo
	}
	rels, extra, body := takeSection(titleLine.ReplaceAllString(m[2], ""))
	// a field of the old shape says what a relation says: it is read as one, unless the section has it already
	for _, l := range legacyRels(old, me, t.Status) {
		if !slices.ContainsFunc(rels, func(r Rel) bool {
			return r.Rel == l.Rel && r.Kind == l.Kind && strings.EqualFold(r.Target, l.Target)
		}) {
			rels = append(rels, l)
		}
	}
	t.Notes = strings.TrimSpace(body)
	if len(rels) > 0 {
		t.Rels = rels
	}
	t.RelExtra = extra
	return derive(&t)
}
