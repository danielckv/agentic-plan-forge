//go:build client && !server

package ingestion

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// List marker kinds carried in the single Rows entry of a list block.
const (
	// ListBullet marks an unordered list item.
	ListBullet = "bullet"
	// ListDecimal marks an ordered list item.
	ListDecimal = "decimal"
)

// ListMarker reads the marker of a list block. A list block is one list item:
// Text is the item text, Level is its 1-based nesting depth, and Rows holds
// exactly one row {kind, ordinal} where kind is ListBullet or ListDecimal and
// ordinal is the item number for ListDecimal and empty for ListBullet. ok is
// false when b is not a well-formed list block.
func ListMarker(b contracts.DocumentBlock) (kind string, ordinal int, ok bool) {
	if b.Kind != contracts.BlockList || len(b.Rows) != 1 || len(b.Rows[0]) != 2 {
		return "", 0, false
	}
	kind = b.Rows[0][0]
	if kind != ListBullet && kind != ListDecimal {
		return "", 0, false
	}
	if kind == ListDecimal {
		n, err := strconv.Atoi(b.Rows[0][1])
		if err != nil {
			return "", 0, false
		}
		ordinal = n
	}
	return kind, ordinal, true
}

// parseDOCX reads a Word package with the standard library only. It enforces
// the package bounds before reading any part, resolves heading levels through
// styles.xml and list markers through numbering.xml, and reports every
// approximation instead of guessing.
func parseDOCX(ctx context.Context, r io.ReaderAt, size int64, rep *IngestReport) ([]contracts.DocumentBlock, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if err := checkDOCXPackage(zr); err != nil {
		return nil, err
	}
	parts := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		parts[f.Name] = f
	}
	docPart := parts["word/document.xml"]
	if docPart == nil {
		return nil, fmt.Errorf("%w: package has no word/document.xml", contracts.ErrUnsupportedFormat)
	}

	bud := newBudget(MaxDOCXUncompressedBytes)
	var styles map[string]*docxStyle
	if f := parts["word/styles.xml"]; f != nil {
		rc, err := openPart(f, bud)
		if err != nil {
			return nil, classifyXMLErr(f.Name, err)
		}
		styles, err = parseStyles(ctx, rc)
		rc.Close()
		if err != nil {
			return nil, classifyXMLErr(f.Name, err)
		}
	}
	var nums *numDefs
	if f := parts["word/numbering.xml"]; f != nil {
		rc, err := openPart(f, bud)
		if err != nil {
			return nil, classifyXMLErr(f.Name, err)
		}
		nums, err = parseNumbering(ctx, rc)
		rc.Close()
		if err != nil {
			return nil, classifyXMLErr(f.Name, err)
		}
	}

	rc, err := openPart(docPart, bud)
	if err != nil {
		return nil, classifyXMLErr(docPart.Name, err)
	}
	items, err := parseDocumentItems(ctx, rc)
	rc.Close()
	if err != nil {
		return nil, classifyXMLErr(docPart.Name, err)
	}

	conv := &docxConverter{
		styles:    styles,
		hasStyles: len(styles) > 0,
		nums:      nums,
		counters:  map[string]map[int]int{},
		usedNums:  map[string]string{},
		bb:        newBlockBuilder(rep.SourceID),
		rep:       rep,
		warned:    map[string]bool{},
	}
	if !conv.hasStyles {
		conv.warn("document has no styles part; headings could not be resolved")
	}
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch {
		case it.table != nil:
			conv.table(it.table)
		case it.para != nil:
			conv.paragraph(it.para)
		}
	}
	if len(conv.bb.blocks) == 0 {
		conv.warn("document contains no text")
	}
	return conv.bb.blocks, nil
}

