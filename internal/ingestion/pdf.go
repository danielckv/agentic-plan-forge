//go:build client && !server

package ingestion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Heuristic constants for born-digital PDF layout recovery.
const (
	// pdfHeadingRatio is how much larger than the median body size a line must
	// be to count as a heading.
	pdfHeadingRatio = 1.2
	// pdfLineTolerance is the share of the font size within which two glyphs
	// share a line.
	pdfLineTolerance = 0.35
	// pdfParagraphGap is the largest line pitch, in font sizes, that still
	// continues a paragraph.
	pdfParagraphGap = 1.7
	// pdfMaxHeadingRunes keeps long large-type passages out of the heading class.
	pdfMaxHeadingRunes = 200
	// pdfMaxGlyphsPerPage bounds work on pathological pages.
	pdfMaxGlyphsPerPage = 1_000_000
	// pdfIndentStep is the horizontal distance, in points, between list levels.
	pdfIndentStep = 18.0
	// pdfMaxSearchLines is the line count above which glyph-to-line assignment
	// only compares against the previous line, keeping the work linear.
	pdfMaxSearchLines = 400
)

// listMarkerPattern recognises bullet and decimal list markers at the start of
// a line. A marker must be followed by whitespace, so "3.14" is not a list.
var listMarkerPattern = regexp.MustCompile(`^(?:([•◦▪▫●○■□·*]|[-–—])|([0-9]{1,3})[.)])\s+(.+)$`)

// pdfLine is one visual line of text on a page.
type pdfLine struct {
	text     string
	x0, x1   float64
	y        float64
	size     float64
	segments []float64
}

// pdfPage is the layout recovered from one page.
type pdfPage struct {
	number   int
	lines    []pdfLine
	scanned  bool
	unread   error
	multiCol bool
}

// parsePDF extracts blocks from a born-digital PDF and routes scanned pages to
// OCR when pdftoppm and tesseract are installed. The third-party reader
// panics on malformed input, so every call into it is wrapped.
func parsePDF(ctx context.Context, r io.ReaderAt, size int64, rep *IngestReport, opts Options) (blocks []contracts.DocumentBlock, err error) {
	defer func() {
		if x := recover(); x != nil {
			blocks, err = nil, fmt.Errorf("%w: pdf parser failed: %v", ErrCorrupt, x)
		}
	}()

	rd, err := pdf.NewReader(r, size)
	if err != nil {
		return nil, classifyPDFOpenError(err)
	}
	if !rd.Trailer().Key("Encrypt").IsNull() {
		return nil, contracts.ErrEncrypted
	}
	pageCount := rd.NumPage()
	if pageCount > MaxPDFPages {
		return nil, newBound(BoundPDFPages, MaxPDFPages, int64(pageCount))
	}
	rep.Pages = pageCount

	pages := make([]pdfPage, 0, pageCount)
	hist := map[int]int{}
	for n := 1; n <= pageCount; n++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page := pdfPage{number: n}
		glyphs, perr := pageGlyphs(rd, n)
		switch {
		case perr != nil:
			page.unread = perr
		default:
			if len(glyphs) > pdfMaxGlyphsPerPage {
				glyphs = glyphs[:pdfMaxGlyphsPerPage]
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("page %d: text truncated at %d glyphs", n, pdfMaxGlyphsPerPage))
			}
			if countGlyphs(glyphs) < ScannedPageGlyphThreshold {
				page.scanned = true
			} else {
				page.lines = buildLines(glyphs)
				page.multiCol = multiColumn(page.lines)
				for _, ln := range page.lines {
					hist[int(math.Round(ln.size*10))] += utf8.RuneCountInString(ln.text)
				}
			}
		}
		pages = append(pages, page)
	}

	median := weightedMedian(hist)
	layout := newPDFLayout(rep, median, pages)

	var eng *ocrEngine
	if !opts.DisableOCR {
		eng = opts.ocr
		if eng == nil {
			eng = newOCREngine(opts.language())
		}
	}
	var sess *ocrSession
	defer func() {
		if sess != nil {
			sess.Close()
		}
	}()

	for i := range pages {
		page := &pages[i]
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch {
		case page.unread != nil:
			msg := fmt.Sprintf("UNREADABLE_PAGE: page %d could not be read: %v", page.number, page.unread)
			rep.Warnings = append(rep.Warnings, msg)
			layout.placeholder(page.number, msg)
		case page.scanned:
			rep.ScannedPages = append(rep.ScannedPages, page.number)
			if eng == nil {
				msg := fmt.Sprintf("UNPARSED_SCANNED: page %d has no extractable text and OCR is disabled", page.number)
				rep.Warnings = append(rep.Warnings, msg)
				layout.placeholder(page.number, msg)
				continue
			}
			if sess == nil {
				if _, _, ok := eng.tools(); !ok {
					msg := fmt.Sprintf("UNPARSED_SCANNED: page %d has no extractable text and OCR tools are missing; %s", page.number, OCRInstallGuidance)
					rep.Warnings = append(rep.Warnings, msg)
					layout.placeholder(page.number, msg)
					continue
				}
				if !ocrLanguagePattern.MatchString(eng.language) || len(eng.language) > 64 {
					return nil, fmt.Errorf("invalid OCR language %q", eng.language)
				}
				s, serr := eng.newSession(ctx, r, size)
				if serr != nil {
					msg := fmt.Sprintf("OCR_FAILED: page %d: %v", page.number, serr)
					rep.Warnings = append(rep.Warnings, msg)
					layout.placeholder(page.number, msg)
					continue
				}
				sess = s
			}
			paras, oerr := sess.page(ctx, page.number)
			if oerr != nil {
				if cerr := ctx.Err(); cerr != nil {
					return nil, cerr
				}
				msg := fmt.Sprintf("OCR_FAILED: %v", oerr)
				rep.Warnings = append(rep.Warnings, msg)
				layout.placeholder(page.number, msg)
				continue
			}
			if len(paras) == 0 {
				msg := fmt.Sprintf("OCR_FAILED: page %d: no text recognised", page.number)
				rep.Warnings = append(rep.Warnings, msg)
				layout.placeholder(page.number, msg)
				continue
			}
			layout.ocrBlocks(page.number, paras)
		default:
			layout.page(page)
		}
	}
	if sess != nil {
		prov := sess.provenance()
		rep.OCR = &prov
	}
	if len(layout.bb.blocks) == 0 {
		rep.Warnings = append(rep.Warnings, "document contains no text")
	}
	return layout.bb.blocks, nil
}

