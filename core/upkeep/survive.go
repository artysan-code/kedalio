package upkeep

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// minUnitWords is the words a sentence needs to count as something that can be lost; a list item counts from two.
const minUnitWords = 4

var (
	link     = regexp.MustCompile(`\[\[([^\]|#]+)(?:#[^\]|]*)?(?:\|([^\]]*))?\]\]`)
	frontRe  = regexp.MustCompile(`^---\n(?s:.*?)\n---\n?`)
	marks    = regexp.MustCompile("[*_`>#|\\[\\]]")
	spaces   = regexp.MustCompile(`\s+`)
	itemRe   = regexp.MustCompile(`^(?:[-*]\s+|\d+\.\s+)`)
	relHead  = regexp.MustCompile(`(?i)^##\s+(relations|relazioni)\s*$`)
	partOf   = regexp.MustCompile(`(?i)^[-*]\s+part_of\s`)
	sentence = regexp.MustCompile(`[.!?;]\s+`)
)

var accents = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ã", "a", "ä", "a", "å", "a", "ā", "a", "ă", "a", "ą", "a",
	"ç", "c", "ć", "c", "č", "c", "ď", "d", "đ", "d",
	"è", "e", "é", "e", "ê", "e", "ë", "e", "ē", "e", "ė", "e", "ę", "e", "ě", "e",
	"ì", "i", "í", "i", "î", "i", "ï", "i", "ī", "i", "į", "i",
	"ł", "l", "ľ", "l", "ñ", "n", "ń", "n", "ň", "n",
	"ò", "o", "ó", "o", "ô", "o", "õ", "o", "ö", "o", "ø", "o", "ō", "o", "ő", "o",
	"ŕ", "r", "ř", "r", "ś", "s", "š", "s", "ş", "s", "ș", "s", "ť", "t", "ț", "t", "ţ", "t",
	"ù", "u", "ú", "u", "û", "u", "ü", "u", "ū", "u", "ů", "u", "ű", "u", "ų", "u",
	"ý", "y", "ÿ", "y", "ź", "z", "ž", "z", "ż", "z",
)

// keyOf is a link target by its page name, lower case: [[people/bob]] and [[bob]] are one page.
func keyOf(t string) string {
	t = strings.TrimSpace(t)
	t = strings.TrimSuffix(strings.TrimSuffix(t, ".md"), ".MD")
	if i := strings.LastIndex(t, "/"); i >= 0 {
		t = t[i+1:]
	}
	return strings.ToLower(t)
}

// norm is a text in a form that ignores accents, Markdown and spacing. A labelled link keeps its label, an unlabelled
// one says where it points.
func norm(s string) string {
	s = link.ReplaceAllStringFunc(s, func(m string) string {
		g := link.FindStringSubmatch(m)
		if g[2] != "" {
			return g[2]
		}
		return keyOf(g[1])
	})
	s = accents.Replace(strings.ToLower(s))
	return strings.TrimSpace(spaces.ReplaceAllString(marks.ReplaceAllString(s, " "), " "))
}

func wordCount(s string) int {
	return len(strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }))
}

type unit struct{ raw, n string }

// units are what a page can lose: its sentences of at least minUnitWords words and its list items of at least two;
// front matter, headings, the Relations section and part_of lines are left out (relations are checked by target).
func units(body string) []unit {
	var out []unit
	inRel := false
	for _, line := range strings.Split(frontRe.ReplaceAllString(body, ""), "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "#") {
			inRel = relHead.MatchString(l)
		}
		if l == "" || strings.HasPrefix(l, "#") || inRel || partOf.MatchString(l) {
			continue
		}
		item := itemRe.MatchString(l)
		t := itemRe.ReplaceAllString(l, "")
		if n := norm(t); item && wordCount(n) >= 2 && wordCount(n) < minUnitWords {
			out = append(out, unit{t, n})
			continue
		}
		start := 0
		add := func(s string) {
			if n := norm(s); wordCount(n) >= minUnitWords {
				out = append(out, unit{s, n})
			}
		}
		for _, m := range sentence.FindAllStringIndex(t, -1) {
			add(t[start : m[0]+1])
			start = m[1]
		}
		add(t[start:])
	}
	return out
}

// relationLines are the lines of the Relations section of body that link somewhere, with the targets of each.
func relationLines(body string) (lines []string, targets [][]string) {
	inRel := false
	for _, line := range strings.Split(frontRe.ReplaceAllString(body, ""), "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "#") {
			inRel = relHead.MatchString(l)
			continue
		}
		if !inRel {
			continue
		}
		var ks []string
		for _, g := range link.FindAllStringSubmatch(l, -1) {
			ks = append(ks, keyOf(g[1]))
		}
		if len(ks) > 0 {
			lines = append(lines, l)
			targets = append(targets, ks)
		}
	}
	return
}

// hasWords is strings.Contains that does not match inside a word or a number: "port 80" is not in "port 8080".
func hasWords(hay, needle string) bool {
	if needle == "" {
		return true
	}
	for from := 0; ; {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(needle)
		before, _ := utf8.DecodeLastRuneInString(hay[:i])
		after, _ := utf8.DecodeRuneInString(hay[end:])
		inWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }
		if (i == 0 || !inWord(before)) && (end == len(hay) || !inWord(after)) {
			return true
		}
		from = i + 1
	}
}

// Missing are the sentences and fragments of text, of any length, that none of the pages holds (normalised, whole
// words). Lost leaves short fragments alone, as a page's wording may drift; an inbox entry is the only copy of what was
// said, so nothing of it may be left behind.
func Missing(text string, pages []string) []string {
	hay := norm(strings.Join(pages, "\n"))
	var out []string
	for _, line := range strings.Split(text, "\n") {
		t := itemRe.ReplaceAllString(strings.TrimSpace(line), "")
		start := 0
		check := func(f string) {
			if n := norm(f); n != "" && !hasWords(hay, n) {
				out = append(out, strings.TrimSpace(f))
			}
		}
		for _, m := range sentence.FindAllStringIndex(t, -1) {
			check(t[start : m[0]+1])
			start = m[1]
		}
		check(t[start:])
	}
	return out
}

// Lost are the sentences and list items of before (as written) found in neither after nor any of the pages in keptIn
// (bodies of the pages the text was moved to), plus the Relations lines with a target that none of them links to.
// Case, accents, Markdown and spacing do not count as a change; a reworded sentence does.
func Lost(before, after string, keptIn []string) []string {
	all := append([]string{after}, keptIn...)
	hay := norm(strings.Join(all, "\n"))
	var out []string
	for _, u := range units(before) {
		if !hasWords(hay, u.n) {
			out = append(out, u.raw)
		}
	}
	have := map[string]bool{}
	for _, b := range all {
		for _, g := range link.FindAllStringSubmatch(b, -1) {
			have[keyOf(g[1])] = true
		}
	}
	lines, targets := relationLines(before)
	for i, ks := range targets {
		for _, k := range ks {
			if !have[k] {
				out = append(out, lines[i])
				break
			}
		}
	}
	return out
}
