package tasks

import (
	"regexp"
	"slices"
	"sort"
	"strings"

	"kedalio/core/memory"
)

// A task's relations: the `## Relations` section, one edge per line, and the roles read from it. Pages and tasks are
// [[links]] (so the graph sees them); a run, an engine, an agent or a date is written plain (`- worked_by run:r-12`),
// since it is not a page. The old front matter (`owner`, `project`, `parent`, `blocked_by`) is read as the same
// relations, so the two shapes of a file mean one thing.

// TaskRels are the relations a task knows by name; any other name is kept as written.
var TaskRels = []string{"about", "part_of", "blocked_by", "related", "assigned_to", "worked_by", "waiting_on", "for"}

// engines are the ones a bare word in `worked_by` (and an old `owner`) names (`claude`); any other bare word is an agent.
var engines = []string{"claude", "codex"}

var (
	relHeadRe = regexp.MustCompile(`(?i)^##\s+(relations|relazioni)\s*$`)
	relLineRe = regexp.MustCompile(
		`^\s*[-*]\s+(.+?)\s+(?:\[\[([^\]|#]+)(?:#[^\]|]*)?(?:\|[^\]]*)?\]\]|(run|engine|agent|name):([^\s()]+)|(\d{4}-\d{2}-\d{2}))\s*(?:\((.*)\))?\s*$`)
	wikiEnds   = regexp.MustCompile(`^\[\[|\]\]$`)
	mdSuffix   = regexp.MustCompile(`(?i)\.md$`)
	prefixedRe = regexp.MustCompile(`(?i)^(run|engine|agent|name):(.+)$`)
	prefixOnly = regexp.MustCompile(`(?i)^(run|engine|agent):`)
	pathChars  = regexp.MustCompile(`[\[\]/]`)
)

// classify is what a written target is: `run:r-1`, `engine:claude`, `agent:x`, a day, a task id (`t-…`) or a page.
func classify(raw string) (RelKind, string) {
	s := strings.TrimSpace(mdSuffix.ReplaceAllString(wikiEnds.ReplaceAllString(strings.TrimSpace(raw), ""), ""))
	if m := prefixedRe.FindStringSubmatch(s); m != nil {
		return RelKind(strings.ToLower(m[1])), strings.TrimSpace(m[2])
	}
	if dayRe.MatchString(s) {
		return KindDate, s
	}
	base := lastPart(strings.TrimPrefix(s, "#"))
	if IsID(base) {
		return KindTask, base
	}
	return KindPage, s
}

func lastPart(s string) string { return s[strings.LastIndex(s, "/")+1:] }

// takeSection splits the section's lines from the rest of the notes. A line that is not an edge (`- about` and a word,
// say) is returned as written, not dropped.
func takeSection(notes string) (rels []Rel, extra []string, rest string) {
	lines := strings.Split(notes, "\n")
	at := slices.IndexFunc(lines, relHeadRe.MatchString)
	if at < 0 {
		return nil, nil, notes
	}
	end := sectionEnd(lines, at)
	for _, l := range lines[at+1 : end] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		m := relLineRe.FindStringSubmatch(l)
		name := ""
		if m != nil {
			name = memory.RelName(m[1])
		}
		if m == nil || name == "" {
			extra = append(extra, l)
			continue
		}
		r := Rel{Rel: name}
		switch {
		case m[2] != "":
			r.Kind, r.Target = classify("[[" + m[2] + "]]")
		case m[3] != "":
			r.Kind, r.Target = RelKind(strings.ToLower(m[3])), m[4]
		default:
			r.Kind, r.Target = KindDate, m[5]
		}
		if m[6] != "" {
			r.Note = strings.TrimSpace(m[6])
		}
		rels = append(rels, r)
	}
	kept := append(slices.Clone(lines[:at]), lines[end:]...)
	return rels, extra, strings.TrimSpace(blankRunRe.ReplaceAllString(strings.Join(kept, "\n"), "\n\n"))
}

// RelLine is a relation as its line.
func RelLine(r Rel) string {
	var to string
	switch r.Kind {
	case KindPage, KindTask:
		to = "[[" + r.Target + "]]"
	case KindDate:
		to = r.Target
	default:
		to = string(r.Kind) + ":" + r.Target
	}
	note := ""
	if r.Note != "" {
		note = " (" + r.Note + ")"
	}
	return "- " + r.Rel + " " + to + note
}

// sectionOf is the section as text, the known relations in their order; "" when there is nothing to say.
func sectionOf(rels []Rel, extra []string) string {
	if len(rels) == 0 && len(extra) == 0 {
		return ""
	}
	rank := func(r Rel) int {
		if i := slices.Index(TaskRels, r.Rel); i >= 0 {
			return i
		}
		return len(TaskRels)
	}
	sorted := slices.Clone(rels)
	sort.SliceStable(sorted, func(i, j int) bool { return rank(sorted[i]) < rank(sorted[j]) })
	lines := []string{"## Relations", ""}
	for _, r := range sorted {
		lines = append(lines, RelLine(r))
	}
	return strings.Join(append(lines, extra...), "\n")
}

