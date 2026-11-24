// Package clip reads a long text in pieces: a window of it from an offset, or one section of a Markdown text by its
// heading. The tools use it so an answer stays short by default and the rest is one call away.
package clip

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Heading is a Markdown heading: its text, its level (1-6) and the offset, in characters, of its line.
type Heading struct {
	Title  string `json:"title"`
	Level  int    `json:"level"`
	Offset int    `json:"offset"`
}

// Headings lists the headings of text in order. A line in a fenced code block is not a heading.
func Headings(text string) []Heading {
	var out []Heading
	fence := ""
	pos := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case fence != "":
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
		case strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~"):
			fence = trimmed[:3]
		default:
			if level, title, ok := heading(trimmed); ok {
				out = append(out, Heading{Title: title, Level: level, Offset: pos})
			}
		}
		pos += utf8.RuneCountInString(line)
	}
	return out
}

func heading(line string) (int, string, bool) {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(line) || line[n] != ' ' {
		return 0, "", false
	}
	title := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(line[n:]), "#"))
	return n, title, title != ""
}

// Section is the part of text under the heading called title (case and surrounding spaces do not matter): from its
// line to the next heading of the same or a higher level. Where no heading has exactly that title, a title that
// contains it is taken when only one does. offset is where the section starts in text.
func Section(text, title string) (body string, offset int, ok bool) {
	hs := Headings(text)
	want := strings.ToLower(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(title), "#")))
	at := -1
	for i, h := range hs {
		if strings.ToLower(h.Title) == want {
			at = i
			break
		}
	}
	if at < 0 {
		for i, h := range hs {
			if strings.Contains(strings.ToLower(h.Title), want) {
				if at >= 0 {
					return "", 0, false // ambiguous
				}
				at = i
			}
		}
	}
	if at < 0 || want == "" {
		return "", 0, false
	}
	end := -1
	for _, h := range hs[at+1:] {
		if h.Level <= hs[at].Level {
			end = h.Offset
			break
		}
	}
	runes := []rune(text)
	if end < 0 {
		end = len(runes)
	}
	return string(runes[hs[at].Offset:end]), hs[at].Offset, true
}

// Cut says how a window sits in its text.
type Cut struct {
	Total  int  // characters in the text
	Offset int  // where the window starts
	Next   int  // where the rest starts, when there is some
	Cut    bool // there is more after the window
}

// Window is at most max characters of text from offset, cut at a line end when one is near. max 0 or less is the whole
// rest.
func Window(text string, offset, max int) (string, Cut) {
	runes := []rune(text)
	c := Cut{Total: len(runes)}
	offset = min(max0(offset), len(runes))
	c.Offset = offset
	end := len(runes)
	if max > 0 && offset+max < end {
		end = offset + max
		// a line end in the last quarter of the window is a better place to stop than the middle of a line
		for i := end; i > offset+max*3/4; i-- {
			if runes[i-1] == '\n' {
				end = i
				break
			}
		}
		c.Cut, c.Next = true, end
	}
	return string(runes[offset:end]), c
}

// Words is text with its whitespace folded to single spaces, cut to at most n characters at a word boundary and ended
// with «…» when it was cut. A single word longer than n is cut where n falls.
func Words(text string, n int) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) <= n {
		return string(runes)
	}
	cut := n
	for cut > 0 && runes[cut] != ' ' {
		cut--
	}
	if cut == 0 {
		cut = n
	}
	return strings.TrimRight(string(runes[:cut]), " ") + "…"
}

func max0(n int) int { return max(n, 0) }

// WindowJSON is Window for a text that goes out as a JSON string: max counts the string as sent, where a newline or a
// quote takes two characters, so the window shrinks until it fits.
func WindowJSON(text string, offset, max int) (string, Cut) {
	piece, c := Window(text, offset, max)
	// also when nothing was cut: the text can be shorter than max in characters and longer as sent
	for i := 0; max > 0 && i < 6; i++ {
		enc, _ := json.Marshal(piece)
		if sent := len(enc) - 2; sent <= max {
			break
		} else {
			// never <= 0: Window reads that as no limit
			smaller := utf8.RuneCountInString(piece)*max/sent - 1
			if smaller < 1 {
				smaller = 1
			}
			piece, c = Window(text, offset, smaller)
		}
	}
	return piece, c
}
