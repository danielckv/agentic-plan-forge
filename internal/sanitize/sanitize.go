// Package sanitize removes characters that can forge terminal output or smuggle
// invisible instructions into agent context. Every manifest- or spec-derived
// string that reaches a terminal, a banner or an agent prompt passes Terminal:
// those files can arrive through a clone and are untrusted input.
package sanitize

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Terminal returns s with every control, format and invisible character
// neutralised. The steps run in this exact order:
//
//  1. Every rune in category Cc (C0 controls, DEL and the C1 range, which
//     includes tab, newline, carriage return and U+0085) and in Zl or Zp is
//     replaced by U+0020.
//  2. Every rune in Cf (zero-width characters U+200B-U+200D, LRM/RLM, ALM
//     U+061C, the bidi embeddings and isolates, U+2060-U+2064, U+FEFF, soft
//     hyphen and the tag characters U+E0000-U+E007F), Co, Cs and
//     unicode.Variation_Selector is deleted.
//  3. Invalid UTF-8 is replaced by U+FFFD.
//  4. Runs of whitespace collapse to one space and the result is trimmed
//     (strings.Fields).
//
// ESC (U+001B) is a Cc rune, so an OSC or CSI sequence loses its introducer and
// can no longer be interpreted by a terminal.
func Terminal(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(unicode.ReplacementChar)
			continue
		}
		switch {
		case unicode.Is(unicode.Cc, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r):
			b.WriteByte(' ')
		case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r), unicode.Is(unicode.Cs, r),
			unicode.Is(unicode.Variation_Selector, r):
			// Deleted: no visible effect, but able to reorder or hide text.
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// Truncate shortens s to at most n runes. It never splits a rune.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n])
}

// Title prepares an untrusted plan title for display: sanitized and truncated
// to 80 runes. Callers quote the result with %q.
func Title(s string) string {
	return Truncate(Terminal(s), 80)
}

// Lines sanitizes multi-line text for a terminal while keeping its line
// structure: each line passes through Terminal, so no control sequence survives
// but the line breaks do. It treats CR LF, CR and LF as line breaks, as well as
// the Unicode line and paragraph separators and U+0085.
func Lines(s string) string {
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\u2028", "\n", "\u2029", "\n", "\u0085", "\n").Replace(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = Terminal(l)
	}
	return strings.Join(lines, "\n")
}
