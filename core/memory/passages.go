package memory

import (
	"bytes"
	"encoding/json"
	"strings"

	"kedalio/core/memory/search"
)

const (
	// docsMax is how many documents lead the pack with a passage (one each), and docPassages the most passages of any
	// one document in the whole pack, those a source page brings included: a long document must not crowd out the others.
	docsMax     = 3
	docPassages = 2
	// docPageShare: when a document passage scores above every page, a page needs this share of the passage's score (its
	// seed weight over the passage's lead) to come in full.
	docPageShare = 0.8
	// passagesMax is how many passages a source page brings for the query, and passageTokens how long each may be.
	passagesMax   = 2
	passageTokens = 120
)

// Passage is a passage of a document as the memory cites it: enough to say «from <document> § <section>, version <v>»
// and to find it again (the collection, the document's id and the chunk's position in it).
type Passage struct {
	Title  string `json:"title"`
	Origin string `json:"origin,omitempty"`
	Text   string `json:"text"`
	// Collection is the collection's name.
	Collection string `json:"collection,omitempty"`
	// Document is the document's id (memory_list lists a collection's).
	Document string `json:"document,omitempty"`
	Version  string `json:"version,omitempty"`
	// Superseded: a newer version or the end of its origin replaced the document; the passage is how it was.
	Superseded bool `json:"superseded,omitempty"`
	// Section is the heading path, «Title › Heading › Subheading», when a heading sits above the passage.
	Section string `json:"section,omitempty"`
	// At is where it sits in its source: a PDF page, a heading, a URL.
	At search.Locator `json:"at,omitzero"`
	// Chunk is the passage's position among the document's, from 0.
	Chunk int `json:"chunk"`
}

// cutPassage is a passage cut to max tokens at the end of a sentence, else at a word. A passage is often one long line
// under its heading, which cutTo (lines) would leave as the heading alone.
func cutPassage(text string, limit int) string {
	if Tokens(text) <= limit {
		return text
	}
	head := prefixUnits(text, limit*4)
	stop := max(strings.LastIndex(head, ". "), strings.LastIndex(head, ".\n"), strings.LastIndex(head, "\n"))
	if stop > len(head)/2 {
		return strings.TrimRight(head[:stop+1], " \t\r\n")
	}
	if word := strings.LastIndex(head, " "); word > 0 {
		head = head[:word]
	}
	return strings.TrimRight(head, " \t\r\n")
}

// PassageOf is a document result's passage as it is cited, its text cut to max tokens; nil for a page.
func PassageOf(r search.Result, c search.Citation, max int) *Passage {
	d := r.Document
	if d == nil {
		return nil
	}
	return &Passage{Title: r.Title, Origin: d.Origin, Text: cutPassage(c.Text, max), Collection: d.Collection,
		Document: d.ID, Version: d.Version, Superseded: d.Superseded, Section: c.Context, At: c.At, Chunk: c.Position}
}

// cost is what a passage costs the pack: all of it as it is sent, citation included.
func (p Passage) cost() int {
	n, _ := wireTokens(p) // a plain struct of strings and numbers always encodes
	return n
}

// wireTokens is the size, in tokens, of v as it is sent: JSON with no HTML escaping.
func wireTokens(v any) (int, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return 0, err
	}
	return Tokens(strings.TrimSuffix(buf.String(), "\n")), nil
}
