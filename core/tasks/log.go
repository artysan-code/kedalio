package tasks

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// A task's log is an episode page: `tasks/<id>/log.md`, kind episode, part_of the task. What happened to the task is an
// event, so it lives where the memory keeps events; the task keeps its decisions.

// LogPath is the episode page of a task's log.
func LogPath(id string) string { return "tasks/" + id + "/log.md" }

var (
	logPathRe   = regexp.MustCompile(`^tasks/t-[\w-]+/log\.md$`)
	taskPathRe  = regexp.MustCompile(`^tasks/(t-[\w-]+)\.md$`)
	summaryHead = regexp.MustCompile(`(?i)^##\s+(riassunto|summary)\s*$`)
	entriesHead = regexp.MustCompile(`(?i)^##\s+(registro|log)\s*$`)
	frontStrip  = regexp.MustCompile(`^---\n(?s:.*?)\n---\n?`)
	h2Re        = regexp.MustCompile(`^##\s`)
	bulletRe    = regexp.MustCompile(`^\s*[-*]\s+(.+)$`)
	dayLineRe   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\s*·\s*(.+)$`)
)

// IsLogPath reports whether a path is a task's log episode, not a task page.
func IsLogPath(path string) bool { return logPathRe.MatchString(path) }

// TaskIDOf is the id a path holds when it is a task page (`tasks/t-….md`), else "".
func TaskIDOf(path string) string {
	if m := taskPathRe.FindStringSubmatch(path); m != nil {
		return m[1]
	}
	return ""
}

// summaryOf is the log page's summary section as written (heading and lines, no trailing blank), or "" when it has
// none. It sits above the entries; the entries below stay as they are.
func summaryOf(body string) string {
	lines := strings.Split(frontStrip.ReplaceAllString(body, ""), "\n")
	from := -1
	for i, l := range lines {
		if summaryHead.MatchString(strings.TrimSpace(l)) {
			from = i
			break
		}
	}
	if from < 0 {
		return ""
	}
	to := len(lines)
	for i := from + 1; i < len(lines); i++ {
		if h2Re.MatchString(lines[i]) {
			to = i
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines[from:to], "\n"))
}

// logBody is the log page: its lines (`2026-10-26 · text`, as the task wrote them) and the relation to the task; a
// summary written above them is kept, the lines then under a `## Log` heading.
func logBody(id, title string, lines []string, summary string) (string, error) {
	if !IsID(id) {
		return "", refusef("not a task id: %s", id)
	}
	head := ""
	if s := strings.TrimSpace(summary); s != "" {
		head = s + "\n\n## Log\n\n"
	}
	var items []string
	for _, l := range lines {
		items = append(items, "- "+l)
	}
	return fmt.Sprintf("---\nkind: episode\n---\n\n# Log · %s\n\n%s%s\n\n## Relations\n\n- part_of [[%s]]\n",
		title, head, strings.Join(items, "\n"), id), nil
}

// logLines are the lines of a log page, oldest first. The summary section above the entries is not a line of the log.
func logLines(body string) []string {
	var out []string
	skip := false
	for _, l := range strings.Split(frontStrip.ReplaceAllString(body, ""), "\n") {
		if h2Re.MatchString(l) {
			h := strings.TrimSpace(l)
			switch {
			case summaryHead.MatchString(h):
				skip = true
			case entriesHead.MatchString(h):
				skip = false
			default:
				return out
			}
			continue
		}
		if skip {
			continue
		}
		if m := bulletRe.FindStringSubmatch(l); m != nil {
			out = append(out, strings.TrimSpace(m[1]))
		}
	}
	return out
}

// lineOf is a log line as day and text, the way the task page's notes were read.
func lineOf(l string) Note {
	if m := dayLineRe.FindStringSubmatch(l); m != nil {
		return Note{Day: m[1], Text: strings.TrimSpace(m[2])}
	}
	return Note{Text: l}
}

// joinLines is add after have, a line that is there already left out (a move done twice adds nothing).
func joinLines(have, add []string) []string {
	out := slices.Clone(have)
	for _, l := range add {
		if !slices.Contains(have, l) {
			out = append(out, l)
		}
	}
	return out
}
