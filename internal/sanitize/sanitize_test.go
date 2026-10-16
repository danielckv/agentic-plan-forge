package sanitize_test

import (
	"strings"
	"testing"
	"unicode"

	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
)

func TestTerminalCollapsesControlWhitespace(t *testing.T) {
	if got := sanitize.Terminal("a\nb\tc"); got != "a b c" {
		t.Fatalf("Terminal(a\\nb\\tc) = %q, want %q", got, "a b c")
	}
}

func TestTerminalStripsEscapeAndBidi(t *testing.T) {
	title := "Release \x1b]0;pwned\x07 plan \u202eevil\u202c \u2066x\u2069 end"
	got := sanitize.Terminal(title)
	for _, r := range got {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			t.Fatalf("Terminal left control/format rune %U in %q", r, got)
		}
	}
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, 0x07) || strings.ContainsRune(got, 0x202e) {
		t.Fatalf("Terminal left ESC, BEL or U+202E in %q", got)
	}
}

func TestTerminalReplacementClasses(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"c1 nel", "a\u0085b", "a b"},
		{"del", "a\x7fb", "a b"},
		{"cr lf", "a\r\nb", "a b"},
		{"line separator", "a\u2028b", "a b"},
		{"paragraph separator", "a\u2029b", "a b"},
		{"zero width", "a\u200b\u200c\u200db", "ab"},
		{"lrm rlm", "a\u200e\u200fb", "ab"},
		{"alm", "a\u061cb", "ab"},
		{"word joiner range", "a\u2060\u2061\u2062\u2063\u2064b", "ab"},
		{"bom", "\ufeffab", "ab"},
		{"soft hyphen", "a\u00adb", "ab"},
		{"tag characters", "a\U000e0001\U000e0041\U000e007fb", "ab"},
		{"private use", "a\ue000b", "ab"},
		{"variation selector", "a\ufe0fb", "ab"},
		{"variation selector supplement", "a\U000e0100b", "ab"},
		{"collapse", "  a   b  ", "a b"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		if got := sanitize.Terminal(tc.in); got != tc.want {
			t.Errorf("%s: Terminal(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestTerminalReplacesInvalidUTF8(t *testing.T) {
	got := sanitize.Terminal("a\xffb\xc3")
	if got != "a\ufffdb\ufffd" {
		t.Fatalf("Terminal invalid UTF-8 = %q", got)
	}
}

func TestTitleTruncatesToEightyRunes(t *testing.T) {
	got := sanitize.Title(strings.Repeat("é", 200))
	if n := len([]rune(got)); n != 80 {
		t.Fatalf("Title length = %d runes, want 80", n)
	}
}
