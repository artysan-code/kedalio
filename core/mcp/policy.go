package mcp

import (
	"context"

	"kedalio/protocol"
)

// policy is what a caller narrower than the owner may do: the tools it may call, and the limits inside the two tools
// that write (memory_write's ops and paths, tasks_save's fields). The owner's token and the local OAuth clients have
// none: they may do everything. One place decides which policy a call has (policyOf), so a new kind of caller is a new
// value here, not another if-chain in the handler and the tools.
type policy struct {
	// who is the caller as a refusal names it: «the upkeep token»
	who string
	may map[string]bool
	// uses is the refusal's advice when a tool is not in may: what the caller may call instead
	uses string
	// ownAuthor: the task book records the call's writes under the caller (s.by), not under the book's own author
	ownAuthor bool
	// write and task, when set, judge memory_write and tasks_save before they run: nil lets everything through
	write func(s *Server, ctx context.Context, a protocol.MemoryWrite) error
	task  func(s *Server, ctx context.Context, q protocol.TasksSave) error
}

var (
	upkeepPolicy = &policy{who: "the upkeep token", may: upkeepMay, uses: "the upkeep uses " + upkeepMayList}
	cloudPolicy  = &policy{who: "a cloud client", may: cloudMay, uses: "a cloud client uses " + cloudMayList, ownAuthor: true,
		write: (*Server).cloudWrite, task: func(_ *Server, _ context.Context, q protocol.TasksSave) error { return cloudTask(q) }}
)

// policyOf is the policy of the caller behind a request, nil for the owner and the local clients. The tokens come
// first: the upkeep token is never an OAuth token, and cloud is only ever set for an OAuth one.
func (s *Server) policyOf(ctx context.Context) *policy {
	switch by := s.by(ctx); {
	case by == upkeepAuthor:
		return upkeepPolicy
	case isCloud(ctx):
		return cloudPolicy
	}
	return nil
}

// allowed is the policy's refusal of a call to a tool, nil when it may make it.
func (p *policy) allowed(tool string) []issue {
	if p == nil || p.may[tool] {
		return nil
	}
	return []issue{{"(call)", tool + " is not for " + p.who, p.uses}}
}

// checkWrite and checkTask are the policy's judgement of a memory_write or a tasks_save, nil when it lets it through.
func (s *Server) checkWrite(ctx context.Context, a protocol.MemoryWrite) error {
	if p := s.policyOf(ctx); p != nil && p.write != nil {
		return p.write(s, ctx, a)
	}
	return nil
}

func (s *Server) checkTask(ctx context.Context, q protocol.TasksSave) error {
	if p := s.policyOf(ctx); p != nil && p.task != nil {
		return p.task(s, ctx, q)
	}
	return nil
}
