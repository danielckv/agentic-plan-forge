//go:build client && !server

package tui

import (
	"os"
	"regexp"
	"strings"
)

// Colour is off until a real terminal turns it on. Render is pure text by
// default, so tests measure the layout they will actually draw and a piped
// frame carries no escape codes.
//
// NO_COLOR is honoured whatever the terminal says (no-color.org): any value,
// including an empty one, disables colour.
var useColour bool

// EnableColour turns styling on or off for every frame drawn after it. The
// terminal calls it once at start.
func EnableColour(on bool) { useColour = on }

// colourWanted reports whether a terminal should draw in colour. tty is what
// the caller measured; the environment can only veto it.
func colourWanted(tty bool) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return tty
}

// Select Graphic Rendition codes. Only the 8 basic colours and the two
// attributes every terminal has: the frame must read the same over SSH, in
// tmux and in a plain console.
const (
	sgrReset   = "\x1b[0m"
	sgrBold    = "1"
	sgrDim     = "2"
	sgrReverse = "7"
	sgrRed     = "31"
	sgrGreen   = "32"
	sgrYellow  = "33"
	sgrBlue    = "34"
	sgrCyan    = "36"
)

// paint wraps text in one or more SGR codes, or returns it unchanged when
// colour is off. Nested painting is safe: the inner reset ends only the inner
// run, so the caller repaints what follows.
func paint(text string, codes ...string) string {
	if !useColour || text == "" || len(codes) == 0 {
		return text
	}
	return "\x1b[" + strings.Join(codes, ";") + "m" + text + sgrReset
}

func dim(text string) string     { return paint(text, sgrDim) }
func bold(text string) string    { return paint(text, sgrBold) }
func heading(text string) string { return paint(text, sgrBold, sgrCyan) }
func active(text string) string  { return paint(text, sgrReverse, sgrBold) }
func good(text string) string    { return paint(text, sgrGreen) }
func warn(text string) string    { return paint(text, sgrYellow) }
func bad(text string) string     { return paint(text, sgrRed) }
func note(text string) string    { return paint(text, sgrBlue) }

var sgrPattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

// plain strips every SGR sequence, so width arithmetic counts what the reader
// sees rather than what the terminal consumes.
func plain(text string) string { return sgrPattern.ReplaceAllString(text, "") }

// statusColour maps an agent status to its colour. Unknown statuses stay
// unpainted rather than guess.
func statusColour(status string) func(string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error", "blocked", "cancelled":
		return bad
	case "drafting", "thinking", "working", "running", "queued":
		return warn
	case "synced", "done", "finished", "completed", "approved", "ready":
		return good
	default:
		return func(s string) string { return s }
	}
}
