package docs

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"kedalio/core/memory/search"
)

// MaxUpload is what one added file may weigh. Its original is kept whole in the core's database.
const MaxUpload = 25_000_000

// Source is a document to add: one URL, or one file given to the core.
type Source struct {
	// URL is a page or a file to fetch, http(s).
	URL string
	// File is a file given as bytes: its name (the document's origin and, by its ending, its kind) and the bytes.
	File *File
	// Title is the document's title, when the file's own is not the one to show.
	Title string
	// Description says in words what an image shows: an image has no text of its own, so this and the title are what
	// search finds.
	Description string
}

// File is a file given to the core.
type File struct {
	Name  string
	Bytes []byte
	Type  string
	// Pasted is text given as text: there is no file to keep.
	Pasted bool
}

// Prepared is a document read and ready for search.AddDocument.
type Prepared struct {
	Origin   string
	Kind     search.Kind
	Title    string
	Sections []search.Section
	Original *search.Original
}

var (
	endings      = regexp.MustCompile(`(?i)\.(pdf|md|markdown|html?|txt|text|csv|json|xlsx|png|jpe?g|webp|gif)$`)
	contentTypes = regexp.MustCompile(`^(text/|application/(pdf|json|xhtml\+xml|vnd\.openxmlformats-officedocument\.spreadsheetml\.sheet)|image/(png|jpeg|webp|gif))`)
	imageName    = regexp.MustCompile(`(?i)\.(png|jpe?g|webp|gif)$`)
	imageType    = regexp.MustCompile(`^image/(png|jpeg|webp|gif)$`)
	pdfName      = regexp.MustCompile(`(?i)\.pdf$`)
	mdName       = regexp.MustCompile(`(?i)\.(md|markdown)$`)
	csvName      = regexp.MustCompile(`(?i)\.csv$`)
	xlsxName     = regexp.MustCompile(`(?i)\.xlsx$`)
	htmlStart    = regexp.MustCompile(`(?i)^\s*(<!doctype html|<html)`)
	extension    = regexp.MustCompile(`(?i)\.([a-z0-9]+)$`)
)

func hasPDFHeader(b []byte) bool { return len(b) >= 5 && string(b[:5]) == "%PDF-" }

func pathOf(s string) string { p, _, _ := strings.Cut(strings.SplitN(s, "#", 2)[0], "?"); return p }

// RefuseType gives the reason a file may not be added (an unsupported type, or the old .xls), or "" when it may.
func RefuseType(name, typ string, b []byte) string {
	n := pathOf(name)
	ext := ""
	if m := extension.FindStringSubmatch(n); m != nil {
		ext = strings.ToLower(m[1])
	}
	if ext == "xls" || strings.Contains(typ, "ms-excel") {
		return "save it as .xlsx or .csv"
	}
	if endings.MatchString(n) || contentTypes.MatchString(typ) || hasPDFHeader(b) {
		return ""
	}
	if ext == "" {
		ext = typ
	}
	if ext == "" {
		ext = "no extension"
	}
	return "type not supported: " + ext
}

var types = map[string]string{
	"pdf": "application/pdf", "md": "text/markdown", "markdown": "text/markdown", "html": "text/html", "htm": "text/html",
	"txt": "text/plain", "csv": "text/csv", "png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg",
	"webp": "image/webp", "gif": "image/gif",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
}

// GuessType is a content type from a file's ending, for a file that came with none.
func GuessType(name string) string {
	if m := extension.FindStringSubmatch(name); m != nil {
		if t, ok := types[strings.ToLower(m[1])]; ok {
			return t
		}
	}
	return "application/octet-stream"
}

