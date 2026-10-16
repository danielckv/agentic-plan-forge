//go:build client && !server

package ingestion

import (
	"errors"
	"io"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Limits enforced by the parsers. Every limit is reported through
// *contracts.ErrBound so callers can show the limit and the observed value.
const (
	// MaxFileBytesUI is the largest document the UI accepts.
	MaxFileBytesUI int64 = 25 << 20
	// MaxFileBytesCLI is the largest document the command line accepts.
	MaxFileBytesCLI int64 = 50 << 20
	// MaxPDFPages is the largest page count a PDF may declare.
	MaxPDFPages = 500
	// MaxDOCXEntries is the largest number of entries a DOCX package may hold.
	MaxDOCXEntries = 10000
	// MaxDOCXUncompressedBytes is the largest total declared and decompressed
	// size of a DOCX package.
	MaxDOCXUncompressedBytes int64 = 256 << 20
	// MaxDOCXCompressionRatio is the largest per-entry uncompressed to
	// compressed ratio, the zip-bomb guard.
	MaxDOCXCompressionRatio int64 = 100
	// MaxXMLTokens is the largest number of tokens read from one XML part.
	MaxXMLTokens = 1_000_000
	// MaxXMLDepth is the deepest element nesting accepted in one XML part.
	MaxXMLDepth = 256
)

// Bound names carried in ErrBound.Name.
const (
	BoundFileBytes        = "file_bytes"
	BoundPDFPages         = "pdf_pages"
	BoundDOCXEntries      = "docx_entries"
	BoundDOCXUncompressed = "docx_uncompressed_bytes"
	BoundDOCXRatio        = "docx_compression_ratio"
	BoundXMLTokens        = "xml_tokens"
	BoundXMLDepth         = "xml_depth"
)

// OCR and scanned-page tuning shared by the PDF parser and the OCR driver.
const (
	// ScannedPageGlyphThreshold is the glyph count below which a PDF page is
	// treated as scanned and handed to OCR.
	ScannedPageGlyphThreshold = 10
	// OCRDPI is the rasterisation resolution passed to pdftoppm.
	OCRDPI = 200
	// OCRPageTimeout bounds rasterisation plus recognition of one page.
	OCRPageTimeout = 60 * time.Second
	// OCRUnverifiableConfidence is the mean OCR confidence (0-100) below which
	// recovered text is marked UNVERIFIABLE for documentation-drift purposes.
	OCRUnverifiableConfidence = 60.0
)

// ErrCorrupt reports a document that matches a supported format by signature
// but cannot be parsed: a damaged archive, malformed XML, a broken PDF or text
// that is not valid UTF-8. Callers distinguish it from ErrUnsupportedFormat so
// the UI can say "corrupt" rather than "unsupported".
var ErrCorrupt = errors.New("document is corrupt")

// Options tunes one ingestion call.
type Options struct {
	// MaxFileBytes is the largest accepted input. Zero selects MaxFileBytesCLI.
	MaxFileBytes int64
	// OCRLanguage is the tesseract language code. Empty selects "eng".
	OCRLanguage string
	// DisableOCR skips OCR even when pdftoppm and tesseract are installed.
	DisableOCR bool

	// ocr overrides the OCR driver. Tests inject a fake; production leaves it
	// nil and the driver resolves the real tools.
	ocr *ocrEngine
}

// UIOptions returns the limits the UI upload path uses.
func UIOptions() Options { return Options{MaxFileBytes: MaxFileBytesUI} }

// CLIOptions returns the limits the command line uses.
func CLIOptions() Options { return Options{MaxFileBytes: MaxFileBytesCLI} }

func (o Options) fileLimit() int64 {
	if o.MaxFileBytes <= 0 {
		return MaxFileBytesCLI
	}
	return o.MaxFileBytes
}

func (o Options) language() string {
	if o.OCRLanguage == "" {
		return "eng"
	}
	return o.OCRLanguage
}

func newBound(name string, limit, observed int64) *contracts.ErrBound {
	return &contracts.ErrBound{Name: name, Limit: limit, Observed: observed}
}

// budget tracks the bytes decompressed across every part of one package.
type budget struct {
	limit     int64
	remaining int64
}

func newBudget(limit int64) *budget { return &budget{limit: limit, remaining: limit} }

// budgetReader fails with ErrBound once the shared budget is exhausted, so a
// package that lies about its declared sizes still cannot expand without
// bound.
type budgetReader struct {
	r io.Reader
	b *budget
}

func (br *budgetReader) Read(p []byte) (int, error) {
	n, err := br.r.Read(p)
	br.b.remaining -= int64(n)
	if br.b.remaining < 0 {
		return n, newBound(BoundDOCXUncompressed, br.b.limit, br.b.limit-br.b.remaining)
	}
	return n, err
}
