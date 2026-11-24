package search

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Collections of documents: a stack's documentation, a contract. A collection has a
// version, and its documents are cut into chunks that are searched beside the wiki's, in both lanes. A document
// belongs to the version the collection had when it was written; one replaced by a newer text under a newer version
// is kept, `superseded`, and found only by asking for its version.

// Kind of document, by what it was made from.
type Kind string

const (
	KindPDF      Kind = "pdf"
	KindMarkdown Kind = "markdown"
	KindHTML     Kind = "html"
	KindText     Kind = "text"
	KindSheet    Kind = "sheet"
	KindImage    Kind = "image"
)

// Collection is a set of documents.
type Collection struct {
	ID   string
	Name string
	// Version is a free string («5.4», «2026-10»): documents written under another one are kept, superseded.
	Version string
	// Hub is the path of the page (kind source) that stands for the collection in the graph; "" while it has none.
	Hub string
	// Important marks the collection as one that deserves more care (a context sentence per chunk, once the core writes them).
	Important bool
	// Documents counts the live documents.
	Documents int
	Created   string
	Updated   string
}

// OriginalInfo is what is kept of a document's file, without the bytes.
type OriginalInfo struct {
	Name string
	Type string
	Size int
}

// Original is a document's file as it was added.
type Original struct {
	OriginalInfo
	Bytes []byte
}

// Document is a document of a collection.
type Document struct {
	ID         string
	Collection string
	Kind       Kind
	Title      string
	// Origin is the file name or URL it came from: one live document per origin in a collection.
	Origin string
	Hash   string
	// Version is the collection's version when it was written.
	Version string
	// Superseded is when a newer document replaced it; "" while it is current.
	Superseded string
	Chunks     int
	Added      string
	Updated    string
	// Original is the file it was made from; nil for pasted text.
	Original *OriginalInfo
}

// NewDocument is a document to add: its text as sections, and the file it came from.
type NewDocument struct {
	Collection string
	Kind       Kind
	Title      string
	Origin     string
	Sections   []Section
	Original   *Original
}

// Change says what AddDocument did.
type Change string

const (
	Added     Change = "added"
	Updated   Change = "updated"
	Unchanged Change = "unchanged"
)

// AddedDocument is the result of AddDocument.
type AddedDocument struct {
	ID     string
	Chunks int
	Change Change
}

// ErrNoCollection is returned for a collection that is not there.
var ErrNoCollection = errors.New("no such collection")

// ErrExists is a collection name already taken; ErrNoText a document with nothing to index. Both are the caller's to
// fix, unlike a database error.
var (
	ErrExists = errors.New("exists already")
	ErrNoText = errors.New("no text found")
)

const stamp = "2006-01-02T15:04:05.000Z"

func now() string { return time.Now().UTC().Format(stamp) }

// newID is a random id in the form of a UUID, as today's server makes them.
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// CollectionFields are the fields a collection changes; nil leaves the field as it is.
type CollectionFields struct {
	Name      *string
	Version   *string
	Hub       *string
	Important *bool
}

// CreateCollection makes a collection and returns its id. The name is unique.
func (ix *Index) CreateCollection(ctx context.Context, name string, f CollectionFields) (string, error) {
	id, at := newID(), now()
	version, hub, important := "", sql.NullString{}, 0
	if f.Version != nil {
		version = *f.Version
	}
	if f.Hub != nil {
		hub = sql.NullString{String: *f.Hub, Valid: true}
	}
	if f.Important != nil && *f.Important {
		important = 1
	}
	_, err := ix.db.ExecContext(ctx,
		"insert into search_collections (id, name, version, hub, important, created, updated) values (?, ?, ?, ?, ?, ?, ?)",
		id, name, version, hub, important, at, at)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", fmt.Errorf("a collection named %q %w", name, ErrExists)
		}
		return "", err
	}
	return id, nil
}

