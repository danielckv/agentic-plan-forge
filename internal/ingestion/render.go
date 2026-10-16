//go:build client && !server

package ingestion

import (
	"strconv"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// nestIndent is the Markdown indentation added per list nesting level. Three
// spaces nest correctly under both "- " and "1. " markers.
const nestIndent = "   "

// Render deterministically renders parsed blocks to Markdown. The same blocks
// always produce the same bytes: headings become ATX headings, consecutive
// list items form one list, tables become pipe tables whose first row is the
// header, and a block that carries only warnings (an unparsed scanned page, for
// example) becomes a visible block quote so the gap is never silent. Output
// uses LF line endings and ends with one newline unless it is empty.
func Render(blocks []contracts.DocumentBlock) string {
	var groups []string
	for i := 0; i < len(blocks); i++ {
		b := blocks[i]
		switch b.Kind {
		case contracts.BlockHeading:
			level := b.Level
			if level < 1 {
				level = 1
			}
			if level > 6 {
				level = 6
			}
			groups = append(groups, strings.Repeat("#", level)+" "+oneLine(b.Text))
		case contracts.BlockList:
			var items []string
			for ; i < len(blocks) && blocks[i].Kind == contracts.BlockList; i++ {
				items = append(items, renderListItem(blocks[i]))
			}
			i--
			groups = append(groups, strings.Join(items, "\n"))
		case contracts.BlockTable:
			if t := renderTable(b.Rows); t != "" {
				groups = append(groups, t)
			}
		default:
			if g := renderParagraph(b); g != "" {
				groups = append(groups, g)
			}
		}
	}
	if len(groups) == 0 {
		return ""
	}
	return strings.Join(groups, "\n\n") + "\n"
}

// RenderText renders raw text, such as the composer's input, as Markdown by
// normalising its line endings. The text is otherwise preserved verbatim.
func RenderText(text string) string {
	return Render([]contracts.DocumentBlock{{Kind: contracts.BlockParagraph, Text: text}})
}

func renderParagraph(b contracts.DocumentBlock) string {
	text := strings.ReplaceAll(strings.ReplaceAll(b.Text, "\r\n", "\n"), "\r", "\n")
	text = strings.Trim(text, "\n")
	text = strings.TrimRight(text, " \t")
	if strings.TrimSpace(text) != "" {
		return text
	}
	var quotes []string
	for _, w := range b.Warnings {
		if w = oneLine(w); w != "" {
			quotes = append(quotes, "> ["+w+"]")
		}
	}
	return strings.Join(quotes, "\n")
}

func renderListItem(b contracts.DocumentBlock) string {
	level := b.Level
	if level < 1 {
		level = 1
	}
	marker := "-"
	if kind, ordinal, ok := ListMarker(b); ok && kind == ListDecimal {
		if ordinal < 1 {
			ordinal = 1
		}
		marker = strconv.Itoa(ordinal) + "."
	}
	return strings.Repeat(nestIndent, level-1) + marker + " " + oneLine(b.Text)
}

// renderTable renders a pipe table. Rows are padded to the widest row, pipes
// in cells are escaped, and line breaks inside cells become spaces.
func renderTable(rows [][]string) string {
	width := 0
	for _, r := range rows {
		if len(r) > width {
			width = len(r)
		}
	}
	if width == 0 {
		return ""
	}
	line := func(r []string) string {
		cells := make([]string, width)
		for i := range cells {
			if i < len(r) {
				cells[i] = strings.ReplaceAll(oneLine(r[i]), "|", `\|`)
			}
		}
		return "| " + strings.Join(cells, " | ") + " |"
	}
	sep := make([]string, width)
	for i := range sep {
		sep[i] = "---"
	}
	out := []string{line(rows[0]), "| " + strings.Join(sep, " | ") + " |"}
	for _, r := range rows[1:] {
		out = append(out, line(r))
	}
	return strings.Join(out, "\n")
}
