package mcp

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"kedalio/core/memory"
	"kedalio/core/tasks"
	"kedalio/core/vault"
)

// How the memory is written. Agents write without asking, so the rules are
// enforced here rather than recommended: a write that breaks one is refused with the reason, and the writer fixes it.
// Folders are free; the areas are only where the diary and the inbox live. Pages are small: one subject
// each, a title and a sentence saying what it is.

const (
	// hardWords is the longest a page may be. It is the only length limit enforced: a hub past 500 words is
	// not refused, since the core has no reorganisation to ask for.
	hardWords    = 800
	maxWordsLog  = 1000 // the inbox: what is said in passing waits there to be sorted, it does not pile up
	maxEntry     = 40   // one diary line: what changed, in a sentence; the detail lives on the project page
	maxCodeLines = 15
	// duplicate is the title similarity from which a new page is taken for an existing one.
	duplicate = 0.6
	// maxRemember is the longest text memory_write op remember takes.
	maxRemember = 2000
)

// fold is a text in lower case without the accents of the Latin alphabets.
func fold(s string) string { return memory.Fold(s) }

var (
	notSlug   = regexp.MustCompile(`[^a-z0-9._-]+`)
	dashes    = regexp.MustCompile(`-{2,}`)
	frontRe   = regexp.MustCompile(`^---\n(?s:.*?)\n---\n?`)
	wordRe    = regexp.MustCompile(`[\p{L}\p{N}]+`)
	linkRe    = regexp.MustCompile(`\[\[[^\]]*\]\]`)
	titleRe   = regexp.MustCompile(`(?m)^# (.+)$`)
	h1Re      = regexp.MustCompile(`^# \S`)
	notPlain  = regexp.MustCompile("^\\s*([#>|-]|\\*|\\d+\\.|```)")
	fenceRe   = regexp.MustCompile("^\\s*```")
	diaryRest = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	weekRest  = regexp.MustCompile(`(?i)^(settimane|weeks)/\d{4}-w(0[1-9]|[1-4]\d|5[0-3])$`)
)

// slugPath is a path in the form the memory keeps: lower case, no accents, dashes for spaces.
func slugPath(p string) string {
	s := strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	s = strings.TrimLeft(s, "/")
	if len(s) >= 3 && strings.EqualFold(s[len(s)-3:], ".md") {
		s = s[:len(s)-3]
	}
	var segs []string
	for _, seg := range strings.Split(s, "/") {
		seg = notSlug.ReplaceAllString(fold(seg), "-")
		seg = strings.Trim(dashes.ReplaceAllString(seg, "-"), "-")
		if seg != "" {
			segs = append(segs, seg)
		}
	}
	return strings.Join(segs, "/") + ".md"
}

func withoutFront(body string) string { return frontRe.ReplaceAllString(body, "") }

// pageWords is the words of a page, its front matter left out.
func pageWords(s string) int { return len(wordRe.FindAllString(withoutFront(s), -1)) }

func areaOf(path string) string { p, _, _ := strings.Cut(path, "/"); return p }

func (s *Server) isLog(area string) bool { return area == s.areas.Diary || area == s.areas.Inbox }

// maxWords is how long a page of the area may be; the diary is a record, so nothing is refused for its size.
func (s *Server) maxWords(area string) int {
	switch area {
	case s.areas.Diary:
		return int(^uint(0) >> 1)
	case s.areas.Inbox:
		return maxWordsLog
	}
	return hardWords
}

// secret reports whether the mask would change a text: it looks like a secret, which is never kept.
func (s *Server) secret(text string) bool { return s.cfg.Mask != nil && s.cfg.Mask(text) != text }

// newSecretPairs is the names of the credential-like name = value pairs (vault.SecretPairs) a text holds that was did
// not. Only the new ones: a page that already holds one, which the health scan lists for the owner, stays editable.
func newSecretPairs(was, text string) []string {
	old := map[vault.Pair]int{}
	for _, p := range vault.SecretPairs(was) {
		old[p]++
	}
	var names []string
	for _, p := range vault.SecretPairs(text) {
		if old[p] > 0 {
			old[p]--
			continue
		}
		if !slices.Contains(names, p.Name) {
			names = append(names, p.Name)
		}
	}
	return names
}

// secretHits is how many things the mask hides in a text (a value, a shape, the password of an address), counted as the
// placeholders it leaves that the text did not hold already. The mask judges the text whole, so a secret split over
// lines (a Bearer and its token, a key in two parts) is one hit.
func (s *Server) secretHits(text string) int {
	if s.cfg.Mask == nil {
		return 0
	}
	count := func(t string) int { return strings.Count(t, "\u2039hidden") + strings.Count(t, "\u2039password\u203a") }
	return max(0, count(s.cfg.Mask(text))-count(text))
}

