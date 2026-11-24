package search

import "context"

// Optimize merges the full-text index into one segment. FTS5 drops a removed chunk's words only when its segments are
// merged, so until then a text taken out of a page (a secret redacted in place) still sits in the index's files.
func (ix *Index) Optimize(ctx context.Context) error {
	_, err := ix.db.ExecContext(ctx, "insert into search_chunks_fts (search_chunks_fts) values ('optimize')")
	return err
}
