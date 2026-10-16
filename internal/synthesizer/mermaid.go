package synthesizer

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Label limits.
const (
	// MaxLabelRunes bounds flowchart and sequence labels.
	MaxLabelRunes = 120
	// MaxGanttLabelRunes bounds Gantt task and section labels.
	MaxGanttLabelRunes = 60
)

// labelEntities maps each character that Mermaid or this pipeline treats
// specially to its decimal entity. Mermaid decodes #NN; at render time.
var labelEntities = map[rune]string{
	'&': "#38;", '<': "#60;", '>': "#62;", '"': "#34;", '\'': "#39;",
	'#': "#35;", '%': "#37;", ';': "#59;", '\\': "#92;", '`': "#96;", '|': "#124;",
}

// cleanLabel removes control and invisible characters, collapses newlines and
// whitespace runs to one space and limits the label to max runes.
func cleanLabel(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || r == '\u0085' || r == '\u2028' || r == '\u2029':
			b.WriteByte(' ')
		case unicode.Is(unicode.Cc, r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r),
			unicode.Is(unicode.Cs, r), r == unicode.ReplacementChar:
			// removed
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(out); len(r) > max {
		out = strings.TrimSpace(string(r[:max]))
	}
	return out
}

// QuoteLabel renders a flowchart or sequence label: at most 120 runes, control
// characters removed, newlines collapsed to a space, every special character
// encoded as a #NN; entity, and always double-quoted.
func QuoteLabel(s string) string {
	clean := cleanLabel(s, MaxLabelRunes)
	if clean == "" {
		clean = "unnamed"
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range clean {
		if ent, ok := labelEntities[r]; ok {
			b.WriteString(ent)
			continue
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// GanttLabel renders a Gantt task or section label. The Gantt lexer ends a task
// name at ':' and treats '#' and ';' specially without decoding #NN;, so the
// label is restricted to [A-Za-z0-9 _.()/+-] (anything else becomes a space)
// and at most 60 runes.
func GanttLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == ' ', r == '_', r == '.', r == '(', r == ')', r == '/', r == '+', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(out); len(r) > MaxGanttLabelRunes {
		out = strings.TrimSpace(string(r[:MaxGanttLabelRunes]))
	}
	if out == "" {
		out = "task"
	}
	return out
}

func denylist() (map[string]bool, error) {
	tokens, err := contracts.MermaidDenylistTokens()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		out[strings.ToLower(t)] = true
	}
	return out, nil
}

// splitStatements splits Mermaid source on every newline and every ';' and
// trims each statement, the exact rule the frontend gate applies.
func splitStatements(src string) []string {
	parts := strings.FieldsFunc(src, func(r rune) bool { return r == '\n' || r == ';' })
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstToken(stmt string) string {
	f := strings.Fields(stmt)
	if len(f) == 0 {
		return ""
	}
	return strings.ToLower(f[0])
}

// CheckMermaid enforces the compiler's output rules on generated Mermaid: no
// backticks, "%%", "themeCSS", raw '<', leading frontmatter or any statement
// whose first token is on the shared denylist.
func CheckMermaid(src string) error {
	if strings.Contains(src, "`") {
		return fmt.Errorf("mermaid source contains a backtick")
	}
	if strings.Contains(src, "%%") {
		return fmt.Errorf("mermaid source contains a directive (%%%%)")
	}
	if strings.Contains(src, "themeCSS") {
		return fmt.Errorf("mermaid source contains themeCSS")
	}
	if strings.Contains(src, "<") {
		return fmt.Errorf("mermaid source contains a raw '<'")
	}
	if strings.HasPrefix(strings.TrimLeft(strings.TrimPrefix(src, "\ufeff"), " \t\r\n"), "---") {
		return fmt.Errorf("mermaid source starts with frontmatter")
	}
	deny, err := denylist()
	if err != nil {
		return err
	}
	for _, st := range splitStatements(src) {
		if tok := firstToken(st); deny[tok] {
			return fmt.Errorf("mermaid statement begins with denied keyword %q", tok)
		}
	}
	return nil
}

// defang rewrites any statement that would begin with a denied keyword. A label
// can contain a ';' entity (#59;) after which a denied word follows, and the
// statement splitter treats that ';' as a boundary. The first rune of the word
// becomes a numeric entity, which Mermaid decodes back at render time, so the
// visible text is unchanged and no statement starts with a denied token.
func defang(src string) (string, error) {
	deny, err := denylist()
	if err != nil {
		return "", err
	}
	for range 64 {
		changed := false
		var b strings.Builder
		start := 0
		flush := func(end int, delim string) {
			seg := src[start:end]
			trim := strings.TrimLeft(seg, " \t\r")
			lead := seg[:len(seg)-len(trim)]
			if tok := firstToken(trim); deny[tok] {
				r := []rune(trim)
				seg = lead + fmt.Sprintf("#%d;", r[0]) + string(r[1:])
				changed = true
			}
			b.WriteString(seg)
			b.WriteString(delim)
		}
		for i := range len(src) {
			if src[i] == '\n' || src[i] == ';' {
				flush(i, string(src[i]))
				start = i + 1
			}
		}
		flush(len(src), "")
		src = b.String()
		if !changed {
			return src, nil
		}
	}
	return "", fmt.Errorf("could not remove denied statement keywords")
}

// RenderGraph renders the architecture diagram as `graph TD` with generated IDs
// n<index>. Edge endpoints use the same generated IDs.
func RenderGraph(g GraphDiagram) (string, error) {
	index := map[string]string{}
	var b strings.Builder
	b.WriteString("graph TD\n")
	for i, n := range g.Nodes {
		id := fmt.Sprintf("n%d", i)
		index[n.ID] = id
		fmt.Fprintf(&b, "    %s[%s]\n", id, QuoteLabel(n.Label))
	}
	for _, e := range g.Edges {
		from, ok1 := index[e.From]
		to, ok2 := index[e.To]
		if !ok1 || !ok2 {
			return "", fmt.Errorf("edge %s->%s references an unknown node", e.From, e.To)
		}
		if strings.TrimSpace(e.Label) == "" {
			fmt.Fprintf(&b, "    %s --> %s\n", from, to)
		} else {
			fmt.Fprintf(&b, "    %s -->|%s| %s\n", from, QuoteLabel(e.Label), to)
		}
	}
	out, err := defang(b.String())
	if err != nil {
		return "", err
	}
	return out, CheckMermaid(out)
}

// RenderSequence renders the sequence diagram with generated IDs n<index>.
func RenderSequence(s SequenceDiagram) (string, error) {
	index := map[string]string{}
	var b strings.Builder
	b.WriteString("sequenceDiagram\n")
	for i, p := range s.Participants {
		id := fmt.Sprintf("n%d", i)
		index[p.ID] = id
		fmt.Fprintf(&b, "    participant %s as %s\n", id, QuoteLabel(p.Label))
	}
	for _, m := range s.Messages {
		from, ok1 := index[m.From]
		to, ok2 := index[m.To]
		if !ok1 || !ok2 {
			return "", fmt.Errorf("message %s->%s references an unknown participant", m.From, m.To)
		}
		arrow := "->>"
		switch m.Kind {
		case MessageCall:
		case MessageReturn:
			arrow = "-->>"
		default:
			return "", fmt.Errorf("message %s->%s has unknown kind %q", m.From, m.To, m.Kind)
		}
		fmt.Fprintf(&b, "    %s%s%s: %s\n", from, arrow, to, QuoteLabel(m.Label))
	}
	out, err := defang(b.String())
	if err != nil {
		return "", err
	}
	return out, CheckMermaid(out)
}

// Gantt kinds.
const (
	GanttHuman = "human"
	GanttAgent = "agent"
)

// RenderGantt renders one schedule side as a Mermaid Gantt chart. Each task
// line is `t<index> <label> :t<index>, <start>, <duration>` and each section is
// `section s<index> <label>`; the title is a fixed constant and full titles stay
// in the Jira matrix.
func RenderGantt(kind string, side Side, d DualSchedule) (string, error) {
	start, err := time.Parse("2006-01-02", d.StartDate)
	if err != nil {
		return "", fmt.Errorf("schedule start date: %w", err)
	}
	var b strings.Builder
	b.WriteString("gantt\n")
	switch kind {
	case GanttHuman:
		b.WriteString("    title Human developer schedule\n")
		b.WriteString("    dateFormat YYYY-MM-DD HH:mm\n")
		b.WriteString("    axisFormat %d %b\n")
		b.WriteString("    excludes weekends\n")
	case GanttAgent:
		b.WriteString("    title Coding agent schedule\n")
		b.WriteString("    dateFormat YYYY-MM-DD HH:mm\n")
		b.WriteString("    axisFormat %H:%M\n")
	default:
		return "", fmt.Errorf("unknown gantt kind %q", kind)
	}
	var sections []string
	byWS := map[string][]ScheduledTask{}
	for _, t := range side.Tasks {
		if _, seen := byWS[t.WorkstreamID]; !seen {
			sections = append(sections, t.WorkstreamID)
		}
		byWS[t.WorkstreamID] = append(byWS[t.WorkstreamID], t)
	}
	slices.Sort(sections)
	n := 0
	for si, ws := range sections {
		fmt.Fprintf(&b, "    section s%d %s\n", si, GanttLabel(ws))
		for _, t := range byWS[ws] {
			n++
			var from, dur string
			switch kind {
			case GanttHuman:
				// One date column is one productive working day: the offset
				// within the day maps proportionally onto the 24 clock hours,
				// so serial work by one developer never overlaps on the chart.
				dayF := t.Start / d.HoursPerDay
				dayIdx := int(dayF + 1e-9)
				frac := dayF - float64(dayIdx)
				at := addWorkingDays(start, dayIdx).Add(time.Duration(frac*24*60+0.5) * time.Minute)
				length := int((t.End-t.Start)/d.HoursPerDay*24*60 + 0.5)
				if length < 1 {
					length = 1
				}
				from = at.Format("2006-01-02 15:04")
				dur = fmt.Sprintf("%dm", length)
			default:
				mins := int(t.Start + 0.5)
				length := int(ceilDiv(t.End-t.Start, 1))
				if length < 1 {
					length = 1
				}
				from = start.Add(time.Duration(mins) * time.Minute).Format("2006-01-02 15:04")
				dur = fmt.Sprintf("%dm", length)
			}
			fmt.Fprintf(&b, "    t%d %s :t%d, %s, %s\n", n, GanttLabel(t.ID+" "+t.Title), n, from, dur)
		}
	}
	out, err := defang(b.String())
	if err != nil {
		return "", err
	}
	return out, CheckMermaid(out)
}

func ceilDiv(a, b float64) float64 {
	q := a / b
	i := float64(int(q))
	if q > i {
		return i + 1
	}
	return i
}
