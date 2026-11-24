package mcp

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"kedalio/core/memory"
	"kedalio/core/memory/search"
	"kedalio/core/tasks"
)

// What an agent learned, placed by the server: no model in
// the call, never refused for its shape. The page it goes to is, in order: the one `about` names; the page whose title
// the text names (the most specific, when it names several); the page nearest in meaning, when it is near enough;
// else the inbox, to be sorted. On a page it becomes one dated line of its notes section. A text that looks like a
// secret is the one thing refused: it is never stored, anywhere.

const (
	// minName: a title this short is a word, not a name: «App», «Note».
	minName = 4
	// nearFloor and belowPages: a text goes to the page nearest in meaning from a little under the account's «near»
	// between pages (a sentence sits further from a page than a page from another), never under the floor.
	nearFloor, belowPages = 0.5, 0.05
	embedWait             = 4 * time.Second
)

var (
	notesHead = regexp.MustCompile(`(?i)^##\s+(notes|note)\s*$`)
	sectionRe = regexp.MustCompile(`^##\s`)
	nonWord   = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	whyTail   = regexp.MustCompile(`(?s)\s*[.:].*$`)
	blankTail = regexp.MustCompile(`\s+\n`)
)

// addNote is the body with line added at the end of its notes section (made at the end of the page when it has none).
func addNote(body, line, head string) string {
	lines := strings.Split(strings.TrimRight(body, " \t\r\n"), "\n")
	at := -1
	for i, l := range lines {
		if notesHead.MatchString(l) {
			at = i
			break
		}
	}
	if at < 0 {
		return strings.Join(lines, "\n") + "\n\n" + head + "\n\n" + line + "\n"
	}
	end := at + 1
	for i := at + 1; i < len(lines) && !sectionRe.MatchString(lines[i]); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			end = i + 1
		}
	}
	lines = append(lines[:end], append([]string{line}, lines[end:]...)...)
	return strings.Join(lines, "\n") + "\n"
}

func words(s string) string { return strings.TrimSpace(nonWord.ReplaceAllString(fold(s), " ")) }

// namedIn is the most specific of the pages whose title or name the text names as a whole word; episodes and short
// titles are not names.
func namedIn(text string, pages []memory.Info) string {
	t := " " + words(text) + " "
	best, bestLen := "", 0
	for _, p := range pages {
		for _, name := range []string{p.Title, nameOf(p.Path)} {
			n := words(name)
			if utf8.RuneCountInString(n) < minName || !strings.Contains(t, " "+n+" ") {
				continue
			}
			if l := utf8.RuneCountInString(n); best == "" || l > bestLen {
				best, bestLen = p.Path, l
			}
		}
	}
	return best
}

type remembered struct {
	Remembered string  `json:"remembered"`
	How        string  `json:"how"`
	Line       string  `json:"line"`
	Score      float64 `json:"score,omitempty"`
	Reason     string  `json:"reason,omitempty"`
}