// classifyPDFOpenError maps reader failures to typed errors. A password
// requirement or an unsupported encryption scheme is ErrEncrypted.
func classifyPDFOpenError(err error) error {
	if errors.Is(err, pdf.ErrInvalidPassword) || strings.Contains(strings.ToLower(err.Error()), "encrypt") {
		return contracts.ErrEncrypted
	}
	return fmt.Errorf("%w: %v", ErrCorrupt, err)
}

// pageGlyphs returns the positioned glyphs of one page, recovering from the
// reader's internal panics.
func pageGlyphs(rd *pdf.Reader, n int) (glyphs []pdf.Text, err error) {
	defer func() {
		if x := recover(); x != nil {
			glyphs, err = nil, fmt.Errorf("%v", x)
		}
	}()
	p := rd.Page(n)
	if p.V.IsNull() {
		return nil, nil
	}
	return p.Content().Text, nil
}

// countGlyphs counts the non-space characters on a page.
func countGlyphs(glyphs []pdf.Text) int {
	n := 0
	for _, g := range glyphs {
		for _, r := range g.S {
			if !unicode.IsSpace(r) {
				n++
			}
		}
	}
	return n
}

func glyphSize(g pdf.Text) float64 {
	if g.FontSize > 0 && !math.IsNaN(g.FontSize) && !math.IsInf(g.FontSize, 0) {
		return g.FontSize
	}
	return 10
}

// buildLines groups glyphs into lines by vertical position, orders each line
// left to right, and reconstructs word and column gaps from glyph advances.
// Glyph order within a line is stable, so text from a font without width data
// keeps the order of the content stream.
func buildLines(glyphs []pdf.Text) []pdfLine {
	type acc struct {
		y float64
		g []pdf.Text
	}
	var accs []*acc
	var last *acc
	for _, g := range glyphs {
		if g.S == "" {
			continue
		}
		tol := pdfLineTolerance * glyphSize(g)
		var a *acc
		switch {
		case last != nil && math.Abs(last.y-g.Y) <= tol:
			a = last
		case len(accs) <= pdfMaxSearchLines:
			for _, c := range accs {
				if math.Abs(c.y-g.Y) <= tol {
					a = c
					break
				}
			}
		}
		if a == nil {
			a = &acc{y: g.Y}
			accs = append(accs, a)
		}
		a.g = append(a.g, g)
		last = a
	}

	lines := make([]pdfLine, 0, len(accs))
	for _, a := range accs {
		sort.SliceStable(a.g, func(i, j int) bool { return a.g[i].X < a.g[j].X })
		if ln, ok := lineFromGlyphs(a.g, a.y); ok {
			lines = append(lines, ln)
		}
	}
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].y != lines[j].y {
			return lines[i].y > lines[j].y
		}
		return lines[i].x0 < lines[j].x0
	})
	return lines
}

