package memory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"kedalio/core/memory/docs"
	"kedalio/core/memory/search"
)

// A collection as a sub-graph: it has a hub page of kind `source` that the core writes when the collection is made and keeps when it
// is renamed, versioned or given documents, so it is linked into the graph like any page. The documents are in the
// search index, not pages: they are found by recall and search, which cite them.

// Collections is the memory's collections: the pages of the store and the documents of the index.
type Collections struct {
	Store *Store
	Index *search.Index
	// Fetch gets the URLs documents are added from; nil is the guarded fetcher.
	Fetch *docs.Fetcher
	// Slug gives a name the form the memory keeps in a path (lower case, no accents, dashes); required.
	Slug func(path string) string
}

// RefusedError is a collection call the caller has to change (a missing name, a taken path, an unknown id, a document
// that cannot be read), as against a failure of the store, which is not the caller's to fix.
type RefusedError struct{ Msg string }

func (e *RefusedError) Error() string { return e.Msg }

func refusedf(format string, args ...any) error { return &RefusedError{fmt.Sprintf(format, args...)} }

// IsRefusal reports whether err is one of the caller's to fix.
func IsRefusal(err error) bool {
	var r *RefusedError
	return errors.As(err, &r) || errors.Is(err, search.ErrExists) || errors.Is(err, search.ErrNoText) ||
		errors.Is(err, search.ErrNoCollection)
}

// CollectionInput is what Save takes; an empty field leaves the collection's as it is.
type CollectionInput struct {
	Name string
	// Description is a line or two on what the collection is, written into the hub when the collection is made.
	Description string
	Version     *string
	Important   *bool
}

var (
	managed  = regexp.MustCompile(`(?m)^Collection of documents.*$`)
	hubTitle = regexp.MustCompile(`(?m)^# .*$`)
)

// HubLine is the line of the hub that says what the collection is, the part the core keeps up to date.
func HubLine(c search.Collection) string {
	line := "Collection of documents"
	if c.Version != "" {
		line += ", version " + c.Version
	}
	if c.Important {
		line += ", marked important"
	}
	return line + ". Search it with memory_recall."
}

func hubBody(c search.Collection, description string) string {
	body := "---\nkind: source\n---\n# " + c.Name + "\n\n" + HubLine(c) + "\n"
	if description != "" {
		body += "\n" + description + "\n"
	}
	return body
}

// HubListed is how many documents the hub names; past it, the count of the rest. A big collection is searched, not listed.
const HubListed = 30

// DocumentsSection is the hub's documents section for these documents, or "" when there are none.
func DocumentsSection(ds []search.Document) string {
	if len(ds) == 0 {
		return ""
	}
	var lines []string
	for _, d := range ds[:min(len(ds), HubListed)] {
		lines = append(lines, fmt.Sprintf("- %s (%s)", d.Title, d.Kind))
	}
	if len(ds) > HubListed {
		lines = append(lines, fmt.Sprintf("- and %d more: memory_list lists them all", len(ds)-HubListed))
	}
	return "## Documents\n\n" + strings.Join(lines, "\n") + "\n"
}

// withDocuments is the hub body with its documents section replaced by section: the section runs to the next «## »
// heading or the end, and the new one goes last.
func withDocuments(body, section string) string {
	const head = "## Documents\n"
	start := -1
	if strings.HasPrefix(body, head) {
		start = 0
	} else if i := strings.Index(body, "\n"+head); i >= 0 {
		start = i
	}
	base := body
	if start >= 0 {
		end := len(body)
		skip := start + len(head)
		if start > 0 {
			skip++ // the line break that opens the section
		}
		if j := strings.Index(body[skip:], "\n## "); j >= 0 {
			end = skip + j
		}
		base = body[:start] + body[end:]
	}
	base = strings.TrimRight(base, " \t\r\n")
	if section == "" {
		return base + "\n"
	}
	return base + "\n\n" + section
}

