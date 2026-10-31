//go:build client && !server

package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A frame drawn without a terminal carries no escape codes, so a piped or
// redirected frame stays readable and every layout test measures real columns.
func TestRenderIsPlainTextUntilColourIsEnabled(t *testing.T) {
	frame := Render(sampleView(), 100)
	if strings.Contains(frame, "\x1b[") {
		t.Errorf("colour is off by default, but the frame carries escape codes:\n%q", frame)
	}
}

// With colour on, the frame is styled and still fits: the width arithmetic
// counts visible runes, never the escape codes.
func TestColouredFrameKeepsItsWidth(t *testing.T) {
	EnableColour(true)
	defer EnableColour(false)
	for _, w := range []Width{60, 80, 100, 200} {
		for _, tab := range Tabs {
			v := sampleView()
			v.Tab = tab
			frame := Render(v, w)
			if !strings.Contains(frame, "\x1b[") {
				t.Fatalf("width %d tab %s: colour is on but nothing is painted", w, tab)
			}
			for _, ln := range strings.Split(frame, "\n") {
				if got := utf8.RuneCountInString(plain(ln)); got > int(w) {
					t.Errorf("width %d tab %s: visible line of %d columns: %q", w, tab, got, plain(ln))
				}
			}
		}
	}
}

// The open tab is the only one reversed, and every tab names the key that
// opens it.
func TestTabBarMarksTheOpenTab(t *testing.T) {
	EnableColour(true)
	defer EnableColour(false)
	for i, tab := range Tabs {
		v := sampleView()
		v.Tab = tab
		bar := strings.Split(Render(v, 200), "\n")[1]
		for j, other := range Tabs {
			label := " " + string(rune('1'+j)) + " " + other.Title() + " "
			if !strings.Contains(plain(bar), label) {
				t.Errorf("tab bar %q does not name %q", plain(bar), label)
			}
			styled := active(label)
			if got := strings.Contains(bar, styled); got != (i == j) {
				t.Errorf("tab %s reversed = %v, want %v", other, got, i == j)
			}
		}
	}
}

func TestNoColourEnvironmentVetoesATerminal(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	if !colourWanted(true) {
		t.Error("a terminal with no veto should want colour")
	}
	if colourWanted(false) {
		t.Error("colour must stay off when the output is not a terminal")
	}
	t.Setenv("NO_COLOR", "")
	if colourWanted(true) {
		t.Error("NO_COLOR set to any value, including empty, disables colour")
	}
}

func TestDumbTerminalGetsNoColour(t *testing.T) {
	t.Setenv("TERM", "dumb")
	if colourWanted(true) {
		t.Error("TERM=dumb cannot render SGR codes")
	}
}

// Truncation spends width on visible runes only, and closes the style run it
// cut through so the rest of the frame is not painted by accident.
func TestTruncateCountsVisibleRunesOnly(t *testing.T) {
	EnableColour(true)
	defer EnableColour(false)
	styled := bold("abcdefghij") + dim("klmnopqrst")
	got := truncate(styled, 10)
	if visible := plain(got); utf8.RuneCountInString(visible) != 10 {
		t.Errorf("visible length = %d, want 10: %q", utf8.RuneCountInString(visible), visible)
	}
	if !strings.HasSuffix(got, sgrReset) {
		t.Errorf("a cut run must end in a reset: %q", got)
	}
	if short := truncate(styled, 100); short != styled {
		t.Errorf("a string that fits is returned unchanged, got %q", short)
	}
}

// The brief pane bounds what it draws by rendered lines. One unwrapped
// paragraph is a single source line and hundreds of rendered ones, and it used
// to push the header off the screen.
func TestBriefPaneBoundsOneLongParagraph(t *testing.T) {
	v := sampleView()
	v.Tab = TabBrief
	frame := Render(v, 100)
	if n := strings.Count(frame, "\n"); n > 25 {
		t.Errorf("brief frame is %d lines; the pane is bounded to a tail", n)
	}
	if !strings.Contains(frame, "earlier lines") {
		t.Error("the frame must say how much it is not showing")
	}
}
