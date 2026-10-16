//go:build client && !server

package ingestion

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

func ingestBytes(t *testing.T, name string, data []byte, opts Options) ([]contracts.DocumentBlock, IngestReport, error) {
	t.Helper()
	return IngestWithOptions(context.Background(), name, bytes.NewReader(data), int64(len(data)), opts)
}

func mustIngest(t *testing.T, name string, data []byte, opts Options) ([]contracts.DocumentBlock, IngestReport) {
	t.Helper()
	blocks, rep, err := ingestBytes(t, name, data, opts)
	if err != nil {
		t.Fatalf("ingest %s: %v", name, err)
	}
	return blocks, rep
}

// squash removes all whitespace. PDF word spacing is reconstructed from glyph
// gaps, so these tests compare content and order, not spacing.
func squash(s string) string { return strings.Join(strings.Fields(s), "") }

func wantBound(t *testing.T, err error, name string) *contracts.ErrBound {
	t.Helper()
	var b *contracts.ErrBound
	if !errors.As(err, &b) {
		t.Fatalf("error = %v (%T), want *contracts.ErrBound", err, err)
	}
	if b.Name != name {
		t.Fatalf("bound name = %q, want %q", b.Name, name)
	}
	if b.Observed <= b.Limit {
		t.Fatalf("bound %s: observed %d does not exceed limit %d", b.Name, b.Observed, b.Limit)
	}
	return b
}

// ------------------------------------------------------------------- DOCX

func TestDOCXStyledNumbered(t *testing.T) {
	blocks, rep := mustIngest(t, "styled-numbered.docx", styledNumberedDOCX(t), Options{})

	if rep.Format != FormatDOCX || rep.Blocks != len(blocks) || !strings.HasPrefix(rep.SourceID, "src-") {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", rep.Warnings)
	}

	type want struct {
		kind  string
		level int
		text  string
	}
	wants := []want{
		{contracts.BlockHeading, 1, "Billing export"},
		{contracts.BlockParagraph, 0, "Finance needs a monthly export."},
		{contracts.BlockHeading, 2, "Requirements"},
		{contracts.BlockList, 1, "Export invoices"},
		{contracts.BlockList, 2, "Choose a date range"},
		{contracts.BlockList, 3, "Pick a start date"},
		{contracts.BlockList, 1, "Download the file"},
		{contracts.BlockTable, 0, ""},
		{contracts.BlockParagraph, 0, "Tabbed\ttext"},
	}
	if len(blocks) != len(wants) {
		t.Fatalf("got %d blocks, want %d: %+v", len(blocks), len(wants), blocks)
	}
	ids := map[string]bool{}
	for i, w := range wants {
		b := blocks[i]
		if b.Kind != w.kind || b.Level != w.level || b.Text != w.text {
			t.Errorf("block %d = {%s level %d %q}, want {%s level %d %q}", i, b.Kind, b.Level, b.Text, w.kind, w.level, w.text)
		}
		if b.Confidence != contracts.ConfidenceHigh {
			t.Errorf("block %d confidence = %s, want HIGH for a styled document", i, b.Confidence)
		}
		if b.ID == "" || b.ID != b.Source.BlockID || ids[b.ID] {
			t.Errorf("block %d has a missing, mismatched or duplicate ID %q", i, b.ID)
		}
		ids[b.ID] = true
		if b.Source.SourceID != rep.SourceID || b.Source.SHA256 == "" {
			t.Errorf("block %d source = %+v", i, b.Source)
		}
		if b.Source.Page != 0 {
			t.Errorf("block %d: a DOCX block must not invent a page, got %d", i, b.Source.Page)
		}
	}

	// The three nesting levels count independently; the fourth item resumes level 1.
	markers := []struct {
		kind    string
		ordinal int
	}{{ListDecimal, 1}, {ListDecimal, 1}, {ListDecimal, 1}, {ListDecimal, 2}}
	for i, m := range markers {
		kind, ordinal, ok := ListMarker(blocks[3+i])
		if !ok || kind != m.kind || ordinal != m.ordinal {
			t.Errorf("list item %d marker = (%s, %d, %v), want (%s, %d)", i, kind, ordinal, ok, m.kind, m.ordinal)
		}
	}
	for _, i := range []int{3, 4, 5, 6} {
		if blocks[i].Source.Heading != "Requirements" {
			t.Errorf("block %d heading provenance = %q, want Requirements", i, blocks[i].Source.Heading)
		}
	}

	wantRows := [][]string{{"Field", "Type"}, {"invoice_id", "text"}}
	if !reflect.DeepEqual(blocks[7].Rows, wantRows) {
		t.Errorf("table rows = %v, want %v", blocks[7].Rows, wantRows)
	}
}