func lineFromGlyphs(g []pdf.Text, y float64) (pdfLine, bool) {
	var sb strings.Builder
	var segs []float64
	sizeWeight := map[int]int{}
	x0, x1 := g[0].X, g[0].X+g[0].W
	var prev *pdf.Text
	for i := range g {
		cur := &g[i]
		if prev != nil {
			if cur.S == prev.S && math.Abs(cur.X-prev.X) < 0.5 && math.Abs(cur.Y-prev.Y) < 0.5 {
				continue // the same glyph drawn twice to fake bold
			}
			word, col := glyphGap(prev, cur)
			if col {
				segs = append(segs, cur.X)
			}
			if word || col {
				sb.WriteByte(' ')
			}
		} else {
			segs = append(segs, cur.X)
		}
		sb.WriteString(cur.S)
		if cur.X < x0 {
			x0 = cur.X
		}
		if end := cur.X + cur.W; end > x1 {
			x1 = end
		}
		if strings.TrimSpace(cur.S) != "" {
			sizeWeight[int(math.Round(glyphSize(*cur)*10))] += utf8.RuneCountInString(cur.S)
		}
		prev = cur
	}
	text := strings.Join(strings.Fields(sb.String()), " ")
	if text == "" {
		return pdfLine{}, false
	}
	bestKey, bestCount := 0, -1
	for k, c := range sizeWeight {
		if c > bestCount || (c == bestCount && k > bestKey) {
			bestKey, bestCount = k, c
		}
	}
	size := float64(bestKey) / 10
	if size <= 0 {
		size = glyphSize(g[0])
	}
	return pdfLine{text: text, x0: x0, x1: x1, y: y, size: size, segments: segs}, true
}

// glyphGap classifies the space between two consecutive glyphs. When the font
// has no width data the reader reports zero advances, so a changed position is
// treated as a word boundary and never as a column boundary.
func glyphGap(p, c *pdf.Text) (word, col bool) {
	size := glyphSize(*c)
	if p.W <= 0 {
		return math.Abs(c.X-p.X) > 0.01, false
	}
	gap := c.X - (p.X + p.W)
	return gap > 0.2*size, gap >= math.Max(24, 2.5*size)
}

// multiColumn reports whether a page shows two or more distinct column starts
// across several gap-separated lines.
func multiColumn(lines []pdfLine) bool {
	columnar := 0
	var starts []float64
	for _, l := range lines {
		if len(l.segments) >= 2 {
			columnar++
			starts = append(starts, l.segments...)
		}
	}
	if columnar < 3 {
		return false
	}
	sort.Float64s(starts)
	clusters := 1
	for i := 1; i < len(starts); i++ {
		if starts[i]-starts[i-1] > 15 {
			clusters++
		}
	}
	return clusters >= 2
}

// weightedMedian returns the median font size, weighted by character count,
// from a histogram keyed by size in tenths of a point. Zero means no usable
// font sizes were found.
func weightedMedian(hist map[int]int) float64 {
	total := 0
	keys := make([]int, 0, len(hist))
	for k, c := range hist {
		total += c
		keys = append(keys, k)
	}
	if total == 0 {
		return 0
	}
	sort.Ints(keys)
	half := (total + 1) / 2
	cum := 0
	for _, k := range keys {
		cum += hist[k]
		if cum >= half {
			return float64(k) / 10
		}
	}
	return 0
}

// pdfLayout turns page lines into blocks.
type pdfLayout struct {
	rep          *IngestReport
	bb           *blockBuilder
	median       float64
	usable       bool
	headingSizes []float64 // distinct heading sizes, largest first
	acc          *pdfAcc
	curPage      int
	pageWarn     []string
	base         float64
	warned       map[string]bool
}

// pdfAcc accumulates the lines of one block until it is flushed.
type pdfAcc struct {
	kind    string
	level   int
	marker  string
	ordinal string
	text    []string
	x0, y   float64
	size    float64
}

func newPDFLayout(rep *IngestReport, median float64, pages []pdfPage) *pdfLayout {
	l := &pdfLayout{
		rep:    rep,
		bb:     newBlockBuilder(rep.SourceID),
		median: median,
		usable: median > 0,
		warned: map[string]bool{},
	}
	if !l.usable {
		return l
	}
	seen := map[float64]bool{}
	for _, p := range pages {
		for _, ln := range p.lines {
			if l.isHeadingLine(ln) {
				s := roundHalf(ln.size)
				if !seen[s] {
					seen[s] = true
					l.headingSizes = append(l.headingSizes, s)
				}
			}
		}
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(l.headingSizes)))
	return l
}

func roundHalf(v float64) float64 { return math.Round(v*2) / 2 }

func (l *pdfLayout) confidence() string {
	if l.usable {
		return contracts.ConfidenceHigh
	}
	return contracts.ConfidenceMedium
}

func (l *pdfLayout) isHeadingLine(ln pdfLine) bool {
	return l.usable && ln.size >= pdfHeadingRatio*l.median && utf8.RuneCountInString(ln.text) <= pdfMaxHeadingRunes
}

