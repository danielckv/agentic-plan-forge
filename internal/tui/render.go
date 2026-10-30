//go:build client && !server

package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Width is the column budget the renderer works to. The caller measures the
// terminal and passes it in; a width of zero falls back to DefaultWidth so a
// test or a pipe still prints something readable.
type Width int

// DefaultWidth is the fallback when the terminal size is unknown.
const DefaultWidth Width = 100

// MinWidth is the narrowest layout the panes are designed for. Below it the
// renderer keeps working but single-column, because a two-column layout that
// cannot fit is worse than a list.
const MinWidth Width = 60

// View is what the renderer draws: the snapshot plus the reader's position.
type View struct {
	Snapshot Snapshot
	Tab      Tab
	// Selected indexes the rows of the current tab, so arrows move a cursor
	// that the action keys act on.
	Selected int
	// Help reports whether the help pane is showing.
	Help bool
	// Notice is a one-line message: what a key just did, or why it refused.
	Notice string
	// Editing is true while the brief is being typed. Every printable key then
	// belongs to Draft, so a brief containing "q" does not quit the session.
	Editing bool
	// Draft is the text typed so far, appended to the document on Ctrl-S.
	Draft string
	// quit is set by 'q'; Run reads it and returns.
	quit bool
}

// Render draws the whole window. It returns text sized for the given width, so
// the caller only has to clear the screen and write it.
//
// The layout is a header, a banner when something needs saying, the active pane,
// and a footer with the keys and the pending-question count. Terminal rendering
// is a list, never an attempted 3D scene: the plan asks for a state-equivalent
// view, and a text terminal is the worst place to pretend otherwise.
func Render(v View, w Width) string {
	if w <= 0 {
		w = DefaultWidth
	}
	cols := int(w)
	var b strings.Builder

	writeHeader(&b, v, cols)
	if v.Snapshot.Err != "" {
		b.WriteString(line(cols, "!", "error: "+v.Snapshot.Err))
	}
	if v.Snapshot.Banner != "" {
		b.WriteString(line(cols, "*", v.Snapshot.Banner))
	}
	if v.Notice != "" {
		b.WriteString(line(cols, ">", v.Notice))
	}
	b.WriteString(rule(cols))

	if v.Help {
		writeHelp(&b, cols)
		b.WriteString(rule(cols))
		writeFooter(&b, v, cols)
		return b.String()
	}

	switch v.Tab {
	case TabBrief:
		writeBrief(&b, v, cols)
	case TabAgents:
		writeAgents(&b, v, cols)
	case TabPlan:
		writePlan(&b, v, cols)
	case TabQuestions:
		writeQuestions(&b, v, cols)
	case TabSettings:
		writeSettings(&b, v, cols)
	default:
		writeAgents(&b, v, cols)
	}
	b.WriteString(rule(cols))
	writeFooter(&b, v, cols)
	return b.String()
}

// writeHeader is the one line that says where the session stands: phase, cycle,
// role, and how many people are in the room.
func writeHeader(b *strings.Builder, v View, cols int) {
	phase := string(v.Snapshot.Phase)
	if phase == "" {
		phase = "connecting"
	}
	cycle := ""
	if v.Snapshot.Cycle > 0 {
		cycle = fmt.Sprintf(" · cycle %d", v.Snapshot.Cycle)
	}
	role := ""
	if v.Snapshot.Role != "" {
		role = " · " + string(v.Snapshot.Role)
	}
	members := ""
	if n := len(v.Snapshot.Members); n > 1 {
		members = fmt.Sprintf(" · %d members", n)
	}
	title := "Plan Forge"
	if v.Snapshot.DocTitle != "" {
		title = truncate(v.Snapshot.DocTitle, cols/3)
	}
	head := fmt.Sprintf("%s — %s%s%s%s", title, phase, cycle, role, members)
	b.WriteString(line(cols, "=", head))
}

// writeFooter names the keys and the count that matters most: questions waiting.
func writeFooter(b *strings.Builder, v View, cols int) {
	pending := len(v.Snapshot.Questions)
	keys := "1-5 tabs · Tab/Shift-Tab panes · ↑↓ select · Enter act · ? help · q quit"
	if pending > 0 {
		keys = fmt.Sprintf("%d open question(s) — press 4 · %s", pending, keys)
	}
	b.WriteString(line(cols, "-", truncate(keys, cols)))
}

