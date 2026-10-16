//go:build client && !server

package ingestion

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// WordprocessingML namespaces. Word writes the transitional namespace; the ISO
// strict variant is accepted too.
const (
	nsW       = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsWStrict = "http://purl.oclc.org/ooxml/wordprocessingml/main"
	nsMC      = "http://schemas.openxmlformats.org/markup-compatibility/2006"
)

func isW(n xml.Name) bool { return n.Space == nsW || n.Space == nsWStrict }

func isMCFallback(n xml.Name) bool { return n.Space == nsMC && n.Local == "Fallback" }

// xmlStream wraps encoding/xml with the token and nesting caps. encoding/xml
// never resolves external entities, and DTDs are refused outright, so the only
// resource risks left are size and depth, which this type bounds.
type xmlStream struct {
	ctx    context.Context
	dec    *xml.Decoder
	tokens int
	depth  int
}

func newXMLStream(ctx context.Context, r io.Reader) *xmlStream {
	d := xml.NewDecoder(r)
	d.Strict = true
	return &xmlStream{ctx: ctx, dec: d}
}

// next returns the next token. CharData bytes are copied, so callers may keep
// them across calls.
func (s *xmlStream) next() (xml.Token, error) {
	if s.tokens&0x3ff == 0 {
		if err := s.ctx.Err(); err != nil {
			return nil, err
		}
	}
	tok, err := s.dec.Token()
	if err != nil {
		return nil, err
	}
	s.tokens++
	if s.tokens > MaxXMLTokens {
		return nil, newBound(BoundXMLTokens, MaxXMLTokens, int64(s.tokens))
	}
	switch t := tok.(type) {
	case xml.StartElement:
		s.depth++
		if s.depth > MaxXMLDepth {
			return nil, newBound(BoundXMLDepth, MaxXMLDepth, int64(s.depth))
		}
	case xml.EndElement:
		s.depth--
	case xml.CharData:
		return xml.CharData(append([]byte(nil), t...)), nil
	case xml.Directive:
		return nil, fmt.Errorf("%w: XML directives are not allowed", ErrCorrupt)
	}
	return tok, nil
}

// skip consumes the remainder of the element whose start tag was just read.
func (s *xmlStream) skip() error {
	d0 := s.depth
	for {
		tok, err := s.next()
		if err != nil {
			return err
		}
		if _, ok := tok.(xml.EndElement); ok && s.depth < d0 {
			return nil
		}
	}
}

// text consumes the element whose start tag was just read and returns its
// character data. Nested elements are skipped.
func (s *xmlStream) text() (string, error) {
	d0 := s.depth
	var out []byte
	for {
		tok, err := s.next()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			out = append(out, t...)
		case xml.StartElement:
			if err := s.skip(); err != nil {
				return "", err
			}
		case xml.EndElement:
			if s.depth < d0 {
				return string(out), nil
			}
		}
	}
}

// walk visits the start tags below the element whose start tag was just read.
// A handler consumes the elements it recognises (reading through the matching
// end tag, usually with skip or text); for any element it leaves untouched the
// walker simply keeps reading, so wrappers such as hyperlinks and content
// controls are transparent.
func (s *xmlStream) walk(fn func(xml.StartElement) error) error {
	d0 := s.depth
	for {
		tok, err := s.next()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if err := fn(t); err != nil {
				return err
			}
		case xml.EndElement:
			if s.depth < d0 {
				return nil
			}
		}
	}
}

// attr returns the value of the attribute with the given local name.
func attr(st xml.StartElement, local string) string {
	for _, a := range st.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// classifyXMLErr maps a decoder failure to the package's typed errors: bound
// violations and context cancellation pass through unchanged, everything else
// is corruption.
func classifyXMLErr(part string, err error) error {
	var b *contracts.ErrBound
	switch {
	case errors.As(err, &b):
		return b
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, ErrCorrupt):
		return fmt.Errorf("%w (%s)", err, part)
	}
	return fmt.Errorf("%w: %s: %v", ErrCorrupt, part, err)
}
