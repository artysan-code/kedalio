package memory

import (
	"context"
	"database/sql"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"kedalio/core/memory/search"
)

// Facts in time. A line added to a page that is near in meaning to a line the page already had, and differs from it in
// a number, a date or a name, is likely to replace it. The old line is never touched here: the pair is queued for the
// upkeep, which moves the old line to the history section or keeps both. It is found in the background embedding
// lane, not at write: a write never waits for the model.

const (
	// ConflictSimilarity is the cosine from which two lines say the same thing about the same subject.
	ConflictSimilarity = 0.85
	// conflictAdded and conflictOld bound the work per page: the added lines compared, and the longest old lines (the
	// ones that carry a fact) they are compared with.
	conflictAdded = 10
	conflictOld   = 40
	factMinWords  = 3
	// conflictLineRunes is how much of a line is embedded: a page that is one long paragraph must not make the call
	// too big for the model, or fail it for ever.
	conflictLineRunes = 500
	// conflictTries is how many rounds the embedder may fail on one page before the page is passed over, so that one
	// page cannot hold up every page after it.
	conflictTries = 3
)

var (
	sectionOut = regexp.MustCompile(`(?i)^##\s+(storia|history|relazioni|relations)\s*$`)
	headingRe  = regexp.MustCompile(`^#{1,6}\s`)
	listMark   = regexp.MustCompile(`^\s*[-*]\s+`)
	frontRe    = regexp.MustCompile(`^---\n(?s:.*?)\n---\n?`)
	// stamp is what a line may end with, `(2026-10-26)` or `(26/10)`: when it was said, not what.
	stamp = regexp.MustCompile(`\s*\((?:\d{4}-\d{2}(?:-\d{2})?|\d{1,2}/\d{1,2}(?:/\d{2,4})?)\)\s*$`)
	// leadStamp is what a line may open with, `2026-11-04 20:54` as an inbox entry placed word for word does: when it
	// was written down, not what.
	leadStamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(?:[ T]\d{2}:\d{2}(?::\d{2})?)?(?:\s*[:·—–-])?\s+`)
	// pointer is a line of an index, `[[page]] — what it holds`: the facts are on that page, and two entries for
	// different pages differ in their link and their gloss's date by design.
	pointer   = regexp.MustCompile(`^\[\[[^\]]+\]\]\s*[—–:-]\s`)
	wordSplit = regexp.MustCompile(`[\s,;:()«»"]+`)
)

// FactLines are the lines of a page that state a fact: list items and sentences, outside the front matter, the
// headings, the history and relations sections and the index entries, with the list marker taken off.
func FactLines(body string) []string {
	var out []string
	skip := false
	for _, raw := range strings.Split(frontRe.ReplaceAllString(body, ""), "\n") {
		if headingRe.MatchString(raw) {
			skip = sectionOut.MatchString(raw)
			continue
		}
		l := strings.TrimSpace(listMark.ReplaceAllString(raw, ""))
		if skip || len(strings.Fields(l)) < factMinWords || pointer.MatchString(l) {
			continue
		}
		out = append(out, l)
	}
	return out
}

