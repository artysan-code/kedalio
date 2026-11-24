package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"kedalio/core/memory"
	"kedalio/core/projects"
	"kedalio/core/tasks"
	"kedalio/protocol"
)

// session_start: what a session learns when it starts, in one call, assembled from the
// memory, the tasks, the places and the settings. It only reads, and only what exists. It is read at every session
// start, so every list is short and every task one line.

// listMax is how many lines a list of tasks shows before it says how many more there are.
const listMax = 6

// taskLine is a task as one line: its id, its ref, its title, and the day and time when they say something.
func taskLine(t tasks.Task, withDay bool) string {
	var when []string
	if withDay && t.Due != "" {
		when = append(when, t.Due)
	}
	if t.Time != "" {
		when = append(when, t.Time)
	}
	line := t.ID
	if t.Ref != "" {
		line += " " + t.Ref
	}
	line += " " + t.Title
	if len(when) > 0 {
		line += " · " + strings.Join(when, " ")
	}
	return line
}

// capped is at most listMax lines, the last saying how many were left out.
func capped(lines []string) []string {
	if len(lines) <= listMax {
		return lines
	}
	return append(append([]string{}, lines[:listMax-1]...), fmt.Sprintf("+%d more", len(lines)-listMax+1))
}

func (s *Server) sessionStartTool() *tool {
	return &tool{
		name: protocol.ToolSessionStart, kind: read, upFront: true,
		description: "Call first in every conversation. Answers the project (or the folder question: ask the owner, then projects_link), card, prefs, tasks due, urgent ones, unread inbox.",
		schema: object(nil,
			str("dir", "session folder"),
			str("remote", "its git remote"),
			str("project", "the project, when known")),
		run: func(ctx context.Context, raw []byte) (any, error) {
			a, err := decode[protocol.SessionStart](raw)
			if err != nil {
				return nil, err
			}
			return s.sessionStart(ctx, a)
		},
	}
}