// nameKey is a name as compared: the last folder-less part, lower case, no `.md`.
func nameKey(s string) string {
	return strings.ToLower(strings.TrimSpace(lastPart(mdSuffix.ReplaceAllString(s, ""))))
}

// relKey is what the index and the filters compare a relation's target by.
func relKey(r Rel) string {
	if r.Kind == KindPage || r.Kind == KindName {
		return nameKey(r.Target)
	}
	return strings.ToLower(strings.TrimSpace(r.Target))
}

// isMe reports whether a page-name target is the account itself.
func isMe(target, me string) bool {
	return strings.TrimSpace(me) != "" && nameKey(target) == strings.ToLower(strings.TrimSpace(me))
}

// ---------------------------------------------------------------- the old shape

type oldFields struct {
	Owner, Project, Parent string
	BlockedBy              []string
}

// legacyRels are the relations the old front matter said. `owner` is claude (an engine works on it) or a person. With
// the account's id (me) the old owner is read as it was meant: the account itself is the default, so nothing; and on
// a `waiting` task another person was the one waited on, so `waiting_on` rather than `assigned_to`.
func legacyRels(o oldFields, me string, status Status) []Rel {
	out := []Rel{}
	if o.Project != "" {
		out = append(out, Rel{Rel: "part_of", Kind: KindPage, Target: o.Project})
	}
	if o.Parent != "" {
		out = append(out, Rel{Rel: "part_of", Kind: KindTask, Target: o.Parent})
	}
	for _, b := range o.BlockedBy {
		out = append(out, Rel{Rel: "blocked_by", Kind: KindTask, Target: b})
	}
	w := strings.TrimSpace(o.Owner)
	switch {
	case w != "" && slices.Contains(engines, strings.ToLower(w)):
		out = append(out, Rel{Rel: "worked_by", Kind: KindEngine, Target: strings.ToLower(w)})
	case w != "" && !isMe(w, me):
		rel := "assigned_to"
		if me != "" && status == Waiting {
			rel = "waiting_on"
		}
		out = append(out, Rel{Rel: rel, Kind: KindPage, Target: w})
	}
	return out
}

// RelsOf is a task's relations, from Rels or, for a task built from the old fields, from those.
func RelsOf(t Task) []Rel {
	if t.Rels != nil {
		return t.Rels
	}
	return legacyRels(oldFields{t.Owner, t.Project, t.Parent, t.BlockedBy}, "", "")
}

func firstRel(t Task, rel string) *Rel {
	for _, r := range RelsOf(t) {
		if r.Rel == rel {
			return &r
		}
	}
	return nil
}

// ForOf is who the task is for, nil when it names no one.
func ForOf(t Task) *Rel { return firstRel(t, "for") }

// AssignedOf is the person the task is assigned to.
func AssignedOf(t Task) *Rel { return firstRel(t, "assigned_to") }

// WorkedByOf is the agent, engine or run working on the task.
func WorkedByOf(t Task) *Rel { return firstRel(t, "worked_by") }

// WaitingOnOf is what the task waits on.
func WaitingOnOf(t Task) *Rel { return firstRel(t, "waiting_on") }

// projectAreas are the folders the projects of an account live in, in either language: a project's slug is what follows.
var projectAreas = []string{"progetti", "projects"}

// slugOfProject is a project page's path as the old `project` field says it: without the projects area.
func slugOfProject(target string) string {
	t := mdSuffix.ReplaceAllString(target, "")
	for _, a := range projectAreas {
		if strings.HasPrefix(t, a+"/") {
			return t[len(a)+1:]
		}
	}
	return t
}

// derive sets the derived fields of the old shape from the relations (cleared when there is no relation).
func derive(t *Task) *Task {
	rels := RelsOf(*t)
	t.Project, t.Parent, t.Owner, t.BlockedBy = "", "", "", nil
	for _, r := range rels {
		switch {
		case r.Rel == "part_of" && r.Kind == KindPage && t.Project == "":
			t.Project = slugOfProject(r.Target)
		case r.Rel == "part_of" && r.Kind == KindTask && t.Parent == "":
			t.Parent = r.Target
		case r.Rel == "blocked_by" && r.Kind == KindTask:
			t.BlockedBy = append(t.BlockedBy, r.Target)
		}
	}
	who, by := AssignedOf(*t), WorkedByOf(*t)
	switch {
	case who != nil:
		t.Owner = nameKey(who.Target)
	case by != nil && by.Kind == KindRun:
		t.Owner = "claude"
	case by != nil:
		t.Owner = by.Target
	}
	return t
}

// ---------------------------------------------------------------- editing

