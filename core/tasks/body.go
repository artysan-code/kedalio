package tasks

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// A task's notes are plain Markdown, the way Obsidian writes them: a description, a `## Steps` checklist (`- [ ]` /
// `- [x]` lines) whose ticks give the progress, `## Decisions`, and a section of attachments (`## Attachments`, or
// `## Allegati`): one link per line — a URL, a local path, a file stored with the task (`files/<id>/…`), or a wiki page
// (`[[page]]`).

var (
	stepRe      = regexp.MustCompile(`^(\s*)[-*] \[([ xX])\] (.*)$`)
	attHead     = regexp.MustCompile(`(?i)^##\s+(attachments|allegati)\s*$`)
	stepsHead   = regexp.MustCompile(`(?i)^##\s+(steps|passi)\s*$`)
	logHead     = regexp.MustCompile(`(?i)^##\s+(log|diario)\s*$`)
	decHead     = regexp.MustCompile(`(?i)^##\s+(decisions|decisioni)\s*$`)
	promotedRe  = regexp.MustCompile(`^(.*?)\s+→\s+\[\[(t-[\w-]+)\]\]$`)
	headingRe   = regexp.MustCompile(`^#{1,2}\s`)
	blankRunRe  = regexp.MustCompile(`\n{3,}`)
	itemRe      = regexp.MustCompile(`^\s*[-*]\s+(.*)$`)
	pageAttRe   = regexp.MustCompile(`^\[\[([^\]|]+)(?:\|([^\]]+))?\]\]$`)
	mdLinkRe    = regexp.MustCompile(`^\[([^\]]*)\]\((?:<([^>]+)>|([^)\s]+))\)$`)
	wikiOnlyRe  = regexp.MustCompile(`^\[\[[^\]\n]+\]\]$`)
	badLabelRe  = regexp.MustCompile(`[\[\]\n]`)
	spacesRe    = regexp.MustCompile(`\s+`)
	lineBreakRe = regexp.MustCompile(`\s*\n\s*`)
	unsafeAttRe = regexp.MustCompile(`[\n<>]`)
	needAngleRe = regexp.MustCompile(`[\s()]`)
	httpRe      = regexp.MustCompile(`^https?://`)
)

// Step is a line of the checklist.
type Step struct {
	// Text is the step as written, without the arrow to the task it became.
	Text string `json:"text"`
	Done bool   `json:"done"`
	// Promoted is the sub-task this step was promoted to: `- [ ] text → [[t-…]]`.
	Promoted string `json:"promoted,omitempty"`
}

// sectionEnd is the index of the heading that closes the section whose heading is at lines[at], or len(lines).
func sectionEnd(lines []string, at int) int {
	end := at + 1
	for end < len(lines) && !headingRe.MatchString(lines[end]) {
		end++
	}
	return end
}

// stepLines are the indexes of the lines that are steps. Only the `## Steps` section is the list; a body with no such
// section (not yet tidied) has its checkboxes read wherever they are, so nothing is lost before the move.
func stepLines(lines []string) []int {
	at := slices.IndexFunc(lines, stepsHead.MatchString)
	end := len(lines)
	if at >= 0 {
		end = sectionEnd(lines, at)
	}
	var out []int
	for i := at + 1; i < end; i++ {
		if stepRe.MatchString(lines[i]) {
			out = append(out, i)
		}
	}
	return out
}

// Steps is the checklist of a task's notes, in order. A step promoted to a task counts as done once closed says that
// task is closed (nil: none is).
func Steps(notes string, closed func(id string) bool) []Step {
	lines := strings.Split(notes, "\n")
	out := []Step{}
	for _, i := range stepLines(lines) {
		m := stepRe.FindStringSubmatch(lines[i])
		text := strings.TrimSpace(m[3])
		s := Step{Text: text, Done: m[2] != " "}
		if p := promotedRe.FindStringSubmatch(text); p != nil {
			s.Text = strings.TrimSpace(p[1])
			s.Promoted = p[2]
			if closed != nil && closed(p[2]) {
				s.Done = true
			}
		}
		out = append(out, s)
	}
	return out
}

// WithoutSteps is the notes with the checklist taken out of the `## Steps` section, for an answer that lists the steps
// apart; the heading goes too when nothing but blank lines stays under it. Notes with no such section are returned as
// they are.
func WithoutSteps(notes string) string {
	lines := strings.Split(notes, "\n")
	at := slices.IndexFunc(lines, stepsHead.MatchString)
	if at < 0 {
		return notes
	}
	end := sectionEnd(lines, at)
	kept := lines[: at+1 : at+1]
	rest := false
	for _, l := range lines[at+1 : end] {
		if stepRe.MatchString(l) {
			continue
		}
		if strings.TrimSpace(l) != "" {
			rest = true
		}
		kept = append(kept, l)
	}
	if !rest {
		kept = lines[:at:at]
	}
	return strings.TrimRight(strings.Join(append(kept, lines[end:]...), "\n"), "\n")
}

