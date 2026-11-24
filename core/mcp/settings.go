package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"kedalio/core/projects"
	"kedalio/core/tasks"
	"kedalio/protocol"
)

// settings_get and settings_set over a fixed list of keys, each with its own value
// shape. Only the keys this core has data for: tasks.stalled and, for a project, session, environments, repo and base.
// The agent, squad, engine, statusline, pause and permission keys belong to parts the Go core does not have
// (ADR 0004): asking for one is answered with the list of keys there are.

const (
	keyStalled = "tasks.stalled"

	stalledShape = "{doingDays?, todoDays?}: whole days, 1 to 365"
	sessionShape = "{mode?: conversation|design|work, start?: [checks of its light start], end?: [steps of its end]}, in plain words; null inherits"
	envsShape    = "a list of {name, kind: development|production, match?: {databases, branches, deployTargets, commands}}; null goes back to inheriting"
	repoShape    = "the repository's https address"
	baseShape    = "the base branch"
)

var projectKeys = []struct{ name, shape string }{
	{"session", sessionShape}, {"environments", envsShape}, {"repo", repoShape}, {"base", baseShape},
}

var projectKeyRe = regexp.MustCompile(`^project\.(.+)\.([a-z]+)$`)

// settingKey resolves a key's name: the setting, and the project path of a project.<path>.<name> key.
func settingKey(name string) (setting, path string, err error) {
	if name == keyStalled {
		return name, "", nil
	}
	if m := projectKeyRe.FindStringSubmatch(name); m != nil {
		for _, k := range projectKeys {
			if k.name == m[2] {
				return m[2], projects.CleanPath(m[1]), nil
			}
		}
	}
	names := []string{keyStalled}
	for _, k := range projectKeys {
		names = append(names, "project.<path>."+k.name)
	}
	return "", "", refusal("key", fmt.Sprintf("no setting %q", name), "use one of: "+strings.Join(names, ", "))
}

func (s *Server) settingsGetTool() *tool {
	return &tool{
		name: protocol.ToolSettingsGet, kind: read,
		description: "Account setting; no `key` lists keys and value shapes. Keys: tasks.stalled, project.<path>.session|environments|repo|base.",
		schema:      object(nil, str("key")),
		run: func(ctx context.Context, raw []byte) (any, error) {
			a, err := decode[protocol.SettingsGet](raw)
			if err != nil {
				return nil, err
			}
			if a.Key == "" {
				keys := map[string]string{keyStalled: stalledShape}
				for _, k := range projectKeys {
					keys["project.<path>."+k.name] = k.shape
				}
				return map[string]any{"keys": keys}, nil
			}
			return s.readSetting(ctx, a.Key)
		},
	}
}

func (s *Server) settingsSetTool() *tool {
	return &tool{
		name: protocol.ToolSettingsSet, kind: write,
		description: "Change an account setting, only what the owner asked, and say so. `key` and the shape of `value` as in settings_get. Answers with the value read back.",
		schema: object([]string{"key"},
			str("key"),
			prop{name: "value", def: map[string]any{"description": "null: a project's environments or session inherits"}}),
		run: func(ctx context.Context, raw []byte) (any, error) {
			a, err := decode[protocol.SettingsSet](raw)
			if err != nil {
				return nil, err
			}
			set, path, err := settingKey(a.Key)
			if err != nil {
				return nil, err
			}
			value := bytes.TrimSpace(a.Value)
			if len(value) == 0 {
				return nil, refusal("value", "missing", "give "+shapeOf(a.Key))
			}
			if err := s.writeSetting(ctx, set, path, value); err != nil {
				var ce *callError
				if errors.As(err, &ce) {
					return nil, err
				}
				return nil, refusal("value", err.Error(), "change the value so the rule is met and call again")
			}
			return s.readSetting(ctx, a.Key)
		},
	}
}

func shapeOf(key string) string {
	set, _, _ := settingKey(key)
	switch set {
	case keyStalled:
		return stalledShape
	}
	for _, k := range projectKeys {
		if k.name == set {
			return k.shape
		}
	}
	return "a value"
}

func (s *Server) readSetting(ctx context.Context, key string) (any, error) {
	set, path, err := settingKey(key)
	if err != nil {
		return nil, err
	}
	ps := s.cfg.Ops.Projects
	switch set {
	case keyStalled:
		th, err := tasks.LoadThresholds(ctx, s.cfg.Tasks.DB)
		if err != nil {
			return nil, err
		}
		return map[string]any{"key": key, "value": th}, nil
	case "repo", "base":
		repo, branch, err := ps.RepoOf(ctx, path)
		if err != nil {
			return nil, err
		}
		if repo == "" {
			return nil, refusal("key", "no project "+path+" with a repository", "set its repo first")
		}
		v := repo
		if set == "base" {
			v = branch
		}
		return map[string]any{"key": key, "value": v}, nil
	}
	v, err := ps.SettingOf(ctx, path, set)
	if err != nil {
		return nil, err
	}
	return map[string]any{"key": key, "value": v.Value, "origin": v.Origin}, nil
}