// list keeps each word once, in order, empty ones out.
func list(xs []string) []string {
	out := []string{}
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// EditRels is the relations after an input: each named relation replaced whole. me is the account's id: a person who
// is the account is the default and is not written. The error says what is wrong.
func EditRels(base []Rel, in Input, me, self string) ([]Rel, error) {
	rels := slices.Clone(base)
	drop := func(pred func(Rel) bool) {
		rels = slices.DeleteFunc(rels, pred)
	}
	var failure error
	put := func(rel string, targets []string, allow []RelKind, what string) {
		for _, raw := range targets {
			kind, target := classify(raw)
			if !slices.Contains(allow, kind) {
				if failure == nil {
					failure = refusef("%s takes %s: %s", rel, what, raw)
				}
				return
			}
			if !slices.ContainsFunc(rels, func(r Rel) bool {
				return r.Rel == rel && r.Kind == kind && strings.EqualFold(r.Target, target)
			}) {
				rels = append(rels, Rel{Rel: rel, Kind: kind, Target: target})
			}
		}
	}

	if in.Project != nil {
		p := strings.TrimSpace(*in.Project)
		if cur := derive(&Task{Rels: rels}).Project; p != cur {
			drop(func(r Rel) bool { return r.Rel == "part_of" && r.Kind == KindPage })
			if p != "" {
				rels = append(rels, Rel{Rel: "part_of", Kind: KindPage, Target: p})
			}
		}
	}
	if in.Parent != nil {
		p := strings.TrimPrefix(strings.TrimSpace(*in.Parent), "#")
		if p != "" && !IsID(p) {
			return nil, refuse("parent is a task id, t-…")
		}
		drop(func(r Rel) bool { return r.Rel == "part_of" && r.Kind == KindTask })
		if p != "" {
			rels = append(rels, Rel{Rel: "part_of", Kind: KindTask, Target: p})
		}
	}

	if in.About != nil {
		drop(func(r Rel) bool { return r.Rel == "about" })
		put("about", list(*in.About), []RelKind{KindPage, KindTask}, "pages")
	}
	if in.PartOf != nil {
		drop(func(r Rel) bool { return r.Rel == "part_of" })
		put("part_of", list(*in.PartOf), []RelKind{KindPage, KindTask}, "a project page or a parent task")
	}
	if in.BlockedBy != nil {
		drop(func(r Rel) bool { return r.Rel == "blocked_by" })
		var ids []string
		for _, x := range list(*in.BlockedBy) {
			ids = append(ids, strings.TrimPrefix(x, "#"))
		}
		put("blocked_by", ids, []RelKind{KindTask}, "task ids, t-…")
	}
	if in.Related != nil {
		drop(func(r Rel) bool { return r.Rel == "related" })
		put("related", list(*in.Related), []RelKind{KindPage, KindTask}, "pages or tasks")
	}
	one := func(rel string, v *string, allow []RelKind, what string, plain func(string) (string, error)) {
		if v == nil {
			return
		}
		drop(func(r Rel) bool { return r.Rel == rel })
		s := strings.TrimSpace(*v)
		if s == "" {
			return
		}
		if plain != nil {
			var err error
			if s, err = plain(s); err != nil {
				if failure == nil {
					failure = err
				}
				return
			}
		}
		put(rel, []string{s}, allow, what)
	}
	one("assigned_to", in.AssignedTo, []RelKind{KindPage}, "a person: an agent, an engine or a run is worked_by", nil)
	rels = slices.DeleteFunc(rels, func(r Rel) bool { return r.Rel == "assigned_to" && isMe(r.Target, me) })
	one("worked_by", in.WorkedBy, []RelKind{KindAgent, KindEngine, KindRun},
		"an agent, an engine or a run: agent:name, engine:claude, run:r-…",
		func(s string) (string, error) {
			if prefixOnly.MatchString(s) {
				return s, nil
			}
			if pathChars.MatchString(s) {
				return "", refusef("worked_by takes an agent, an engine or a run, not a page: %s", s)
			}
			if slices.Contains(engines, strings.ToLower(s)) {
				return "engine:" + s, nil
			}
			return "agent:" + s, nil
		})
	one("waiting_on", in.WaitingOn, []RelKind{KindPage, KindRun, KindDate, KindTask}, "a person, a run, a day or a task", nil)
	one("for", in.For, []RelKind{KindPage}, "a person", nil)
	rels = slices.DeleteFunc(rels, func(r Rel) bool { return r.Rel == "for" && isMe(r.Target, me) })
	if failure != nil {
		return nil, failure
	}

	own := func(rel string) bool {
		return slices.ContainsFunc(rels, func(r Rel) bool { return r.Rel == rel && r.Kind == KindTask && strings.EqualFold(r.Target, self) })
	}
	if self != "" && own("part_of") {
		return nil, refuse("a task cannot be part of itself")
	}
	if self != "" && own("blocked_by") {
		return nil, refuse("a task cannot wait for itself")
	}
	return rels, nil
}
