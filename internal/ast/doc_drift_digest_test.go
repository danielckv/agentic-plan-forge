//go:build client && !server

package ast

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// driftItems builds n items of one type, each large enough that a few thousand
// of them pass any stage bound.
func driftItems(prefix, driftType string, n int, path string) []contracts.DocDriftItem {
	out := make([]contracts.DocDriftItem, 0, n)
	for i := range n {
		out = append(out, contracts.DocDriftItem{
			ID:              prefix + string(rune('a'+i%26)) + strings.Repeat("0", 3),
			DocFilePath:     path,
			DocLine:         i + 1,
			DriftType:       driftType,
			Expected:        strings.Repeat("x", 400),
			Observed:        strings.Repeat("y", 400),
			EvidenceKind:    EvidenceUndocumented,
			Confidence:      "MEDIUM",
			RemediationTask: strings.Repeat("z", 200),
		})
	}
	return out
}

// The drift list is the one part of a stage payload the scan did not bound:
// per-document caps still let a docs-heavy repository produce megabytes. The
// digest must fit the budget and say that it cut.
func TestDriftDigestFitsTheBudget(t *testing.T) {
	items := append(driftItems("m", contracts.DriftMissingDoc, 2000, "docs/guide.md"),
		driftItems("s", contracts.DriftStaleSignature, 10, "docs/api.md")...)

	got, info := DriftDigest(items, DriftDigestOptions{})

	size := len(mustMarshal(t, got))
	if size > DriftBudgetBytes {
		t.Fatalf("digest is %d bytes, over the %d byte budget", size, DriftBudgetBytes)
	}
	if !info.Truncated {
		t.Error("info.Truncated is false after a cut")
	}
	if info.IncludedItems != len(got) || info.TotalItems != len(items) {
		t.Errorf("info counts %d of %d, got %d items", info.IncludedItems, info.TotalItems, len(got))
	}
	if !strings.Contains(info.Statement, "documentation drift") {
		t.Errorf("info.Statement = %q, want it to name what was cut", info.Statement)
	}
}

// Confirmed drift is evidence a plan can act on; UNVERIFIABLE is a coverage
// note. A cut keeps the confirmed items even when thousands of weaker ones
// come first in document order.
func TestDriftDigestKeepsConfirmedDriftOverUnverifiable(t *testing.T) {
	items := append(driftItems("u", contracts.DriftUnverifiable, 2000, "docs/aaa.md"),
		driftItems("s", contracts.DriftStaleSignature, 5, "docs/zzz.md")...)

	got, _ := DriftDigest(items, DriftDigestOptions{})

	stale := 0
	for _, it := range got {
		if it.DriftType == contracts.DriftStaleSignature {
			stale++
		}
	}
	if stale != 5 {
		t.Fatalf("the digest kept %d of 5 STALE_SIGNATURE items", stale)
	}
	if got[0].DriftType != contracts.DriftStaleSignature {
		t.Errorf("the digest leads with %q, want the confirmed drift first", got[0].DriftType)
	}
}

// Items whose document or symbol matches the story keywords rank above the
// rest of their tier, so a cut keeps the drift the plan is about.
func TestDriftDigestRanksKeywordMatchesFirst(t *testing.T) {
	items := append(driftItems("o", contracts.DriftMissingDoc, 2000, "docs/other.md"),
		driftItems("b", contracts.DriftMissingDoc, 5, "docs/billing.md")...)

	got, _ := DriftDigest(items, DriftDigestOptions{Keywords: []string{"billing"}})

	if got[0].DocFilePath != "docs/billing.md" {
		t.Fatalf("the digest leads with %q, want the keyword match first", got[0].DocFilePath)
	}
}

// A list that already fits is handed over unchanged, in the order detection
// produced, with no statement to explain.
func TestDriftDigestLeavesASmallListAlone(t *testing.T) {
	items := driftItems("s", contracts.DriftStaleSignature, 3, "docs/api.md")

	got, info := DriftDigest(items, DriftDigestOptions{})

	if len(got) != len(items) || got[0].ID != items[0].ID {
		t.Fatalf("the digest changed a list that fits: %d items, first %q", len(got), got[0].ID)
	}
	if info.Truncated || info.Statement != "" {
		t.Errorf("info reports a cut that did not happen: %+v", info)
	}
}

// A nil list stays an empty list: the payload field is always present.
func TestDriftDigestOfNothingIsEmpty(t *testing.T) {
	got, info := DriftDigest(nil, DriftDigestOptions{})
	if got == nil || len(got) != 0 {
		t.Fatalf("DriftDigest(nil) = %v, want an empty list", got)
	}
	if info.Truncated {
		t.Error("info.Truncated is true for an empty list")
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