// newSecret reports whether a write brings a secret: the new body holds more of what the mask hides than the old one.
// A page that holds a secret-like line from before the scan stays editable, whatever is added above or below it, but a
// secret added, finished or swapped in by the added text is refused. With no old body it is the whole text.
func (s *Server) newSecret(was, body string) bool {
	if !s.secret(body) {
		return false
	}
	hits := s.secretHits(body)
	if hits == 0 {
		return !s.secret(was) // the mask changed the text in a way it does not count
	}
	if hits > s.secretHits(was) {
		return true
	}
	// the count did not grow, but a secret may have been swapped for another on a changed line: a hit that is gone
	// once the lines the old body did not have are emptied rests, even partly, on what the write brings
	return hits > s.secretHits(withoutNewLines(was, body))
}

// withoutNewLines is body with the lines the old body did not have (as many times) emptied, the others in place.
func withoutNewLines(was, body string) string {
	old := map[string]int{}
	for _, l := range strings.Split(was, "\n") {
		old[l]++
	}
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if old[l] > 0 {
			old[l]--
		} else {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// bodyOf is a page's body, "" when there is no page.
func bodyOf(d *memory.Doc) string {
	if d == nil {
		return ""
	}
	return d.Body
}

// looksSecret is whether a text holds what the memory never keeps: a secret by the mask, or a credential-like pair.
func (s *Server) looksSecret(text string) bool {
	return s.secret(text) || len(vault.SecretPairs(text)) > 0
}

// secretKinds is why a text looks secret, in words and without its values: the shapes (vault.ShapeKinds), «a value
// the vault holds» when the mask hides more than the shapes, and «name = value: NAME» for each pair. Nothing for a text
// that holds none, or only placeholders a mask left.
func (s *Server) secretKinds(text string) []string {
	kinds := vault.ShapeKinds(text)
	if s.cfg.Mask != nil && s.cfg.Mask(text) != vault.MaskField("", text) {
		kinds = append(kinds, "a value the vault holds")
	}
	seen := map[string]bool{}
	for _, p := range vault.SecretPairs(text) {
		if !seen[p.Name] {
			seen[p.Name] = true
			kinds = append(kinds, "name = value: "+p.Name)
		}
	}
	return kinds
}

// similarity is how alike two titles are; see memory.TitleSimilarity.
func similarity(a, b string) float64 { return memory.TitleSimilarity(a, b) }

// entryErrors: a diary entry too long to be one line of the record; its [[links]] are not counted.
func entryErrors(text string) []string {
	n := pageWords(linkRe.ReplaceAllString(text, ""))
	if n > maxEntry {
		return []string{fmt.Sprintf("%d words: a diary line says what changed in at most %d; the detail goes on the project page", n, maxEntry)}
	}
	return nil
}

// diaryRewriteErrors: a diary page may be rewritten only to tidy it, never edited away: every timed line stays, at the
// same time and in the same order, none is added, and each fits a line.
func diaryRewriteErrors(cur, next string) []string {
	entries := func(b string) []string {
		var out []string
		for _, l := range strings.Split(b, "\n") {
			if entryHead.MatchString(l) {
				out = append(out, l)
			}
		}
		return out
	}
	times := func(ls []string) []string {
		var out []string
		for _, l := range ls {
			out = append(out, l[2:7])
		}
		return out
	}
	was, now := times(entries(cur)), entries(next)
	if strings.Join(times(now), ",") != strings.Join(was, ",") {
		return []string{fmt.Sprintf("a diary page is tidied, not edited: keep every timed line, at its time and in its order (%s)", strings.Join(was, ", "))}
	}
	var out []string
	for _, l := range now {
		for _, e := range entryErrors(entryText(l)) {
			out = append(out, l[2:7]+": "+e)
		}
	}
	return out
}

func (s *Server) weeksDir() string {
	if s.areas == memory.AreasIT {
		return "settimane"
	}
	return "weeks"
}

func isDiaryRest(rest string) bool { return diaryRest.MatchString(rest) || weekRest.MatchString(rest) }

// isWeekPath: a week summary page of the diary (`diario/settimane/2026-w42`).
func (s *Server) isWeekPath(path string) bool {
	parts := strings.Split(strings.TrimSuffix(path, ".md"), "/")
	return parts[0] == s.areas.Diary && len(parts) == 3 && isDiaryRest(strings.Join(parts[1:], "/"))
}

// shapeErrors: what is wrong with a page's place and shape, before anything is looked up.
// was is the page's body before the write ("" for a new page): a secret is refused only when the write brings it.
func (s *Server) shapeErrors(path, was, body string) []string {
	var errs []string
	if tasks.IsLogPath(path) {
		// a task's log is the task book's page, not a note: no title, lede or length rules
		if s.newSecret(was, body) {
			errs = append(errs, "it contains what looks like a secret: never in the memory")
		}
		return errs
	}
	parts := strings.Split(strings.TrimSuffix(path, ".md"), "/")
	area := parts[0]
	if area == s.areas.Diary && !isDiaryRest(strings.Join(parts[1:], "/")) {
		errs = append(errs, fmt.Sprintf("a diary page is %s/YYYY-MM-DD.md, one a day (or %s/%s/YYYY-Www for a week)",
			s.areas.Diary, s.areas.Diary, s.weeksDir()))
	}
	text := strings.TrimSpace(withoutFront(body))
	lines := strings.Split(text, "\n")
	if !h1Re.MatchString(lines[0]) {
		errs = append(errs, `the page starts with its title: "# Title"`)
	}
	first := ""
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) != "" {
			first = l
			break
		}
	}
	if first == "" || notPlain.MatchString(first) {
		errs = append(errs, "under the title, one plain sentence saying what the page is")
	}
	if n, max := pageWords(body), s.maxWords(area); n > max {
		if max == hardWords {
			errs = append(errs, fmt.Sprintf("%d words: at most %d. Keep here what matters and move the details into pages of their own, each with "+
				"`- part_of [[this page]]` in its relations", n, max))
		} else {
			errs = append(errs, fmt.Sprintf("%d words: at most %d. Split it into smaller pages linked to each other", n, max))
		}
	}
	code, in := 0, false
	for _, l := range lines {
		if fenceRe.MatchString(l) {
			in = !in
		} else if in {
			code++
		}
	}
	if code > maxCodeLines {
		errs = append(errs, fmt.Sprintf("%d lines of code: at most %d. Code lives in its repository; here, where to find it", code, maxCodeLines))
	}
	if s.newSecret(was, body) {
		errs = append(errs, "it contains what looks like a secret (a password, a token, credentials in an address): never in the memory")
	}
	return errs
}

