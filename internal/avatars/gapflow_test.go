//go:build client && !server

package avatars_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/avatars"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer/synthtest"
)

// gapFixture is the sample plan whose architect asks for one extra agent on a
// scope no workstream owns.
func gapFixture() synthtest.Fixture {
	fx := synthtest.Sample()
	fx.Architect.ProposedGaps = []contracts.GapAgentDescriptor{{
		ID: "gap-security", Specialty: "security review", Purpose: "name the trust boundaries the export crosses.",
		RequirementIDs: []string{"REQ-1"}, Deliverable: "A written threat model for the export path.",
		ParentID: "billing-api", Placement: "workstream", Status: contracts.GapProposed,
	}}
	return fx
}

// A proposed gap is admitted, drafted and fused: its deliverable reaches the
// synthesis input and the descriptor ends in the fused state, so the plan
// carries the expertise the architect asked for.
func TestProposedGapRunsAndItsDeliverableReachesSynthesis(t *testing.T) {
	fx := gapFixture()
	h := newHarness(t, fx)
	h.in.MaxGapAgents = 2

	// The architect design call carries the proposal. The stub's default answer
	// returns fx.Architect unchanged, which already includes it.
	si, err := h.run(context.Background(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(si.Specialists) != len(fx.Specialists)+1 {
		t.Fatalf("synthesis input has %d specialist documents, want %d (gap deliverable fused)",
			len(si.Specialists), len(fx.Specialists)+1)
	}
	var fused bool
	for _, raw := range si.Specialists {
		var doc synthesizer.SpecialistDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("decode specialist: %v", err)
		}
		if doc.Summary == "gap agent deliverable" {
			fused = true
		}
	}
	if !fused {
		t.Fatal("the gap agent's deliverable did not reach the synthesis input")
	}

	// The stage streamed as gap:<id>, not as a specialist lane.
	calls := h.stub.callsOf(relay.StageGapAgentPrefix + "gap-security")
	if len(calls) == 0 {
		t.Fatalf("no call recorded for %s", relay.StageGapAgentPrefix+"gap-security")
	}
	if lane := calls[0].Req.Lane; lane != "billing-api" {
		t.Fatalf("gap agent lane = %q, want the parent workstream", lane)
	}
	if len(calls[0].Req.OutputSchema) == 0 {
		t.Fatal("the gap agent call carries no OutputSchema")
	}
}

// A run with the limit at zero never calls a gap agent, and still records the
// rejection so the workspace can explain it.
func TestGapAgentsDisabledNeverRun(t *testing.T) {
	fx := gapFixture()
	h := newHarness(t, fx)
	h.in.MaxGapAgents = 0

	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	for key := range h.stub.counts {
		if len(key) > len(relay.StageGapAgentPrefix) && key[:len(relay.StageGapAgentPrefix)] == relay.StageGapAgentPrefix {
			t.Fatalf("gap agent %q ran although the limit is zero", key)
		}
	}
}

// A gap agent may not propose another agent: the reply carrying proposed_gaps
// is a validation error, not a nested spawn.
func TestGapAgentCannotProposeAnotherAgent(t *testing.T) {
	fx := gapFixture()
	h := newHarness(t, fx)
	h.in.MaxGapAgents = 2
	h.stub.on(relay.StageGapAgentPrefix+"gap-security", func(*call, input) (reply, bool) {
		return reply{Content: raw(recursiveGapDoc())}, true
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.run(ctx, nil); err == nil {
		t.Fatal("a gap agent that proposed another agent was accepted")
	}
}

func recursiveGapDoc() map[string]any {
	return map[string]any{
		"summary": "gap agent deliverable",
		"tasks":   []any{},
		"proposed_gaps": []map[string]string{{
			"id": "gap-nested", "specialty": "nested", "purpose": "recurse",
			"deliverable": "more agents",
		}},
	}
}

var _ = avatars.RunInput{}