// KeepSteps is notes written whole by a client that read them without the checklist (tasks_get leaves it out): when
// the given notes have no `## Steps` section and no checkbox, the stored `## Steps` section is appended at their end, so
// a round trip never wipes the steps. Notes that bring their own steps replace the stored ones; removing steps is the
// step operations' job, or dropping the task.
func KeepSteps(stored, given string) string {
	if stepsHead.MatchString(given) || slices.ContainsFunc(strings.Split(given, "\n"), stepRe.MatchString) {
		return given
	}
	lines := strings.Split(stored, "\n")
	at := slices.IndexFunc(lines, stepsHead.MatchString)
	if at < 0 || len(stepLines(lines)) == 0 {
		return given
	}
	section := strings.TrimRight(strings.Join(lines[at:sectionEnd(lines, at)], "\n"), "\n")
	if strings.TrimSpace(given) == "" {
		return section
	}
	return strings.TrimRight(given, "\n") + "\n\n" + section
}

// Progress is how much of the checklist is ticked.
type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
	Pct   int `json:"pct"`
}

// ProgressOf is how much of the checklist is ticked; nil when there is none.
func ProgressOf(notes string, closed func(id string) bool) *Progress {
	s := Steps(notes, closed)
	if len(s) == 0 {
		return nil
	}
	done := 0
	for _, x := range s {
		if x.Done {
			done++
		}
	}
	return &Progress{done, len(s), int(math.Round(float64(done) / float64(len(s)) * 100))}
}

// SetStep is the notes with step index ticked or unticked; done nil toggles it. An index with no step changes nothing.
func SetStep(notes string, index int, done *bool) string {
	lines := strings.Split(notes, "\n")
	idx := stepLines(lines)
	if index < 0 || index >= len(idx) {
		return notes
	}
	at := idx[index]
	m := stepRe.FindStringSubmatch(lines[at])
	tick := m[2] == " "
	if done != nil {
		tick = *done
	}
	mark := " "
	if tick {
		mark = "x"
	}
	lines[at] = fmt.Sprintf("%s- [%s] %s", m[1], mark, m[3])
	return strings.Join(lines, "\n")
}

// Ticked is SetStep's done for a step to be set ticked (true) or unticked (false).
func Ticked(done bool) *bool { return &done }

// UntickSteps is every step unticked and no longer promoted to a task: the list of a recurring task as it starts again.
func UntickSteps(notes string) string {
	lines := strings.Split(notes, "\n")
	for _, i := range stepLines(lines) {
		m := stepRe.FindStringSubmatch(lines[i])
		text := m[3]
		if p := promotedRe.FindStringSubmatch(strings.TrimSpace(m[3])); p != nil {
			text = p[1]
		}
		lines[i] = fmt.Sprintf("%s- [ ] %s", m[1], strings.TrimSpace(text))
	}
	return strings.Join(lines, "\n")
}

// PromoteStep is step index marked as promoted to task id: `- [ ] text → [[id]]`.
func PromoteStep(notes string, index int, id string) (string, error) {
	lines := strings.Split(notes, "\n")
	idx := stepLines(lines)
	if index < 0 || index >= len(idx) {
		return "", refusef("no step %d", index)
	}
	m := stepRe.FindStringSubmatch(lines[idx[index]])
	if promotedRe.MatchString(strings.TrimSpace(m[3])) {
		return "", refusef("step %d is already a task", index)
	}
	lines[idx[index]] = fmt.Sprintf("%s- [%s] %s → [[%s]]", m[1], m[2], strings.TrimSpace(m[3]), id)
	return strings.Join(lines, "\n"), nil
}

// looked is what a body needs to be tidy: the log lines to move to the task's episode, and the checkboxes that are not
// in `## Steps` or are there twice.
type looked struct {
	lines   []string
	inLog   map[int]bool
	log     []string
	at      int
	inSteps []int
	stray   []int
	dup     bool
}

