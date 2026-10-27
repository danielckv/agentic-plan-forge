//go:build client && !server

package avatars_test

import (
	"context"
	"testing"
	"time"
)

// overlaps reports whether two half-open sequence intervals intersect. A call
// still running when the run ended has End == 0 and is treated as open.
func overlaps(aStart, aEnd, bStart, bEnd int) bool {
	if aEnd == 0 || bEnd == 0 {
		return true
	}
	return aStart < bEnd && bStart < aEnd
}

// The architect's diagrams and documentation batches must run beside the
// specialists, not before them. The specialists read the summary, the
// constraints, the workstreams and the contracts, and the design part carries
// all four.
//
// Measured against this repository on 2026-10-25: the architect spent 43m46s
// over 15 calls, 13 of them documentation batches, and every one of those 13
// blocked the specialists from starting.
func TestDiagramsAndDocBatchesRunBesideTheSpecialists(t *testing.T) {
	h := newHarness(t, bigFixture(2)) // four workstreams on MaxParallel=3, so specialists queue
	h.in.MaxParallel = 3
	h.stub.delay = 15 * time.Millisecond

	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	design := h.stub.callsOf("architect")
	if len(design) != 1 {
		t.Fatalf("design calls = %d, want 1", len(design))
	}
	rest := append(h.stub.callsOf(keyArchDiagrams), h.stub.callsOf(keyArchDocs)...)
	if len(rest) == 0 {
		t.Fatal("the fixture produced no diagram or documentation calls; nothing to overlap")
	}

	var specialists []*call
	for _, c := range h.stub.allCalls() {
		if len(c.Key) > 11 && c.Key[:11] == "specialist:" {
			specialists = append(specialists, c)
		}
	}
	if len(specialists) == 0 {
		t.Fatal("no specialist ran")
	}

	// Every specialist starts after the design part finished.
	for _, s := range specialists {
		if design[0].End == 0 || s.Start < design[0].End {
			t.Errorf("%s started at %d, before the design part finished at %d",
				s.Key, s.Start, design[0].End)
		}
	}

	// At least one of the architect's other parts ran while a specialist did.
	found := ""
	for _, r := range rest {
		for _, s := range specialists {
			if overlaps(r.Start, r.End, s.Start, s.End) {
				found = r.Key + " with " + s.Key
			}
		}
	}
	if found == "" {
		t.Errorf("no architect part overlapped a specialist; the %d diagram and documentation calls still block the stage", len(rest))
	} else {
		t.Logf("overlapped: %s", found)
	}
}

// One semaphore now bounds the whole cycle. Two stages each holding a limit of
// par would put 2*par calls in flight, which is exactly what overlapping them
// would otherwise cause.
func TestTheCycleNeverExceedsMaxParallelAcrossBothStages(t *testing.T) {
	for _, maxParallel := range []int{1, 2, 3} {
		h := newHarness(t, bigFixture(2))
		h.in.MaxParallel = maxParallel
		h.stub.delay = 12 * time.Millisecond

		if _, err := h.run(context.Background(), nil); err != nil {
			t.Fatalf("MaxParallel=%d: Run: %v", maxParallel, err)
		}
		if got := h.stub.maxConcurrent(); got > maxParallel {
			t.Errorf("MaxParallel=%d: %d calls were in flight at once", maxParallel, got)
		}
	}
}

// The architect's draft still reaches the room, and it carries the parts that
// were drawn while the specialists worked.
func TestTheAssembledArchitectDraftIsStillPublished(t *testing.T) {
	h := newHarness(t, bigFixture(2))

	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	drafts := h.sink.drafts("architect")
	if len(drafts) != 1 || !drafts[0].Complete || drafts[0].ContentHash == "" {
		t.Fatalf("architect drafts = %+v, want one complete draft", drafts)
	}
}