func TestDOCXUnstyledIsMediumConfidence(t *testing.T) {
	blocks, rep := mustIngest(t, "unstyled.docx", unstyledDOCX(t), Options{})
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2: %+v", len(blocks), blocks)
	}
	for i, b := range blocks {
		if b.Kind != contracts.BlockParagraph {
			t.Errorf("block %d kind = %s, want paragraph: no styles means no headings", i, b.Kind)
		}
		if b.Confidence != contracts.ConfidenceMedium {
			t.Errorf("block %d confidence = %s, want MEDIUM without styles", i, b.Confidence)
		}
	}
	if !containsSubstring(rep.Warnings, "no styles part") {
		t.Errorf("warnings = %v, want one about the missing styles part", rep.Warnings)
	}
}

func containsSubstring(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestDOCXRejectsZipBomb(t *testing.T) {
	_, _, err := ingestBytes(t, "zip-bomb.docx", zipBombDOCX(t), Options{})
	b := wantBound(t, err, BoundDOCXRatio)
	if b.Limit != MaxDOCXCompressionRatio {
		t.Errorf("limit = %d, want %d", b.Limit, MaxDOCXCompressionRatio)
	}
}

func TestDOCXRejectsTooManyEntries(t *testing.T) {
	_, _, err := ingestBytes(t, "many.docx", manyEntriesDOCX(t), Options{})
	b := wantBound(t, err, BoundDOCXEntries)
	if b.Limit != MaxDOCXEntries {
		t.Errorf("limit = %d, want %d", b.Limit, MaxDOCXEntries)
	}
}

func TestDOCXRejectsEntryEscapingPackage(t *testing.T) {
	_, _, err := ingestBytes(t, "escape.docx", escapingDOCX(t), Options{})
	if !errors.Is(err, contracts.ErrUnsupportedFormat) {
		t.Fatalf("error = %v, want ErrUnsupportedFormat", err)
	}
}

func TestDOCXPasswordProtectedIsEncrypted(t *testing.T) {
	ole := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 512)...)
	_, _, err := ingestBytes(t, "secret.docx", ole, Options{})
	if !errors.Is(err, contracts.ErrEncrypted) {
		t.Fatalf("error = %v, want ErrEncrypted", err)
	}
}

func TestDOCXCorruptArchive(t *testing.T) {
	_, _, err := ingestBytes(t, "broken.docx", append([]byte("PK\x03\x04"), []byte("not a zip archive")...), Options{})
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("error = %v, want ErrCorrupt", err)
	}
}

// ---------------------------------------------------------------- routing

func TestRoutingRejectsMismatchedTypes(t *testing.T) {
	docx := styledNumberedDOCX(t)
	pdf := oneColumnPDF()
	cases := []struct {
		name string
		data []byte
	}{
		{"report.docx", pdf},
		{"report.pdf", []byte("plain text, not a PDF")},
		{"notes.md", docx},
		{"notes.txt", pdf},
		{"report.rtf", []byte("{\\rtf1}")},
		{"README", []byte("# no extension")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ingestBytes(t, tc.name, tc.data, Options{})
			if !errors.Is(err, contracts.ErrUnsupportedFormat) {
				t.Fatalf("error = %v, want ErrUnsupportedFormat and no plain-text fallback", err)
			}
		})
	}
}