func look(notes string) looked {
	l := looked{lines: strings.Split(notes, "\n"), inLog: map[int]bool{}}
	for i := 0; i < len(l.lines); i++ {
		if !logHead.MatchString(l.lines[i]) {
			continue
		}
		l.inLog[i] = true
		j := i + 1
		for end := sectionEnd(l.lines, i); j < end; j++ {
			l.inLog[j] = true
			t := strings.TrimSpace(l.lines[j])
			if len(t) > 1 && (t[0] == '-' || t[0] == '*') && (t[1] == ' ' || t[1] == '\t') {
				t = strings.TrimSpace(t[2:])
			}
			if t != "" {
				l.log = append(l.log, t)
			}
		}
		i = j - 1
	}
	l.at = slices.IndexFunc(l.lines, stepsHead.MatchString)
	if l.at >= 0 {
		l.inSteps = stepLines(l.lines)
	}
	inSteps := map[int]bool{}
	for _, i := range l.inSteps {
		inSteps[i] = true
	}
	for i, line := range l.lines {
		if stepRe.MatchString(line) && !l.inLog[i] && !inSteps[i] {
			l.stray = append(l.stray, i)
		}
	}
	keys := map[string]bool{}
	for _, i := range append(slices.Clone(l.inSteps), l.stray...) {
		k := strings.TrimSpace(stepRe.FindStringSubmatch(l.lines[i])[3])
		if keys[k] {
			l.dup = true
		}
		keys[k] = true
	}
	return l
}

// NeedsTidy reports whether a body has a log section, checkboxes outside `## Steps` or the same step twice.
func NeedsTidy(notes string) bool {
	l := look(notes)
	return len(l.inLog) > 0 || len(l.stray) > 0 || l.dup
}

// Tidy is the body tidied. The log section leaves (its lines come back as log, for the task's episode). The checkboxes
// found elsewhere join `## Steps` after the ones already there, in order, a repeated step once with its tick kept.
// Nothing else is touched; a body that is tidy comes back as it was.
func Tidy(notes string) (tidied string, log []string) {
	l := look(notes)
	if len(l.inLog) == 0 && len(l.stray) == 0 && !l.dup {
		return notes, nil
	}
	type merged struct {
		indent string
		done   bool
	}
	var keys []string
	byKey := map[string]merged{}
	for _, i := range append(slices.Clone(l.inSteps), l.stray...) {
		m := stepRe.FindStringSubmatch(l.lines[i])
		k := strings.TrimSpace(m[3])
		was, seen := byKey[k]
		if !seen {
			keys = append(keys, k)
			was.indent = m[1]
		}
		byKey[k] = merged{was.indent, was.done || m[2] != " "}
	}
	var box []string
	for _, k := range keys {
		mark := " "
		if byKey[k].done {
			mark = "x"
		}
		box = append(box, fmt.Sprintf("%s- [%s] %s", byKey[k].indent, mark, k))
	}
	stray := map[int]bool{}
	for _, i := range l.stray {
		stray[i] = true
	}
	var out []string
	placed := false
	for i := 0; i < len(l.lines); i++ {
		if l.inLog[i] || stray[i] {
			continue
		}
		if i == l.at && len(keys) > 0 {
			// the Steps section: its heading, the merged steps, then whatever else it held
			end := sectionEnd(l.lines, i)
			var rest []string
			for _, x := range l.lines[i+1 : end] {
				if strings.TrimSpace(x) != "" && !stepRe.MatchString(x) {
					rest = append(rest, x)
				}
			}
			out = append(out, l.lines[i], "")
			out = append(out, box...)
			if len(rest) > 0 {
				out = append(out, "")
				out = append(out, rest...)
			}
			out = append(out, "")
			i = end - 1
			placed = true
			continue
		}
		out = append(out, l.lines[i])
	}
	text := strings.TrimSpace(blankRunRe.ReplaceAllString(strings.Join(out, "\n"), "\n\n"))
	if len(keys) > 0 && !placed {
		ls := strings.Split(text, "\n")
		block := append([]string{"## Steps", ""}, box...)
		if att := slices.IndexFunc(ls, attHead.MatchString); att >= 0 {
			ls = slices.Insert(ls, att, append(block, "")...)
		} else {
			if text != "" {
				ls = append(ls, "")
			}
			ls = append(ls, block...)
		}
		text = strings.Join(ls, "\n")
	}
	if text != "" {
		text += "\n"
	}
	return text, l.log
}

