//go:build client && !server

package ast

import (
	"fmt"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// smallDriftItems builds compact items, so a few hundred of them still fit the
// byte budget. That is the case the item budget exists for.
func smallDriftItems(prefix, driftType, confidence string, n int) []contracts.DocDriftItem {
	out := make([]contracts.DocDriftItem, 0, n)
	for i := range n {
		out = append(out, contracts.DocDriftItem{
			ID:              fmt.Sprintf("%s-%d", prefix, i),
			DocFilePath:     fmt.Sprintf("docs/p%d.md", i),
			DocLine:         i + 1,
			DriftType:       driftType,
			EvidenceKind:    EvidenceUndocumented,
			Confidence:      confidence,
			RemediationTask: "document it",
		})
	}
	return out
}

// A byte budget does not bound the work the list causes. The architect writes
// one documentation task per confirmed item and answers 20 items a call, so the
// item count decides how many provider calls the stage makes. Measured against
// this repository on 2026-10-25: 249 items fitted the 128 KiB byte budget and
// became 13 of the architect's 15 calls.
func TestDriftDigestBoundsTheItemCountAndNotOnlyTheBytes(t *testing.T) {
	items := smallDriftItems("m", contracts.DriftMissingDoc, "MEDIUM", 249)
	if size := len(mustMarshal(t, items)); size > DriftBudgetBytes {
		t.Fatalf("the fixture must fit the byte budget to prove the point: %d > %d", size, DriftBudgetBytes)
	}

	kept, info := DriftDigest(items, DriftDigestOptions{})

	if len(kept) != DriftItemBudget {
		t.Errorf("kept %d items, want the %d-item budget", len(kept), DriftItemBudget)
	}
	if !info.Truncated || !info.ItemBound {
		t.Errorf("info = %+v, want a truncated, item-bound digest", info)
	}
	if !strings.Contains(info.Statement, "most useful items") {
		t.Errorf("the statement names a size limit for a count cut: %q", info.Statement)
	}
	if !strings.Contains(info.Statement, "249") {
		t.Errorf("the statement does not say how many there were: %q", info.Statement)
	}
}

// The cap keeps what a plan can act on: confirmed drift outranks a missing
// document, whatever order detection found them in.
func TestTheItemCapKeepsConfirmedDriftFirst(t *testing.T) {
	items := append(smallDriftItems("m", contracts.DriftMissingDoc, "LOW", 100),
		smallDriftItems("s", contracts.DriftStaleSignature, "HIGH", 5)...)

	kept, _ := DriftDigest(items, DriftDigestOptions{})

	stale := 0
	for _, it := range kept {
		if it.DriftType == contracts.DriftStaleSignature {
			stale++
		}
	}
	if stale != 5 {
		t.Errorf("kept %d of 5 confirmed drift items; the cap dropped actionable drift", stale)
	}
}

// A negative MaxItems removes the item bound, which is how a caller that wants
// exhaustive documentation coverage asks for it.
func TestANegativeItemBudgetLeavesOnlyTheByteBound(t *testing.T) {
	items := smallDriftItems("m", contracts.DriftMissingDoc, "MEDIUM", 120)

	kept, info := DriftDigest(items, DriftDigestOptions{MaxItems: -1})

	if len(kept) != 120 || info.Truncated {
		t.Errorf("kept %d of 120 with truncated=%v; a negative budget must not cut by count", len(kept), info.Truncated)
	}
}

// The byte budget still rules when both could bite: a short list of enormous
// items is cut for its size, and the statement names that bound, not the count.
func TestTheByteBudgetStillCutsAListUnderTheItemCap(t *testing.T) {
	items := driftItems("m", contracts.DriftMissingDoc, 50, "docs/guide.md")

	// 50 items is under the 60-item cap, so only the size can cut here.
	kept, info := DriftDigest(items, DriftDigestOptions{MaxBytes: 8 << 10})

	if len(kept) >= 50 {
		t.Fatalf("kept %d of 50 large items; the byte budget did not cut", len(kept))
	}
	if info.ItemBound {
		t.Errorf("info says the item budget cut a list of %d, under the %d cap", len(items), DriftItemBudget)
	}
	if !strings.Contains(info.Statement, "KiB stage budget") {
		t.Errorf("the statement does not name the size limit that cut: %q", info.Statement)
	}
}
