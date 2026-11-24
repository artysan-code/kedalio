package mcp

import (
	"context"
	"encoding/json"
	"regexp"

	"kedalio/core/projects"
	"kedalio/protocol"
)

// projects_link: the answer to the folder question session_start asks with
// `folder: {ask}`. The session asks the owner in the conversation and calls this to link the folder to a project, make a
// new one, or mark the folder «not a project».

var branchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)

func (s *Server) projectsLinkTool() *tool {
	return &tool{
		name: protocol.ToolProjectsLink, kind: write,
		description: "Answer session_start's folder question once the owner chose. Answers {project, path, needs?}; needs [environments]: agree them with the owner (session-start skill) and set project.<path>.environments with settings_set.",
		schema: object([]string{"op", "dir"},
			choice("op", []string{"link", "create", "ignore"}, "link: in an existing project; create: a new one; ignore: not a project"),
			str("dir", "absolute path"),
			str("project", "link: as session_start proposed"),
			str("name", "create: lower case a/b/c"),
			str("repo", "git remote; becomes the repository"),
			str("branch", "default branch")),
		run: func(ctx context.Context, raw []byte) (any, error) {
			a, err := decode[protocol.ProjectsLink](raw)
			if err != nil {
				return nil, err
			}
			return s.linkFolder(ctx, a)
		},
	}
}

func (s *Server) linkFolder(ctx context.Context, a protocol.ProjectsLink) (any, error) {
	ps := s.cfg.Ops.Projects
	if a.Dir == "" {
		return nil, refusal("dir", "empty", "give the folder's absolute path")
	}
	if a.Op == "ignore" {
		if err := ps.Ignore(ctx, a.Dir); err != nil {
			return nil, err
		}
		return map[string]any{"project": nil, "path": nil, "ignored": true}, nil
	}
	given, name := a.Project, "project"
	if a.Op == "create" {
		given, name = a.Name, "name"
	}
	if given == "" {
		return nil, refusal(name, "missing", a.Op+" needs the project's path")
	}
	path := projects.CleanPath(given)
	if !projects.Name.MatchString(path) || len(path) > 120 {
		return nil, refusal(name, "project is a path: lower case, a/b/c", "choose the project again")
	}
	known, err := s.knownProjects(ctx)
	if err != nil {
		return nil, err
	}
	// a folder that is a project's already is not moved by a tool call; the same project is a no-op that still answers
	claimed, err := ps.ClaimedBy(ctx, a.Dir)
	if err != nil {
		return nil, err
	}
	if claimed != "" && claimed != path {
		return nil, refusal("dir", "the folder is a place of "+claimed+" already", "link it to "+claimed+", or remove that place first")
	}
	created := false
	switch a.Op {
	case "create":
		created = !known[path]
	default:
		if !known[path] {
			return nil, refusal(name, "no project "+path+": answer create to make it", "choose the project again")
		}
	}
	if err := ps.AddPlace(ctx, path, "folder", projects.SameDir(a.Dir), ""); err != nil {
		return nil, err
	}
	repo := "none"
	if addr := projects.RepoAddress(a.Repo); addr != "" {
		holder, err := ps.RepoHolder(ctx, addr)
		if err != nil {
			return nil, err
		}
		if holder != "" {
			repo = "already"
		} else {
			branch := ""
			if branchRe.MatchString(a.Branch) {
				branch = a.Branch
			}
			if err := ps.AddPlace(ctx, path, "repo", addr, branch); err != nil {
				return nil, err
			}
			repo = "added"
		}
	}
	out := map[string]any{"project": path, "path": path, "created": created, "repo": repo}
	envs, err := ps.SettingOf(ctx, path, "environments")
	if err != nil {
		return nil, err
	}
	var list []any
	if json.Unmarshal(envs.Value, &list) != nil || len(list) == 0 {
		out["needs"] = []string{"environments"}
	}
	return out, nil
}