func TestMarkdownStripsBOMAndKeepsHeadings(t *testing.T) {
	blocks, rep := mustIngest(t, "prd.md", []byte("\ufeff# Title\n\nBody text.\n"), Options{})
	if rep.Format != FormatMarkdown {
		t.Fatalf("format = %s", rep.Format)
	}
	if len(blocks) != 2 || blocks[0].Kind != contracts.BlockHeading || blocks[0].Text != "Title" || blocks[0].Level != 1 {
		t.Fatalf("blocks = %+v", blocks)
	}
	for _, b := range blocks {
		if strings.Contains(b.Text, "\ufeff") {
			t.Errorf("BOM left in %q", b.Text)
		}
	}
}

func TestTextRejectsInvalidUTF8(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "bad-utf8.txt"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, ierr := ingestBytes(t, "bad-utf8.txt", data, Options{})
	if !errors.Is(ierr, ErrCorrupt) {
		t.Fatalf("error = %v, want ErrCorrupt for invalid UTF-8", ierr)
	}
}

func TestTextParagraphs(t *testing.T) {
	blocks, _ := mustIngest(t, "idea.txt", []byte("First idea\nstill first\n\nSecond idea\n"), Options{})
	if len(blocks) != 2 || blocks[0].Text != "First idea\nstill first" || blocks[1].Text != "Second idea" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

// ----------------------------------------------------------------- bounds

func TestFileSizeBounds(t *testing.T) {
	head := sparseReaderAt{head: []byte("%PDF-1.4\n")}
	cases := []struct {
		name  string
		size  int64
		opts  Options
		limit int64
	}{
		{"ui", MaxFileBytesUI + 1, UIOptions(), MaxFileBytesUI},
		{"cli", MaxFileBytesCLI + 1, CLIOptions(), MaxFileBytesCLI},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := IngestWithOptions(context.Background(), "oversized.pdf", head, tc.size, tc.opts)
			b := wantBound(t, err, BoundFileBytes)
			if b.Limit != tc.limit || b.Observed != tc.size {
				t.Errorf("bound = %+v, want limit %d observed %d", b, tc.limit, tc.size)
			}
		})
	}
}

func TestPDFRejectsTooManyPages(t *testing.T) {
	_, _, err := ingestBytes(t, "oversized.pdf", manyPagesPDF(MaxPDFPages+1), Options{DisableOCR: true})
	b := wantBound(t, err, BoundPDFPages)
	if b.Limit != MaxPDFPages || b.Observed != MaxPDFPages+1 {
		t.Errorf("bound = %+v", b)
	}
}

// ---------------------------------------------------------------- PDF text

func TestPDFOneColumn(t *testing.T) {
	blocks, rep := mustIngest(t, "one-column.pdf", oneColumnPDF(), Options{DisableOCR: true})
	if rep.Format != FormatPDF || rep.Pages != 1 || len(rep.ScannedPages) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", rep.Warnings)
	}
	type want struct {
		kind string
		text string
	}
	wants := []want{
		{contracts.BlockHeading, "BillingExport"},
		{contracts.BlockParagraph, "Financeneedsamonthlyexportofinvoices.Itmustbereliableandfast."},
		{contracts.BlockList, "ExportinvoicesasCSV"},
		{contracts.BlockList, "Downloadthefile"},
	}
	if len(blocks) != len(wants) {
		t.Fatalf("got %d blocks, want %d: %+v", len(blocks), len(wants), blocks)
	}
	for i, w := range wants {
		b := blocks[i]
		if b.Kind != w.kind || squash(b.Text) != w.text {
			t.Errorf("block %d = {%s %q}, want {%s %q}", i, b.Kind, squash(b.Text), w.kind, w.text)
		}
		if b.Source.Page != 1 {
			t.Errorf("block %d page = %d, want 1", i, b.Source.Page)
		}
		if b.Confidence != contracts.ConfidenceHigh {
			t.Errorf("block %d confidence = %s, want HIGH for positioned text", i, b.Confidence)
		}
	}
	if blocks[0].Level != 1 {
		t.Errorf("heading level = %d, want 1", blocks[0].Level)
	}
	if kind, _, ok := ListMarker(blocks[2]); !ok || kind != ListBullet {
		t.Errorf("block 2 marker = %q, want a bullet", kind)
	}
	if kind, ordinal, ok := ListMarker(blocks[3]); !ok || kind != ListDecimal || ordinal != 2 {
		t.Errorf("block 3 marker = (%s, %d), want (decimal, 2)", kind, ordinal)
	}
}