func (s *Server) sessionStart(ctx context.Context, a protocol.SessionStart) (map[string]any, error) {
	ps := s.cfg.Ops.Projects
	linked, err := ps.Resolve(ctx, a.Dir, a.Remote)
	if err != nil {
		return nil, err
	}
	project := linked
	if a.Project != "" {
		project = projects.CleanPath(a.Project)
	}

	folder, err := s.folderState(ctx, a, linked)
	if err != nil {
		return nil, err
	}

	all, err := s.cfg.Tasks.List(ctx)
	if err != nil {
		return nil, err
	}
	b := tasks.Brief(all, time.Now(), s.cfg.Tasks.Me, s.zone)
	here := func(t tasks.Task) bool {
		p := projects.CleanPath(t.Project)
		return project != "" && (p == project || strings.HasPrefix(p, project+"/"))
	}
	var overdue, today, urgent []string
	for _, t := range b.Overdue {
		if here(t) {
			overdue = append(overdue, taskLine(t, true))
		}
	}
	for _, t := range append(append([]tasks.Task{}, b.Missed...), b.Today...) {
		if here(t) {
			today = append(today, taskLine(t, false))
		}
	}
	// «there is an urgent task on project B today»: high priority, due today or late, elsewhere
	for _, t := range append(append(append([]tasks.Task{}, b.Overdue...), b.Missed...), b.Today...) {
		if !here(t) && t.Priority == 1 {
			line := taskLine(t, true)
			if t.Project != "" {
				line = projects.CleanPath(t.Project) + ": " + line
			}
			urgent = append(urgent, line)
		}
	}

	var card any
	var stale any
	if project != "" {
		d, err := s.cfg.Memory.Get(ctx, s.cardPath(project))
		if err != nil {
			return nil, err
		}
		if d != nil {
			card = map[string]any{"text": strings.TrimSpace(withoutFront(d.Body)), "rev": d.Rev, "updated": d.Updated}
			if v := memory.FrontMatter(d.Body)["memory_stale"]; v != "" {
				stale = v
			}
		}
	}

	var prefs json.RawMessage
	var pending []string
	if project != "" {
		set, err := ps.SettingOf(ctx, project, "session")
		if err != nil {
			return nil, err
		}
		if v := string(set.Value); v != "null" && v != "{}" {
			prefs = set.Value
		}
		envs, err := ps.SettingOf(ctx, project, "environments")
		if err != nil {
			return nil, err
		}
		var list []any
		if json.Unmarshal(envs.Value, &list) != nil || len(list) == 0 {
			pending = []string{"environments"}
		}
	}
	inbox, err := s.inboxCount(ctx)
	if err != nil {
		return nil, err
	}
	entries, _, err := s.inboxEntries(ctx)
	if err != nil {
		return nil, err
	}
	// the entries a cloud client left are counted apart: the upkeep leaves them, so only the owner takes them out
	box := map[string]any{"count": inbox}
	fromCloud := 0
	for _, e := range entries {
		if e.cloud {
			fromCloud++
		}
	}
	if fromCloud > 0 {
		box["from_cloud"] = fromCloud
	}
	var proj any
	if project != "" {
		proj = project
	}
	up, err := s.upkeepState(ctx)
	if err != nil {
		return nil, err
	}
	clock := s.cfg.Tasks.Clock()
	out := map[string]any{
		"today": clock.Today, "weekday": clock.Weekday,
		"project": proj, "folder": folder, "card": card,
		"own":    map[string]any{"overdue": capped(nonNil(overdue)), "today": capped(nonNil(today))},
		"urgent": capped(nonNil(urgent)),
		"inbox":  box, "upkeep": up,
	}
	// what is empty is left out (missed is always empty here: the brief has the missed ones); a client reads an absent
	// prefs, memory_stale or pending_onboarding as empty
	if len(prefs) > 0 {
		out["prefs"] = prefs
	}
	if stale != nil {
		out["memory_stale"] = stale
	}
	if len(pending) > 0 {
		out["pending_onboarding"] = pending
	}
	return out, nil
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// folderState is `known` (the folder is a place of a project), `ignored` (the owner answered «no project» for it), `none`
// (no folder was given, or nothing in it suggests a project), or the question to ask, answered with projects_link.
func (s *Server) folderState(ctx context.Context, a protocol.SessionStart, linked string) (any, error) {
	ps := s.cfg.Ops.Projects
	if linked != "" {
		return "known", nil
	}
	if a.Dir == "" {
		return "none", nil
	}
	if ig, err := ps.Ignored(ctx, a.Dir); err != nil || ig {
		if err != nil {
			return nil, err
		}
		return "ignored", nil
	}
	known, err := s.knownProjects(ctx)
	if err != nil {
		return nil, err
	}
	byRepo := map[string]string{}
	places, err := ps.Places(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range places {
		if p.Kind == "repo" {
			byRepo[projects.SameRemote(p.Value)] = p.Path
		}
	}
	prop := projects.Propose(a.Dir, a.Remote, known, byRepo)
	if prop == nil {
		return "none", nil
	}
	parts := strings.Split(strings.Trim(projects.SameDir(a.Dir), "/"), "/")
	base := parts[len(parts)-1]
	group := strings.Split(prop.Path, "/")
	fresh := projects.PathOf(append(append([]string{}, group[:len(group)-1]...), base)...)
	var freshVal any
	if fresh != "" && fresh != prop.Path && !known[fresh] {
		freshVal = fresh
	}
	return map[string]any{"ask": map[string]any{
		"proposal": prop, "nearby": nonNil(projects.Nearby(prop.Path, slices.Sorted(maps.Keys(known)), 3)), "fresh": freshVal,
	}}, nil
}

// inboxCount is the entries waiting in the inbox page: each is a line starting «- ».
func (s *Server) inboxCount(ctx context.Context) (int, error) {
	d, err := s.cfg.Memory.Get(ctx, s.areas.Inbox+"/inbox.md")
	if err != nil || d == nil {
		return 0, err
	}
	n := 0
	for _, l := range strings.Split(d.Body, "\n") {
		if strings.HasPrefix(l, "- ") {
			n++
		}
	}
	return n, nil
}