// SyncHub lists the collection's live documents on its hub, so that recall, search and the graph see them on the page
// that stands for the collection; a section the core owns, rewritten only when the list changed.
func (c *Collections) SyncHub(ctx context.Context, collection, by string) error {
	// the hub may change between the read and the write: read it again once before giving up
	err := c.syncHub(ctx, collection, by)
	if errors.Is(err, ErrChanged) {
		err = c.syncHub(ctx, collection, by)
	}
	return err
}

func (c *Collections) syncHub(ctx context.Context, collection, by string) error {
	col, err := c.Index.Collection(ctx, collection)
	if err != nil || col == nil || col.Hub == "" {
		return err
	}
	cur, err := c.Store.Get(ctx, col.Hub)
	if err != nil || cur == nil {
		return err
	}
	live, err := c.Index.Documents(ctx, collection, true)
	if err != nil {
		return err
	}
	body := withDocuments(cur.Body, DocumentsSection(live))
	if body == cur.Body {
		return nil
	}
	_, err = c.Store.WriteAt(ctx, col.Hub, body, by, cur.Rev)
	return err
}

// Save makes a collection, or changes the one with id; its hub page is kept to match. It returns the collection as it is now.
func (c *Collections) Save(ctx context.Context, by string, in CollectionInput, id string) (*search.Collection, error) {
	name := strings.TrimSpace(in.Name)
	f := search.CollectionFields{Version: in.Version, Important: in.Important}
	if f.Version != nil {
		v := strings.TrimSpace(*f.Version)
		f.Version = &v
	}
	// not one transaction: the page write has its own. A save that stops half way is finished by the next one (a
	// collection with no hub gets it)
	created := id == ""
	if created {
		if name == "" {
			return nil, refusedf("a collection has a name")
		}
		var err error
		if id, err = c.Index.CreateCollection(ctx, name, f); err != nil {
			return nil, err
		}
	} else {
		if cur, err := c.Index.Collection(ctx, id); err != nil {
			return nil, err
		} else if cur == nil {
			return nil, refusedf("no collection %s", id)
		}
		if name != "" {
			f.Name = &name
		}
		if err := c.Index.UpdateCollection(ctx, id, f); err != nil {
			return nil, err
		}
	}
	col, err := c.Index.Collection(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := c.keepHub(ctx, by, col, created, strings.TrimSpace(in.Description)); err != nil {
		// a collection made just now with no hub would be left behind, and its name taken, by a refused save
		if created {
			if rerr := c.Index.RemoveCollection(ctx, id); rerr != nil {
				err = errors.Join(err, rerr)
			}
		}
		return nil, err
	}
	return c.Index.Collection(ctx, id)
}

// keepHub writes the hub page (when the collection is new) or brings its title and managed line up to date.
func (c *Collections) keepHub(ctx context.Context, by string, col *search.Collection, created bool, description string) error {
	if col.Hub == "" || created {
		path := c.Slug("sources/" + col.Name)
		if cur, err := c.Store.Get(ctx, path); err != nil {
			return err
		} else if cur != nil {
			path = c.Slug("sources/" + col.Name + "-" + col.ID[:4])
			if cur, err := c.Store.Get(ctx, path); err != nil {
				return err
			} else if cur != nil {
				return refusedf("a page %s exists already: pick another name", path)
			}
		}
		if _, err := c.Store.Write(ctx, path, hubBody(*col, description), by); err != nil {
			return err
		}
		return c.Index.UpdateCollection(ctx, col.ID, search.CollectionFields{Hub: &path})
	}
	cur, err := c.Store.Get(ctx, col.Hub)
	if err != nil {
		return err
	}
	if cur == nil {
		_, err = c.Store.Write(ctx, col.Hub, hubBody(*col, ""), by)
		return err
	}
	// the first of each only: a second H1 or a second line that starts the same way is the owner's text
	body := replaceFirst(hubTitle, cur.Body, "# "+col.Name)
	body = replaceFirst(managed, body, HubLine(*col))
	if body != cur.Body {
		_, err = c.Store.WriteAt(ctx, col.Hub, body, by, cur.Rev)
	}
	return err
}

func replaceFirst(re *regexp.Regexp, s, with string) string {
	loc := re.FindStringIndex(s)
	if loc == nil {
		return s
	}
	return s[:loc[0]] + with + s[loc[1]:]
}

// Delete removes a collection with everything in it: its documents, their chunks and original files, and its hub page
// (a revision, as any page removal). It returns the name and how many documents went, the superseded ones too.
func (c *Collections) Delete(ctx context.Context, id, by string) (name string, documents int, err error) {
	col, err := c.Index.Collection(ctx, id)
	if err != nil {
		return "", 0, err
	}
	if col == nil {
		return "", 0, refusedf("no collection %s", id)
	}
	all, err := c.Index.Documents(ctx, id, false)
	if err != nil {
		return "", 0, err
	}
	if err := c.Index.RemoveCollection(ctx, id); err != nil {
		return "", 0, err
	}
	// the hub goes after the rows: a stop in between leaves a hub with no collection, which the owner can delete as a page
	if col.Hub != "" {
		if cur, err := c.Store.Get(ctx, col.Hub); err != nil {
			return "", 0, err
		} else if cur != nil {
			if err := c.Store.Remove(ctx, col.Hub, by); err != nil {
				return "", 0, err
			}
		}
	}
	return col.Name, len(all), nil
}

// Added is what AddDocument did.
type Added struct {
	ID     string
	Title  string
	Origin string
	Kind   search.Kind
	Chunks int
	// Original is the file kept beside the text; nil for pasted text.
	Original *search.OriginalInfo
	Change   search.Change
}

// AddDocument adds a document to a collection, or replaces it when the same file name or URL is already there. The
// error says why it was refused, in words for the caller.
func (c *Collections) AddDocument(ctx context.Context, by, collection string, in docs.Source) (Added, error) {
	col, err := c.Index.Collection(ctx, collection)
	if err != nil {
		return Added{}, err
	}
	if col == nil {
		return Added{}, refusedf("no collection %s", collection)
	}
	p, err := c.Fetch.Prepare(ctx, in)
	if err != nil {
		// its errors say, in words for the caller, why the source was refused; a stopped call is not one
		if ctx.Err() != nil {
			return Added{}, err
		}
		return Added{}, &RefusedError{err.Error()}
	}
	r, err := c.Index.AddDocument(ctx, search.NewDocument{
		Collection: collection, Kind: p.Kind, Title: p.Title, Origin: p.Origin, Sections: p.Sections, Original: p.Original,
	})
	if err != nil {
		return Added{}, err
	}
	// the document is in the index by now: a hub that was edited twice meanwhile is listed again by the next change
	if err := c.SyncHub(ctx, collection, by); err != nil && !errors.Is(err, ErrChanged) {
		return Added{}, err
	}
	a := Added{ID: r.ID, Title: p.Title, Origin: p.Origin, Kind: p.Kind, Chunks: r.Chunks, Change: r.Change}
	if p.Original != nil {
		a.Original = &p.Original.OriginalInfo
	}
	return a, nil
}

// RemoveDocument takes a document, with its chunks, out of its collection; its title.
func (c *Collections) RemoveDocument(ctx context.Context, by, collection, id string) (string, error) {
	all, err := c.Index.Documents(ctx, collection, false)
	if err != nil {
		return "", err
	}
	for _, d := range all {
		if d.ID == id {
			if err := c.Index.RemoveDocument(ctx, id); err != nil {
				return "", err
			}
			// as in AddDocument: the document is gone, a hub that kept changing is not a failure of the removal
			if err := c.SyncHub(ctx, collection, by); err != nil && !errors.Is(err, ErrChanged) {
				return d.Title, err
			}
			return d.Title, nil
		}
	}
	return "", refusedf("no document %s in collection %s", id, collection)
}

// Documents lists a collection's documents, the superseded ones too.
func (c *Collections) Documents(ctx context.Context, collection string) ([]search.Document, error) {
	col, err := c.Index.Collection(ctx, collection)
	if err != nil {
		return nil, err
	}
	if col == nil {
		return nil, refusedf("no collection %s", collection)
	}
	return c.Index.Documents(ctx, collection, false)
}
