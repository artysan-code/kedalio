package docs

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/ledongthuc/pdf"

	"kedalio/core/memory/search"
)

const (
	defaultPDFMaxPages = 2000
	defaultPDFMaxText  = 8 << 20 // bytes of text; the upload is at most 10 MB, but compressed streams expand
)

// variables so a test can lower them without building a huge file
var pdfMaxPages, pdfMaxText = defaultPDFMaxPages, defaultPDFMaxText

// PDFSections reads the pages of a PDF, numbered from 1; pages with no text are left out. A scan has none: nothing is read
// from it, and the file's title and the words the caller gave it are what finds it.
func PDFSections(data []byte) (out []search.Section, err error) {
	// the reader panics on a file it cannot follow: the file is not a PDF it can read, not a crash
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("this PDF cannot be read (%v)", r)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("this PDF cannot be read (%v)", err)
	}
	// a hostile file can declare any number of pages and make each one expensive: read what a document plausibly holds
	// and keep it, rather than refuse or run without bound
	pages, size := min(r.NumPage(), pdfMaxPages), 0
	for i := 1; i <= pages && size < pdfMaxText; i++ {
		text, err := r.Page(i).GetPlainText(nil)
		if err != nil {
			// one page that does not read is left out; the others stand
			continue
		}
		if text = strings.TrimSpace(text); text != "" {
			size += len(text)
			out = append(out, search.Section{Text: text, Page: i})
		}
	}
	return out, nil
}

var (
	titleTag    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	h1Tag       = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)
	mainTag     = regexp.MustCompile(`(?is)<main[^>]*>(.*?)</main>`)
	bodyTag     = regexp.MustCompile(`(?is)<body[^>]*>(.*?)</body>`)
	comment     = regexp.MustCompile(`(?s)<!--.*?-->`)
	preTag      = regexp.MustCompile(`(?is)<pre\b[^>]*>(.*?)</pre>`)
	brTag       = regexp.MustCompile(`(?i)<br\s*/?>`)
	anyTag      = regexp.MustCompile(`<[^>]+>`)
	liTag       = regexp.MustCompile(`(?i)<li[^>]*>`)
	blockEnd    = regexp.MustCompile(`(?i)</(p|div|section|article|ul|ol|table|tr|blockquote|pre|dl)>`)
	blanks      = regexp.MustCompile(`[ \t\x{a0}]+`)
	manyLines   = regexp.MustCompile(`\n{3,}`)
	spaces      = regexp.MustCompile(`\s+`)
	anchorLink  = regexp.MustCompile(`(?is)<a\b[^>]*(?:anchor|permalink|heading-link|headerlink)[^>]*>.*?</a>`)
	headingTail = regexp.MustCompile(`(?i)(?:\s*(?:Jump to heading|Direct link to [^#¶§]*|[#¶§]))+$`)
	codeMark    = regexp.MustCompile(`\x{E000}(\d+)\x{E000}`)
	headings    [6]*regexp.Regexp
	dropped     []*regexp.Regexp
)

func init() {
	for i := range headings {
		headings[i] = regexp.MustCompile(fmt.Sprintf(`(?is)<h%d[^>]*>(.*?)</h%d>`, i+1, i+1))
	}
	// what is not content: scripts, styles, navigation, headers and footers
	for _, t := range []string{"script", "style", "noscript", "template", "svg", "nav", "header", "footer", "aside", "form"} {
		dropped = append(dropped, regexp.MustCompile(fmt.Sprintf(`(?is)<%s\b.*?</%s>`, t, t)))
	}
}

// headingText is a heading's inner HTML as plain text, without the link a docs site hangs on it («Jump to heading», «#», «¶»).
func headingText(inner string) string {
	s := html.UnescapeString(anyTag.ReplaceAllString(anchorLink.ReplaceAllString(inner, ""), ""))
	return strings.TrimSpace(headingTail.ReplaceAllString(strings.TrimSpace(spaces.ReplaceAllString(s, " ")), ""))
}