func TestPDFTwoColumnWarnsAboutReadingOrder(t *testing.T) {
	blocks, rep := mustIngest(t, "two-column.pdf", twoColumnPDF(), Options{DisableOCR: true})
	if !containsSubstring(rep.Warnings, "multi-column layout detected") {
		t.Fatalf("warnings = %v, want a multi-column warning", rep.Warnings)
	}
	flagged := false
	for _, b := range blocks {
		if containsSubstring(b.Warnings, "multi-column") {
			flagged = true
		}
	}
	if !flagged {
		t.Error("no block carries the multi-column warning")
	}
}

// ------------------------------------------------------------- scanned pages

func TestPDFScannedWithoutOCRToolsIsReportedNotGuessed(t *testing.T) {
	isolateHome(t)
	blocks, rep := mustIngest(t, "scanned-only.pdf", scannedOnlyPDF(), Options{ocr: absentOCR()})
	if !reflect.DeepEqual(rep.ScannedPages, []int{1}) {
		t.Fatalf("scanned pages = %v, want [1]", rep.ScannedPages)
	}
	if rep.OCR != nil {
		t.Errorf("OCR provenance = %+v, want none when no page was recognised", rep.OCR)
	}
	if len(blocks) != 1 || blocks[0].Text != "" || blocks[0].Confidence != contracts.ConfidenceUnverifiable {
		t.Fatalf("blocks = %+v, want one UNVERIFIABLE placeholder with no text", blocks)
	}
	if len(blocks[0].Warnings) != 1 || !strings.HasPrefix(blocks[0].Warnings[0], "UNPARSED_SCANNED") ||
		!strings.Contains(blocks[0].Warnings[0], "brew install poppler tesseract") {
		t.Errorf("warnings = %v, want UNPARSED_SCANNED with install guidance", blocks[0].Warnings)
	}
	if blocks[0].Source.Page != 1 {
		t.Errorf("page = %d, want 1", blocks[0].Source.Page)
	}
	if md := Render(blocks); !strings.Contains(md, "> [UNPARSED_SCANNED") {
		t.Errorf("rendered Markdown %q hides the unparsed page", md)
	}
}

func TestPDFScannedPageUsesLocalOCR(t *testing.T) {
	isolateHome(t)
	f := &fakeOCR{tsv: tsvHeader +
		tsvWord(1, 1, 1, 1, 96, "Scanned") + tsvWord(1, 1, 1, 2, 94, "invoice") + tsvWord(1, 1, 1, 3, 95, "total") +
		tsvWord(2, 1, 1, 1, 90, "Due") + tsvWord(2, 1, 1, 2, 92, "soon")}
	blocks, rep := mustIngest(t, "mixed.pdf", mixedPDF(), Options{ocr: f.engine("eng")})

	if !reflect.DeepEqual(rep.ScannedPages, []int{2}) {
		t.Fatalf("scanned pages = %v, want [2]", rep.ScannedPages)
	}
	if len(blocks) != 4 {
		t.Fatalf("got %d blocks, want 4 (heading, paragraph, two OCR paragraphs): %+v", len(blocks), blocks)
	}
	if blocks[0].Kind != contracts.BlockHeading || blocks[0].Source.Page != 1 {
		t.Errorf("block 0 = %+v", blocks[0])
	}
	for i, wantText := range []string{"Scanned invoice total", "Due soon"} {
		b := blocks[2+i]
		if b.Text != wantText || b.Source.Page != 2 {
			t.Errorf("OCR block %d = {%q page %d}, want {%q page 2}", i, b.Text, b.Source.Page, wantText)
		}
		if b.Confidence != contracts.ConfidenceLow {
			t.Errorf("OCR block %d confidence = %s, want LOW", i, b.Confidence)
		}
		if len(b.Warnings) != 1 || !strings.HasPrefix(b.Warnings[0], "OCR text; mean word confidence") {
			t.Errorf("OCR block %d warnings = %v", i, b.Warnings)
		}
	}

	if rep.OCR == nil {
		t.Fatal("report has no OCR provenance")
	}
	if rep.OCR.DPI != 200 || rep.OCR.Language != "eng" || !reflect.DeepEqual(rep.OCR.Pages, []int{2}) {
		t.Errorf("OCR provenance = %+v", rep.OCR)
	}
	if rep.OCR.PdftoppmVersion != "pdftoppm version 24.02.0" || rep.OCR.TesseractVersion != "tesseract 5.3.4" {
		t.Errorf("tool versions = %q / %q", rep.OCR.PdftoppmVersion, rep.OCR.TesseractVersion)
	}
	if m := rep.OCR.MeanConfidence; m < 93.3 || m > 93.5 {
		t.Errorf("mean confidence = %.2f, want 93.4", m)
	}

	// Tools run through argument vectors, never a shell.
	var raster, recognise []string
	for _, c := range f.invocations() {
		switch {
		case strings.HasSuffix(c[0], "pdftoppm") && len(c) > 2:
			raster = c
		case strings.HasSuffix(c[0], "tesseract") && len(c) > 2:
			recognise = c
		}
	}
	if raster == nil || recognise == nil {
		t.Fatalf("OCR tools were not both invoked: %v", f.invocations())
	}
	if len(raster) != 10 || !reflect.DeepEqual(raster[1:8], []string{"-r", "200", "-png", "-f", "2", "-l", "2"}) ||
		filepath.Base(raster[8]) != "input.pdf" || filepath.Base(raster[9]) != "p" {
		t.Errorf("pdftoppm argv = %q", raster)
	}
	if len(recognise) != 6 || filepath.Base(recognise[1]) != "p-2.png" ||
		!reflect.DeepEqual(recognise[2:], []string{"stdout", "tsv", "-l", "eng"}) {
		t.Errorf("tesseract argv = %q", recognise)
	}
}

