package mcp

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kedalio/core/memory"
	"kedalio/core/tasks"
)

// The diary and the inbox are added to, never rewritten.

const maxWhy = 200

var (
	whyLink    = regexp.MustCompile(`\[\[([^\]|]+)(?:\|([^\]]*))?\]\]`)
	whyLine    = regexp.MustCompile(`(?i)^why:\s*(.*)$`)
	entryLine  = regexp.MustCompile(`^- \d{4}-\d{2}-\d{2} \d{2}:\d{2} `)
	continued  = regexp.MustCompile(`^ {2}\S`)
	trailingWS = regexp.MustCompile(`\s+$`)
)

// cleanWhy is a reason in plain words and one short line: a [[link]] in it would be a link to a page that is not there.
func cleanWhy(why string) string {
	w := whyLink.ReplaceAllStringFunc(why, func(m string) string {
		g := whyLink.FindStringSubmatch(m)
		if g[2] != "" {
			return g[2]
		}
		return g[1]
	})
	w = strings.TrimSpace(spaceRe.ReplaceAllString(w, " "))
	if r := []rune(w); len(r) > maxWhy {
		w = string(r[:maxWhy])
	}
	return w
}

// withoutOrphanWhys is the body without the `why:` lines that belong to no entry.
func withoutOrphanWhys(body string) string {
	entry, why := false, false
	var out []string
	for _, l := range strings.Split(body, "\n") {
		switch {
		case entryLine.MatchString(l):
			entry, why = true, false
		case entry && strings.TrimSpace(l) == "" && l != "":
			// a blank line inside an entry («  ») goes on with it, as inboxEntries reads it: ending the entry there
			// would make its why line an orphan, and drop it (a cloud client's mark among them)
		case !continued.MatchString(l):
			entry = false
		case whyLine.MatchString(strings.TrimSpace(l)):
			if !entry || why {
				continue
			}
			why = true
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

var (
	daysIT   = []string{"domenica", "lunedì", "martedì", "mercoledì", "giovedì", "venerdì", "sabato"}
	daysEN   = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	monthsIT = []string{"gennaio", "febbraio", "marzo", "aprile", "maggio", "giugno", "luglio", "agosto", "settembre", "ottobre", "novembre", "dicembre"}
	monthsEN = []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
)

// longDate is a day as the diary's lede says it: «martedì 3 novembre 2026», «Tuesday 3 November 2026».
func longDate(t time.Time, italian bool) string {
	days, months := daysEN, monthsEN
	if italian {
		days, months = daysIT, monthsIT
	}
	return days[t.Weekday()] + " " + strconv.Itoa(t.Day()) + " " + months[t.Month()-1] + " " + strconv.Itoa(t.Year())
}

// added is what an append answers with.
type added struct {
	path string
	rev  int
}

// appendLine adds a timed line to today's diary or to the inbox, the page made when it does not exist; the reasons
// when the line or the page would break the memory's rules. why is the reason an inbox entry waits, kept under it.
func (s *Server) appendLine(ctx context.Context, inbox bool, lines []string, now time.Time, why string) (*added, []string, error) {
	return s.appendWith(ctx, inbox, lines, now, why, nil)
}

// appendClosure adds the closing of a task to today's diary: a lone line, or one grouped line with the closures just
// before it (addClosure).
func (s *Server) appendClosure(ctx context.Context, c tasks.Closed, now time.Time) (*added, []string, error) {
	italian := s.areas == memory.AreasIT
	line := "[[" + c.ID + "]] " + c.Title
	return s.appendWith(ctx, false, []string{line}, now, "", func(body, hhmm string) string {
		return addClosure(body, hhmm, c, italian, func(id string) string { return s.taskTitle(ctx, id) })
	})
}

// taskTitle is the title of a task as its page has it, "" when there is none to read.
func (s *Server) taskTitle(ctx context.Context, id string) string {
	d, err := s.cfg.Memory.Get(ctx, "tasks/"+id+".md")
	if err != nil || d == nil {
		return ""
	}
	if t := tasks.FromFile(d.Body, s.cfg.Tasks.Me); t != nil {
		return t.Title
	}
	return ""
}

// appendWith is appendLine, the line put into the day by add when it is not nil (addDiaryLine otherwise).
func (s *Server) appendWith(ctx context.Context, inbox bool, lines []string, now time.Time, why string, add func(body, hhmm string) string) (*added, []string, error) {
	now = now.In(s.zone)
	day := tasks.DayOf(now, s.zone)
	if !inbox {
		if e := entryErrors(strings.Join(lines, "\n")); len(e) > 0 {
			return nil, e, nil
		}
	}
	path := s.areas.Diary + "/" + day + ".md"
	if inbox {
		path = s.areas.Inbox + "/inbox.md"
	}
	cur, err := s.cfg.Memory.Get(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	italian := s.areas == memory.AreasIT
	date := longDate(now, italian)
	var head string
	switch {
	case inbox && italian:
		head = "# Inbox\n\nCose dette al volo, da sistemare nelle pagine giuste e poi togliere da qui.\n"
	case inbox:
		head = "# Inbox\n\nThings said in passing, to sort into the right pages and then take out of here.\n"
	case italian:
		head = "# " + day + "\n\nCosa è successo il " + date + ".\n"
	default:
		head = "# " + day + "\n\nWhat happened on " + date + ".\n"
	}
	var body string
	if inbox {
		// the inbox takes many entries at once; each is a line with its continuation lines indented
		var entries []string
		for _, one := range lines {
			var b strings.Builder
			for i, l := range strings.Split(strings.TrimSpace(one), "\n") {
				if i == 0 {
					b.WriteString("- " + day + " " + tasks.HHMM(now, s.zone) + " " + strings.TrimSpace(l))
				} else {
					b.WriteString("\n  " + strings.TrimSpace(l))
				}
			}
			if w := cleanWhy(why); w != "" {
				b.WriteString("\n  why: " + w)
			}
			entries = append(entries, b.String())
		}
		was := head
		sep := "\n"
		if cur != nil {
			was, sep = withoutOrphanWhys(cur.Body), ""
		}
		body = trailingWS.ReplaceAllString(was, "") + "\n" + sep + strings.Join(entries, "\n") + "\n"
	} else {
		// a line about the same pages as one written a little before is joined to it (diary.go)
		base := head
		if cur != nil {
			base = cur.Body
		}
		if add != nil {
			body = add(base, tasks.HHMM(now, s.zone))
		} else {
			body = addDiaryLine(base, tasks.HHMM(now, s.zone), strings.Join(lines, "\n"))
		}
		if cur == nil {
			body = strings.Replace(body, strings.TrimRight(head, " \t\r\n"), strings.TrimRight(head, " \t\r\n")+"\n", 1)
		}
	}
	v, err := s.check(ctx, path, body, bodyOf(cur), cur == nil, false)
	if err != nil {
		return nil, nil, err
	}
	if len(v.errors) > 0 {
		return nil, v.errors, nil
	}
	rev := 0
	if cur != nil {
		rev = cur.Rev
	}
	d, err := s.cfg.Memory.WriteAt(ctx, path, body, s.by(ctx), rev)
	if err != nil {
		return nil, nil, err
	}
	return &added{d.Path, d.Rev}, nil, nil
}
