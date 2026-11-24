package tasks

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// How tasks refer to each other: ref, parent, blocked_by.

// idRe is what a task id looks like.
var idRe = regexp.MustCompile(`^t-[\w-]+$`)

// IsID reports whether s is a task id (t-…).
func IsID(s string) bool { return idRe.MatchString(s) }

// ResolveTask is the task a reference names — its id (t-…, #t-…) or its ref (TASK-495, any case). A ref is looked for
// in project first; one that names several tasks, or none, is an error that says so.
func ResolveTask(tasks []Task, key, project string) (Task, error) {
	k := strings.TrimSpace(key)
	if strings.HasPrefix(k, "#t-") {
		k = k[1:]
	}
	if IsID(k) {
		for _, t := range tasks {
			if t.ID == k {
				return t, nil
			}
		}
		return Task{}, refusef("no task %s", k)
	}
	var named []Task
	for _, t := range tasks {
		if t.Ref != "" && strings.EqualFold(t.Ref, k) {
			named = append(named, t)
		}
	}
	pick := named
	if project != "" {
		var here []Task
		for _, t := range named {
			if t.Project == project {
				here = append(here, t)
			}
		}
		if len(here) > 0 {
			pick = here
		}
	}
	if len(pick) == 1 {
		return pick[0], nil
	}
	if len(pick) == 0 {
		return Task{}, refusef("no task with id or ref %s", k)
	}
	var live []Task
	for _, t := range pick {
		if !t.Status.Closed() {
			live = append(live, t)
		}
	}
	if len(live) == 1 {
		return live[0], nil
	}
	var names []string
	for _, t := range pick {
		p := t.Project
		if p == "" {
			p = "no project"
		}
		names = append(names, fmt.Sprintf("%s in %s", t.ID, p))
	}
	return Task{}, refusef("%d tasks have ref %s (%s): give the project or the id", len(pick), k, strings.Join(names, ", "))
}

// RefTaken is another task of the same project already called ref (and not dropped), if there is one.
func RefTaken(tasks []Task, ref, project, self string) *Task {
	for i, t := range tasks {
		if t.ID != self && t.Project == project && strings.EqualFold(t.Ref, ref) && t.Status != Dropped {
			return &tasks[i]
		}
	}
	return nil
}

// ClosedIn says, by id, over a list, whether a task is closed: what a promoted step counts as done by.
func ClosedIn(tasks []Task) func(id string) bool {
	byID := map[string]Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	return func(id string) bool {
		t, ok := byID[id]
		return ok && t.Status.Closed()
	}
}

// Standing is how a task stands with the others.
type Standing struct {
	Parent     *Task
	Parts      []Task
	PartsDone  int
	WaitingFor []Task // what it waits for, still open
	Blocking   []Task // what waits for it
}

// Relations is how a task stands with the others — its parts, what it waits for still open, what waits for it.
func Relations(tasks []Task, t Task) Standing {
	byID := map[string]*Task{}
	for i := range tasks {
		byID[tasks[i].ID] = &tasks[i]
	}
	var s Standing
	if t.Parent != "" {
		s.Parent = byID[t.Parent]
	}
	for _, x := range tasks {
		if x.Parent == t.ID {
			s.Parts = append(s.Parts, x)
			if x.Status.Closed() {
				s.PartsDone++
			}
		}
	}
	for _, id := range t.BlockedBy {
		if x := byID[id]; x != nil && !x.Status.Closed() {
			s.WaitingFor = append(s.WaitingFor, *x)
		}
	}
	for _, x := range tasks {
		if !x.Status.Closed() && slices.Contains(x.BlockedBy, t.ID) {
			s.Blocking = append(s.Blocking, x)
		}
	}
	return s
}

// IsBlocked reports whether anything the task waits for is still open.
func IsBlocked(tasks []Task, t Task) bool { return len(Relations(tasks, t).WaitingFor) > 0 }

// WouldLoop reports whether making parent the parent of id would go round in a circle.
func WouldLoop(tasks []Task, id, parent string) bool {
	byID := map[string]Task{}
	for _, x := range tasks {
		byID[x.ID] = x
	}
	cur := parent
	for n := 0; cur != "" && n < 100; n++ {
		if cur == id {
			return true
		}
		cur = byID[cur].Parent
	}
	return false
}

var errLoop = refuse("that parent is a part of this task: it would go round in a circle")

// ResolveRefs is an input with its references to other tasks turned into ids and checked against the list: a ref
// taken in the project, a parent that would go round in a circle. self is the task being changed, nil for a new one.
func ResolveRefs(all []Task, in Input, self *Task) (Input, error) {
	project := ""
	switch {
	case in.Project != nil:
		project = *in.Project
	case self != nil:
		project = self.Project
	}
	out := in
	if in.Parent != nil && *in.Parent != "" {
		p, err := ResolveTask(all, *in.Parent, project)
		if err != nil {
			return in, err
		}
		out.Parent = S(p.ID)
		if self != nil && WouldLoop(all, self.ID, p.ID) {
			return in, errLoop
		}
	}
	if in.PartOf != nil && self != nil {
		for _, x := range *in.PartOf {
			x = strings.TrimSpace(x)
			x = strings.TrimPrefix(x, "#")
			x = strings.TrimSuffix(strings.TrimPrefix(x, "[["), "]]")
			if IsID(x) && WouldLoop(all, self.ID, x) {
				return in, errLoop
			}
		}
	}
	if in.BlockedBy != nil {
		ids := []string{}
		for _, b := range *in.BlockedBy {
			t, err := ResolveTask(all, b, project)
			if err != nil {
				return in, err
			}
			ids = append(ids, t.ID)
		}
		out.BlockedBy = &ids
	}
	ref := ""
	switch {
	case in.Ref != nil:
		ref = *in.Ref
	case self != nil:
		ref = self.Ref
	}
	if ref != "" && (in.Ref != nil || in.Project != nil) {
		selfID := ""
		if self != nil {
			selfID = self.ID
		}
		if other := RefTaken(all, ref, project, selfID); other != nil {
			where := project
			if where == "" {
				where = "no project"
			}
			return in, refusef("%s is already %s (%s) in %s: update that one", ref, other.ID, other.Title, where)
		}
	}
	return out, nil
}
