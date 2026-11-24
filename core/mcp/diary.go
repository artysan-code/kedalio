package mcp

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"kedalio/core/tasks"
)

// A diary line that says the same thing again. One piece of work wrote its
// line three or four times in an hour; when the new line is about the very pages an earlier line of the day is about,
// it is joined to that line instead of adding one, and the line then covers the span (`- 10:22–10:41 …`). The old
// text stays in the page's revisions.

// entryHead is a timed diary line's head: `- 10:22 ` or, once merged, `- 10:22–10:41 `.
var entryHead = regexp.MustCompile(`^- (\d{2}:\d{2})(?:–(\d{2}:\d{2}))? `)

// mergeWindow is how far back a merge reaches, in minutes.
const mergeWindow = 120

var (
	linkSetRe = regexp.MustCompile(`\[\[([^\]|#]+)(?:#[^\]|]*)?(?:\|[^\]]*)?\]\]`)
	spaceRe   = regexp.MustCompile(`\s+`)
	indentRe  = regexp.MustCompile(`^\s+\S`)
)

func squash(t string) string {
	return strings.TrimSpace(spaceRe.ReplaceAllString(strings.ToLower(t), " "))
}

func minutes(hhmm string) int {
	h, _ := strconv.Atoi(hhmm[:2])
	m, _ := strconv.Atoi(hhmm[3:5])
	return h*60 + m
}