// remember keeps a fact; the second value is the reasons it was refused.
func (s *Server) remember(ctx context.Context, said, about string) (*remembered, []string, error) {
	said = strings.TrimSpace(said)
	said = blankTail.ReplaceAllString(said, "\n")
	if said == "" {
		return nil, []string{"nothing to remember"}, nil
	}
	if n := utf8.RuneCountInString(said); n > maxRemember {
		return nil, []string{strconv.Itoa(n) + " characters: at most " + strconv.Itoa(maxRemember) + "; write a page instead"}, nil
	}
	if s.secret(said) {
		return nil, []string{"it looks like a secret (a password, a token): never in the memory"}, nil
	}
	now := time.Now()
	if isCloud(ctx) {
		// a cloud client's fact waits in the inbox for the owner, whatever page it names or is near
		why := s.cloudWhy(ctx)
		if about = strings.TrimSpace(about); about != "" {
			why += "; about " + about
		}
		line := strings.ReplaceAll(said, "\n", " ")
		r, errs, err := s.appendLine(ctx, true, []string{line}, now, why)
		if err != nil || errs != nil {
			return nil, errs, err
		}
		return &remembered{Remembered: r.path, How: "inbox", Line: line, Reason: "a cloud client's facts wait in the inbox for the owner"}, nil, nil
	}
	all, err := s.cfg.Memory.List(ctx, "", memory.ListOpts{Limit: 1_000_000})
	if err != nil {
		return nil, nil, err
	}
	var pages []memory.Info
	for _, p := range all {
		// a task is the tasks' own: a fact about it goes through tasks_save, never into its page by name
		if p.Kind != memory.KindEpisode && !isTask(p.Path) {
			pages = append(pages, p)
		}
	}
	one := strings.ReplaceAll(said, "\n", " ")
	day := tasks.DayOf(now, s.zone)

	type target struct {
		path, how string
		score     float64
	}
	var tgt *target
	why := ""
	if about = strings.TrimSpace(about); about != "" {
		resolve, err := s.cfg.Memory.Resolver(ctx)
		if err != nil {
			return nil, nil, err
		}
		p := resolve(about)
		if p == "" {
			p = resolve(strings.TrimSuffix(slugPath(about), ".md"))
		}
		if p != "" && isTask(p) {
			return nil, []string{about + " is a task: write its notes with tasks_save"}, nil
		}
		switch {
		case p != "":
			tgt = &target{path: p, how: "about"}
		default:
			// a name with no page yet: the page is made, with the text as its first sentence
			path := slugPath(about)
			title := strings.Join(strings.Fields(about), " ")
			r, n := utf8.DecodeRuneInString(title)
			body := "# " + string(unicode.ToUpper(r)) + title[n:] + "\n\n" + one + "\n"
			v, err := s.check(ctx, path, body, "", true, false)
			if err != nil {
				return nil, nil, err
			}
			if len(v.errors) == 0 {
				if _, err := s.cfg.Memory.WriteAt(ctx, path, body, s.by(ctx), 0); err != nil {
					return nil, nil, err
				}
				return &remembered{Remembered: path, How: "new", Line: one}, nil, nil
			}
			why = "a page for " + about + " would not be made (" + whyTail.ReplaceAllString(v.errors[0], "") + ")"
		}
	}
	if tgt == nil {
		if named := namedIn(one, pages); named != "" {
			tgt = &target{path: named, how: "named"}
		}
	}
	if tgt == nil && s.cfg.Embedder != nil {
		// the text's vector, asked of the model once and only when needed; a model that does not answer leaves the names
		// and the inbox
		ectx, cancel := context.WithTimeout(ctx, embedWait)
		vecs, err := s.cfg.Embedder.Embed(ectx, []string{one})
		cancel()
		if err == nil && len(vecs) == 1 {
			near, err := s.cfg.Memory.NearThreshold(ctx)
			if err != nil {
				return nil, nil, err
			}
			path, w, err := s.cfg.Memory.NearestPage(ctx, search.Normalize(vecs[0]), s.cfg.Embedder.Model(), max(nearFloor, near-belowPages))
			if err != nil {
				return nil, nil, err
			}
			if path != "" {
				tgt = &target{path, "meaning", float64(int(w*1000+0.5)) / 1000}
			}
		}
	}
	reason := "no page names it or is near it in meaning"
	if why != "" {
		reason = why
	}
	if tgt != nil {
		cur, err := s.cfg.Memory.Get(ctx, tgt.path)
		if err != nil {
			return nil, nil, err
		}
		if cur != nil {
			line := "- " + one + " (" + day + ")"
			head := "## Notes"
			if s.areas == memory.AreasIT {
				head = "## Note"
			}
			next := addNote(cur.Body, line, head)
			v, err := s.check(ctx, cur.Path, next, cur.Body, false, false)
			if err != nil {
				return nil, nil, err
			}
			if len(v.errors) == 0 {
				w, err := s.cfg.Memory.WriteAt(ctx, cur.Path, next, s.by(ctx), cur.Rev)
				if err != nil {
					return nil, nil, err
				}
				return &remembered{Remembered: w.Path, How: tgt.how, Line: line, Score: tgt.score}, nil, nil
			}
			// the reason is for the owner, in plain words: the rules' own message names «this page» in a link
			reason = cur.Path + " would not take it (" + whyTail.ReplaceAllString(v.errors[0], "") + ")"
		}
	}
	r, errs, err := s.appendLine(ctx, true, []string{one}, now, reason)
	if err != nil || errs != nil {
		return nil, errs, err
	}
	return &remembered{Remembered: r.path, How: "inbox", Line: one, Reason: reason}, nil, nil
}
