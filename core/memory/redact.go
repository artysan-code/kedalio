package memory

import (
	"context"
	"database/sql"
	"fmt"
)

// Redacted is what Redact did to one revision: how many values it replaced in the body, whether that revision is the
// live page (so the page, its links and its index were rewritten too), how many it replaced in the text a removed page
// keeps and how many rows of fact_conflicts it rewrote.
type Redacted struct {
	Path      string
	Rev       int
	Replaced  int
	Live      bool
	Removed   int
	Conflicts int
}

// Redact rewrites one stored revision of a page in place, its body passed through redact (which returns the new body
// and how many values it replaced), and never adds a revision: it is for taking a secret out of the history without
// losing the history. rev 0 is the live page. When the revision is the live one the page itself, its links and its
// derived indexes are rewritten too, in the same transaction; the facts compared from the page (fact_conflicts) are
// passed through redact as well, since they hold lines of its old versions. With dry the counts come back and nothing
// is written. A revision that is not there, or a deleted page asked for as rev 0, is an error.
func (s *Store) Redact(ctx context.Context, path string, rev int, redact func(string) (string, int), dry bool) (Redacted, error) {
	p, err := CleanPath(path)
	if err != nil {
		return Redacted{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Redacted{}, err
	}
	defer tx.Rollback()

	var liveRev, deleted int
	err = tx.QueryRowContext(ctx, "select rev, deleted from docs where path = ?", p).Scan(&liveRev, &deleted)
	if err == sql.ErrNoRows {
		liveRev = -1
	} else if err != nil {
		return Redacted{}, err
	}
	if rev == 0 {
		if liveRev < 0 || deleted != 0 {
			return Redacted{}, fmt.Errorf("%s: no live page", p)
		}
		rev = liveRev
	}
	out := Redacted{Path: p, Rev: rev, Live: rev == liveRev && deleted == 0}

	var body string
	err = tx.QueryRowContext(ctx, "select body from revisions where path = ? and rev = ?", p, rev).Scan(&body)
	if err == sql.ErrNoRows {
		return Redacted{}, fmt.Errorf("%s@%d: no such revision", p, rev)
	} else if err != nil {
		return Redacted{}, err
	}
	clean, n := redact(body)
	out.Replaced = n

	// a removed page keeps its last text in docs (Remove only marks it), which is not any revision's row: it is
	// redacted with whichever revision of the page is named
	var removedClean string
	if liveRev >= 0 && deleted != 0 {
		var kept string
		if err := tx.QueryRowContext(ctx, "select body from docs where path = ?", p).Scan(&kept); err != nil {
			return Redacted{}, err
		}
		removedClean, out.Removed = redact(kept)
	}

	type fact struct{ old, new, day string }
	var facts []fact
	rows, err := tx.QueryContext(ctx, "select old, new, day from fact_conflicts where path = ?", p)
	if err != nil {
		return Redacted{}, err
	}
	for rows.Next() {
		var f fact
		if err := rows.Scan(&f.old, &f.new, &f.day); err != nil {
			rows.Close()
			return Redacted{}, err
		}
		facts = append(facts, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Redacted{}, err
	}

	if dry {
		for _, f := range facts {
			_, a := redact(f.old)
			_, b := redact(f.new)
			if a+b > 0 {
				out.Conflicts++
			}
		}
		return out, nil
	}

	if n > 0 {
		if _, err := tx.ExecContext(ctx, "update revisions set body = ? where path = ? and rev = ?", clean, p, rev); err != nil {
			return Redacted{}, err
		}
		if out.Live {
			title := TitleOf(p, clean)
			if _, err := tx.ExecContext(ctx, "update docs set body = ?, title = ? where path = ?", clean, title, p); err != nil {
				return Redacted{}, err
			}
			if err := setLinks(ctx, tx, p, clean); err != nil {
				return Redacted{}, err
			}
			if taskIDRe.MatchString(p) && s.tasks != nil {
				if err := s.tasks.IndexTask(ctx, tx, p, clean); err != nil {
					return Redacted{}, err
				}
			}
			if !isTaskPage(p) && s.idx != nil {
				if err := s.idx.IndexPage(ctx, tx, p, title, clean); err != nil {
					return Redacted{}, err
				}
			}
		}
	}
	if out.Removed > 0 {
		if _, err := tx.ExecContext(ctx, "update docs set body = ? where path = ? and deleted != 0", removedClean, p); err != nil {
			return Redacted{}, err
		}
	}
	for _, f := range facts {
		o, a := redact(f.old)
		w, b := redact(f.new)
		if a+b == 0 {
			continue
		}
		// the key is (path, old, new): the row goes and its redacted twin comes in, once even when two collapse
		if _, err := tx.ExecContext(ctx, "delete from fact_conflicts where path = ? and old = ? and new = ?", p, f.old, f.new); err != nil {
			return Redacted{}, err
		}
		if _, err := tx.ExecContext(ctx, "insert or ignore into fact_conflicts (path, old, new, day) values (?, ?, ?, ?)", p, o, w, f.day); err != nil {
			return Redacted{}, err
		}
		out.Conflicts++
	}
	err = tx.Commit()
	s.graphChanged() // a page body is cleaned: its ended relations may read differently
	return out, err
}
