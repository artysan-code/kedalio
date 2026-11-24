package mcp

import (
	"context"
	"strings"

	"kedalio/core/memory"
	"kedalio/core/projects"
)

// Ops are the stores the session and project tools read and write besides the memory and the tasks: session_start,
// projects_link, settings_get and settings_set. Without them those tools are not offered.
type Ops struct {
	Projects *projects.Store
}

var memoryListAll = memory.ListOpts{Limit: 100000}

// opsTools are the tools Ops adds; none without Ops.
func (s *Server) opsTools() []*tool {
	if s.cfg.Ops == nil || s.cfg.Ops.Projects == nil {
		return nil
	}
	return []*tool{s.sessionStartTool(), s.projectsLinkTool(), s.settingsGetTool(), s.settingsSetTool()}
}

// knownProjects is every project path: the cards in the projects area and the paths that have a place.
func (s *Server) knownProjects(ctx context.Context) (map[string]bool, error) {
	known := map[string]bool{}
	pages, err := s.cfg.Memory.List(ctx, s.areas.Projects+"/", memoryListAll)
	if err != nil {
		return nil, err
	}
	for _, p := range pages {
		if rest, ok := strings.CutPrefix(p.Path, s.areas.Projects+"/"); ok {
			if path, ok := strings.CutSuffix(rest, "/card.md"); ok && path != "" {
				known[path] = true
			}
		}
	}
	placed, err := s.cfg.Ops.Projects.Known(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range placed {
		known[p] = true
	}
	return known, nil
}
