//go:build client && !server

package app

import "testing"

// Empty and the documented names select the browser, so a caller that says
// nothing gets the default and never has to know this type exists.
func TestParseFrontendDefaultsToBrowser(t *testing.T) {
	for _, raw := range []string{"", "browser", "web", " Browser ", "WEB"} {
		got, err := ParseFrontend(raw)
		if err != nil {
			t.Fatalf("ParseFrontend(%q): %v", raw, err)
		}
		if got.Terminal() {
			t.Errorf("ParseFrontend(%q) = terminal, want browser", raw)
		}
	}
}

// The terminal spellings all mean the same frontend, because a reader types
// whichever word comes to mind.
func TestParseFrontendAcceptsTerminalSpellings(t *testing.T) {
	for _, raw := range []string{"terminal", "tty", "tui", "TERMINAL"} {
		got, err := ParseFrontend(raw)
		if err != nil {
			t.Fatalf("ParseFrontend(%q): %v", raw, err)
		}
		if !got.Terminal() {
			t.Errorf("ParseFrontend(%q) = browser, want terminal", raw)
		}
	}
}

// An unknown value is refused with the supported list, never silently ignored:
// a typo must not quietly open a browser on a headless machine.
func TestParseFrontendRejectsUnknown(t *testing.T) {
	if _, err := ParseFrontend("headless"); err == nil {
		t.Fatal("an unknown frontend was accepted")
	}
}
