package docs

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"kedalio/core/memory/search"
)

var ctx = context.Background()

// miniPDF is a PDF with one line of text per page, in Helvetica, written by hand so the test leans on no PDF writer.
func miniPDF(pages ...string) []byte {
	var b bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	b.WriteString("%PDF-1.4\n")
	var kids []string
	for i := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", 4+2*i))
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	for i, text := range pages {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents %d 0 R /Resources << /Font << /F1 3 0 R >> >> >>", 5+2*i))
		stream := fmt.Sprintf("BT /F1 12 Tf 20 100 Td (%s) Tj ET", text)
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return b.Bytes()
}

func TestPDFSectionsOnePerPageNumberedFromOne(t *testing.T) {
	got, err := PDFSections(miniPDF("Plug the quokka in", "", "Then press start"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Page != 1 || got[1].Page != 3 ||
		!strings.Contains(got[0].Text, "Plug the quokka in") || !strings.Contains(got[1].Text, "press start") {
		t.Fatalf("got %+v", got)
	}
}

func TestPDFSectionsStopsAtThePageAndTextCaps(t *testing.T) {
	pdfMaxPages, pdfMaxText = 2, 1000
	t.Cleanup(func() { pdfMaxPages, pdfMaxText = defaultPDFMaxPages, defaultPDFMaxText })
	got, err := PDFSections(miniPDF("one", "two", "three", "four"))
	if err != nil || len(got) != 2 || got[1].Page != 2 {
		t.Fatalf("page cap: %+v, %v", got, err)
	}
	pdfMaxPages, pdfMaxText = 100, 8
	got, err = PDFSections(miniPDF("aaaaa", "bbbbb", "ccccc"))
	if err != nil || len(got) != 2 {
		t.Fatalf("text cap: %+v, %v", got, err)
	}
}

func TestPDFSectionsRefusesAFileThatIsNotOne(t *testing.T) {
	if _, err := PDFSections([]byte("%PDF-1.4 but nothing else")); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("got %v", err)
	}
}

func TestHTMLTextKeepsHeadingsAndCodeAndDropsChrome(t *testing.T) {
	title, text := HTMLText(`<html><head><title>The &amp; Guide</title><script>var x = 1;</script></head><body>
<nav>Home | About</nav><header>Site</header>
<main><h1>Install<a class="anchor" href="#install">#</a></h1><p>Run the tool.</p>
<pre><code># not a heading
  indented &lt;b&gt;</code></pre>
<h2>Use</h2><ul><li>one</li><li>two</li></ul></main><footer>(c)</footer></body></html>`)
	if title != "The & Guide" {
		t.Fatalf("title %q", title)
	}
	want := "# Install\n\nRun the tool.\n\n```\n# not a heading\n  indented <b>\n```\n\n## Use\n\n- one\n- two"
	if text != want {
		t.Fatalf("got\n%q\nwant\n%q", text, want)
	}
}

func TestHTMLSectionsCarryTheURLAndNothingFromAnEmptyPage(t *testing.T) {
	title, s := HTMLSections("<html><body><h1>Hello</h1><p>World</p></body></html>", "https://example.com/a")
	if title != "Hello" || len(s) != 1 || s[0].URL != "https://example.com/a" {
		t.Fatalf("%q %+v", title, s)
	}
	if _, s := HTMLSections("<html><body><script>x</script></body></html>", ""); s != nil {
		t.Fatalf("got %+v", s)
	}
}

func TestCSVSectionsDetectsTheSeparatorAndKeepsQuotedLines(t *testing.T) {
	got, err := CSVSections("\ufeffname;note\n\"Alice\";\"two\nlines\"\n;\nBob;ok\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "name | note\nAlice | two lines\nBob | ok" {
		t.Fatalf("got %+v", got)
	}
}

// xlsx is a workbook with one sheet, in the shape a spreadsheet program writes it.
func xlsx(t *testing.T) []byte {
	t.Helper()
	return xlsxSheet(t, `<worksheet><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="C1" t="inlineStr"><is><t>Qty</t></is></c></row>
<row r="2"><c r="A2" t="s"><v>1</v></c><c r="B2"><v>3</v></c><c r="C2" t="b"><v>1</v></c></row>
</sheetData></worksheet>`)
}

func xlsxSheet(t *testing.T, sheet string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range map[string]string{
		"xl/workbook.xml":            `<workbook><sheets><sheet name="Fruit &amp; veg" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>Item</t></si><si><r><t>Ap</t></r><r><t>ple</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml":   sheet,
	} {
		f, _ := w.Create(name)
		f.Write([]byte(body))
	}
	w.Close()
	return b.Bytes()
}

func TestSheetSectionsRefusesAWorkbookThatInflatesPastTheTotalOrHasTooManyParts(t *testing.T) {
	sheets := func(n, size int) []byte {
		var b bytes.Buffer
		w := zip.NewWriter(&b)
		f, _ := w.Create("xl/workbook.xml")
		f.Write([]byte(`<workbook/>`))
		for i := 0; i < n; i++ {
			f, _ = w.Create(fmt.Sprintf("xl/worksheets/s%d.xml", i))
			f.Write(bytes.Repeat([]byte(" "), size))
		}
		w.Close()
		return b.Bytes()
	}
	total, entry, count := maxXLSXBytes, maxXLSXEntryBytes, maxXLSXEntries
	t.Cleanup(func() { maxXLSXBytes, maxXLSXEntryBytes, maxXLSXEntries = total, entry, count })
	maxXLSXBytes, maxXLSXEntryBytes, maxXLSXEntries = 1000, 800, 5

	// each part is under the entry cap, together they are not
	if _, err := SheetSections(sheets(3, 500)); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("three parts of 500 against a total of 1000: %v", err)
	}
	if _, err := SheetSections(sheets(6, 1)); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("seven parts against a cap of five: %v", err)
	}
	// within the caps the workbook is read as before (this one has no sheet, which is its own error)
	if _, err := SheetSections(sheets(1, 500)); err != nil && strings.Contains(err.Error(), "too") {
		t.Errorf("a small workbook: %v", err)
	}
}

func TestSheetSectionsReadsCellsWherTheyAre(t *testing.T) {
	got, err := SheetSections(xlsx(t))
	if err != nil {
		t.Fatal(err)
	}
	want := "## Fruit & veg\n\nItem |  | Qty\nApple | 3 | TRUE"
	if len(got) != 1 || got[0].Text != want {
		t.Fatalf("got %+v want %q", got, want)
	}
	if _, err := SheetSections([]byte("not a zip")); err == nil {
		t.Fatal("a file that is no workbook was read")
	}
}

// A reference like ZZZZZZZZ1 would be a column of billions: the cell is read in sequence instead of padding its row.
func TestSheetSectionsIgnoresAColumnPastTheLastOne(t *testing.T) {
	for ref, want := range map[string]int{"A1": 1, "Z9": 26, "AA1": 27, "XFD1": 16384, "XFE1": 0, "ZZZZZZZZ1": 0, "1": 0} {
		if got := column(ref); got != want {
			t.Errorf("column(%q) = %d, want %d", ref, got, want)
		}
	}
	got, err := SheetSections(xlsxSheet(t, `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>a</t></is></c>`+
		`<c r="ZZZZZZZZ1" t="inlineStr"><is><t>b</t></is></c></row></sheetData></worksheet>`))
	if err != nil || len(got) != 1 || !strings.HasSuffix(got[0].Text, "a | b") {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// The marker that holds a code block's place is a private-use character; a page that contains it must not crash the
// reader.
func TestHTMLTextKeepsAMarkerThatIsNotACodeBlock(t *testing.T) {
	_, text := HTMLText("<html><body><p>before \uE0007\uE000 after</p></body></html>")
	if !strings.Contains(text, "before") || !strings.Contains(text, "after") {
		t.Fatalf("got %q", text)
	}
	_, text = HTMLText("<html><body><pre>x := 1</pre><p>\uE0000\uE000 \uE0009\uE000</p></body></html>")
	if !strings.Contains(text, "x := 1") {
		t.Fatalf("got %q", text)
	}
}

func TestPrivateAddressesAndHostsAreRefused(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0",
		"224.0.0.1", "::1", "fc00::1", "fe80::1", "::ffff:10.0.0.1",
		"0.1.2.3", "192.0.0.8", "192.0.0.170", "192.0.2.5", "192.88.99.1", "198.18.0.1", "198.19.255.255", "198.51.100.7", "203.0.113.7",
		"255.255.255.255", "240.0.0.1", "::", "64:ff9b::808:808", "64:ff9b::a00:1", "64:ff9b:1::1", "100::1", "2001::1", "2001:db8::1",
		"2002:a00:1::1", "3fff::1", "5f00::1", "fd12:3456::1", "ff02::1", "::ffff:198.18.0.1"} {
		if !IsPrivateIP(netip.MustParseAddr(ip)) {
			t.Errorf("%s is not refused", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "93.184.216.34", "2606:4700::1111", "198.20.0.1", "192.0.1.1", "192.0.3.1", "2a00:1450::1"} {
		if IsPrivateIP(netip.MustParseAddr(ip)) {
			t.Errorf("%s is refused", ip)
		}
	}
	for _, h := range []string{"localhost", "printer", "db.internal", "nas.local", "[::1]", "10.0.0.5", "127.0.0.1."} {
		if RefusedHost(h) == "" {
			t.Errorf("%s is not refused", h)
		}
	}
	if why := RefusedHost("docs.example.com"); why != "" {
		t.Errorf("refused: %s", why)
	}
}

func TestCleanURL(t *testing.T) {
	for in, want := range map[string]string{
		"ftp://example.com/a":          "http or https",
		"https://user:pw@example.com/": "credentials",
		"http://localhost:8080/x":      "not a public host",
		"http://192.168.0.1/x":         "private address",
		"not a url":                    "URL",
	} {
		if _, err := CleanURL(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CleanURL(%q) = %v, want %q", in, err, want)
		}
	}
	if u, err := CleanURL(" https://example.com/a b "); err != nil || u != "https://example.com/a%20b" {
		t.Errorf("got %q %v", u, err)
	}
}

func TestTheGuardedFetcherNeverReachesTheLoopback(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	_, err := (&Fetcher{}).Get(ctx, srv.URL, 1000)
	if err == nil || !strings.Contains(err.Error(), "private address") || hit {
		t.Fatalf("err %v, reached %v", err, hit)
	}
	// the guard also holds on the dial, for a name that the URL check lets through
	_, err = guarded().Get(srv.URL)
	if err == nil || hit {
		t.Fatalf("dial: err %v, reached %v", err, hit)
	}
}

func TestFetcherReadsUpToTheLimitAndSaysWhyItFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			w.Header().Set("Content-Type", "text/plain")
			w.Write(bytes.Repeat([]byte("x"), 100))
		case "/gone":
			http.NotFound(w, r)
		default:
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.Write([]byte("# Page\n\nhello"))
		}
	}))
	defer srv.Close()
	f := &Fetcher{Client: srv.Client()}
	b, err := f.Get(ctx, srv.URL+"/ok", 1000)
	if err != nil || b.Type != "text/markdown; charset=utf-8" || string(b.Bytes) != "# Page\n\nhello" {
		t.Fatalf("%+v %v", b, err)
	}
	if _, err := f.Get(ctx, srv.URL+"/big", 50); err == nil || !strings.Contains(err.Error(), "is over") {
		t.Fatalf("got %v", err)
	}
	if _, err := f.Get(ctx, srv.URL+"/gone", 50); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("got %v", err)
	}
}

// A link often carries a token in its query; the error ends in logs and tool results.
func TestFetchErrorsKeepTheQueryAndUserInfoOut(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	f := &Fetcher{Client: srv.Client()}
	for _, max := range []int{1000, 0} {
		_, err := f.Get(ctx, srv.URL+"/gone?token=s3cret#frag", max)
		if err == nil || strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "/gone") {
			t.Fatalf("got %v", err)
		}
	}
	if got := plainURL("https://user:pw@example.com/a/b?token=s3cret#x"); got != "https://example.com/a/b" {
		t.Fatalf("got %q", got)
	}
}

func TestRefuseType(t *testing.T) {
	for _, c := range []struct{ name, typ, want string }{
		{"guide.md", "", ""},
		{"page", "text/html", ""},
		{"data.xls", "", "save it as .xlsx or .csv"},
		{"noending", "", "type not supported: no extension"},
		{"tool.exe", "application/octet-stream", "type not supported: exe"},
	} {
		if got := RefuseType(c.name, c.typ, nil); got != c.want {
			t.Errorf("RefuseType(%q, %q) = %q, want %q", c.name, c.typ, got, c.want)
		}
	}
	if RefuseType("renamed", "application/octet-stream", []byte("%PDF-1.7")) != "" {
		t.Error("a PDF is known by its first bytes")
	}
}

func prepare(t *testing.T, in Source) Prepared {
	t.Helper()
	p, err := (&Fetcher{}).Prepare(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPrepareMarkdownTextAndPastedText(t *testing.T) {
	p := prepare(t, Source{File: &File{Name: "guide.md", Bytes: []byte("# Guide\n\nhello"), Pasted: true}})
	if p.Kind != search.KindMarkdown || p.Title != "guide.md" || p.Origin != "guide.md" || p.Original != nil ||
		len(p.Sections) != 1 || p.Sections[0].URL != "guide.md" {
		t.Fatalf("got %+v", p)
	}
	p = prepare(t, Source{File: &File{Name: "notes.txt", Bytes: []byte("plain")}, Title: "My notes"})
	if p.Kind != search.KindText || p.Title != "My notes" || p.Original == nil || p.Original.Type != "text/plain" || p.Original.Size != 5 {
		t.Fatalf("got %+v", p)
	}
}

func TestPrepareHTMLTakesItsTitleFromThePage(t *testing.T) {
	p := prepare(t, Source{File: &File{Name: "page.html", Bytes: []byte("<html><head><title>Hi there</title></head><body><p>text</p></body></html>")}})
	if p.Kind != search.KindHTML || p.Title != "Hi there" {
		t.Fatalf("got %+v", p)
	}
}

func TestPreparePDFByItsFirstBytesWhateverItIsCalled(t *testing.T) {
	p := prepare(t, Source{File: &File{Name: "manual", Bytes: miniPDF("Plug the quokka in"), Type: "application/octet-stream"}})
	if p.Kind != search.KindPDF || len(p.Sections) != 1 || p.Sections[0].Page != 1 || p.Original == nil {
		t.Fatalf("got %+v", p)
	}
}

func TestPrepareAnImageIsFoundByItsTitleAndDescription(t *testing.T) {
	p := prepare(t, Source{File: &File{Name: "chart.png", Bytes: []byte{0x89, 'P', 'N', 'G'}}, Description: "a bar chart of sales"})
	if p.Kind != search.KindImage || p.Sections[0].Text != "chart.png\n\na bar chart of sales" {
		t.Fatalf("got %+v", p)
	}
}

func TestPrepareRefusals(t *testing.T) {
	f := &Fetcher{}
	for _, c := range []struct {
		in   Source
		want string
	}{
		{Source{}, "a url or as a file"},
		{Source{URL: "https://example.com/a", File: &File{Name: "a.md", Bytes: []byte("x")}}, "a url or as a file"},
		{Source{File: &File{Name: " ", Bytes: []byte("x")}}, "a file has a name"},
		{Source{File: &File{Name: "a.md"}}, "is empty"},
		{Source{File: &File{Name: "a.exe", Bytes: []byte("x")}}, "type not supported: exe"},
		{Source{File: &File{Name: "a.xls", Bytes: []byte("x")}}, "save it as .xlsx or .csv"},
		{Source{File: &File{Name: "a.md", Bytes: []byte("   \n")}}, "no text found"},
		{Source{File: &File{Name: "a.pdf", Bytes: []byte("not a pdf")}}, "cannot be read"},
		{Source{URL: "http://127.0.0.1/a.md"}, "private address"},
	} {
		if _, err := f.Prepare(ctx, c.in); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: got %v, want %q", c.in, err, c.want)
		}
	}
	big := &File{Name: "big.txt", Bytes: make([]byte, MaxUpload+1)}
	if _, err := f.Prepare(ctx, Source{File: big}); err == nil || !strings.Contains(err.Error(), "split it") {
		t.Errorf("got %v", err)
	}
}

func TestPrepareFetchesAURLAndKeepsTheOriginal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html><head><title>Remote</title></head><body><p>from afar</p></body></html>"))
	}))
	defer srv.Close()
	// the loopback is refused by the URL check, so the test serves through a client that bypasses it and calls Read
	b, err := (&Fetcher{Client: srv.Client()}).Get(ctx, srv.URL+"/docs/start", MaxFetch)
	if err != nil {
		t.Fatal(err)
	}
	kind, title, sections, err := Read(srv.URL+"/docs/start", b.Type, b.Bytes)
	if err != nil || kind != search.KindHTML || title != "Remote" || len(sections) != 1 || !strings.Contains(sections[0].Text, "from afar") {
		t.Fatalf("%v %q %+v %v", kind, title, sections, err)
	}
}