// verdict is a write checked against the rules and against what the memory already holds.
type verdict struct {
	errors  []string
	similar []pageRef
}

type pageRef struct {
	Path  string `json:"path"`
	Title string `json:"title"`
}

// kindFor is the kind (and `is`) a page will have once written: as its front matter says, else as it is now, else from
// its folder (the same order as Store.Write).
func (s *Server) kindFor(ctx context.Context, path, body string) (memory.Kind, string, error) {
	fm := memory.FrontMatter(body)
	if memory.IsKind(fm["kind"]) {
		return memory.Kind(fm["kind"]), fm["is"], nil
	}
	cur, err := s.cfg.Memory.Get(ctx, path)
	if err != nil {
		return "", "", err
	}
	if cur != nil && cur.Kind != "" {
		return cur.Kind, memory.FrontMatter(cur.Body)["is"], nil
	}
	k := memory.InferKind(path, s.areas)
	return k.Kind, k.Is, nil
}

// check is a write against the rules. A new page whose title is close to one of the same kind is refused unless
// distinct says it is another subject.
// was is the page's body before this write ("" for a new page): a credential-like pair is refused only when the write
// brings it (newSecretPairs).
func (s *Server) check(ctx context.Context, path, body, was string, creating, distinct bool) (verdict, error) {
	v := verdict{errors: s.shapeErrors(path, was, body)}
	if names := newSecretPairs(was, body); len(names) > 0 {
		v.errors = append(v.errors, "it contains what looks like a credential ("+strings.Join(names, ", ")+
			" with a value): never in the memory; say where it is kept instead (the service's panel, the secrets file)")
	}
	if !creating || distinct || s.isLog(areaOf(path)) {
		return v, nil
	}
	title := path
	if m := titleRe.FindStringSubmatch(strings.TrimSpace(withoutFront(body))); m != nil {
		title = m[1]
	}
	name := nameOf(path)
	kind, is, err := s.kindFor(ctx, path, body)
	if err != nil {
		return v, err
	}
	// only among pages of the same kind and the same `is`: a client and its project, or a person and the client they
	// work for, share a name by nature
	pages, err := s.cfg.Memory.List(ctx, "", memory.ListOpts{Kind: kind, Limit: 1_000_000})
	if err != nil {
		return v, err
	}
	for _, p := range pages {
		if p.Path == path || strings.HasPrefix(p.Path, "tasks/") || max(similarity(title, p.Title), similarity(name, nameOf(p.Path))) < duplicate {
			continue
		}
		other, err := s.cfg.Memory.Get(ctx, p.Path)
		if err != nil {
			return v, err
		}
		if other == nil || memory.FrontMatter(other.Body)["is"] != is {
			continue
		}
		if v.similar = append(v.similar, pageRef{p.Path, p.Title}); len(v.similar) == 5 {
			break
		}
	}
	if len(v.similar) > 0 {
		v.errors = append(v.errors,
			"pages with a very similar title exist: update one of them, or pass distinct: true if this really is another subject")
	}
	return v, nil
}

// nameOf is a page's file name as words: `some-page`, `some page`.
func nameOf(path string) string {
	n := path[strings.LastIndex(path, "/")+1:]
	return strings.ReplaceAll(strings.TrimSuffix(n, ".md"), "-", " ")
}

var linkTarget = regexp.MustCompile(`\[\[([^\]|#]+)((?:#[^\]|]*)?)((?:\|[^\]]*)?)\]\]`)

// relink is a document's text with every link that meant `from` pointing at `to` instead.
func relink(body, from, to string, resolve memory.Resolver) string {
	target := strings.TrimSuffix(to, ".md")
	return linkTarget.ReplaceAllStringFunc(body, func(m string) string {
		g := linkTarget.FindStringSubmatch(m)
		if resolve(strings.TrimSpace(g[1])) == from {
			return "[[" + target + g[2] + g[3] + "]]"
		}
		return m
	})
}
