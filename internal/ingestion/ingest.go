//go:build client && !server

// Package ingestion turns uploaded requirement documents into explicit,
// provenance-carrying blocks and then into a normalised product/technical
// split. It parses .docx, .pdf, .md and .txt entirely in-process, bounds every
// untrusted input, and never calls a cloud service: scanned PDF pages are
// recognised only through locally installed pdftoppm and tesseract.
package ingestion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Document formats reported in IngestReport.Format.
const (
	FormatDOCX     = "docx"
	FormatPDF      = "pdf"
	FormatMarkdown = "markdown"
	FormatText     = "text"
)

var (
	zipMagic = []byte("PK\x03\x04")
	pdfMagic = []byte("%PDF-")
	// oleMagic is the OLE compound file signature. Word writes password
	// protected .docx files as an OLE container wrapping an encrypted package.
	oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
)

// IngestReport describes one ingestion run. It carries everything the UI needs
// to explain what was and was not recovered, without exposing document text.
type IngestReport struct {
	// SourceID identifies the document in every block's SourceRef.
	SourceID string `json:"source_id"`
	// Name is the base file name the caller supplied. It is informational and is
	// never used to build a path.
	Name string `json:"name"`
	// Format is one of the Format constants.
	Format string `json:"format"`
	// Bytes is the input size.
	Bytes int64 `json:"bytes"`
	// SHA256 is the hex digest of the input bytes.
	SHA256 string `json:"sha256"`
	// Pages is the page count for paged formats and zero otherwise.
	Pages int `json:"pages,omitempty"`
	// Blocks is the number of blocks produced.
	Blocks int `json:"blocks"`
	// Warnings lists extraction approximations and omissions in reading order.
	Warnings []string `json:"warnings,omitempty"`
	// ScannedPages lists PDF pages that carried too little text to parse.
	ScannedPages []int `json:"scanned_pages,omitempty"`
	// OCR records the tools and settings used when OCR recovered any page.
	OCR *OCRProvenance `json:"ocr,omitempty"`
}

// Ingest parses one document into blocks using the command-line limits. It
// routes by extension and by magic bytes, and returns contracts.ErrUnsupportedFormat
// when the two disagree, contracts.ErrEncrypted for password protected input,
// and a *contracts.ErrBound when a size, page, entry, ratio or nesting limit is
// exceeded. It never falls back to treating unknown bytes as plain text.
func Ingest(ctx context.Context, name string, r io.ReaderAt, size int64) ([]contracts.DocumentBlock, IngestReport, error) {
	return IngestWithOptions(ctx, name, r, size, CLIOptions())
}

// IngestWithOptions is Ingest with explicit limits, used by the UI upload path
// (UIOptions) and by tests.
func IngestWithOptions(ctx context.Context, name string, r io.ReaderAt, size int64, opts Options) ([]contracts.DocumentBlock, IngestReport, error) {
	rep := IngestReport{Name: baseName(name), Bytes: size}
	if err := ctx.Err(); err != nil {
		return nil, rep, err
	}
	if r == nil || size < 0 {
		return nil, rep, fmt.Errorf("%w: no input", ErrCorrupt)
	}
	if limit := opts.fileLimit(); size > limit {
		return nil, rep, newBound(BoundFileBytes, limit, size)
	}

	head := make([]byte, 8)
	n, err := r.ReadAt(head, 0)
	if err != nil && err != io.EOF {
		return nil, rep, fmt.Errorf("%w: read header: %v", ErrCorrupt, err)
	}
	head = head[:n]

	format, err := routeFormat(rep.Name, head)
	if err != nil {
		return nil, rep, err
	}
	rep.Format = format

	sum, err := hashReaderAt(ctx, r, size)
	if err != nil {
		return nil, rep, err
	}
	rep.SHA256 = sum
	rep.SourceID = "src-" + sum[:12]

	var blocks []contracts.DocumentBlock
	switch format {
	case FormatDOCX:
		blocks, err = parseDOCX(ctx, r, size, &rep)
	case FormatPDF:
		blocks, err = parsePDF(ctx, r, size, &rep, opts)
	default:
		blocks, err = parseText(ctx, r, size, format, &rep)
	}
	if err != nil {
		return nil, rep, err
	}
	if blocks == nil {
		blocks = []contracts.DocumentBlock{}
	}
	rep.Blocks = len(blocks)
	return blocks, rep, nil
}

// routeFormat decides the format from the extension and confirms it against the
// leading bytes. A mismatch is ErrUnsupportedFormat, except an OLE container
// behind a .docx name, which is a password protected Word document.
func routeFormat(name string, head []byte) (string, error) {
	ext := strings.ToLower(path.Ext(name))
	isZip := bytes.HasPrefix(head, zipMagic)
	isPDF := bytes.HasPrefix(head, pdfMagic)
	isOLE := bytes.HasPrefix(head, oleMagic[:4])

	switch ext {
	case ".docx":
		switch {
		case isOLE:
			return "", contracts.ErrEncrypted
		case isZip:
			return FormatDOCX, nil
		}
		return "", fmt.Errorf("%w: %q is not a DOCX package", contracts.ErrUnsupportedFormat, name)
	case ".pdf":
		if isPDF {
			return FormatPDF, nil
		}
		return "", fmt.Errorf("%w: %q does not start with %%PDF-", contracts.ErrUnsupportedFormat, name)
	case ".md", ".markdown", ".txt":
		if isZip || isPDF || isOLE {
			return "", fmt.Errorf("%w: %q has binary content for a text extension", contracts.ErrUnsupportedFormat, name)
		}
		if ext == ".txt" {
			return FormatText, nil
		}
		return FormatMarkdown, nil
	case "":
		return "", fmt.Errorf("%w: %q has no extension; supported types are .docx, .pdf, .md, .txt", contracts.ErrUnsupportedFormat, name)
	}
	return "", fmt.Errorf("%w: %q; supported types are .docx, .pdf, .md, .txt", contracts.ErrUnsupportedFormat, ext)
}

// baseName strips directories of either separator style from a caller-supplied
// name. Uploaded names arrive from browsers and may use Windows separators.
func baseName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	return name
}

func hashReaderAt(ctx context.Context, r io.ReaderAt, size int64) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, &ctxReader{ctx: ctx, r: io.NewSectionReader(r, 0, size)}); err != nil {
		return "", fmt.Errorf("%w: hash input: %v", ErrCorrupt, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ctxReader aborts a long copy when the context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func hexSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// blockBuilder assigns block IDs and source references in reading order and
// tracks the most recent heading so every block records where it sits.
type blockBuilder struct {
	sourceID string
	heading  string
	blocks   []contracts.DocumentBlock
}

func newBlockBuilder(sourceID string) *blockBuilder { return &blockBuilder{sourceID: sourceID} }

func (b *blockBuilder) add(blk contracts.DocumentBlock, page int) {
	id := fmt.Sprintf("%s:b%04d", b.sourceID, len(b.blocks)+1)
	if blk.Kind == contracts.BlockHeading {
		b.heading = blk.Text
	}
	digest := blk.Text
	if len(blk.Rows) > 0 {
		var sb strings.Builder
		for _, row := range blk.Rows {
			sb.WriteString(strings.Join(row, "\x1f"))
			sb.WriteString("\x1e")
		}
		digest = sb.String()
	}
	blk.ID = id
	blk.Source = contracts.SourceRef{
		SourceID: b.sourceID,
		BlockID:  id,
		Page:     page,
		Heading:  b.heading,
		SHA256:   hexSHA256(digest),
	}
	b.blocks = append(b.blocks, blk)
}
