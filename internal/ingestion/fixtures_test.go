//go:build client && !server

package ingestion

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// This file builds the test documents in memory. Nothing in testdata/ except
// bad-utf8.txt is committed: DOCX and PDF fixtures are generated here so their
// structure is reviewable as code. TestMaterializeFixtures writes them to
// testdata/ on request.

const wNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`

const xmlProlog = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

const contentTypesXML = xmlProlog + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
	`<Default Extension="xml" ContentType="application/xml"/></Types>`

const stylesXML = xmlProlog + `<w:styles ` + wNS + `>` +
	`<w:style w:type="paragraph" w:styleId="Normal"><w:name w:val="Normal"/></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:pPr><w:outlineLvl w:val="1"/></w:pPr></w:style>` +
	`</w:styles>`

// numberingXML defines one three-level decimal list as numId 1.
const numberingXML = xmlProlog + `<w:numbering ` + wNS + `>` +
	`<w:abstractNum w:abstractNumId="0">` +
	`<w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/></w:lvl>` +
	`<w:lvl w:ilvl="1"><w:start w:val="1"/><w:numFmt w:val="decimal"/></w:lvl>` +
	`<w:lvl w:ilvl="2"><w:start w:val="1"/><w:numFmt w:val="decimal"/></w:lvl>` +
	`</w:abstractNum>` +
	`<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>` +
	`</w:numbering>`

func xmlText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func docxParagraph(text string) string {
	return `<w:p><w:r><w:t xml:space="preserve">` + xmlText(text) + `</w:t></w:r></w:p>`
}

func docxHeading(style, text string) string {
	return `<w:p><w:pPr><w:pStyle w:val="` + style + `"/></w:pPr><w:r><w:t>` + xmlText(text) + `</w:t></w:r></w:p>`
}

func docxListItem(ilvl, numID int, text string) string {
	return fmt.Sprintf(`<w:p><w:pPr><w:numPr><w:ilvl w:val="%d"/><w:numId w:val="%d"/></w:numPr></w:pPr><w:r><w:t>%s</w:t></w:r></w:p>`,
		ilvl, numID, xmlText(text))
}

func docxTableXML(rows ...[]string) string {
	var sb strings.Builder
	sb.WriteString(`<w:tbl>`)
	for _, row := range rows {
		sb.WriteString(`<w:tr>`)
		for _, cell := range row {
			sb.WriteString(`<w:tc>` + docxParagraph(cell) + `</w:tc>`)
		}
		sb.WriteString(`</w:tr>`)
	}
	sb.WriteString(`</w:tbl>`)
	return sb.String()
}

func docxDocument(body string) string {
	return xmlProlog + `<w:document ` + wNS + `><w:body>` + body + `</w:body></w:document>`
}

type zipEntry struct {
	name string
	body []byte
}

// buildZip writes a deflate-compressed archive.
func buildZip(tb testing.TB, entries []zipEntry) []byte {
	tb.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Deflate})
		if err != nil {
			tb.Fatalf("zip entry %q: %v", e.name, err)
		}
		if _, err := w.Write(e.body); err != nil {
			tb.Fatalf("zip write %q: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		tb.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// styledNumberedDOCX has heading styles, a three-level decimal list, a table and
// a paragraph with a tab.
func styledNumberedDOCX(tb testing.TB) []byte {
	body := docxHeading("Heading1", "Billing export") +
		docxParagraph("Finance needs a monthly export.") +
		docxHeading("Heading2", "Requirements") +
		docxListItem(0, 1, "Export invoices") +
		docxListItem(1, 1, "Choose a date range") +
		docxListItem(2, 1, "Pick a start date") +
		docxListItem(0, 1, "Download the file") +
		docxTableXML([]string{"Field", "Type"}, []string{"invoice_id", "text"}) +
		`<w:p><w:r><w:t>Tabbed</w:t></w:r><w:r><w:tab/></w:r><w:r><w:t>text</w:t></w:r></w:p>`
	return buildZip(tb, []zipEntry{
		{"[Content_Types].xml", []byte(contentTypesXML)},
		{"word/document.xml", []byte(docxDocument(body))},
		{"word/styles.xml", []byte(stylesXML)},
		{"word/numbering.xml", []byte(numberingXML)},
	})
}

// unstyledDOCX has neither a styles part nor a numbering part.
func unstyledDOCX(tb testing.TB) []byte {
	body := docxParagraph("Billing export") + docxParagraph("Finance needs a monthly export.")
	return buildZip(tb, []zipEntry{
		{"[Content_Types].xml", []byte(contentTypesXML)},
		{"word/document.xml", []byte(docxDocument(body))},
	})
}

// zipBombDOCX is a tiny archive whose document part inflates to 8 MiB, far above
// the 100:1 per-entry ratio limit.
func zipBombDOCX(tb testing.TB) []byte {
	return buildZip(tb, []zipEntry{
		{"[Content_Types].xml", []byte(contentTypesXML)},
		{"word/document.xml", bytes.Repeat([]byte("A"), 8<<20)},
	})
}

// manyEntriesDOCX holds more entries than a package may.
func manyEntriesDOCX(tb testing.TB) []byte {
	entries := []zipEntry{{"word/document.xml", []byte(docxDocument(docxParagraph("x")))}}
	for i := range MaxDOCXEntries + 1 {
		entries = append(entries, zipEntry{fmt.Sprintf("extra/e%05d.txt", i), nil})
	}
	return buildZip(tb, entries)
}

// escapingDOCX has an entry that climbs out of the package root.
func escapingDOCX(tb testing.TB) []byte {
	return buildZip(tb, []zipEntry{
		{"word/document.xml", []byte(docxDocument(docxParagraph("x")))},
		{"../evil.xml", []byte("<x/>")},
	})
}

// ---------------------------------------------------------------------- PDF

// pdfRun is one positioned piece of text on a page.
type pdfRun struct {
	x, y, size float64
	text       string
}

func pdfEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(s)
}

// writePDF serialises numbered objects with a classic cross-reference table.
func writePDF(objs map[int]string, count int) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, count+1)
	for i := 1; i <= count; i++ {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i, objs[i])
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", count+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= count; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", count+1, xref)
	return buf.Bytes()
}

// buildPDF writes a born-digital PDF with one Helvetica font whose glyphs are
// all 500 units wide, so the reader reports real advances. An empty page has no
// content at all, which the ingester treats as scanned.
func buildPDF(pages ...[]pdfRun) []byte {
	objs := map[int]string{1: "<< /Type /Catalog /Pages 2 0 R >>"}
	widths := strings.TrimSpace(strings.Repeat("500 ", 95))
	objs[3] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding /FirstChar 32 /LastChar 126 /Widths [" + widths + "] >>"
	next := 4
	var kids []string
	for _, runs := range pages {
		pageObj, contentObj := next, next+1
		next += 2
		kids = append(kids, fmt.Sprintf("%d 0 R", pageObj))
		var cs strings.Builder
		for _, r := range runs {
			fmt.Fprintf(&cs, "BT /F1 %g Tf 1 0 0 1 %g %g Tm (%s) Tj ET\n", r.size, r.x, r.y, pdfEscape(r.text))
		}
		stream := cs.String()
		objs[pageObj] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 3 0 R >> >> >>", contentObj)
		objs[contentObj] = fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream)
	}
	objs[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages))
	return writePDF(objs, next-1)
}

// manyPagesPDF declares n empty pages.
func manyPagesPDF(n int) []byte {
	objs := map[int]string{1: "<< /Type /Catalog /Pages 2 0 R >>"}
	var kids []string
	for i := range n {
		obj := 3 + i
		kids = append(kids, fmt.Sprintf("%d 0 R", obj))
		objs[obj] = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 10 10] >>"
	}
	objs[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n)
	return writePDF(objs, 2+n)
}

func oneColumnPDF() []byte {
	return buildPDF([]pdfRun{
		{72, 720, 20, "Billing Export"},
		{72, 690, 12, "Finance needs a monthly export of invoices."},
		{72, 676, 12, "It must be reliable and fast."},
		{72, 640, 12, "- Export invoices as CSV"},
		{72, 600, 12, "2. Download the file"},
	})
}

func twoColumnPDF() []byte {
	var runs []pdfRun
	for i, y := range []float64{700, 680, 660, 640} {
		runs = append(runs,
			pdfRun{72, y, 12, fmt.Sprintf("Left column line %d", i+1)},
			pdfRun{330, y, 12, fmt.Sprintf("Right column line %d", i+1)},
		)
	}
	return buildPDF(runs)
}

// scannedOnlyPDF has one page with no extractable text.
func scannedOnlyPDF() []byte { return buildPDF(nil) }

// mixedPDF has a text page followed by a page with no text.
func mixedPDF() []byte {
	return buildPDF(
		[]pdfRun{
			{72, 720, 20, "Billing Export"},
			{72, 690, 12, "Finance needs a monthly export of invoices."},
		},
		nil,
	)
}

// ---------------------------------------------------------------------- OCR

const tsvHeader = "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n"

func tsvWord(block, par, line, word int, conf float64, text string) string {
	return fmt.Sprintf("5\t1\t%d\t%d\t%d\t%d\t10\t10\t50\t20\t%.1f\t%s\n", block, par, line, word, conf, text)
}

// fakeOCR replaces pdftoppm and tesseract. It records every invocation so tests
// can assert the exact argument vectors.
type fakeOCR struct {
	mu    sync.Mutex
	calls [][]string
	tsv   string
}

func (f *fakeOCR) engine(language string) *ocrEngine {
	return &ocrEngine{
		lookPath: func(file string) (string, error) { return "/fake/bin/" + file, nil },
		run:      f.run,
		language: language,
	}
}

func (f *fakeOCR) run(_ context.Context, dir, name string, args ...string) (stdout, stderr []byte, err error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{name}, args...))
	f.mu.Unlock()
	switch {
	case strings.HasSuffix(name, "pdftoppm") && len(args) == 1 && args[0] == "-v":
		return nil, []byte("pdftoppm version 24.02.0\n"), nil
	case strings.HasSuffix(name, "tesseract") && len(args) == 1 && args[0] == "--version":
		return []byte("tesseract 5.3.4\n leptonica-1.82.0\n"), nil, nil
	case strings.HasSuffix(name, "pdftoppm"):
		// -r 200 -png -f N -l N <pdf> <prefix>
		prefix, page := args[len(args)-1], args[4]
		if werr := os.WriteFile(prefix+"-"+page+".png", []byte("png"), 0o600); werr != nil {
			return nil, nil, werr
		}
		return nil, nil, nil
	case strings.HasSuffix(name, "tesseract"):
		return []byte(f.tsv), nil, nil
	}
	return nil, nil, fmt.Errorf("unexpected tool %q", name)
}

func (f *fakeOCR) invocations() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

// absentOCR is an engine whose tools are not installed.
func absentOCR() *ocrEngine {
	return &ocrEngine{
		lookPath: func(string) (string, error) { return "", os.ErrNotExist },
		run: func(context.Context, string, string, ...string) ([]byte, []byte, error) {
			return nil, nil, os.ErrNotExist
		},
		language: "eng",
	}
}

// isolateHome keeps the OCR session directory out of the developer's real
// Plan-Forge cache.
func isolateHome(tb testing.TB) {
	tb.Helper()
	tb.Setenv("PLAN_FORGE_HOME", tb.TempDir())
}

// sparseReaderAt reports a huge size without holding the bytes: it serves head
// first and zeros afterwards.
type sparseReaderAt struct{ head []byte }

func (s sparseReaderAt) ReadAt(p []byte, off int64) (int, error) {
	for i := range p {
		idx := off + int64(i)
		if idx < int64(len(s.head)) {
			p[i] = s.head[idx]
		} else {
			p[i] = 0
		}
	}
	return len(p), nil
}

// TestMaterializeFixtures writes the generated documents to testdata/ so they
// can be inspected or opened in other tools. It runs only when
// PLAN_FORGE_WRITE_FIXTURES=1; the other tests never read these files.
func TestMaterializeFixtures(t *testing.T) {
	if os.Getenv("PLAN_FORGE_WRITE_FIXTURES") != "1" {
		t.Skip("set PLAN_FORGE_WRITE_FIXTURES=1 to write the generated fixtures to testdata/")
	}
	files := map[string][]byte{
		"styled-numbered.docx": styledNumberedDOCX(t),
		"unstyled.docx":        unstyledDOCX(t),
		"zip-bomb.docx":        zipBombDOCX(t),
		"one-column.pdf":       oneColumnPDF(),
		"two-column.pdf":       twoColumnPDF(),
		"scanned-only.pdf":     scannedOnlyPDF(),
		"oversized.pdf":        manyPagesPDF(MaxPDFPages + 1),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join("testdata", name), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}