func unescape(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

// lastSegment is the name a path or URL ends with.
func lastSegment(raw string) string {
	parts := strings.Split(pathOf(raw), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return raw
}

// Read turns the bytes of a file or a fetched answer into a document by their type, or by the name when the type says
// nothing.
func Read(origin string, typ string, b []byte) (search.Kind, string, []search.Section, error) {
	name := unescape(lastSegment(origin))
	path := pathOf(origin)
	switch {
	case strings.Contains(typ, "pdf") || pdfName.MatchString(path):
		s, err := PDFSections(b)
		return search.KindPDF, name, s, err
	case strings.HasSuffix(strings.ToLower(path), ".xls") || strings.Contains(typ, "ms-excel"):
		return "", "", nil, errors.New("save it as .xlsx or .csv")
	case xlsxName.MatchString(path) || strings.Contains(typ, "spreadsheetml"):
		s, err := SheetSections(b)
		return search.KindSheet, name, s, err
	}
	text := string(b)
	switch {
	case csvName.MatchString(path) || strings.Contains(typ, "csv"):
		s, err := CSVSections(text)
		return search.KindSheet, name, s, err
	case strings.Contains(typ, "html") || (typ == "" && htmlStart.MatchString(text)):
		title, s := HTMLSections(text, origin)
		if title == "" {
			title = name
		}
		return search.KindHTML, title, s, nil
	}
	kind := search.KindText
	if strings.Contains(typ, "markdown") || mdName.MatchString(path) {
		kind = search.KindMarkdown
	}
	return kind, name, []search.Section{{Text: text, URL: origin}}, nil
}

// Prepare reads a document from its source. The error says why it was refused, in words for the caller.
func (f *Fetcher) Prepare(ctx context.Context, in Source) (Prepared, error) {
	if (in.URL != "") == (in.File != nil) {
		return Prepared{}, errors.New("give the document as a url or as a file, one of the two")
	}
	var p Prepared
	var name, typ string
	var body []byte
	if in.File != nil {
		name = strings.TrimSpace(in.File.Name)
		if name == "" {
			return p, errors.New("a file has a name")
		}
		if len(in.File.Bytes) == 0 {
			return p, fmt.Errorf("%s is empty", name)
		}
		if len(in.File.Bytes) > MaxUpload {
			return p, fmt.Errorf("%s is over %d MB: split it", name, MaxUpload/1_000_000)
		}
		p.Origin, body = name, in.File.Bytes
		typ = strings.TrimSpace(strings.SplitN(strings.ToLower(in.File.Type), ";", 2)[0])
		if why := RefuseType(name, typ, body); why != "" {
			return p, errors.New(why)
		}
		if !in.File.Pasted {
			t := typ
			if t == "" {
				t = GuessType(name)
			}
			p.Original = &search.Original{OriginalInfo: search.OriginalInfo{Name: name, Type: t, Size: len(body)}, Bytes: body}
		}
	} else {
		origin, err := f.clean(in.URL)
		if err != nil {
			return p, err
		}
		got, err := f.Get(ctx, origin, MaxFetch)
		if err != nil {
			return p, err
		}
		p.Origin, body = origin, got.Bytes
		name = unescape(lastSegment(origin))
		typ = strings.TrimSpace(strings.SplitN(got.Type, ";", 2)[0])
		// a page has no ending, and says what it is by its type
		if why := RefuseType(name, typ, body); why != "" {
			return p, errors.New(why)
		}
		t := typ
		if t == "" {
			t = GuessType(name)
		}
		p.Original = &search.Original{OriginalInfo: search.OriginalInfo{Name: name, Type: t, Size: len(body)}, Bytes: body}
	}
	if imageName.MatchString(pathOf(name)) || imageType.MatchString(typ) {
		// no OCR: an image is found by its title and the words the caller gave it
		title := strings.TrimSpace(in.Title)
		if title == "" {
			title = name
		}
		var parts []string
		for _, s := range []string{title, strings.TrimSpace(in.Description)} {
			if s != "" {
				parts = append(parts, s)
			}
		}
		p.Kind, p.Title, p.Sections = search.KindImage, title, []search.Section{{Text: strings.Join(parts, "\n\n")}}
	} else {
		read := p.Origin
		// a PDF is known by its first bytes whatever it is called
		if in.File != nil && hasPDFHeader(body) && !pdfName.MatchString(read) {
			read += ".pdf"
		}
		kind, title, sections, err := Read(read, typ, body)
		if err != nil {
			return p, err
		}
		p.Kind, p.Title, p.Sections = kind, title, sections
		if t := strings.TrimSpace(in.Title); t != "" {
			p.Title = t
		}
	}
	kept := p.Sections[:0]
	for _, s := range p.Sections {
		if strings.TrimSpace(s.Text) != "" {
			kept = append(kept, s)
		}
	}
	p.Sections = kept
	if len(p.Sections) == 0 {
		return p, fmt.Errorf("no text found in %s (a scan or a page drawn by script?)", p.Origin)
	}
	if t := strings.TrimSpace(in.Title); t != "" {
		p.Title = t
	}
	return p, nil
}
