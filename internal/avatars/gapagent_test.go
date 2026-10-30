//go:build client && !server

package avatars

import (
	"fmt"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

func gapPM(storyID, reqID string) synthesizer.PMDoc {
	return synthesizer.PMDoc{
		Epics: []contracts.Epic{{ID: "epic-1", Title: "Epic one", StoryIDs: []string{storyID}}},
		Stories: []contracts.Story{{
			ID: storyID, EpicID: "epic-1", Actor: "owner", Need: "a thing",
			AcceptanceCriteria: []string{"it works"}, RequirementIDs: []string{reqID},
		}},
	}
}

func gapArch() synthesizer.ArchitectDoc {
	return synthesizer.ArchitectDoc{
		Summary: "a design",
		Workstreams: []contracts.Workstream{
			{ID: "ws-store", Role: "persistence"},
		},
	}
}

func gapProposal(id, reqID, parent string) contracts.GapAgentDescriptor {
	return contracts.GapAgentDescriptor{
		ID: id, Specialty: "Query planning", Purpose: "cover index selection",
		RequirementIDs: []string{reqID}, Deliverable: "A query plan memo",
		ParentID: parent, Placement: "workstream", Status: contracts.GapProposed,
	}
}

func TestAdmitGapsAdmitsValidProposal(t *testing.T) {
	pm := gapPM("story-1", "REQ-1")
	arch := gapArch()
	arch.ProposedGaps = []contracts.GapAgentDescriptor{gapProposal("gap-1", "REQ-1", "ws-store")}

	admitted, rejected := AdmitGaps(arch, pm, 2)
	if len(admitted) != 1 || len(rejected) != 0 {
		t.Fatalf("admit: got %d admitted, %d rejected, want 1, 0", len(admitted), len(rejected))
	}
	if admitted[0].Status != contracts.GapAdmitted {
		t.Fatalf("status = %q, want %q", admitted[0].Status, contracts.GapAdmitted)
	}
}

func TestAdmitGapsRejectsUnknownRequirement(t *testing.T) {
	pm := gapPM("story-1", "REQ-1")
	arch := gapArch()
	arch.ProposedGaps = []contracts.GapAgentDescriptor{gapProposal("gap-1", "REQ-9", "")}

	admitted, rejected := AdmitGaps(arch, pm, 2)
	if len(admitted) != 0 || len(rejected) != 1 {
		t.Fatalf("got %d admitted, %d rejected, want 0, 1", len(admitted), len(rejected))
	}
}

func TestAdmitGapsRejectsUnknownParent(t *testing.T) {
	pm := gapPM("story-1", "REQ-1")
	arch := gapArch()
	g := gapProposal("gap-1", "REQ-1", "ws-missing")
	arch.ProposedGaps = []contracts.GapAgentDescriptor{g}

	admitted, rejected := AdmitGaps(arch, pm, 2)
	if len(admitted) != 0 || len(rejected) != 1 {
		t.Fatalf("got %d admitted, %d rejected, want 0, 1", len(admitted), len(rejected))
	}
}

func TestAdmitGapsRejectsDuplicateScope(t *testing.T) {
	pm := gapPM("story-1", "REQ-1")
	arch := gapArch()
	arch.Workstreams[0].Role = "Query planning"
	arch.ProposedGaps = []contracts.GapAgentDescriptor{gapProposal("gap-1", "REQ-1", "")}

	// The workstream already owns the scope, so the proposal adds nothing.
	admitted, rejected := AdmitGaps(arch, pm, 2)
	if len(admitted) != 0 || len(rejected) != 1 {
		t.Fatalf("got %d admitted, %d rejected, want 0, 1", len(admitted), len(rejected))
	}
}

func TestAdmitGapsEnforcesCap(t *testing.T) {
	pm := gapPM("story-1", "REQ-1")
	arch := gapArch()
	first, second, third := gapProposal("gap-1", "REQ-1", ""), gapProposal("gap-2", "REQ-1", ""), gapProposal("gap-3", "REQ-1", "")
	second.Specialty = "Caching strategy"
	third.Specialty = "Migration tooling"
	arch.ProposedGaps = []contracts.GapAgentDescriptor{first, second, third}

	admitted, rejected := AdmitGaps(arch, pm, 2)
	if len(admitted) != 2 || len(rejected) != 1 {
		t.Fatalf("got %d admitted, %d rejected, want 2, 1", len(admitted), len(rejected))
	}
	if rejected[0].ID != "gap-3" || rejected[0].Status != contracts.GapRejected {
		t.Fatalf("rejected = %+v, want gap-3 rejected", rejected[0])
	}
}

// A run with the limit at zero still accounts for what the model asked for:
// every proposal is rejected with the reason, so the owner can see why no
// agent ran. Nothing is silently dropped.
func TestAdmitGapsZeroCapRecordsEveryProposalAsRejected(t *testing.T) {
	pm := gapPM("story-1", "REQ-1")
	arch := gapArch()
	arch.ProposedGaps = []contracts.GapAgentDescriptor{gapProposal("gap-1", "REQ-1", "")}

	admitted, rejected := AdmitGaps(arch, pm, 0)
	if len(admitted) != 0 || len(rejected) != 1 {
		t.Fatalf("got %d admitted, %d rejected, want 0, 1", len(admitted), len(rejected))
	}
	if rejected[0].Status != contracts.GapRejected || rejected[0].Reason == "" {
		t.Fatalf("rejected = %+v, want a recorded reason", rejected[0])
	}
}

// Every proposal past the per-reply bound is retained as rejected, never
// truncated: the run history shows what the architect asked for.
func TestAdmitGapsRetainsOverflowPastTheProposalBound(t *testing.T) {
	pm := gapPM("story-1", "REQ-1")
	arch := gapArch()
	for i := range maxGapProposals + 3 {
		g := gapProposal(fmt.Sprintf("gap-%d", i), "REQ-1", "")
		g.Specialty = fmt.Sprintf("scope %d", i)
		arch.ProposedGaps = append(arch.ProposedGaps, g)
	}

	admitted, rejected := AdmitGaps(arch, pm, 1)
	if len(admitted) != 1 {
		t.Fatalf("admitted = %d, want 1", len(admitted))
	}
	if len(rejected) != maxGapProposals+2 {
		t.Fatalf("rejected = %d, want %d (every proposal accounted for)", len(rejected), maxGapProposals+2)
	}
	for _, r := range rejected {
		if r.Status != contracts.GapRejected || r.Reason == "" {
			t.Fatalf("rejected proposal %q has no recorded reason: %+v", r.ID, r)
		}
	}
}

func TestAdmitGapsSecondProposalCannotShadowAdmittedScope(t *testing.T) {
	pm := gapPM("story-1", "REQ-1")
	arch := gapArch()
	first := gapProposal("gap-1", "REQ-1", "")
	second := gapProposal("gap-2", "REQ-1", "")
	second.Specialty = "query-planning specialist"
	arch.ProposedGaps = []contracts.GapAgentDescriptor{first, second}

	admitted, rejected := AdmitGaps(arch, pm, 3)
	if len(admitted) != 1 || len(rejected) != 1 {
		t.Fatalf("got %d admitted, %d rejected, want 1, 1", len(admitted), len(rejected))
	}
}

func TestRunStateClonesGapState(t *testing.T) {
	st := newRunState()
	st.Gaps = []contracts.GapAgentDescriptor{gapProposal("gap-1", "REQ-1", "")}
	st.GapDocs["gap-1"] = draftRec{Raw: []byte("{}"), Cycle: 1, Rev: 1}
	st.GapsAdmitted = true

	c := st.clone()
	c.Gaps[0].ID = "changed"
	c.GapDocs["gap-2"] = draftRec{}
	if st.Gaps[0].ID != "gap-1" || len(st.GapDocs) != 1 {
		t.Fatal("clone shares gap state with the original")
	}
}