// writeBrief is the requirements pane: the document, and the editor when it is
// open.
func writeBrief(b *strings.Builder, v View, cols int) {
	b.WriteString(section(cols, "Brief"))
	if v.Editing {
		b.WriteString(wrapped("Type the brief. Enter is a newline, Ctrl-S appends it, Esc cancels.", cols))
		b.WriteString(rule(cols))
		draft := v.Draft
		if strings.TrimSpace(draft) == "" {
			b.WriteString(wrapped("(empty — nothing typed yet)", cols))
		} else {
			for _, ln := range strings.Split(draft, "\n") {
				b.WriteString(wrapped(ln, cols))
			}
		}
		b.WriteString(rule(cols))
		b.WriteString(line(cols, "|", truncate("cursor", cols)))
		return
	}
	if v.Snapshot.DocText == "" {
		b.WriteString(wrapped("No requirements read yet. Press e to write the brief here, or pass --brief.", cols))
		return
	}
	words := len(strings.Fields(v.Snapshot.DocText))
	b.WriteString(wrapped(fmt.Sprintf("%d words, revision %d. Press e to append.", words, v.Snapshot.DocVersion), cols))
	b.WriteString(rule(cols))
	// The tail is what the reader needs: the document grows by appending.
	lines := strings.Split(strings.TrimRight(v.Snapshot.DocText, "\n"), "\n")
	const tail = 12
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	for _, ln := range lines {
		b.WriteString(wrapped(ln, cols))
	}
}

// writeAgents lists every avatar with its status, cycle, host and last line. It
// is the dependency-ordered roster the plan asks for, with the temporary agents
// marked so a run-scoped agent is never mistaken for the fixed team.
func writeAgents(b *strings.Builder, v View, cols int) {
	b.WriteString(section(cols, "Agents"))
	if len(v.Snapshot.Agents) == 0 {
		b.WriteString(wrapped("No avatars have spoken yet. They appear as soon as the run starts.", cols))
		return
	}
	for i, a := range v.Snapshot.Agents {
		cursor := "  "
		if i == v.Selected {
			cursor = "> "
		}
		where := ""
		switch {
		case a.Host != "" && a.Model != "":
			where = fmt.Sprintf(" [%s/%s]", a.Host, a.Model)
		case a.Host != "":
			where = fmt.Sprintf(" [%s]", a.Host)
		}
		kind := ""
		if a.Kind == "temporary" {
			kind = " (temporary)"
		}
		name := a.Name + kind
		status := a.Status
		if status == "" {
			status = "idle"
		}
		head := fmt.Sprintf("%s%s%s — %s", name, located(a.Cycle), where, status)
		b.WriteString(line(cols, " ", cursor+truncate(head, cols-2)))
		if a.Summary != "" {
			b.WriteString(wrappedIndent(a.Summary, cols, 6))
		}
	}
}

// located names the cycle an avatar is working in, when it is working.
func located(cycle int) string {
	if cycle <= 0 {
		return ""
	}
	return fmt.Sprintf(" · cycle %d", cycle)
}

// writePlan lists the plan items the run has produced so far.
func writePlan(b *strings.Builder, v View, cols int) {
	b.WriteString(section(cols, "Plan"))
	if len(v.Snapshot.Plan) == 0 {
		b.WriteString(wrapped("No plan items yet. Epics appear as the Product Manager drafts them.", cols))
		return
	}
	for _, item := range v.Snapshot.Plan {
		b.WriteString(line(cols, " ", truncate("· "+item, cols-2)))
	}
	if len(v.Snapshot.Findings) > 0 {
		b.WriteString(section(cols, "Findings"))
		for _, f := range v.Snapshot.Findings {
			b.WriteString(line(cols, " ", truncate("· "+f, cols-2)))
		}
	}
}

// writeQuestions lists what waits for the owner, and how to answer it. It is the
// terminal equivalent of the question drawer: the same rows, the same options,
// the same recommendation marked rather than assumed.
func writeQuestions(b *strings.Builder, v View, cols int) {
	b.WriteString(section(cols, "Questions"))
	if len(v.Snapshot.Questions) == 0 {
		b.WriteString(wrapped("Nothing waiting for you. Decisions and brief ambiguities appear here.", cols))
		return
	}
	for i, q := range v.Snapshot.Questions {
		cursor := "  "
		if i == v.Selected {
			cursor = "> "
		}
		label := q.Kind
		if label == "" {
			label = "question"
		}
		b.WriteString(line(cols, " ", cursor+truncate(q.ID+"  ["+label+"]", cols-2)))
		b.WriteString(wrappedIndent(q.Prompt, cols, 6))
		for n, o := range q.Options {
			mark := " "
			if q.Recommended != "" && strings.HasPrefix(o, q.Recommended+":") {
				mark = "*"
			}
			b.WriteString(wrappedIndent(fmt.Sprintf("%s %d) %s", mark, n+1, o), cols, 8))
		}
		if q.Recommended != "" {
			b.WriteString(wrappedIndent("(* recommended, a hint rather than an answer)", cols, 8))
		}
		b.WriteString(wrappedIndent("Press the option number to answer.", cols, 6))
	}
}

