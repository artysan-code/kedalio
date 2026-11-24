package mcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"kedalio/core/memory"
	"kedalio/core/memory/docs"
	"kedalio/core/memory/search"
	"kedalio/protocol"
)

// Collections of documents (memory_sources): a stack's docs, a contract. The tool
// makes them and adds their documents; memory_list lists them, memory_delete removes them, memory_recall cites them.

func (s *Server) sourceTools() []*tool {
	return []*tool{{
		name: protocol.ToolMemorySources, kind: write,
		description: "Document collections (a stack's docs). save: make one, or change it with id. add_document: a url, or file_name + text or base64; same url or name replaces.",
		schema: object([]string{"op"},
			choice("op", []string{"save", "add_document"}),
			str("id", "save: to change"),
			str("name", "save"),
			str("description", "save"),
			str("version", "save: older ones are kept"),
			boolean("important", "save"),
			str("collection", "add_document: collection id"),
			str("url"),
			str("file_name", "with ending: guide.md"),
			str("text"), str("base64"), str("title"),
			str("image_description", "what an image shows, to find it")),
		run: s.sources,
	}}
}

// collectionOut is a collection as the tools answer it.
type collectionOut struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Version   string  `json:"version"`
	Hub       *string `json:"hub,omitempty"`
	Important bool    `json:"important"`
	Documents int     `json:"documents"`
	Created   string  `json:"created"`
	Updated   string  `json:"updated"`
}

func orNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func collectionJSON(c search.Collection) collectionOut {
	return collectionOut{c.ID, c.Name, c.Version, orNil(c.Hub), c.Important, c.Documents,
		c.Created, c.Updated}
}

type originalOut struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int    `json:"size"`
}

func originalJSON(o *search.OriginalInfo) *originalOut {
	if o == nil {
		return nil
	}
	return &originalOut{o.Name, o.Type, o.Size}
}

// documentOut is a document as memory_list answers it: what picks one (the collection is the one asked for; the hash,
// the version and the date it was added are in the hub and on the document itself).
type documentOut struct {
	ID         string       `json:"id"`
	Kind       string       `json:"kind"`
	Title      string       `json:"title"`
	Origin     string       `json:"origin"`
	Superseded *string      `json:"superseded,omitempty"`
	Chunks     int          `json:"chunks"`
	Updated    string       `json:"updated"`
	Original   *originalOut `json:"original,omitempty"`
}

func (s *Server) listCollections(ctx context.Context) (any, error) {
	cols, err := s.cfg.Search.Collections(ctx)
	if err != nil {
		return nil, err
	}
	out := []collectionOut{}
	for _, c := range cols {
		out = append(out, collectionJSON(c))
	}
	return map[string]any{"collections": out}, nil
}

func (s *Server) listDocuments(ctx context.Context, collection string, limit int) (any, error) {
	if err := needs("what documents", f("collection", collection)); err != nil {
		return nil, err
	}
	ds, err := s.collections.Documents(ctx, collection)
	if err != nil {
		return refusedOrFailed(err)
	}
	if limit == 0 {
		limit = listLimit
	}
	out := []documentOut{}
	for _, d := range ds {
		if len(out) == limit {
			break
		}
		out = append(out, documentOut{d.ID, string(d.Kind), d.Title, d.Origin, orNil(d.Superseded), d.Chunks, d.Updated,
			originalJSON(d.Original)})
	}
	ans := map[string]any{"documents": out}
	if len(ds) > len(out) {
		ans["more"] = fmt.Sprintf("%d of %d shown: pass a limit up to 1000", len(out), len(ds))
	}
	return ans, nil
}

func (s *Server) sources(ctx context.Context, raw []byte) (any, error) {
	a, err := decode[protocol.MemorySources](raw)
	if err != nil {
		return nil, err
	}
	if a.Op == "save" {
		if a.ID == "" && a.Name == "" {
			return nil, needs("op save", field{"id or name", true})
		}
		col, err := s.collections.Save(ctx, s.by(ctx), memory.CollectionInput{
			Name: a.Name, Description: a.Description, Version: a.Version, Important: a.Important}, a.ID)
		if err != nil {
			return refusedOrFailed(err)
		}
		return collectionJSON(*col), nil
	}
	if err := needs("op add_document", f("collection", a.Collection)); err != nil {
		return nil, err
	}
	in := docs.Source{URL: a.URL, Title: a.Title, Description: a.ImageDescription}
	if a.Text != nil || a.Base64 != nil {
		file, err := fileOf(a)
		if err != nil {
			return refuse([]string{err.Error()}), nil
		}
		in.File = file
	}
	added, err := s.collections.AddDocument(ctx, s.by(ctx), a.Collection, in)
	if err != nil {
		return refusedOrFailed(err)
	}
	return struct {
		ID       string       `json:"id"`
		Title    string       `json:"title"`
		Origin   string       `json:"origin"`
		Kind     string       `json:"kind"`
		Chunks   int          `json:"chunks"`
		Original *originalOut `json:"original"`
		Change   string       `json:"change"`
	}{added.ID, added.Title, added.Origin, string(added.Kind), added.Chunks, originalJSON(added.Original), string(added.Change)}, nil
}

// refusedOrFailed answers a refusal the caller can act on as one, and any other error (a database that failed) as an
// error: a refusal that hides a broken store reads as "nothing to do".
func refusedOrFailed(err error) (any, error) {
	if memory.IsRefusal(err) {
		return refuse([]string{err.Error()}), nil
	}
	return nil, err
}

// fileOf is the file a call gives as text or as base64.
func fileOf(a protocol.MemorySources) (*docs.File, error) {
	if a.FileName == "" {
		return nil, clientErrorf("a file given as text or base64 has a name")
	}
	if (a.Text != nil) == (a.Base64 != nil) {
		return nil, clientErrorf("give text or base64, not both")
	}
	if a.Text != nil {
		return &docs.File{Name: a.FileName, Bytes: []byte(*a.Text), Pasted: true}, nil
	}
	enc := strings.TrimRight(strings.Join(strings.Fields(*a.Base64), ""), "=")
	b, err := base64.RawStdEncoding.DecodeString(enc)
	if err != nil {
		return nil, clientErrorf("base64 is not valid base64")
	}
	return &docs.File{Name: a.FileName, Bytes: b}, nil
}

// deleteSource removes a document or a whole collection (memory_delete).
func (s *Server) deleteSource(ctx context.Context, a protocol.MemoryDelete) (any, error) {
	if a.What == "document" {
		if err := needs("what document", f("collection", a.Collection), f("id", a.ID)); err != nil {
			return nil, err
		}
		title, err := s.collections.RemoveDocument(ctx, s.by(ctx), a.Collection, a.ID)
		if err != nil {
			return refusedOrFailed(err)
		}
		return map[string]any{"removed": title}, nil
	}
	if err := needs("what collection", f("id", a.ID)); err != nil {
		return nil, err
	}
	name, n, err := s.collections.Delete(ctx, a.ID, s.by(ctx))
	if err != nil {
		return refusedOrFailed(err)
	}
	return map[string]any{"deleted": map[string]any{"name": name, "documents": n}}, nil
}

// locatorOut is where a passage sits in its source, as a citation tells it.
func locatorOut(l search.Locator) map[string]any {
	at := map[string]any{}
	if l.Page != 0 {
		at["page"] = l.Page
	}
	if l.Heading != "" {
		at["heading"] = l.Heading
	}
	if l.URL != "" {
		at["url"] = l.URL
	}
	return at
}
