//go:build client && !server

package ingestion

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// utf8BOM is stripped from the start of text input.
const utf8BOM = "\xEF\xBB\xBF"

// parseText reads a Markdown or plain-text document. Markdown is split at ATX
// headings so each section keeps its heading as provenance; the text between
// headings is preserved verbatim, because Markdown is already the target
// format and re-parsing it would only lose information. Plain text is split
// into paragraphs at blank lines. Input must be valid UTF-8 without NUL bytes.
func parseText(ctx context.Context, r io.ReaderAt, size int64, format string, rep *IngestReport) ([]contracts.DocumentBlock, error) {
	data := make([]byte, size)
	if _, err := io.ReadFull(&ctxReader{ctx: ctx, r: io.NewSectionReader(r, 0, size)}, data); err != nil {
		return nil, fmt.Errorf("%w: read text: %v", ErrCorrupt, err)
	}
	text, err := decodeText(data)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		rep.Warnings = append(rep.Warnings, "document is empty")
		return nil, nil
	}

	bb := newBlockBuilder(rep.SourceID)
	if format == FormatText {
		for _, para := range splitParagraphs(text) {
			bb.add(contracts.DocumentBlock{Kind: contracts.BlockParagraph, Text: para, Confidence: contracts.ConfidenceHigh}, 0)
		}
		return bb.blocks, nil
	}
	for _, sec := range splitMarkdown(text) {
		if sec.heading {
			bb.add(contracts.DocumentBlock{Kind: contracts.BlockHeading, Level: sec.level, Text: sec.text, Confidence: contracts.ConfidenceHigh}, 0)
			continue
		}
		bb.add(contracts.DocumentBlock{Kind: contracts.BlockParagraph, Text: sec.text, Confidence: contracts.ConfidenceHigh}, 0)
	}
	return bb.blocks, nil
}

// decodeText strips a BOM, rejects invalid UTF-8 and NUL bytes, and normalises
// line endings to LF.
func decodeText(data []byte) (string, error) {
	s := strings.TrimPrefix(string(data), utf8BOM)
	if !utf8.ValidString(s) {
		off := 0
		for off < len(s) {
			r, w := utf8.DecodeRuneInString(s[off:])
			if r == utf8.RuneError && w == 1 {
				break
			}
			off += w
		}
		return "", fmt.Errorf("%w: text is not valid UTF-8 (first invalid byte at offset %d)", ErrCorrupt, off)
	}
	if strings.IndexByte(s, 0) >= 0 {
		return "", fmt.Errorf("%w: %v", contracts.ErrUnsupportedFormat, "text contains NUL bytes and looks binary")
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s, nil
}

// splitParagraphs splits plain text at blank lines.
func splitParagraphs(text string) []string {
	var out []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		cur = append(cur, strings.TrimRight(line, " \t"))
	}
	flush()
	return out
}

type mdSection struct {
	heading bool
	level   int
	text    string
}

// splitMarkdown separates ATX headings from the verbatim text between them.
// Headings inside fenced code blocks are not headings.
func splitMarkdown(text string) []mdSection {
	var out []mdSection
	var cur []string
	flush := func() {
		body := strings.Trim(strings.Join(cur, "\n"), "\n")
		body = strings.TrimRight(body, " \t")
		if strings.TrimSpace(body) != "" {
			out = append(out, mdSection{text: body})
		}
		cur = nil
	}

	var fenceChar byte
	fenceLen := 0
	for _, line := range strings.Split(text, "\n") {
		if fenceLen > 0 {
			cur = append(cur, line)
			if c, n, rest := fenceOf(line); n >= fenceLen && c == fenceChar && strings.TrimSpace(rest) == "" {
				fenceLen = 0
			}
			continue
		}
		if c, n, _ := fenceOf(line); n >= 3 {
			fenceChar, fenceLen = c, n
			cur = append(cur, line)
			continue
		}
		if level, title, ok := atxHeading(line); ok {
			flush()
			out = append(out, mdSection{heading: true, level: level, text: title})
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return out
}

// fenceOf reports a code fence marker: its character, its length and the text
// after it. The length is zero when the line is not a fence.
func fenceOf(line string) (char byte, n int, rest string) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || trimmed == "" {
		return 0, 0, ""
	}
	c := trimmed[0]
	if c != '`' && c != '~' {
		return 0, 0, ""
	}
	for n < len(trimmed) && trimmed[n] == c {
		n++
	}
	if n < 3 {
		return 0, 0, ""
	}
	rest = trimmed[n:]
	if c == '`' && strings.Contains(rest, "`") {
		return 0, 0, ""
	}
	return c, n, rest
}

// atxHeading parses a `# Title` line of up to six hashes with at most three
// leading spaces, ignoring an optional closing hash sequence.
func atxHeading(line string) (level int, title string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return 0, "", false
	}
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level > 6 {
		return 0, "", false
	}
	rest := trimmed[level:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0, "", false
	}
	rest = strings.TrimSpace(rest)
	if i := strings.LastIndexAny(rest, " \t"); i >= 0 && strings.Trim(rest[i+1:], "#") == "" {
		rest = strings.TrimSpace(rest[:i])
	} else if strings.Trim(rest, "#") == "" {
		rest = ""
	}
	return level, rest, true
}