// writeSettings is where a session runs: hosts, models, the limit that bounds
// temporary agents. Read-only here; the values come from the snapshot.
func writeSettings(b *strings.Builder, v View, cols int) {
	b.WriteString(section(cols, "Settings"))
	rows := [][2]string{
		{"phase", orDash(string(v.Snapshot.Phase))},
		{"role", orDash(string(v.Snapshot.Role))},
		{"cycle", fmt.Sprintf("%d", v.Snapshot.Cycle)},
		{"brief words", fmt.Sprintf("%d", len(strings.Fields(v.Snapshot.DocText)))},
		{"brief revision", fmt.Sprintf("%d", v.Snapshot.DocVersion)},
		{"readiness", orDash(v.Snapshot.Readiness)},
		{"artifacts", orDash(strings.Join(v.Snapshot.Artifacts, ", "))},
	}
	for _, r := range rows {
		b.WriteString(line(cols, " ", fmt.Sprintf("%-14s %s", r[0], truncate(r[1], cols-16))))
	}
	b.WriteString(wrapped("Change providers, models and the temporary-agent limit with `plan-forge config`; the terminal shows the state, it does not edit it.", cols))
}

// writeHelp lists every key.
func writeHelp(b *strings.Builder, cols int) {
	b.WriteString(section(cols, "Keys"))
	for _, k := range [][2]string{
		{"1 – 5", "brief, agents, plan, questions, settings"},
		{"1 – 9", "on the questions pane: answer the selected question"},
		{"e", "on the brief pane: write the brief (Ctrl-S save, Esc cancel)"},
		{"Tab / Shift-Tab", "next / previous pane"},
		{"↑ / ↓", "move the selection"},
		{"Enter", "act on the selection (answer a question, open a row)"},
		{"a", "approve the plan, when the run reaches synthesis"},
		{"s", "synthesize, when every avatar has finished"},
		{"r", "retry the failed stage"},
		{"c", "cancel the session"},
		{"?", "this help"},
		{"q", "quit the terminal; the session keeps running"},
	} {
		b.WriteString(line(cols, " ", fmt.Sprintf("%-16s %s", k[0], truncate(k[1], cols-19))))
	}
}

// --- small text helpers -------------------------------------------------------

// line writes one row prefixed by a marker, truncated to the width so nothing
// ever wraps and breaks the cursor arithmetic.
func line(cols int, marker, text string) string {
	return truncate(marker+" "+text, cols) + "\n"
}

// section writes a heading row.
func section(cols int, title string) string {
	return line(cols, "", "── "+title+" ") + "\n"
}

// rule writes a divider the full width.
func rule(cols int) string {
	return strings.Repeat("─", cols) + "\n"
}

// wrapped breaks text over lines of at most cols characters, keeping words
// whole where it can. A word longer than the width is cut, because a terminal
// cannot scroll a single word.
func wrapped(text string, cols int) string {
	return wrappedIndent(text, cols, 0)
}

// wrappedIndent is wrapped with a hanging indent.
func wrappedIndent(text string, cols, indent int) string {
	if cols <= indent+8 {
		cols = indent + 8
	}
	width := cols - indent - 2
	pad := strings.Repeat(" ", indent)
	var b strings.Builder
	remaining := strings.TrimSpace(text)
	for remaining != "" {
		if utf8.RuneCountInString(remaining) <= width {
			b.WriteString(pad + remaining + "\n")
			break
		}
		cut := wrapPoint(remaining, width)
		b.WriteString(pad + strings.TrimRight(remaining[:cut], " ") + "\n")
		remaining = strings.TrimLeft(remaining[cut:], " ")
	}
	return b.String()
}

// wrapPoint finds the last space at or before width, or width itself when the
// run has no space (one very long token).
func wrapPoint(s string, width int) int {
	if width >= len(s) {
		return len(s)
	}
	// Byte-accurate for ASCII and safe for UTF-8: step back to a boundary.
	for width > 0 && !utf8.RuneStart(s[width]) {
		width--
	}
	if width == 0 {
		return len(s)
	}
	if i := strings.LastIndexByte(s[:width], ' '); i > 0 {
		return i
	}
	return width
}

// truncate cuts text to cols runes, marking the cut so a reader knows the line
// continues rather than reading a sentence that silently stops.
func truncate(s string, cols int) string {
	if cols <= 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= cols {
		return s
	}
	runes := []rune(s)
	return string(runes[:cols-1]) + "…"
}

// orDash renders an empty value as a dash, so a blank cell is never ambiguous
// between "not set" and "not rendered".
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
