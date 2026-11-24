package memory

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// A page's typed edges: a `## Relations` section (or `## Relazioni`), one per line,
// `- plays_with [[alice]] (since 2025)`. A relation's name is free and kept in one form (lower case, no accents,
// words joined by `_`), so `Plays with` and `plays-with` are one edge; synonyms across languages are merged later, by
// consolidation. A [[link]] anywhere else is an edge with no name.

var (
	relHead  = regexp.MustCompile(`(?i)^##\s+(relations|relazioni)\s*$`)
	relLine  = regexp.MustCompile(`^\s*[-*]\s+(.+?)\s+\[\[([^\]|#]+)(?:#[^\]|]*)?(?:\|[^\]]*)?\]\]\s*(?:\((.*)\))?\s*$`)
	h2       = regexp.MustCompile(`^##\s`)
	nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)
	ended    = regexp.MustCompile(`(?i)(?:fino al|until)\s+(\d{4})-(\d{2})(?:-(\d{2}))?`)
)

// accents maps the precomposed Latin letters of Latin-1 and Latin Extended-A that Unicode decomposes to their base
// letter: the standard library has no normalisation, and a new dependency is not worth these few lines.
var accents = func() map[rune]rune {
	m := map[rune]rune{}
	for base, set := range map[rune]string{
		'a': "àáâãäåāăą", 'c': "çćĉċč", 'd': "ď", 'e': "èéêëēĕėęě", 'g': "ĝğġģ", 'h': "ĥ",
		'i': "ìíîïĩīĭįı", 'j': "ĵ", 'k': "ķ", 'l': "ĺļľ", 'n': "ñńņň", 'o': "òóôõöōŏő",
		'r': "ŕŗř", 's': "śŝşš", 't': "ţť", 'u': "ùúûüũūŭůűų", 'w': "ŵ", 'y': "ýÿŷ", 'z': "źżž",
	} {
		for _, r := range set {
			m[r] = base
		}
	}
	return m
}()

// RelName returns a relation's name in its one form, or "" when nothing is left of it.
func RelName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.Is(unicode.Mn, r) {
			continue // a combining accent of text already decomposed
		}
		if base, ok := accents[r]; ok {
			r = base
		}
		b.WriteRune(r)
	}
	n := strings.Trim(nonAlnum.ReplaceAllString(b.String(), "_"), "_")
	if len(n) > 60 { // all ASCII here
		n = n[:60]
	}
	return n
}

// Edge is a link of a page.
type Edge struct {
	// Target is the target as written, without .md or heading.
	Target string
	// Rel is the relation's name; "" for a plain link.
	Rel string
	// Note is the text in brackets after the link, as written: "since 2025".
	Note string
}

// EndedBefore reports whether a relation's note says it ended (`fino al 2026-09`, `until 2026-09-15`) on a day before
// today (YYYY-MM-DD); a month alone ends on its last day. The line stays on the page, but the relation no longer holds.
func EndedBefore(note, today string) bool {
	m := ended.FindStringSubmatch(note)
	if m == nil {
		return false
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if m[3] != "" {
		day, _ = strconv.Atoi(m[3])
	}
	return fmt.Sprintf("%s-%s-%02d", m[1], m[2], day) < today
}

// EdgesOf returns the edges of a page: every line of its relations section, then every other link with no name.
func EdgesOf(body string) []Edge {
	var typed []Edge
	var rest []string
	inRel := false
	for _, l := range strings.Split(body, "\n") {
		if h2.MatchString(l) {
			inRel = relHead.MatchString(l)
		}
		var m []string
		rel := ""
		if inRel {
			if m = relLine.FindStringSubmatch(l); m != nil {
				rel = RelName(m[1])
			}
		}
		if m != nil && rel != "" {
			typed = append(typed, Edge{
				Target: stripMD(strings.TrimSpace(m[2])), Rel: rel, Note: strings.TrimSpace(m[3]),
			})
		} else {
			rest = append(rest, l)
		}
	}
	out := typed
	for _, t := range LinksIn(strings.Join(rest, "\n")) {
		out = append(out, Edge{Target: t})
	}
	return out
}