// appendTo is line appended at the end of the section whose heading matches, the section created (as `## <title>`)
// where it belongs: steps before the attachments, attachments last.
func appendTo(notes string, head *regexp.Regexp, title, line string) string {
	lines := strings.Split(strings.TrimRightFunc(notes, unicode.IsSpace), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	at := slices.IndexFunc(lines, head.MatchString)
	if at < 0 {
		// a new section goes before the attachments, when they are there
		att := -1
		if head != attHead {
			att = slices.IndexFunc(lines, attHead.MatchString)
		}
		block := []string{"## " + title, "", line}
		if att >= 0 {
			lines = slices.Insert(lines, att, append(block, "")...)
			return strings.Join(lines, "\n") + "\n"
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		return strings.Join(append(lines, block...), "\n") + "\n"
	}
	end := sectionEnd(lines, at)
	last := end - 1
	for last > at && strings.TrimSpace(lines[last]) == "" {
		last--
	}
	lines = slices.Insert(lines, last+1, line)
	return strings.Join(lines, "\n") + "\n"
}

// AddStep is a step appended to the checklist.
func AddStep(notes, text string) (string, error) {
	t := strings.TrimSpace(spacesRe.ReplaceAllString(text, " "))
	if t == "" {
		return "", refuse("a step needs some text")
	}
	return appendTo(notes, stepsHead, "Steps", "- [ ] "+t), nil
}

// NoteSection is where a dated line goes.
type NoteSection string

// The two sections a dated line goes to; the log is the task's own episode page (log.go).
const (
	SectionLog       NoteSection = "log"
	SectionDecisions NoteSection = "decisions"
)

// AddNote is a dated line added to the task's log (what happened) or its decisions (what was decided, by whom): the
// notes grow without being rewritten.
func AddNote(notes string, section NoteSection, text, day string) (string, error) {
	t := strings.TrimSpace(lineBreakRe.ReplaceAllString(strings.TrimSpace(text), " "))
	if t == "" {
		return "", refuse("a note needs some text")
	}
	if section == SectionLog {
		return appendTo(notes, logHead, "Log", "- "+day+" · "+t), nil
	}
	return appendTo(notes, decHead, "Decisions", "- "+day+" · "+t), nil
}

// Note is a line of a section.
type Note struct {
	Day  string `json:"day,omitempty"`
	Text string `json:"text"`
}

// EditNotes is one exact passage of the notes replaced; it has to occur exactly once.
func EditNotes(notes, find, replace string) (string, error) {
	if find == "" {
		return "", refuse("say what to replace")
	}
	switch n := strings.Count(notes, find); n {
	case 1:
		return strings.Replace(notes, find, replace, 1), nil
	case 0:
		return "", refuse("the passage is not in the notes: read them again (tasks_get)")
	default:
		return "", refusef("the passage occurs %d times: include more context", n)
	}
}

// Attachment is a line of the attachments section.
type Attachment struct {
	Label  string `json:"label"`
	Target string `json:"target"`
	Kind   string `json:"kind"` // url, file, path or page
}

func attachmentKind(target string) string {
	switch {
	case httpRe.MatchString(target):
		return "url"
	case strings.HasPrefix(target, "files/"):
		return "file"
	}
	return "path"
}

func lastSeg(s string) string {
	parts := strings.Split(s, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}

// Attachments is the attachments section, one entry per list line.
func Attachments(notes string) []Attachment {
	lines := strings.Split(notes, "\n")
	at := slices.IndexFunc(lines, attHead.MatchString)
	if at < 0 {
		return nil
	}
	var out []Attachment
	for _, l := range lines[at+1:] {
		if headingRe.MatchString(l) {
			break
		}
		m := itemRe.FindStringSubmatch(l)
		if m == nil || strings.TrimSpace(m[1]) == "" {
			continue
		}
		item := strings.TrimSpace(m[1])
		if page := pageAttRe.FindStringSubmatch(item); page != nil {
			label := page[2]
			if label == "" {
				parts := strings.Split(page[1], "/")
				label = parts[len(parts)-1]
			}
			out = append(out, Attachment{Label: label, Target: page[1], Kind: "page"})
			continue
		}
		var target, label string
		if md := mdLinkRe.FindStringSubmatch(item); md != nil {
			target, label = md[2], md[1]
			if target == "" {
				target = md[3]
			}
		} else {
			target = strings.TrimSuffix(strings.TrimPrefix(item, "`"), "`")
		}
		target = strings.TrimSpace(target)
		if label == "" {
			label = lastSeg(target)
		}
		if label == "" {
			label = target
		}
		out = append(out, Attachment{Label: label, Target: target, Kind: attachmentKind(target)})
	}
	return out
}

// AddAttachment is an attachment line added. Paths with spaces go in angle brackets, as CommonMark wants.
func AddAttachment(notes, target, label string) (string, error) {
	t := strings.TrimSpace(target)
	if t == "" {
		return "", refuse("an attachment needs a link or a path")
	}
	if unsafeAttRe.MatchString(t) {
		return "", refuse("an attachment cannot contain a line break, < or >")
	}
	name := strings.TrimSpace(label)
	if name == "" {
		name = lastSeg(t)
	}
	if name == "" {
		name = t
	}
	name = badLabelRe.ReplaceAllString(name, "")
	line := "- " + t
	if !wikiOnlyRe.MatchString(t) {
		// spaces and parentheses need the <…> form of a Markdown link target
		dest := t
		if needAngleRe.MatchString(t) {
			dest = "<" + t + ">"
		}
		line = fmt.Sprintf("- [%s](%s)", name, dest)
	}
	return appendTo(notes, attHead, "Attachments", line), nil
}
