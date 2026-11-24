package tasks

import (
	"context"
	"reflect"
	"strings"
)

// longLogWords is how many words of entries a log may hold before it asks for a summary.
const longLogWords = 300

// oldKeys are the front matter keys of the old shape; the new one says the same in `## Relations`.
var oldKeys = map[string]bool{"owner": true, "project": true, "parent": true, "blocked_by": true}

// oldShape reports whether a task file is in the old shape: relations in the front matter, or no `kind: task`.
func oldShape(body string) bool {
	m := frontOf(body)
	if m == nil {
		return false
	}
	kind := false
	for _, line := range strings.Split(m[1], "\n") {
		kv := kvRe.FindStringSubmatch(line)
		if kv == nil {
			continue
		}
		if oldKeys[kv[1]] {
			return true
		}
		if kv[1] == "kind" && strings.Trim(strings.TrimSpace(kv[2]), `"`) == "task" {
			kind = true
		}
	}
	return !kind
}

// ReshapeResult is what one sweep did: how many tasks it rewrote, the ids it left alone because they would not read
// back the same, and how many more are waiting for the next call.
type ReshapeResult struct {
	Rewritten int      `json:"rewritten"`
	Kept      []string `json:"kept"`
	Left      int      `json:"left"`
}

// Reshape rewrites up to limit tasks that are not in the current shape: the old front matter, a log or checkboxes in
// the notes (NeedsTidy), or a part_of that does not name the project page. The rewrite goes through write, with the
// task's own Updated, so a sweep is not work done on the task. A task whose file would not read back as the same task
// is kept as it is: it is listed, never touched. A tidy task is not written again, so a second call does nothing.
func (b *Book) Reshape(ctx context.Context, limit int) (ReshapeResult, error) {
	res := ReshapeResult{Kept: []string{}}
	// like every change to the book: a task saved while the sweep runs is not overwritten with the page it had before
	b.mu.Lock()
	defer b.mu.Unlock()
	rows, err := b.taskRows(ctx)
	if err != nil {
		return res, err
	}
	project := b.projectResolver(ctx)
	for _, r := range rows {
		// read again: the list is older than this turn of the loop
		doc, err := b.Store.Get(ctx, r.Path)
		if err != nil {
			return res, err
		}
		if doc == nil {
			continue
		}
		t := FromFile(doc.Body, b.Me)
		if t == nil {
			continue
		}
		due := oldShape(doc.Body) || NeedsTidy(t.Notes)
		for _, rel := range RelsOf(*t) {
			if due || rel.Rel != "part_of" || rel.Kind != KindPage {
				continue
			}
			target, err := project(rel.Target)
			if err != nil {
				return res, err
			}
			due = target != mdSuffix.ReplaceAllString(rel.Target, "")
		}
		if !due {
			continue
		}
		if !reflect.DeepEqual(t, FromFile(ToFile(*t), b.Me)) {
			res.Kept = append(res.Kept, t.ID)
			continue
		}
		if res.Rewritten >= limit {
			res.Left++
			continue
		}
		if _, err := b.write(ctx, *t, nil); err != nil {
			return res, err
		}
		res.Rewritten++
	}
	return res, nil
}

// LongLog is a task's log that has grown without a summary.
type LongLog struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Words int    `json:"words"`
}

// LongLogs are up to max task logs with no summary and more than 300 words of entries, in path order.
func (b *Book) LongLogs(ctx context.Context, max int) ([]LongLog, error) {
	rows, err := b.DB.QueryContext(ctx,
		"select path, body from docs where path like 'tasks/t-%/log.md' and deleted = 0 order by path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LongLog{}
	for rows.Next() && len(out) < max {
		var path, body string
		if err := rows.Scan(&path, &body); err != nil {
			return out, err
		}
		if !IsLogPath(path) || summaryOf(body) != "" {
			continue
		}
		n := len(strings.Fields(strings.Join(logLines(body), " ")))
		if n > longLogWords {
			out = append(out, LongLog{ID: strings.Split(path, "/")[1], Path: path, Words: n})
		}
	}
	return out, rows.Err()
}