// UpdateCollection changes the fields given. A new version leaves the documents as they are: the next time one is
// added with another text, the old one is superseded.
func (ix *Index) UpdateCollection(ctx context.Context, id string, f CollectionFields) error {
	var sets []string
	var args []any
	put := func(col string, v any) { sets, args = append(sets, col+" = ?"), append(args, v) }
	if f.Name != nil {
		put("name", *f.Name)
	}
	if f.Version != nil {
		put("version", *f.Version)
	}
	if f.Hub != nil {
		put("hub", *f.Hub)
	}
	if f.Important != nil {
		put("important", boolInt(*f.Important))
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, now(), id)
	r, err := ix.db.ExecContext(ctx, "update search_collections set "+strings.Join(sets, ", ")+", updated = ? where id = ?", args...)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return fmt.Errorf("a collection named %q %w", *f.Name, ErrExists)
		}
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNoCollection
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

const collectionSelect = `select c.id, c.name, c.version, coalesce(c.hub, ''), c.important,
  c.created, c.updated,
  (select count(*) from search_documents d where d.collection = c.id and d.superseded is null)
  from search_collections c`

func scanCollection(r interface{ Scan(...any) error }) (Collection, error) {
	var c Collection
	var important int
	err := r.Scan(&c.ID, &c.Name, &c.Version, &c.Hub, &important, &c.Created, &c.Updated, &c.Documents)
	c.Important = important != 0
	return c, err
}