// HTMLText is an HTML page as its title and its readable text: what is not content (scripts, styles, navigation,
// headers and footers) is dropped; headings become Markdown headings, blocks become paragraphs.
func HTMLText(page string) (title, text string) {
	t := titleTag.FindStringSubmatch(page)
	if t == nil {
		t = h1Tag.FindStringSubmatch(page)
	}
	if t != nil {
		title = strings.TrimSpace(spaces.ReplaceAllString(html.UnescapeString(anyTag.ReplaceAllString(t[1], "")), " "))
	}
	body := page
	if m := mainTag.FindStringSubmatch(page); m != nil {
		body = m[1]
	} else if m := bodyTag.FindStringSubmatch(page); m != nil {
		body = m[1]
	}
	// code blocks are set aside as fenced text and put back after the lines are tidied: a «# comment» in one must
	// never read as a heading, and its indentation is kept
	var code []string
	body = comment.ReplaceAllString(body, "")
	for _, re := range dropped {
		body = re.ReplaceAllString(body, "")
	}
	body = preTag.ReplaceAllStringFunc(body, func(m string) string {
		inner := preTag.FindStringSubmatch(m)[1]
		c := html.UnescapeString(anyTag.ReplaceAllString(brTag.ReplaceAllString(inner, "\n"), ""))
		code = append(code, strings.TrimRight(strings.TrimLeft(c, "\n"), " \t\r\n"))
		return fmt.Sprintf("\n\n\uE000%d\uE000\n\n", len(code)-1)
	})
	for i, re := range headings {
		body = re.ReplaceAllStringFunc(body, func(m string) string {
			return "\n\n" + strings.Repeat("#", i+1) + " " + headingText(re.FindStringSubmatch(m)[1]) + "\n\n"
		})
	}
	body = liTag.ReplaceAllString(body, "\n- ")
	body = brTag.ReplaceAllString(body, "\n")
	body = blockEnd.ReplaceAllString(body, "\n\n")
	body = anyTag.ReplaceAllString(body, "")
	lines := strings.Split(html.UnescapeString(body), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(blanks.ReplaceAllString(l, " "))
	}
	clean := strings.TrimSpace(manyLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
	clean = codeMark.ReplaceAllStringFunc(clean, func(m string) string {
		n, err := strconv.Atoi(codeMark.FindStringSubmatch(m)[1])
		if err != nil || n >= len(code) {
			// the page itself carries the marker: it is text, not a place held for a code block
			return m
		}
		return "```\n" + code[n] + "\n```"
	})
	return title, clean
}

// HTMLSections is an HTML page as its title and one section carrying its URL.
func HTMLSections(page, url string) (string, []search.Section) {
	title, text := HTMLText(page)
	if text == "" {
		return title, nil
	}
	return title, []search.Section{{Text: text, URL: url}}
}

var (
	lineBreaks = regexp.MustCompile(`\s+`)
	trailBars  = regexp.MustCompile(`(?:\s*\|\s*)+$`)
)

func rowLine(cells []string) string {
	c := make([]string, len(cells))
	for i, v := range cells {
		c[i] = strings.TrimSpace(lineBreaks.ReplaceAllString(v, " "))
	}
	return trailBars.ReplaceAllString(strings.Join(c, " | "), "")
}

// CSVSections is CSV text as one section: its rows as lines of cells separated by « | »; empty rows are left out.
// The separator is the comma, semicolon or tab the first line has most of.
func CSVSections(text string) ([]search.Section, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	first, _, _ := strings.Cut(text, "\n")
	sep, most := ',', 0
	for _, s := range []rune{',', ';', '\t'} {
		if n := strings.Count(first, string(s)); n > most {
			sep, most = s, n
		}
	}
	r := csv.NewReader(strings.NewReader(text))
	r.Comma, r.LazyQuotes, r.FieldsPerRecord = sep, true, -1
	var lines []string
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if l := rowLine(rec); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return []search.Section{{Text: strings.Join(lines, "\n")}}, nil
}

// maxCells is the most cells read from one workbook: a sheet of millions of rows is a database, not a document.
const maxCells = 50_000

var (
	sheetRe  = regexp.MustCompile(`<sheet\b[^>]*>`)
	relRe    = regexp.MustCompile(`<Relationship\b[^>]*>`)
	siRe     = regexp.MustCompile(`(?s)<si\b[^>]*?(?:/>|>(.*?)</si>)`)
	rowRe    = regexp.MustCompile(`(?s)<row\b[^>]*?(?:/>|>(.*?)</row>)`)
	cellRe   = regexp.MustCompile(`(?s)<c\b([^>]*?)(?:/>|>(.*?)</c>)`)
	runRe    = regexp.MustCompile(`(?s)<t\b[^>]*?(?:/>|>(.*?)</t>)`)
	phonetic = regexp.MustCompile(`(?s)<rPh\b.*?</rPh>`)
	inlineIs = regexp.MustCompile(`(?s)<is\b[^>]*>(.*?)</is>`)
	valueRe  = regexp.MustCompile(`(?s)<v\b[^>]*>(.*?)</v>`)
	wanted   = regexp.MustCompile(`^xl/(workbook\.xml|_rels/workbook\.xml\.rels|sharedStrings\.xml|worksheets/[^/]+\.xml)$`)
)

// attrRe holds one pattern for each attribute name attr is asked for, compiled once.
var attrRe = func() map[string]*regexp.Regexp {
	out := map[string]*regexp.Regexp{}
	for _, name := range []string{"Id", "Target", "name", "r:id", "t", "r"} {
		out[name] = regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	}
	return out
}()

func attr(tag, name string) (string, bool) {
	m := attrRe[name].FindStringSubmatch(tag)
	if m == nil {
		return "", false
	}
	if m[1] != "" {
		return m[1], true
	}
	return m[2], true
}

// runs are the text runs of an element (rich text has several; phonetic hints are not text).
func runs(xml string) string {
	var b strings.Builder
	for _, m := range runRe.FindAllStringSubmatch(phonetic.ReplaceAllString(xml, ""), -1) {
		b.WriteString(html.UnescapeString(m[1]))
	}
	return b.String()
}

// maxColumns is the last column a workbook has (XFD): a reference past it is garbage, and honouring it would pad a row
// with millions of empty cells.
const maxColumns = 16384

// column is the number of a cell reference's column, A1 → 1; 0 when the reference has no usable column.
func column(ref string) int {
	n := 0
	for _, c := range strings.TrimRight(ref, "0123456789") {
		if n = n*26 + int(c-'@'); n > maxColumns {
			return 0
		}
	}
	return n
}

// What a workbook may inflate to, in total and per part, and how many parts are read; variables so a test can lower them.
var (
	maxXLSXBytes      int64 = 100_000_000
	maxXLSXEntryBytes int64 = 100_000_000
	maxXLSXEntries          = 1000
)

// SheetSections reads a workbook (.xlsx only: the old binary .xls is not read) as sections, one per sheet: its name
// as a heading (so a citation can name it), then its rows as lines of cells separated by « | ». Cells are read as
// written in the file (a date is its serial number, a formula its last result); sheets with no cell are left out. At
// most maxCells cells are read; a last line says when the rest was dropped.
func SheetSections(data []byte) ([]search.Section, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("not an .xlsx workbook")
	}
	files := map[string]string{}
	budget, entries := maxXLSXBytes, 0
	for _, f := range zr.File {
		if !wanted.MatchString(f.Name) || f.UncompressedSize64 >= uint64(maxXLSXEntryBytes) {
			continue
		}
		if entries++; entries > maxXLSXEntries {
			return nil, errors.New("workbook has too many parts to read")
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		// the declared size is the zip's own claim: the read is capped by what the whole workbook may still inflate to,
		// so many entries that each stay under the entry cap cannot add up to a zip bomb
		b, err := io.ReadAll(io.LimitReader(rc, min(maxXLSXEntryBytes, budget)+1))
		rc.Close()
		if budget -= int64(len(b)); budget < 0 || int64(len(b)) > maxXLSXEntryBytes {
			return nil, errors.New("workbook is too large to read")
		}
		if err == nil {
			files[f.Name] = string(b)
		}
	}
	workbook := files["xl/workbook.xml"]
	if workbook == "" {
		return nil, errors.New("not an .xlsx workbook")
	}
	var shared []string
	for _, m := range siRe.FindAllStringSubmatch(files["xl/sharedStrings.xml"], -1) {
		shared = append(shared, runs(m[1]))
	}
	targets := map[string]string{}
	for _, m := range relRe.FindAllString(files["xl/_rels/workbook.xml.rels"], -1) {
		id, ok1 := attr(m, "Id")
		to, ok2 := attr(m, "Target")
		if ok1 && ok2 {
			if strings.HasPrefix(to, "/") {
				targets[id] = to[1:]
			} else {
				targets[id] = "xl/" + to
			}
		}
	}
	var out []search.Section
	cells, dropped := 0, false
	for _, m := range sheetRe.FindAllString(workbook, -1) {
		name, _ := attr(m, "name")
		rid, _ := attr(m, "r:id")
		var lines []string
		for _, row := range rowRe.FindAllStringSubmatch(files[targets[rid]], -1) {
			var line []string
			for _, c := range cellRe.FindAllStringSubmatch(row[1], -1) {
				body := c[2]
				t, _ := attr(c[1], "t")
				v := valueRe.FindStringSubmatch(body)
				var value string
				switch {
				case t == "inlineStr":
					if is := inlineIs.FindStringSubmatch(body); is != nil {
						value = runs(is[1])
					}
				case v == nil:
				case t == "s":
					if n, err := strconv.Atoi(v[1]); err == nil && n >= 0 && n < len(shared) {
						value = shared[n]
					}
				case t == "b":
					value = map[bool]string{true: "TRUE", false: "FALSE"}[v[1] == "1"]
				default:
					value = html.UnescapeString(v[1])
				}
				if strings.TrimSpace(value) == "" {
					continue
				}
				if cells++; cells > maxCells {
					dropped = true
					break
				}
				at := len(line)
				if ref, ok := attr(c[1], "r"); ok && column(ref) > 0 {
					at = column(ref) - 1
				}
				for len(line) <= at {
					line = append(line, "")
				}
				line[at] = value
			}
			if l := rowLine(line); l != "" {
				lines = append(lines, l)
			}
			if dropped {
				break
			}
		}
		if len(lines) > 0 {
			out = append(out, search.Section{Text: "## " + html.UnescapeString(name) + "\n\n" + strings.Join(lines, "\n")})
		}
		if dropped {
			break
		}
	}
	if dropped && len(out) > 0 {
		out[len(out)-1].Text += fmt.Sprintf("\n\n(only the first %d cells were read; the rest of the file is left out)", maxCells)
	}
	return out, nil
}
