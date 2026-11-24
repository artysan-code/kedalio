package search

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// crumb is the marker between the parts of a breadcrumb.
const crumb = " › "

// overlap is how much of the end of a piece opens the next one in the same run of text, as a share of the size: an
// answer that sits across a cut is whole in at least one of the two.
const overlap = 0.12

// defaultSize is the length, in characters, of a piece.
const defaultSize = 1200

// piece is a part of a page that is indexed, embedded and cited.
type piece struct {
	text string
	// heading is the heading the piece sits under, without its # marks.
	heading string
	// context is the breadcrumb «title › heading › subheading», indexed and embedded with the piece so one deep in a
	// page still matches its subject. Empty when there is no heading above it (the title alone says nothing more).
	context string
	// page and url say where in its source the piece sits: a PDF page from 1, a site page's address. Zero for a wiki page.
	page int
	url  string
}

// locator is where the piece sits in its source, as the JSON a chunk keeps.
func (p piece) locator() string {
	l := Locator{Page: p.page, Heading: p.heading, URL: p.url}
	b, _ := json.Marshal(l)
	return string(b)
}

// Locator is where a passage sits in its source: a PDF page, the heading above it, the address of a fetched page.
type Locator struct {
	Page    int    `json:"page,omitempty"`
	Heading string `json:"heading,omitempty"`
	URL     string `json:"url,omitempty"`
}

// Section is a part of a document that is cut on its own: a PDF page, a site page, a sheet or a whole Markdown note.
// Its place travels with every piece cut from it, so a citation can say «p. 12». A piece never crosses a section.
type Section struct {
	Text string
	// Page is the PDF page from 1, URL the address of a fetched page; both may be unset.
	Page int
	URL  string
}

var (
	frontMatter = regexp.MustCompile(`(?s)^---\n.*?\n---\n?`)
	sentenceEnd = regexp.MustCompile(`[.!?…]["')\]]*[\s\p{Z}]+`)
	gap         = regexp.MustCompile(`[\s\p{Z}]+`)
	paragraphs  = regexp.MustCompile(`\n{2,}`)
	fence       = regexp.MustCompile("(?m)^```")
	headingLine = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	hashes      = regexp.MustCompile(`^#+`)
	hashPrefix  = regexp.MustCompile(`^#+\s+`)
	atxHeading  = regexp.MustCompile(`^#{1,6}\s+\S`)
)