func TestPDFLowConfidenceOCRIsUnverifiable(t *testing.T) {
	isolateHome(t)
	f := &fakeOCR{tsv: tsvHeader + tsvWord(1, 1, 1, 1, 30, "smudged") + tsvWord(1, 1, 1, 2, 40, "text")}
	blocks, _ := mustIngest(t, "scanned-only.pdf", scannedOnlyPDF(), Options{ocr: f.engine("eng")})
	if len(blocks) != 1 || blocks[0].Confidence != contracts.ConfidenceUnverifiable {
		t.Fatalf("blocks = %+v, want one UNVERIFIABLE block below mean confidence 60", blocks)
	}
}

func TestPDFRejectsInvalidOCRLanguage(t *testing.T) {
	isolateHome(t)
	f := &fakeOCR{tsv: tsvHeader}
	_, _, err := ingestBytes(t, "scanned-only.pdf", scannedOnlyPDF(), Options{ocr: f.engine("eng; rm -rf /")})
	if err == nil {
		t.Fatal("an OCR language that is not a tesseract code must be refused")
	}
	for _, c := range f.invocations() {
		t.Errorf("no tool may run for an invalid language, saw %q", c)
	}
}

// ------------------------------------------------------------------ render

func TestRenderIsDeterministicMarkdown(t *testing.T) {
	blocks, _ := mustIngest(t, "styled-numbered.docx", styledNumberedDOCX(t), Options{})
	want := "# Billing export\n\n" +
		"Finance needs a monthly export.\n\n" +
		"## Requirements\n\n" +
		"1. Export invoices\n" +
		"   1. Choose a date range\n" +
		"      1. Pick a start date\n" +
		"2. Download the file\n\n" +
		"| Field | Type |\n| --- | --- |\n| invoice_id | text |\n\n" +
		"Tabbed\ttext\n"
	if got := Render(blocks); got != want {
		t.Errorf("Render =\n%q\nwant\n%q", got, want)
	}
	if Render(blocks) != Render(blocks) {
		t.Error("Render is not deterministic")
	}
	if Render(nil) != "" {
		t.Error("Render(nil) must be empty")
	}
}

func TestIngestHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := styledNumberedDOCX(t)
	_, _, err := IngestWithOptions(ctx, "styled-numbered.docx", bytes.NewReader(data), int64(len(data)), Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