// checkDOCXPackage applies the zip-bomb and path-escape guards before any part
// is read: entry count, declared total size, per-entry compression ratio and
// entry names that could leave the package root.
func checkDOCXPackage(zr *zip.Reader) error {
	if len(zr.File) > MaxDOCXEntries {
		return newBound(BoundDOCXEntries, MaxDOCXEntries, int64(len(zr.File)))
	}
	var total uint64
	for _, f := range zr.File {
		if err := checkEntryName(f.Name); err != nil {
			return err
		}
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		if f.UncompressedSize64 > uint64(MaxDOCXUncompressedBytes) || total > uint64(MaxDOCXUncompressedBytes)-f.UncompressedSize64 {
			observed := total + f.UncompressedSize64
			if observed < total || observed > math.MaxInt64 {
				observed = math.MaxInt64
			}
			return newBound(BoundDOCXUncompressed, MaxDOCXUncompressedBytes, int64(observed))
		}
		total += f.UncompressedSize64
		if f.UncompressedSize64 == 0 {
			continue
		}
		if f.CompressedSize64 == 0 {
			return fmt.Errorf("%w: entry %q declares data but no compressed bytes", ErrCorrupt, f.Name)
		}
		ratio := f.UncompressedSize64 / f.CompressedSize64
		if ratio > uint64(MaxDOCXCompressionRatio) {
			if ratio > math.MaxInt64 {
				ratio = math.MaxInt64
			}
			return newBound(BoundDOCXRatio, MaxDOCXCompressionRatio, int64(ratio))
		}
	}
	return nil
}

// checkEntryName rejects entry names that are absolute, contain a parent
// reference, a drive letter, a backslash or a NUL byte.
func checkEntryName(name string) error {
	bad := name == "" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || (len(name) >= 2 && name[1] == ':')
	if !bad {
		for _, seg := range strings.Split(name, "/") {
			if seg == ".." {
				bad = true
				break
			}
		}
	}
	if bad {
		return fmt.Errorf("%w: archive entry %q escapes the package root", contracts.ErrUnsupportedFormat, name)
	}
	return nil
}

type partReader struct {
	io.Reader
	io.Closer
}

// openPart opens one package part with the shared decompression budget.
func openPart(f *zip.File, bud *budget) (io.ReadCloser, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	return partReader{Reader: &budgetReader{r: rc, b: bud}, Closer: rc}, nil
}

// pProps are the paragraph properties the parser reads.
type pProps struct {
	style   string
	outline int
	hasNum  bool
	numID   string
	ilvl    int
}

// readPPr reads a paragraph-properties element whose start tag was just read.
func readPPr(s *xmlStream) (pProps, error) {
	pr := pProps{outline: -1}
	err := s.walk(func(c xml.StartElement) error {
		if !isW(c.Name) {
			return nil
		}
		switch c.Name.Local {
		case "pStyle":
			pr.style = attr(c, "val")
			return s.skip()
		case "outlineLvl":
			if v, err := strconv.Atoi(attr(c, "val")); err == nil {
				pr.outline = v
			}
			return s.skip()
		case "numPr":
			pr.hasNum = true
			return s.walk(func(n xml.StartElement) error {
				if !isW(n.Name) {
					return nil
				}
				switch n.Name.Local {
				case "ilvl":
					if v, err := strconv.Atoi(attr(n, "val")); err == nil && v >= 0 && v <= 8 {
						pr.ilvl = v
					}
					return s.skip()
				case "numId":
					pr.numID = attr(n, "val")
					return s.skip()
				}
				return nil
			})
		case "rPr", "pPrChange", "sectPr", "tabs", "pBdr", "framePr":
			return s.skip()
		}
		return nil
	})
	return pr, err
}

// docxStyle is one paragraph style.
type docxStyle struct {
	id, name, basedOn string
	outline           int
	hasNum            bool
	numID             string
	ilvl              int
}