func factWords(s string) []string {
	var out []string
	s = leadStamp.ReplaceAllString(stamp.ReplaceAllString(s, ""), "")
	for _, w := range wordSplit.Split(s, -1) {
		if w = strings.TrimRight(w, ".!?"); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// DiffersInFact reports whether two lines differ in a fact: a number, a date, a code or a capitalised name that is in
// one and not in the other. The stamp of the day a line was said, at its end or its start, is not a fact, and a capital opening the line is
// grammar, not a name.
func DiffersInFact(a, b string) bool {
	only := func(x, y []string) bool {
		has := map[string]bool{}
		for _, w := range y {
			has[strings.ToLower(w)] = true
		}
		for i, w := range x {
			if has[strings.ToLower(w)] {
				continue
			}
			if strings.IndexFunc(w, unicode.IsDigit) >= 0 {
				return true
			}
			if r, _ := utf8.DecodeRuneInString(w); i > 0 && unicode.IsUpper(r) {
				return true
			}
		}
		return false
	}
	wa, wb := factWords(a), factWords(b)
	return only(wa, wb) || only(wb, wa)
}

// Conflict is a queued pair: a line the page had and the line that was added near it.
type Conflict struct {
	Path, Old, New, Day string
}

// CheckConflicts looks at up to `pages` pages written since they were last checked and queues the pairs of lines that
// conflict. Tasks, episodes, diary days and the inbox are left out: they record what happened or is not settled. A page
// is marked checked once compared; when the embedder fails on it it is not, the pages after it are still compared, and
// the first error comes back for the next round. A page that has failed conflictTries rounds is passed over (marked
// checked, nothing queued) if the embedder answers a short text: it is the page, not the embedder. It returns how many
// pages were checked.
func (s *Store) CheckConflicts(ctx context.Context, e search.Embedder, pages int) (int, error) {
	type todo struct {
		path, updated, body string
		rev, was            int
		hasWas              bool
	}
	rows, err := s.db.QueryContext(ctx, `select d.path, d.rev, d.updated, d.body, f.rev from docs d
     left join fact_checked f on f.path = d.path
     where d.deleted = 0 and (f.path is null or f.rev < d.rev)
       and d.path not like 'tasks/%' and coalesce(d.kind, '') not in ('episode', 'task')
       and d.path not like ? escape '\' and d.path not like ? escape '\'
     order by d.updated, d.path limit ?`,
		folderLike(s.Areas.Diary), folderLike(s.Areas.Inbox), pages)
	if err != nil {
		return 0, err
	}
	var list []todo
	for rows.Next() {
		var t todo
		var was sql.NullInt64
		if err := rows.Scan(&t.path, &t.rev, &t.updated, &t.body, &was); err != nil {
			rows.Close()
			return 0, err
		}
		t.was, t.hasWas = int(was.Int64), was.Valid
		list = append(list, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	done := 0
	var firstErr error
	var failed []todo
	for _, t := range list {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		var before string
		if t.hasWas {
			err := s.db.QueryRowContext(ctx, "select body from revisions where path = ? and rev = ?", t.path, t.was).Scan(&before)
			if err != nil && err != sql.ErrNoRows {
				return done, err
			}
		}
		pairs, err := conflictsOf(ctx, e, before, t.body)
		if err != nil {
			if ctx.Err() != nil {
				return done, ctx.Err()
			}
			if firstErr == nil {
				firstErr = err
			}
			failed = append(failed, t)
			continue
		}

		day := t.updated
		if len(day) > 10 {
			day = day[:10]
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return done, err
		}
		for _, p := range pairs {
			if _, err := tx.ExecContext(ctx, "insert or ignore into fact_conflicts (path, old, new, day) values (?, ?, ?, ?)",
				t.path, p[0], p[1], day); err != nil {
				tx.Rollback()
				return done, err
			}
		}
		if _, err := tx.ExecContext(ctx, `insert into fact_checked (path, rev) values (?, ?)
     on conflict (path) do update set rev = excluded.rev`, t.path, t.rev); err != nil {
			tx.Rollback()
			return done, err
		}
		if err := tx.Commit(); err != nil {
			return done, err
		}
		s.factMu.Lock()
		delete(s.factFails, t.path+"@"+strconv.Itoa(t.rev))
		s.factMu.Unlock()
		done++
	}
	for _, t := range failed {
		key := t.path + "@" + strconv.Itoa(t.rev)
		s.factMu.Lock()
		if s.factFails == nil {
			s.factFails = map[string]int{}
		}
		s.factFails[key]++
		over := s.factFails[key] >= conflictTries
		s.factMu.Unlock()
		if !over {
			continue
		}
		if _, err := e.Embed(ctx, []string{"probe"}); err != nil {
			continue // the embedder is down: the page is not at fault
		}
		s.factMu.Lock()
		delete(s.factFails, key)
		s.factMu.Unlock()
		if _, err := s.db.ExecContext(ctx, `insert into fact_checked (path, rev) values (?, ?)
     on conflict (path) do update set rev = excluded.rev`, t.path, t.rev); err != nil {
			return done, err
		}
		done++
	}
	return done, firstErr
}

// folderLike is the LIKE pattern of a folder's pages; a folder that is not set matches nothing.
func folderLike(folder string) string {
	if folder == "" {
		return "\x00"
	}
	return likePrefix(folder + "/")
}

// conflictsOf are the pairs {old, new} between the lines `after` added to `before` and the longest lines `before`
// had, one embedder call for all of them.
func conflictsOf(ctx context.Context, e search.Embedder, before, after string) ([][2]string, error) {
	had := map[string]bool{}
	for _, l := range FactLines(before) {
		had[l] = true
	}
	var added, old []string
	seen := map[string]bool{}
	for _, l := range FactLines(after) {
		if seen[l] {
			continue
		}
		seen[l] = true
		if had[l] {
			old = append(old, l)
		} else {
			added = append(added, l)
		}
	}
	// lines the page lost cannot conflict with anything on it, so only those it still has are the old ones
	if len(added) == 0 || len(old) == 0 {
		return nil, nil
	}
	added = added[:min(len(added), conflictAdded)]
	sort.SliceStable(old, func(i, j int) bool { return len(old[i]) > len(old[j]) })
	old = old[:min(len(old), conflictOld)]
	texts := make([]string, 0, len(added)+len(old))
	for _, l := range append(append([]string{}, added...), old...) {
		if r := []rune(l); len(r) > conflictLineRunes {
			l = string(r[:conflictLineRunes])
		}
		texts = append(texts, l)
	}
	vecs, err := e.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	if len(vecs) != len(added)+len(old) {
		return nil, nil
	}
	for i := range vecs {
		vecs[i] = search.Normalize(vecs[i])
	}
	var out [][2]string
	for i, a := range added {
		for j, o := range old {
			if search.Dot(vecs[i], vecs[len(added)+j]) >= ConflictSimilarity && DiffersInFact(o, a) {
				out = append(out, [2]string{o, a})
			}
		}
	}
	return out, nil
}

// Pending are the queued pairs, oldest day first, at most max. The pairs whose lines are no longer on their page (the
// writer fixed it, or an earlier upkeep moved it) are dropped from the queue.
func (s *Store) Pending(ctx context.Context, max int) ([]Conflict, error) {
	rows, err := s.db.QueryContext(ctx, "select path, old, new, day from fact_conflicts order by day, rowid")
	if err != nil {
		return nil, err
	}
	var all []Conflict
	for rows.Next() {
		var c Conflict
		if err := rows.Scan(&c.Path, &c.Old, &c.New, &c.Day); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Conflict
	for _, c := range all {
		var body string
		err := s.db.QueryRowContext(ctx, "select body from docs where path = ? and deleted = 0", c.Path).Scan(&body)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		on := map[string]bool{}
		for _, l := range FactLines(body) {
			on[l] = true
		}
		if on[c.Old] && on[c.New] {
			if len(out) < max {
				out = append(out, c)
			}
			continue
		}
		if _, err := s.db.ExecContext(ctx, "delete from fact_conflicts where path = ? and old = ? and new = ?",
			c.Path, c.Old, c.New); err != nil {
			return nil, err
		}
	}
	return out, nil
}