func (s *Server) writeSetting(ctx context.Context, set, path string, value json.RawMessage) error {
	ps := s.cfg.Ops.Projects
	if set != keyStalled && (path == "" || !projects.Name.MatchString(path)) {
		return refusal("key", "no project path in the key", "use project.<path>."+set)
	}
	switch set {
	case keyStalled:
		var v struct {
			DoingDays *int `json:"doingDays"`
			TodoDays  *int `json:"todoDays"`
		}
		if err := strictDecode(value, &v); err != nil {
			return refusal("value", err.Error(), "give "+stalledShape)
		}
		doing, todo := 0, 0
		if v.DoingDays != nil {
			doing = *v.DoingDays
		}
		if v.TodoDays != nil {
			todo = *v.TodoDays
		}
		return tasks.SetThresholds(ctx, s.cfg.Tasks.DB, doing, todo)
	case "session":
		v, err := cleanSessionPrefs(value)
		if err != nil {
			return err
		}
		return ps.SetSetting(ctx, path, set, v)
	case "environments":
		v, err := cleanEnvironments(value)
		if err != nil {
			return err
		}
		return ps.SetSetting(ctx, path, set, v)
	case "repo":
		var addr string
		if err := json.Unmarshal(value, &addr); err != nil || projects.RepoAddress(addr) == "" || !strings.HasPrefix(addr, "https://") {
			return refusal("value", "not an https repository address", "give "+repoShape)
		}
		return ps.SetRepo(ctx, path, projects.RepoAddress(addr))
	case "base":
		var b string
		if err := json.Unmarshal(value, &b); err != nil || !branchRe.MatchString(b) {
			return refusal("value", "not a branch name", "give "+baseShape)
		}
		return ps.SetBranch(ctx, path, b)
	}
	return nil
}

func strictDecode(raw []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	return nil
}

// cleanSessionPrefs is the `session` setting: what a session in the project does at its
// light start and at its end, and the mode it works in. null inherits.
func cleanSessionPrefs(raw json.RawMessage) (json.RawMessage, error) {
	if string(raw) == "null" {
		return raw, nil
	}
	var v struct {
		Mode  *string  `json:"mode,omitempty"`
		Start []string `json:"start,omitempty"`
		End   []string `json:"end,omitempty"`
	}
	if err := strictDecode(raw, &v); err != nil {
		return nil, refusal("value", err.Error(), "give "+sessionShape)
	}
	if v.Mode != nil && *v.Mode != "conversation" && *v.Mode != "design" && *v.Mode != "work" {
		return nil, refusal("value.mode", "invalid option", "use one of: conversation, design, work")
	}
	for _, list := range []struct {
		name  string
		steps []string
	}{{"start", v.Start}, {"end", v.End}} {
		if len(list.steps) > 8 {
			return nil, refusal("value."+list.name, "at most 8 steps", "keep the ones that matter")
		}
		for i, st := range list.steps {
			st = strings.TrimSpace(st)
			if st == "" || len([]rune(st)) > 200 {
				return nil, refusal(fmt.Sprintf("value.%s.%d", list.name, i), "a step says something, in at most 200 characters", "shorten it")
			}
			list.steps[i] = st
		}
	}
	return json.Marshal(v)
}

// cleanEnvironments is the `environments` setting: the places a project's work happens and what marks each one.
func cleanEnvironments(raw json.RawMessage) (json.RawMessage, error) {
	if string(raw) == "null" {
		return raw, nil
	}
	var list []struct {
		Name  string          `json:"name"`
		Kind  string          `json:"kind"`
		Match json.RawMessage `json:"match,omitempty"`
	}
	if err := strictDecode(raw, &list); err != nil {
		return nil, refusal("value", err.Error(), "give "+envsShape)
	}
	seen := map[string]bool{}
	for i, e := range list {
		if strings.TrimSpace(e.Name) == "" || seen[e.Name] {
			return nil, refusal(fmt.Sprintf("value.%d.name", i), "a name, once", "give each environment its own name")
		}
		seen[e.Name] = true
		if e.Kind != "development" && e.Kind != "production" {
			return nil, refusal(fmt.Sprintf("value.%d.kind", i), "invalid option", "use development or production")
		}
	}
	return json.Marshal(list)
}