func (l *pdfLayout) headingLevel(size float64) int {
	s := roundHalf(size)
	for i, h := range l.headingSizes {
		if h == s {
			if i+1 > 6 {
				return 6
			}
			return i + 1
		}
	}
	return 1
}

// placeholder records a page that could not be turned into text.
func (l *pdfLayout) placeholder(page int, warning string) {
	l.flush()
	l.bb.add(contracts.DocumentBlock{
		Kind:       contracts.BlockParagraph,
		Confidence: contracts.ConfidenceUnverifiable,
		Warnings:   []string{warning},
	}, page)
}

// ocrBlocks adds the paragraphs recognised on one scanned page.
func (l *pdfLayout) ocrBlocks(page int, paras []ocrParagraph) {
	l.flush()
	for _, p := range paras {
		warn := fmt.Sprintf("OCR text; mean word confidence %.1f", p.conf)
		l.bb.add(contracts.DocumentBlock{
			Kind:       contracts.BlockParagraph,
			Text:       p.text,
			Confidence: ocrConfidenceTier(p.conf),
			Warnings:   []string{warn},
		}, page)
	}
}

// page lays out one text page.
func (l *pdfLayout) page(p *pdfPage) {
	l.flush()
	l.curPage = p.number
	l.pageWarn = nil
	if p.multiCol {
		msg := fmt.Sprintf("page %d: multi-column layout detected; reading order is approximate", p.number)
		l.pageWarn = []string{msg}
		l.rep.Warnings = append(l.rep.Warnings, msg)
	}
	if !l.usable && !l.warned["sizes"] {
		l.warned["sizes"] = true
		l.rep.Warnings = append(l.rep.Warnings, "no usable font sizes; headings could not be inferred")
	}
	l.base = math.Inf(1)
	for _, ln := range p.lines {
		if ln.x0 < l.base {
			l.base = ln.x0
		}
	}

	for _, ln := range p.lines {
		if l.isHeadingLine(ln) {
			lvl := l.headingLevel(ln.size)
			if a := l.acc; a != nil && a.kind == contracts.BlockHeading && a.level == lvl && a.y-ln.y <= pdfParagraphGap*ln.size {
				a.text = append(a.text, ln.text)
				a.y = ln.y
				continue
			}
			l.flush()
			l.acc = &pdfAcc{kind: contracts.BlockHeading, level: lvl, text: []string{ln.text}, x0: ln.x0, y: ln.y, size: ln.size}
			continue
		}
		if m := listMarkerPattern.FindStringSubmatch(ln.text); m != nil {
			l.flush()
			a := &pdfAcc{kind: contracts.BlockList, text: []string{m[3]}, x0: ln.x0, y: ln.y, size: ln.size}
			a.level = 1 + int(math.Round((ln.x0-l.base)/pdfIndentStep))
			if a.level < 1 {
				a.level = 1
			}
			if a.level > 6 {
				a.level = 6
			}
			if m[2] != "" {
				a.marker, a.ordinal = ListDecimal, m[2]
			} else {
				a.marker = ListBullet
			}
			l.acc = a
			continue
		}
		if a := l.acc; a != nil && l.continues(a, ln) {
			a.text = append(a.text, ln.text)
			a.y = ln.y
			continue
		}
		l.flush()
		l.acc = &pdfAcc{kind: contracts.BlockParagraph, text: []string{ln.text}, x0: ln.x0, y: ln.y, size: ln.size}
	}
	l.flush()
}

// continues reports whether a line extends the open paragraph or list item.
func (l *pdfLayout) continues(a *pdfAcc, ln pdfLine) bool {
	if a.kind == contracts.BlockHeading {
		return false
	}
	if a.y-ln.y > pdfParagraphGap*math.Max(a.size, ln.size) || a.y-ln.y < 0 {
		return false
	}
	if math.Abs(a.size-ln.size) > 0.1*math.Max(a.size, ln.size) {
		return false
	}
	if a.kind == contracts.BlockList && ln.x0 <= a.x0+2 {
		return false
	}
	return true
}

// flush emits the open accumulator as a block.
func (l *pdfLayout) flush() {
	a := l.acc
	l.acc = nil
	if a == nil {
		return
	}
	text := strings.Join(strings.Fields(strings.Join(a.text, " ")), " ")
	if text == "" {
		return
	}
	blk := contracts.DocumentBlock{
		Kind:       a.kind,
		Text:       text,
		Confidence: l.confidence(),
		Warnings:   append([]string(nil), l.pageWarn...),
	}
	switch a.kind {
	case contracts.BlockHeading:
		blk.Level = a.level
	case contracts.BlockList:
		blk.Level = a.level
		blk.Rows = [][]string{{a.marker, a.ordinal}}
	}
	l.bb.add(blk, l.curPage)
}
