//go:build client && !server

package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Secret-scan tuning from the plan: known token shapes, plus any string of at
// least 24 characters whose Shannon entropy is at least 4.0 bits per character.
const (
	entropyMinLen     = 24
	entropyThreshold  = 4.0
	secretPreviewKeep = 4
)

type secretPattern struct {
	kind string
	re   *regexp.Regexp
}

var secretPatterns = []secretPattern{
	{"aws-access-key", regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA|AIDA|AROA|ANPA)[0-9A-Z]{16}\b`)},
	{"aws-secret-key", regexp.MustCompile(`(?i)aws_?secret_?access_?key["']?\s*[:=]\s*["']?[A-Za-z0-9/+=]{40}`)},
	{"github-token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b`)},
	{"github-token", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}\b`)},
	{"slack-token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}\b`)},
	{"anthropic-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"openai-key", regexp.MustCompile(`\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}`)},
	{"google-api-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"private-key", regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`)},
}

// entropyToken matches runs of characters that make up opaque secrets: base64,
// base64url, hex and API-key alphabets.
var entropyToken = regexp.MustCompile(`[A-Za-z0-9+/=_\-.]{24,}`)

// SecretHit is one possible secret found in the shared document. Start and End
// are byte offsets into the scanned text; they stay on the machine and are never
// sent to the UI.
type SecretHit struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Line  int    `json:"line"`
	Start int    `json:"-"`
	End   int    `json:"-"`
	// Preview shows the first characters only, so the chip identifies the hit
	// without repeating the secret.
	Preview string `json:"preview"`
}

// ScanSecrets reports possible secrets in text, ordered by position, with
// overlapping findings merged (a token matched by both a known shape and the
// entropy rule is one hit).
func ScanSecrets(text string) []SecretHit {
	var raw []SecretHit
	for _, p := range secretPatterns {
		for _, m := range p.re.FindAllStringIndex(text, -1) {
			raw = append(raw, newHit(text, p.kind, m[0], m[1]))
		}
	}
	for _, m := range entropyToken.FindAllStringIndex(text, -1) {
		tok := text[m[0]:m[1]]
		if len(tok) >= entropyMinLen && shannon(tok) >= entropyThreshold {
			raw = append(raw, newHit(text, "high-entropy-string", m[0], m[1]))
		}
	}
	sort.SliceStable(raw, func(i, j int) bool {
		if raw[i].Start != raw[j].Start {
			return raw[i].Start < raw[j].Start
		}
		return raw[i].End > raw[j].End
	})
	var out []SecretHit
	for _, h := range raw {
		if n := len(out); n > 0 && h.Start < out[n-1].End {
			if h.End > out[n-1].End && out[n-1].Kind == "high-entropy-string" {
				// A known shape that extends an entropy hit wins.
				out[n-1] = h
			}
			continue
		}
		out = append(out, h)
	}
	return out
}

func newHit(text, kind string, start, end int) SecretHit {
	match := text[start:end]
	sum := sha256.Sum256([]byte(kind + "\x00" + match))
	preview := match
	if r := []rune(match); len(r) > secretPreviewKeep {
		preview = string(r[:secretPreviewKeep]) + "…"
	}
	return SecretHit{
		ID:      hex.EncodeToString(sum[:6]),
		Kind:    kind,
		Line:    1 + strings.Count(text[:start], "\n"),
		Start:   start,
		End:     end,
		Preview: preview,
	}
}

// shannon returns the Shannon entropy of s in bits per byte.
func shannon(s string) float64 {
	if s == "" {
		return 0
	}
	var freq [256]int
	for i := range len(s) {
		freq[s[i]]++
	}
	n := float64(len(s))
	var h float64
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

// redactionText is what replaces a redacted secret.
func redactionText(kind string) string { return "[REDACTED:" + kind + "]" }

// utf16Len is the length of s in UTF-16 code units, the unit CodeMirror
// ChangeSet offsets use.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r == utf8.RuneError {
			n++
			continue
		}
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}

// redactionChanges builds the ChangeSet.toJSON() array that replaces
// text[start:end] with the redaction marker: retain the prefix, replace the
// match, retain the suffix.
func redactionChanges(text string, start, end int, kind string) (json.RawMessage, error) {
	if start < 0 || end > len(text) || start >= end {
		return nil, fmt.Errorf("redaction range %d-%d is outside the document", start, end)
	}
	var parts []any
	if pre := utf16Len(text[:start]); pre > 0 {
		parts = append(parts, pre)
	}
	parts = append(parts, []any{utf16Len(text[start:end]), redactionText(kind)})
	if post := utf16Len(text[end:]); post > 0 {
		parts = append(parts, post)
	}
	return json.Marshal(parts)
}
