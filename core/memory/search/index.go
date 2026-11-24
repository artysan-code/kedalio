// Package search is the memory's hybrid search over wiki pages (ADR 0005): pages are cut into chunks that are indexed
// for words (FTS5, BM25) at once and embedded for meaning later, in the background, so a write never waits for the
// model. Search fuses the two rankings, groups them by page with their best chunks as citations. A model that is down or slow leaves words alone, and the answer says so.
package search

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"kedalio/core/db"
)

// Index is the search index of a database. The page store calls IndexPage and RemovePage inside its own write
// transaction; everything else runs on the database.
type Index struct {
	db *sql.DB

	mu sync.Mutex
	// queries holds the vectors of recent queries, by model and text (see queryVector).
	queries *queryCache
	// embedding holds the query vectors being computed, by the same key (see queryVector).
	embedding map[string]*pendingVector
}

// New migrates the search tables of d and returns the index over it.
func New(ctx context.Context, d *sql.DB) (*Index, error) {
	if err := db.Migrate(ctx, d, "search", migrations); err != nil {
		return nil, err
	}
	return &Index{db: d, queries: newQueryCache(queryCacheSize), embedding: map[string]*pendingVector{}}, nil
}

type oldChunk struct {
	model   sql.NullString
	vec, q8 []byte
}

type chunkKey struct{ title, context, text string }

// IndexPage replaces the chunks of a page with those of its new text, inside tx. A piece whose text was already there
// keeps its vector: editing one paragraph of a long page costs one embedding, not the whole page's.
func (ix *Index) IndexPage(ctx context.Context, tx *sql.Tx, path, title, body string) error {
	return replaceChunks(ctx, tx, path, title, chunk(body, defaultSize, title))
}

// replaceChunks makes pieces the chunks of source (a page's path, or «doc:<id>»), inside tx; a piece that was already
// there keeps its vector.
func replaceChunks(ctx context.Context, tx *sql.Tx, source, title string, pieces []piece) error {
	rows, err := tx.QueryContext(ctx,
		"select title, context, text, model, vec, q8 from search_chunks where path = ?", source)
	if err != nil {
		return err
	}
	old := map[chunkKey]oldChunk{}
	for rows.Next() {
		var k chunkKey
		var o oldChunk
		if err := rows.Scan(&k.title, &k.context, &k.text, &o.model, &o.vec, &o.q8); err != nil {
			rows.Close()
			return err
		}
		old[k] = o
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from search_chunks where path = ?", source); err != nil {
		return err
	}
	for i, p := range pieces {
		kept := old[chunkKey{title, p.context, p.text}]
		_, err := tx.ExecContext(ctx,
			"insert into search_chunks (path, ord, title, context, text, heading, locator, model, vec, q8) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			source, i, title, p.context, p.text, p.heading, p.locator(), nullable(kept.model), blob(kept.vec), blob(kept.q8))
		if err != nil {
			return err
		}
	}
	return nil
}

// RemovePage drops the chunks of a page, inside tx.
func (ix *Index) RemovePage(ctx context.Context, tx *sql.Tx, path string) error {
	_, err := tx.ExecContext(ctx, "delete from search_chunks where path = ?", path)
	return err
}

func nullable(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

// blob is b as a SQL value, null when there is none.
func blob(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// Pending is how many chunks still wait for model.
func (ix *Index) Pending(ctx context.Context, model string) (int, error) {
	var n int
	err := ix.db.QueryRowContext(ctx, "select count(*) from search_chunks where model is null or model <> ?", model).Scan(&n)
	return n, err
}

// EmbedPending embeds up to batch chunks that e's model has not seen and returns the number done, 0 when none is
// left. The core calls it in a loop in the background; a model that fails returns an error, and the chunks stay
// pending.
func (ix *Index) EmbedPending(ctx context.Context, e Embedder, batch int) (int, error) {
	rows, err := ix.db.QueryContext(ctx,
		"select id, title, context, text from search_chunks where model is null or model <> ? order by id limit ?",
		e.Model(), batch)
	if err != nil {
		return 0, err
	}
	type pending struct {
		id                   int64
		title, context, text string
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.title, &p.context, &p.text); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, p)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	if len(todo) == 0 {
		return 0, nil
	}
	texts := make([]string, len(todo))
	for i, p := range todo {
		// the breadcrumb starts with the title: it stands in for it
		lead := p.context
		if lead == "" {
			lead = p.title
		}
		texts[i] = lead + "\n" + p.text
	}
	vecs, err := e.Embed(ctx, texts)
	if err != nil {
		return 0, err
	}
	if len(vecs) != len(todo) {
		return 0, errBadAnswer
	}
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for i, p := range todo {
		unit := Normalize(vecs[i])
		// the chunk may have been rewritten while the model worked (and its id taken again): only the same text is updated
		if _, err := tx.ExecContext(ctx,
			"update search_chunks set model = ?, vec = ?, q8 = ? where id = ? and title = ? and context = ? and text = ?",
			e.Model(), Float32Bytes(unit), i8Bytes(quantize(unit)), p.id, p.title, p.context, p.text); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(todo), nil
}
