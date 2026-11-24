package mcp

import (
	"context"
	"fmt"
	"strings"

	"kedalio/core/memory"
)

// The project card: one short page per project that answers «where were we?» without
// reading the repository. It lives at `<projects area>/<project path>/card.md`, carries `is: card` in its front matter
// and stays under cardWords. A session reads it at its start and replaces it at its end, always with the rev it read,
// so two sessions on the same project never overwrite each other silently.

// cardWords is the card's size, front matter left out: it is read at the start of every session, so it must stay cheap.
const cardWords = 150

func (s *Server) cardPath(project string) string {
	return slugPath(s.areas.Projects + "/" + strings.Trim(project, "/") + "/card.md")
}

// saveCard creates the card (base 0) or replaces it whole. A replace without the rev read, a stale rev and a card over
// the size are refused with what to do. The near-duplicate title check is skipped: every card is titled alike.
func (s *Server) saveCard(ctx context.Context, project, body string, base *int, keptIn []string) (any, error) {
	path := s.cardPath(project)
	cur, err := s.cfg.Memory.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	have := 0
	if cur != nil {
		have = cur.Rev
	}
	if base == nil {
		problem := "a new card is created with base_rev 0"
		if cur != nil {
			problem = fmt.Sprintf("the card is at rev %d", have)
		}
		return nil, refusal("base_rev", problem, "read the card (memory_read card) and pass its rev as base_rev")
	}
	if have != *base {
		return nil, refusal("base_rev", fmt.Sprintf("the card changed meanwhile (now rev %d)", have),
			"read the card again, fold your change into it and write with the new rev")
	}
	if n := pageWords(body); n > cardWords {
		return nil, refusal("body", fmt.Sprintf("the card is %d words, the limit is %d", n, cardWords),
			"keep Last time, Next (refs only), Urgent and Memory; drop detail that lives in the repository or the tasks")
	}
	fm := memory.FrontMatter(body)
	if fm["is"] != "card" || fm["kind"] == "" {
		body = memory.SetFrontMatter(body, memory.FMValue{Key: "kind", Value: "entity"}, memory.FMValue{Key: "is", Value: "card"})
	}
	r, v, err := s.writePage(ctx, path, body, base, true, keptIn)
	if err != nil {
		return nil, err
	}
	if len(v.errors) > 0 {
		var issues []issue
		for _, e := range v.errors {
			issues = append(issues, issue{"body", e, "change the card so the rule is met"})
		}
		return nil, &callError{issues}
	}
	return r, nil
}

// editedCard is a card's text with one passage replaced, for an edit that goes through the same checks as a write.
func editedCard(body, find, replace string) (string, error) {
	switch n := strings.Count(body, find); {
	case n == 0:
		return "", refusal("find", "the passage is not in the card", "give a passage that occurs exactly once")
	case n > 1:
		return "", refusal("find", "the passage occurs more than once", "give a passage that occurs exactly once")
	}
	return strings.Replace(body, find, replace, 1), nil
}