// linkSet is the pages a text links, by target (label and heading left out), lower case, sorted and without repeats.
func linkSet(text string) []string {
	var out []string
	for _, m := range linkSetRe.FindAllStringSubmatch(text, -1) {
		t := strings.ToLower(strings.TrimSpace(m[1]))
		out = append(out, strings.TrimSuffix(t, ".md"))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// entryText is the text of a timed diary line after its time (or span); "" for any other line.
func entryText(line string) string {
	if m := entryHead.FindString(line); m != "" {
		return line[len(m):]
	}
	return ""
}

// addDiaryLine is the page body with text at time (HH:MM) added to the day: by replacing an earlier timed line when
// that line, written at most mergeWindow minutes before, links exactly the same pages (and text links at least one);
// else as a new line after the others. A line carries its continuation lines (indented), which go with it.
func addDiaryLine(body, time, text string) string {
	lines := strings.Split(strings.TrimRight(body, " \t\r\n"), "\n")
	var entry []string
	for i, l := range strings.Split(strings.TrimSpace(text), "\n") {
		if i > 0 {
			l = "  " + strings.TrimSpace(l)
		} else {
			l = strings.TrimSpace(l)
		}
		entry = append(entry, l)
	}
	want := linkSet(text)
	if len(want) > 0 {
		for i := len(lines) - 1; i >= 0; i-- {
			m := entryHead.FindStringSubmatch(lines[i])
			if m == nil {
				continue
			}
			end := i + 1
			for end < len(lines) && indentRe.MatchString(lines[end]) && !entryHead.MatchString(lines[end]) {
				end++
			}
			last := m[1]
			if m[2] != "" {
				last = m[2]
			}
			age := minutes(time) - minutes(last)
			// lines are in time order: an older one than the window ends the search
			if age > mergeWindow {
				break
			}
			if age < 0 {
				continue
			}
			if !slices.Equal(linkSet(strings.Join(lines[i:end], "\n")[len(m[0]):]), want) {
				continue
			}
			span := time
			if time != m[1] {
				span = m[1] + "–" + time
			}
			prev, prevMore := lines[i][len(m[0]):], lines[i+1:end]
			nPrev, nNew := squash(strings.Join(append([]string{prev}, prevMore...), " ")), squash(text)
			// nothing the earlier line said is lost: it stays, joined to the new text unless one already holds the other
			var first string
			var more []string
			switch {
			case strings.Contains(nPrev, nNew):
				first, more = prev, prevMore
			case strings.Contains(nNew, nPrev):
				first, more = entry[0], entry[1:]
			default:
				first = strings.TrimRight(prev, " \t\r\n.;") + "; " + entry[0]
				more = append(slices.Clone(prevMore), entry[1:]...)
			}
			merged := append([]string{"- " + span + " " + first}, more...)
			lines = slices.Concat(lines[:i], merged, lines[end:])
			return strings.Join(lines, "\n") + "\n"
		}
	}
	lines = append(lines, "- "+time+" "+entry[0])
	lines = append(lines, entry[1:]...)
	return strings.Join(lines, "\n") + "\n"
}

// closeWindow is how far back a closure line takes the next closure into itself, in minutes. A bulk close writes one
// line per task a second apart; they become one line, and a closure an hour later starts its own.
const closeWindow = 30

var (
	// closeSingle is a lone closure's text: `[[t-…]] Title: done`, in either language.
	closeSingle = regexp.MustCompile(`^\[\[(t-[^\]|]+)\]\] (.*): (completata|lasciata cadere|done|dropped)$`)
	// closeGroup is a grouped one: `closed 63 tasks: 41 done, 22 dropped — [[t-a]], [[t-b]]`.
	closeGroup = regexp.MustCompile(`^(?:chiuse|closed) \d+ tasks?: (.*) — (\[\[t-[^\]|]+\]\](?:, \[\[t-[^\]|]+\]\])*)$`)
	closeCount = regexp.MustCompile(`^(?:chiuse|closed) (\d+) `)
	closeDone  = regexp.MustCompile(`(\d+) (?:completate|done)`)
	closeDrop  = regexp.MustCompile(`(\d+) (?:lasciate cadere|dropped)`)
	closeLink  = regexp.MustCompile(`\[\[t-[^\]|]+\]\]`)
)

// closureOf reads a diary line's text as a closure: how many tasks it closed as done and as dropped, and their links.
// ok is false for any other line. A line only looks like the server's when it is the server's: a lone line must carry
// the title its task has (titleOf), so a line an agent wrote («[[t-x]] Wrote the spec: done») is not folded and its
// words kept, and a grouped line must count as many tasks as it links.
func closureOf(text string, titleOf func(id string) string) (done, dropped int, links []string, ok bool) {
	if m := closeSingle.FindStringSubmatch(text); m != nil {
		if t := titleOf(m[1]); t == "" || t != m[2] {
			return 0, 0, nil, false
		}
		if m[3] == "completata" || m[3] == "done" {
			return 1, 0, []string{"[[" + m[1] + "]]"}, true
		}
		return 0, 1, []string{"[[" + m[1] + "]]"}, true
	}
	m := closeGroup.FindStringSubmatch(text)
	if m == nil {
		return 0, 0, nil, false
	}
	if d := closeDone.FindStringSubmatch(m[1]); d != nil {
		done, _ = strconv.Atoi(d[1])
	}
	if d := closeDrop.FindStringSubmatch(m[1]); d != nil {
		dropped, _ = strconv.Atoi(d[1])
	}
	links = closeLink.FindAllString(m[2], -1)
	if n, _ := strconv.Atoi(closeCount.FindStringSubmatch(text)[1]); n != done+dropped || n != len(links) {
		return 0, 0, nil, false
	}
	return done, dropped, links, true
}

// closureText is the grouped line's text.
func closureText(done, dropped int, links []string, italian bool) string {
	doneW, dropW, many := "done", "dropped", "closed %d tasks: "
	if italian {
		doneW, dropW, many = "completate", "lasciate cadere", "chiuse %d task: "
	}
	var parts []string
	if done > 0 {
		parts = append(parts, strconv.Itoa(done)+" "+doneW)
	}
	if dropped > 0 {
		parts = append(parts, strconv.Itoa(dropped)+" "+dropW)
	}
	head := strings.Replace(many, "%d", strconv.Itoa(done+dropped), 1)
	return head + strings.Join(parts, ", ") + " — " + strings.Join(links, ", ")
}

// addClosure is the page body with the closing of task c at time (HH:MM) added to the day. When the day already has a
// closure line (a lone one or a grouped one) whose end is at most closeWindow minutes before, that line is replaced by
// one grouped line over the span, counting both kinds and keeping every task's link (titles are not kept: the links
// say which, and a bulk close of sixty tasks is not sixty titles); else the lone line is added, as addDiaryLine does.
// titleOf gives a task's title ("" when it has none to give): see closureOf.
func addClosure(body, time string, c tasks.Closed, italian bool, titleOf func(id string) string) string {
	how := map[bool]string{true: "completata", false: "done"}[italian]
	if !c.Done {
		how = map[bool]string{true: "lasciata cadere", false: "dropped"}[italian]
	}
	link := "[[" + c.ID + "]]"
	lines := strings.Split(strings.TrimRight(body, " \t\r\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		m := entryHead.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		last := m[1]
		if m[2] != "" {
			last = m[2]
		}
		age := minutes(time) - minutes(last)
		// lines are in time order: one older than the window ends the search
		if age > closeWindow {
			break
		}
		if age < 0 {
			continue
		}
		done, dropped, links, ok := closureOf(lines[i][len(m[0]):], func(id string) string {
			if id == c.ID {
				return c.Title
			}
			return titleOf(id)
		})
		if !ok {
			continue
		}
		// the same task closed again is not counted twice
		if !slices.Contains(links, link) {
			links = append(links, link)
			if c.Done {
				done++
			} else {
				dropped++
			}
		}
		span := time
		if time != m[1] {
			span = m[1] + "–" + time
		}
		lines[i] = "- " + span + " " + closureText(done, dropped, links, italian)
		return strings.Join(lines, "\n") + "\n"
	}
	return addDiaryLine(body, time, link+" "+c.Title+": "+how)
}