func parseStyles(ctx context.Context, r io.Reader) (map[string]*docxStyle, error) {
	s := newXMLStream(ctx, r)
	styles := map[string]*docxStyle{}
	for {
		tok, err := s.next()
		if err == io.EOF {
			return styles, nil
		}
		if err != nil {
			return nil, err
		}
		st, ok := tok.(xml.StartElement)
		if !ok || !isW(st.Name) || st.Name.Local != "style" {
			continue
		}
		if t := attr(st, "type"); t != "" && t != "paragraph" {
			if err := s.skip(); err != nil {
				return nil, err
			}
			continue
		}
		style := &docxStyle{id: attr(st, "styleId"), outline: -1}
		err = s.walk(func(c xml.StartElement) error {
			if !isW(c.Name) {
				return nil
			}
			switch c.Name.Local {
			case "name":
				style.name = attr(c, "val")
				return s.skip()
			case "basedOn":
				style.basedOn = attr(c, "val")
				return s.skip()
			case "pPr":
				pr, err := readPPr(s)
				if err != nil {
					return err
				}
				style.outline = pr.outline
				style.hasNum, style.numID, style.ilvl = pr.hasNum, pr.numID, pr.ilvl
			case "rPr", "tblPr", "trPr", "tcPr", "tblStylePr":
				return s.skip()
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if style.id != "" {
			styles[style.id] = style
		}
	}
}

type numLevel struct {
	numFmt string
	start  int
}

type numInst struct {
	abstractID string
	overrides  bool
}

// numDefs are the list definitions from numbering.xml.
type numDefs struct {
	abstract map[string]map[int]numLevel
	nums     map[string]numInst
}

func (n *numDefs) level(numID string, ilvl int) (numLevel, string, bool) {
	if n == nil {
		return numLevel{}, "", false
	}
	inst, ok := n.nums[numID]
	if !ok {
		return numLevel{}, "", false
	}
	lvl, ok := n.abstract[inst.abstractID][ilvl]
	return lvl, inst.abstractID, ok
}

func parseNumbering(ctx context.Context, r io.Reader) (*numDefs, error) {
	s := newXMLStream(ctx, r)
	defs := &numDefs{abstract: map[string]map[int]numLevel{}, nums: map[string]numInst{}}
	for {
		tok, err := s.next()
		if err == io.EOF {
			return defs, nil
		}
		if err != nil {
			return nil, err
		}
		st, ok := tok.(xml.StartElement)
		if !ok || !isW(st.Name) {
			continue
		}
		switch st.Name.Local {
		case "abstractNum":
			id := attr(st, "abstractNumId")
			levels := map[int]numLevel{}
			err := s.walk(func(c xml.StartElement) error {
				if !isW(c.Name) || c.Name.Local != "lvl" {
					return nil
				}
				ilvl, err := strconv.Atoi(attr(c, "ilvl"))
				if err != nil || ilvl < 0 || ilvl > 8 {
					return s.skip()
				}
				lvl := numLevel{start: 1}
				err = s.walk(func(f xml.StartElement) error {
					if !isW(f.Name) {
						return nil
					}
					switch f.Name.Local {
					case "start":
						if v, err := strconv.Atoi(attr(f, "val")); err == nil {
							lvl.start = v
						}
						return s.skip()
					case "numFmt":
						lvl.numFmt = attr(f, "val")
						return s.skip()
					case "pPr", "rPr":
						return s.skip()
					}
					return nil
				})
				levels[ilvl] = lvl
				return err
			})
			if err != nil {
				return nil, err
			}
			defs.abstract[id] = levels
		case "num":
			id := attr(st, "numId")
			inst := numInst{}
			err := s.walk(func(c xml.StartElement) error {
				if !isW(c.Name) {
					return nil
				}
				switch c.Name.Local {
				case "abstractNumId":
					inst.abstractID = attr(c, "val")
					return s.skip()
				case "lvlOverride":
					inst.overrides = true
					return s.skip()
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			defs.nums[id] = inst
		}
	}
}

// docxPara is one paragraph read from document.xml.
type docxPara struct {
	text  string
	props pProps
}

// docxTable is one table read from document.xml.
type docxTable struct {
	rows   [][]string
	merged bool
}

// docxItem is a paragraph or a table in document order.
type docxItem struct {
	para  *docxPara
	table *docxTable
}

type docxCell struct {
	text   string
	span   int
	vmerge string
}

type docxParser struct{ s *xmlStream }

// parseDocumentItems streams document.xml into paragraphs and tables.
func parseDocumentItems(ctx context.Context, r io.Reader) ([]docxItem, error) {
	p := &docxParser{s: newXMLStream(ctx, r)}
	for {
		tok, err := p.s.next()
		if err == io.EOF {
			return nil, fmt.Errorf("%w: document.xml has no body", ErrCorrupt)
		}
		if err != nil {
			return nil, err
		}
		if st, ok := tok.(xml.StartElement); ok && isW(st.Name) && st.Name.Local == "body" {
			return p.readItems()
		}
	}
}

// readItems reads block-level content below the element just opened.
func (p *docxParser) readItems() ([]docxItem, error) {
	var items []docxItem
	err := p.s.walk(func(st xml.StartElement) error { return p.blockElement(st, &items) })
	return items, err
}

// blockElement handles one block-level start tag: paragraphs and tables are
// read, revision-deleted and layout-only content is skipped, and everything
// else (content controls, alternate-content choices) is left for the walker to
// descend into.
func (p *docxParser) blockElement(st xml.StartElement, items *[]docxItem) error {
	if isMCFallback(st.Name) {
		return p.s.skip()
	}
	if !isW(st.Name) {
		return nil
	}
	switch st.Name.Local {
	case "p":
		para, nested, err := p.readParagraph()
		if err != nil {
			return err
		}
		*items = append(*items, docxItem{para: para})
		*items = append(*items, nested...)
	case "tbl":
		tbl, err := p.readTable()
		if err != nil {
			return err
		}
		*items = append(*items, docxItem{table: tbl})
	case "sectPr", "del", "moveFrom":
		return p.s.skip()
	}
	return nil
}

func (p *docxParser) readParagraph() (*docxPara, []docxItem, error) {
	para := &docxPara{props: pProps{outline: -1}}
	var sb strings.Builder
	var nested []docxItem
	err := p.s.walk(func(st xml.StartElement) error {
		if isMCFallback(st.Name) {
			return p.s.skip()
		}
		if !isW(st.Name) {
			return nil
		}
		switch st.Name.Local {
		case "pPr":
			pr, err := readPPr(p.s)
			if err != nil {
				return err
			}
			para.props = pr
		case "r":
			return p.readRun(&sb, &nested)
		case "del", "moveFrom":
			return p.s.skip()
		}
		return nil
	})
	para.text = sb.String()
	return para, nested, err
}

func (p *docxParser) readRun(out *strings.Builder, nested *[]docxItem) error {
	var run strings.Builder
	hidden := false
	err := p.s.walk(func(st xml.StartElement) error {
		if isMCFallback(st.Name) {
			return p.s.skip()
		}
		if !isW(st.Name) {
			return nil
		}
		switch st.Name.Local {
		case "rPr":
			return p.s.walk(func(c xml.StartElement) error {
				if isW(c.Name) && c.Name.Local == "vanish" {
					switch attr(c, "val") {
					case "0", "false", "off":
						hidden = false
					default:
						hidden = true
					}
				}
				return p.s.skip()
			})
		case "t":
			txt, err := p.s.text()
			run.WriteString(txt)
			return err
		case "tab":
			run.WriteByte('\t')
			return p.s.skip()
		case "br":
			if t := attr(st, "type"); t == "" || t == "textWrapping" {
				run.WriteByte('\n')
			}
			return p.s.skip()
		case "cr":
			run.WriteByte('\n')
			return p.s.skip()
		case "noBreakHyphen":
			run.WriteByte('-')
			return p.s.skip()
		case "delText", "instrText":
			return p.s.skip()
		case "txbxContent":
			items, err := p.readItems()
			*nested = append(*nested, items...)
			return err
		}
		return nil
	})
	if !hidden {
		out.WriteString(run.String())
	}
	return err
}

func (p *docxParser) readTable() (*docxTable, error) {
	tbl := &docxTable{}
	err := p.s.walk(func(st xml.StartElement) error {
		if !isW(st.Name) {
			return nil
		}
		switch st.Name.Local {
		case "tr":
			row, err := p.readRow(tbl)
			if err != nil {
				return err
			}
			tbl.rows = append(tbl.rows, row)
		case "tblPr", "tblGrid", "tblPrEx":
			return p.s.skip()
		}
		return nil
	})
	return tbl, err
}

func (p *docxParser) readRow(tbl *docxTable) ([]string, error) {
	var cells []string
	err := p.s.walk(func(st xml.StartElement) error {
		if !isW(st.Name) {
			return nil
		}
		switch st.Name.Local {
		case "tc":
			c, err := p.readCell()
			if err != nil {
				return err
			}
			if c.vmerge != "" || c.span > 1 {
				tbl.merged = true
			}
			if c.vmerge == "continue" {
				cells = append(cells, "")
			} else {
				cells = append(cells, c.text)
			}
			for i := 1; i < c.span; i++ {
				cells = append(cells, "")
			}
		case "trPr", "tblPrEx":
			return p.s.skip()
		}
		return nil
	})
	return cells, err
}

func (p *docxParser) readCell() (docxCell, error) {
	cell := docxCell{span: 1}
	var items []docxItem
	err := p.s.walk(func(st xml.StartElement) error {
		if isW(st.Name) && st.Name.Local == "tcPr" {
			return p.s.walk(func(c xml.StartElement) error {
				if isW(c.Name) {
					switch c.Name.Local {
					case "gridSpan":
						if n, err := strconv.Atoi(attr(c, "val")); err == nil && n > 1 && n <= 64 {
							cell.span = n
						}
					case "vMerge":
						if v := attr(c, "val"); v == "" || v == "continue" {
							cell.vmerge = "continue"
						} else {
							cell.vmerge = "restart"
						}
					}
				}
				return p.s.skip()
			})
		}
		return p.blockElement(st, &items)
	})
	cell.text = joinCellText(items)
	return cell, err
}

// joinCellText flattens a cell's paragraphs and nested tables to one line.
func joinCellText(items []docxItem) string {
	var parts []string
	for _, it := range items {
		switch {
		case it.para != nil:
			if t := oneLine(it.para.text); t != "" {
				parts = append(parts, t)
			}
		case it.table != nil:
			for _, row := range it.table.rows {
				for _, c := range row {
					if c != "" {
						parts = append(parts, c)
					}
				}
			}
		}
	}
	return strings.Join(parts, " ")
}

// oneLine collapses all whitespace, including tabs and line breaks, to single
// spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// docxConverter turns parsed items into blocks, resolving styles and numbering.
type docxConverter struct {
	styles    map[string]*docxStyle
	hasStyles bool
	nums      *numDefs
	counters  map[string]map[int]int
	usedNums  map[string]string // abstractNumId -> first numId seen using it
	bb        *blockBuilder
	rep       *IngestReport
	warned    map[string]bool
}

// warn records a report-level warning once.
func (c *docxConverter) warn(msg string) {
	if c.warned[msg] {
		return
	}
	c.warned[msg] = true
	c.rep.Warnings = append(c.rep.Warnings, msg)
}

func (c *docxConverter) confidence() string {
	if c.hasStyles {
		return contracts.ConfidenceHigh
	}
	return contracts.ConfidenceMedium
}

func (c *docxConverter) table(t *docxTable) {
	width := 0
	for _, row := range t.rows {
		if len(row) > width {
			width = len(row)
		}
	}
	var rows [][]string
	for _, row := range t.rows {
		out := make([]string, width)
		empty := true
		for i, cell := range row {
			out[i] = oneLine(cell)
			if out[i] != "" {
				empty = false
			}
		}
		if !empty {
			rows = append(rows, out)
		}
	}
	if len(rows) == 0 {
		return
	}
	var warnings []string
	if t.merged {
		warnings = []string{"table has merged cells; columns approximated"}
		c.warn(warnings[0])
	}
	c.bb.add(contracts.DocumentBlock{Kind: contracts.BlockTable, Rows: rows, Confidence: c.confidence(), Warnings: warnings}, 0)
}

func (c *docxConverter) paragraph(para *docxPara) {
	text := strings.TrimSpace(para.text)
	if text == "" {
		return
	}
	conf := c.confidence()
	if lvl := c.headingLevel(para.props); lvl > 0 {
		c.bb.add(contracts.DocumentBlock{Kind: contracts.BlockHeading, Level: lvl, Text: oneLine(text), Confidence: conf}, 0)
		return
	}
	if numID, ilvl, ok := c.numPr(para.props); ok && numID != "" && numID != "0" {
		if c.listItem(text, numID, ilvl, conf) {
			return
		}
	}
	c.bb.add(contracts.DocumentBlock{Kind: contracts.BlockParagraph, Text: text, Confidence: conf}, 0)
}

// headingLevel returns 1-9 for a heading paragraph and 0 otherwise. A direct
// outline level wins over the style's, and outline level 9 means body text.
func (c *docxConverter) headingLevel(pr pProps) int {
	outline := pr.outline
	if outline < 0 {
		outline = c.styleOutline(pr.style)
	}
	if outline >= 0 && outline <= 8 {
		return outline + 1
	}
	return 0
}

// styleOutline resolves a style's 0-based outline level through its basedOn
// chain, using an explicit outlineLvl first and the built-in "Heading N" names
// otherwise. It returns -1 when the style is not a heading style.
func (c *docxConverter) styleOutline(id string) int {
	cur := id
	for i := 0; i < 16 && cur != ""; i++ {
		st := c.styles[cur]
		if st == nil {
			if n := headingNameLevel(cur); n > 0 {
				return n - 1
			}
			return -1
		}
		if st.outline >= 0 {
			return st.outline
		}
		if n := headingNameLevel(st.name); n > 0 {
			return n - 1
		}
		if n := headingNameLevel(st.id); n > 0 {
			return n - 1
		}
		cur = st.basedOn
	}
	return -1
}

// headingNameLevel recognises the built-in names "Heading 1" to "Heading 9",
// with or without the space.
func headingNameLevel(name string) int {
	n := strings.ToLower(strings.ReplaceAll(name, " ", ""))
	if len(n) == len("heading")+1 && strings.HasPrefix(n, "heading") {
		if d := n[len(n)-1]; d >= '1' && d <= '9' {
			return int(d - '0')
		}
	}
	return 0
}

// numPr returns the effective numbering reference: the paragraph's own, else
// the nearest one on its style chain.
func (c *docxConverter) numPr(pr pProps) (numID string, ilvl int, ok bool) {
	if pr.hasNum {
		return pr.numID, pr.ilvl, true
	}
	cur := pr.style
	for i := 0; i < 16 && cur != ""; i++ {
		st := c.styles[cur]
		if st == nil {
			break
		}
		if st.hasNum {
			return st.numID, st.ilvl, true
		}
		cur = st.basedOn
	}
	return "", 0, false
}

// listItem emits one list item and reports whether it did. A level whose
// format is "none" is plain text, so it is left for the paragraph path.
func (c *docxConverter) listItem(text, numID string, ilvl int, conf string) bool {
	def, abstractID, known := c.nums.level(numID, ilvl)
	var warnings []string
	note := func(msg string) {
		c.warn(msg)
		warnings = append(warnings, msg)
	}

	kind := ListBullet
	ordinal := ""
	switch {
	case !known:
		note("list numbering approximated: definition missing for numbering id " + numID)
		conf = contracts.ConfidenceMedium
	case def.numFmt == "none":
		return false
	case def.numFmt == "bullet":
	case def.numFmt == "decimal":
		kind = ListDecimal
	default:
		kind = ListDecimal
		note("list numbering approximated: format " + strconv.Quote(def.numFmt) + " rendered as decimal")
	}
	if known {
		if inst := c.nums.nums[numID]; inst.overrides {
			note("list numbering approximated: level overrides are not applied")
		}
		if first, seen := c.usedNums[abstractID]; !seen {
			c.usedNums[abstractID] = numID
		} else if first != numID {
			note("list numbering approximated: separate lists share one definition")
		}
	}
	if kind == ListDecimal {
		ordinal = strconv.Itoa(c.advance(numID, ilvl, def.start))
	} else {
		c.advance(numID, ilvl, 1)
	}
	c.bb.add(contracts.DocumentBlock{
		Kind:       contracts.BlockList,
		Level:      ilvl + 1,
		Text:       oneLine(text),
		Rows:       [][]string{{kind, ordinal}},
		Confidence: conf,
		Warnings:   warnings,
	}, 0)
	return true
}

// advance increments the counter of one list level and restarts deeper levels.
func (c *docxConverter) advance(numID string, ilvl, start int) int {
	m := c.counters[numID]
	if m == nil {
		m = map[int]int{}
		c.counters[numID] = m
	}
	cur, seen := m[ilvl]
	if !seen {
		cur = start - 1
	}
	cur++
	m[ilvl] = cur
	for lvl := range m {
		if lvl > ilvl {
			delete(m, lvl)
		}
	}
	return cur
}