// separateHeadings gives a heading that follows text on the next line a blank line of its own, and reads CRLF as LF,
// so the cut by paragraphs sees both. A line inside a fenced code block is code, whatever it starts with.
func separateHeadings(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines)+4)
	fenced := false
	for i, l := range lines {
		if strings.HasPrefix(l, "```") {
			fenced = !fenced
		} else if !fenced && i > 0 && atxHeading.MatchString(l) && strings.TrimSpace(lines[i-1]) != "" {
			out = append(out, "")
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// tail returns the end of text, about n characters, starting at a sentence or else a word: never a torn word. Empty
// when there is no boundary to start from.
func tail(text string, n int) string {
	r := []rune(text)
	if n < 1 || len(r) <= n {
		return ""
	}
	from := len(r) - n
	cut := string(r[from:])
	if end := sentenceEnd.FindStringIndex(cut); end != nil {
		if s := strings.TrimSpace(cut[end[1]:]); s != "" {
			return s
		}
	}
	// no sentence starts inside it: begin at a word, unless the tail already does
	if unicode.IsSpace(r[from-1]) {
		return strings.TrimSpace(cut)
	}
	if g := gap.FindStringIndex(cut); g != nil {
		return strings.TrimSpace(cut[g[1]:])
	}
	return ""
}

// wordCut is where the piece that starts at from ends: size characters on, moved back to the last space when one lies
// in the second half of the piece; a run with no space in it (a long address, unspaced text) is cut at size.
func wordCut(r []rune, from, size int) int {
	end := from + size
	if end >= len(r) {
		return len(r)
	}
	for j := end; j > from+size/2; j-- {
		if unicode.IsSpace(r[j]) {
			return j
		}
	}
	return end
}

// chunk cuts a page where its structure allows, headings then paragraphs, into pieces of about size characters, each
// carrying its heading so a piece read alone still says what it is. title starts every piece's breadcrumb.
func chunk(text string, size int, title string) []piece {
	return chunkSections([]Section{{Text: text}}, size, title)
}

// chunkSections cuts sections as chunk cuts a page. A heading carries over into the next section (a chapter goes on
// across PDF pages), and so does the breadcrumb.
func chunkSections(sections []Section, size int, title string) []piece {
	var out []piece
	var heading string
	// the headings above the current one, by level: a sibling or a higher heading takes the place of what it closes
	type crumbPart struct {
		level int
		text  string
	}
	var crumbs []crumbPart
	lap := int(float64(size)*overlap + 0.5)
	context := func() string {
		if len(crumbs) == 0 {
			return ""
		}
		var parts []string
		if title != "" {
			parts = append(parts, title)
		}
		for _, c := range crumbs {
			parts = append(parts, c.text)
		}
		return strings.Join(parts, crumb)
	}
	at := func() string { return hashPrefix.ReplaceAllString(heading, "") }

	for _, sec := range sections {
		text := strings.TrimSpace(frontMatter.ReplaceAllString(strings.ReplaceAll(sec.Text, "\r\n", "\n"), ""))
		text = separateHeadings(text)
		if text == "" {
			continue
		}
		var cur string
		// the body of the last piece cut under the current heading in this section: the next one opens with its end. A
		// heading or a section ends it, so an overlap never carries text across either.
		var prev string
		cutPiece := func(body string, own bool) string {
			var lead string
			if prev != "" {
				lead = tail(prev, lap)
			}
			prev = body
			var s string
			if heading != "" && !own {
				s = heading + "\n"
			}
			if lead != "" {
				s += lead + "\n\n"
			}
			return s + body
		}
		emit := func(text string) {
			out = append(out, piece{text: text, heading: at(), context: context(), page: sec.Page, url: sec.URL})
		}
		flush := func() {
			if strings.TrimSpace(cur) != "" {
				own := heading != "" && strings.HasPrefix(cur, heading)
				emit(cutPiece(strings.TrimSpace(cur), own))
				// what the next piece borrows from is the text, not the heading line it began with
				if own {
					prev = strings.TrimSpace(prev[len(heading):])
				}
			}
			cur = ""
		}
		// inside a fenced code block a «# comment» is code, whatever blank lines the block has
		fenced := false
		for _, para := range paragraphs.Split(text, -1) {
			inCode := fenced
			if len(fence.FindAllString(para, -1))%2 == 1 {
				fenced = !fenced
			}
			if h := headingLine.FindStringSubmatch(para); h != nil && !inCode && strings.HasPrefix(strings.TrimSpace(para), "#") {
				flush()
				heading = h[0]
				prev = ""
				level := len(hashes.FindString(strings.TrimSpace(para)))
				kept := crumbs[:0]
				for _, c := range crumbs {
					if c.level < level {
						kept = append(kept, c)
					}
				}
				crumbs = append(kept, crumbPart{level, strings.TrimSpace(h[1])})
			}
			n := utf8.RuneCountInString(para)
			if utf8.RuneCountInString(cur)+n > size {
				flush()
			}
			if n > size {
				// a paragraph longer than a piece is cut at a word, never through one: a torn word would surface in a
				// citation, and the next piece's overlap would carry its first half in front of its second
				r := []rune(para)
				// a paragraph that opens with its heading already carries it in its first piece
				own := heading != "" && strings.HasPrefix(strings.TrimSpace(para), heading)
				for i := 0; i < len(r); {
					end := wordCut(r, i, size)
					emit(cutPiece(strings.TrimSpace(string(r[i:end])), own && i == 0))
					i = end
					for i < len(r) && unicode.IsSpace(r[i]) {
						i++
					}
				}
				continue
			}
			if cur != "" {
				cur += "\n\n"
			}
			cur += para
		}
		flush()
	}
	return out
}