// Collections lists the collections by name.
func (ix *Index) Collections(ctx context.Context) ([]Collection, error) {
	rows, err := ix.db.QueryContext(ctx, collectionSelect+" order by c.name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Collection is the collection with this id, nil when there is none.
func (ix *Index) Collection(ctx context.Context, id string) (*Collection, error) {
	c, err := scanCollection(ix.db.QueryRowContext(ctx, collectionSelect+" where c.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// RemoveCollection deletes a collection with its documents, their chunks and original files.
func (ix *Index) RemoveCollection(ctx context.Context, id string) error {
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		"delete from search_chunks where path in (select 'doc:' || id from search_documents where collection = ?)",
		"delete from search_document_originals where document in (select id from search_documents where collection = ?)",
		"delete from search_documents where collection = ?",
		"delete from search_collections where id = ?",
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DocSource is the path under which a document's chunks are indexed.
func DocSource(id string) string { return "doc:" + id }

func contentHash(title string, sections []Section) string {
	b, _ := json.Marshal([]any{title, sections})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// AddDocument adds a document to a collection, or replaces the one with the same origin; it is left as it is when its
// text has not changed. A changed one under the same version of the collection is rewritten in place; under a newer
// version the old one is kept, superseded, and the new one is added next to it.
func (ix *Index) AddDocument(ctx context.Context, d NewDocument) (AddedDocument, error) {
	hash := contentHash(d.Title, d.Sections)
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return AddedDocument{}, err
	}
	defer tx.Rollback()
	var version string
	if err := tx.QueryRowContext(ctx, "select version from search_collections where id = ?", d.Collection).Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AddedDocument{}, ErrNoCollection
		}
		return AddedDocument{}, err
	}
	at := now()
	var curID, curHash, curVersion string
	err = tx.QueryRowContext(ctx,
		"select id, hash, version from search_documents where collection = ? and origin = ? and superseded is null",
		d.Collection, d.Origin).Scan(&curID, &curHash, &curVersion)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AddedDocument{}, err
	}
	exists := err == nil
	if exists && curHash == hash {
		var n int
		if err := tx.QueryRowContext(ctx, "select count(*) from search_chunks where path = ?", DocSource(curID)).Scan(&n); err != nil {
			return AddedDocument{}, err
		}
		if _, err := tx.ExecContext(ctx, "update search_documents set seen_version = ? where id = ?", version, curID); err != nil {
			return AddedDocument{}, err
		}
		// the same text from a file that was not kept (pasted text first): the file is kept now
		if d.Original != nil {
			var have int
			if err := tx.QueryRowContext(ctx, "select count(*) from search_document_originals where document = ?", curID).Scan(&have); err != nil {
				return AddedDocument{}, err
			}
			if have == 0 {
				if err := keepOriginal(ctx, tx, curID, d.Original); err != nil {
					return AddedDocument{}, err
				}
			}
		}
		return AddedDocument{ID: curID, Chunks: n, Change: Unchanged}, tx.Commit()
	}
	inPlace := exists && curVersion == version
	id := curID
	if exists && !inPlace {
		if _, err := tx.ExecContext(ctx, "update search_documents set superseded = ? where id = ?", at, curID); err != nil {
			return AddedDocument{}, err
		}
	}
	if inPlace {
		if _, err := tx.ExecContext(ctx,
			"update search_documents set kind = ?, title = ?, hash = ?, updated = ?, seen_version = ? where id = ?",
			string(d.Kind), d.Title, hash, at, version, id); err != nil {
			return AddedDocument{}, err
		}
		if err := keepOriginal(ctx, tx, id, d.Original); err != nil {
			return AddedDocument{}, err
		}
	} else {
		id = newID()
		if _, err := tx.ExecContext(ctx,
			`insert into search_documents (id, collection, kind, title, origin, hash, added, updated, version, seen_version)
			 values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, d.Collection, string(d.Kind), d.Title, d.Origin, hash, at, at, version, version); err != nil {
			return AddedDocument{}, err
		}
		if err := keepOriginal(ctx, tx, id, d.Original); err != nil {
			return AddedDocument{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, "update search_collections set updated = ? where id = ?", at, d.Collection); err != nil {
		return AddedDocument{}, err
	}
	pieces := chunkSections(d.Sections, defaultSize, d.Title)
	if len(pieces) == 0 {
		return AddedDocument{}, fmt.Errorf("%w in %s", ErrNoText, d.Origin)
	}
	if err := replaceChunks(ctx, tx, DocSource(id), d.Title, pieces); err != nil {
		return AddedDocument{}, err
	}
	change := Added
	if exists {
		change = Updated
	}
	return AddedDocument{ID: id, Chunks: len(pieces), Change: change}, tx.Commit()
}

func keepOriginal(ctx context.Context, tx *sql.Tx, id string, o *Original) error {
	if o == nil {
		_, err := tx.ExecContext(ctx, "delete from search_document_originals where document = ?", id)
		return err
	}
	sum := sha256.Sum256(o.Bytes)
	_, err := tx.ExecContext(ctx,
		"insert or replace into search_document_originals (document, name, type, size, sha256, bytes) values (?, ?, ?, ?, ?, ?)",
		id, o.Name, o.Type, len(o.Bytes), hex.EncodeToString(sum[:]), o.Bytes)
	return err
}

const documentSelect = `select d.id, d.collection, d.kind, d.title, d.origin, d.hash, d.version, coalesce(d.superseded, ''),
  d.added, d.updated, (select count(*) from search_chunks c where c.path = 'doc:' || d.id),
  o.name, o.type, o.size
  from search_documents d left join search_document_originals o on o.document = d.id`

func scanDocument(r interface{ Scan(...any) error }) (Document, error) {
	var d Document
	var kind string
	var name, typ sql.NullString
	var size sql.NullInt64
	err := r.Scan(&d.ID, &d.Collection, &kind, &d.Title, &d.Origin, &d.Hash, &d.Version, &d.Superseded, &d.Added, &d.Updated,
		&d.Chunks, &name, &typ, &size)
	d.Kind = Kind(kind)
	if name.Valid {
		d.Original = &OriginalInfo{Name: name.String, Type: typ.String, Size: int(size.Int64)}
	}
	return d, err
}

// Documents lists a collection's documents by title, the superseded ones too unless current is asked for.
func (ix *Index) Documents(ctx context.Context, collection string, current bool) ([]Document, error) {
	q := documentSelect + " where d.collection = ?"
	if current {
		q += " and d.superseded is null"
	}
	rows, err := ix.db.QueryContext(ctx, q+" order by d.title, d.added", collection)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RemoveDocument deletes a document with its chunks and original file.
func (ix *Index) RemoveDocument(ctx context.Context, id string) error {
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		"delete from search_chunks where path = 'doc:' || ?",
		"delete from search_document_originals where document = ?",
		"delete from search_documents where id = ?",
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReadOriginal is the file a document was made from; nil when none is kept.
func (ix *Index) ReadOriginal(ctx context.Context, document string) (*Original, error) {
	var o Original
	err := ix.db.QueryRowContext(ctx,
		"select name, type, size, bytes from search_document_originals where document = ?", document).
		Scan(&o.Name, &o.Type, &o.Size, &o.Bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}
