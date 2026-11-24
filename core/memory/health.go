package memory

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

// BrokenLink is a page that links to a page that does not exist.
type BrokenLink struct{ Page, Link string }

// LongPage is a page over the length its area allows.
type LongPage struct {
	Page  string
	Words int
	// Kind is the page's kind, "" when it has none.
	Kind Kind
}

// Health is the memory against its rules: what memory_list what=health answers.
type Health struct {
	Pages       int
	Orphans     []string
	BrokenLinks []BrokenLink
	TooLong     []LongPage
	// Secrets are the live pages, tasks included, whose text looks secret, and SecretRevisions the older versions that
	// do («path@rev»): paths only, never the text. SecretKinds says why, by path or path@rev, in the words of the
	// function Health was given (a jwt, name = value: DB_PASSWORD), never with the value. Nil when Health was not asked.
	Secrets, SecretRevisions []string
	SecretKinds              map[string][]string
}

var healthWord = regexp.MustCompile(`[\p{L}\p{N}]+`)

// Health reads every live page once: the pages nothing links to (the self page, the diary, the inbox and the views are
// entry points, not orphans), the links that lead nowhere (the diary is append-only: a link it made to a page since
// removed is history, not a fault), and the pages over maxWords(area). Tasks are not pages of the brain here.
// With secret set it also reads every page and every older version, tasks too, for what secret says is one: a secret
// written before a check refused it is still there, and only the owner decides what to do about it.
// secret answers why a text looks secret, nothing when it does not; it reads the stored text, not an answer's mask.
func (s *Store) Health(ctx context.Context, areas Areas, maxWords func(area string) int, secret func(string) []string) (Health, error) {
	var h Health
	if secret != nil {
		var err error
		if h.Secrets, h.SecretRevisions, h.SecretKinds, err = s.secrets(ctx, secret); err != nil {
			return h, err
		}
	}
	resolve, err := s.Resolver(ctx)
	if err != nil {
		return h, err
	}
	rows, err := s.db.QueryContext(ctx, "select path, body, coalesce(kind, '') from docs where deleted = 0 and path not like 'tasks/%' order by path")
	if err != nil {
		return h, err
	}
	type page struct {
		path, body string
		kind       Kind
	}
	var pages []page
	for rows.Next() {
		var p page
		if err := rows.Scan(&p.path, &p.body, &p.kind); err != nil {
			rows.Close()
			return h, err
		}
		pages = append(pages, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return h, err
	}
	out := map[string][]string{}
	links, err := s.db.QueryContext(ctx, "select distinct src, dst from links where src not like 'tasks/%' order by src, dst")
	if err != nil {
		return h, err
	}
	for links.Next() {
		var src, dst string
		if err := links.Scan(&src, &dst); err != nil {
			links.Close()
			return h, err
		}
		out[src] = append(out[src], dst)
	}
	links.Close()
	if err := links.Err(); err != nil {
		return h, err
	}
	linked := map[string]bool{}
	h.Pages = len(pages)
	h.Orphans, h.BrokenLinks, h.TooLong = []string{}, []BrokenLink{}, []LongPage{}
	for _, p := range pages {
		area := strings.SplitN(p.path, "/", 2)[0]
		for _, target := range out[p.path] {
			if to := resolve(target); to != "" {
				linked[to] = true
			} else if area != areas.Diary {
				h.BrokenLinks = append(h.BrokenLinks, BrokenLink{p.path, target})
			}
		}
		if n := len(healthWord.FindAllString(fmRe.ReplaceAllString(p.body, ""), -1)); n > maxWords(area) {
			h.TooLong = append(h.TooLong, LongPage{p.path, n, p.kind})
		}
	}
	for _, p := range pages {
		switch area := strings.SplitN(p.path, "/", 2)[0]; area {
		case "views", areas.Self, areas.Diary, areas.Inbox:
		default:
			if !linked[p.path] {
				h.Orphans = append(h.Orphans, p.path)
			}
		}
	}
	return h, nil
}

// secrets is the live pages and the older versions whose stored body secret flags, in path order, with its reasons.
func (s *Store) secrets(ctx context.Context, secret func(string) []string) (live, older []string, why map[string][]string, err error) {
	live, older, why = []string{}, []string{}, map[string][]string{}
	rows, err := s.db.QueryContext(ctx, "select path, body from docs where deleted = 0 order by path")
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var path, body string
		if err := rows.Scan(&path, &body); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		if k := secret(body); len(k) > 0 {
			live, why[path] = append(live, path), k
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	// the live version is in revisions too, as the newest: it is the page's, already counted above
	revs, err := s.db.QueryContext(ctx, `select r.path, r.rev, r.body from revisions r
		left join docs d on d.path = r.path and d.rev = r.rev and d.deleted = 0
		where d.path is null and r.body != '' order by r.path, r.rev`)
	if err != nil {
		return nil, nil, nil, err
	}
	defer revs.Close()
	for revs.Next() {
		var path, body string
		var rev int
		if err := revs.Scan(&path, &rev, &body); err != nil {
			return nil, nil, nil, err
		}
		if k := secret(body); len(k) > 0 {
			at := path + "@" + strconv.Itoa(rev)
			older, why[at] = append(older, at), k
		}
	}
	return live, older, why, revs.Err()
}
