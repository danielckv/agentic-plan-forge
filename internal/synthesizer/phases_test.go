package synthesizer_test

import (
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

func task(id, epic, ws string, deps ...string) contracts.TechnicalTask {
	return contracts.TechnicalTask{
		ID: id, EpicID: epic, WorkstreamID: ws, DependsOn: deps,
		HumanEstimate: contracts.Estimate{Optimistic: 1, Likely: 2, Pessimistic: 3, Basis: "b"},
		AgentEstimate: contracts.Estimate{Optimistic: 6, Likely: 12, Pessimistic: 18, Basis: "b"},
		Verification: []contracts.VerificationCommand{
			{ID: "V-" + id, Argv: []string{"go", "test", "./..."}, ExpectedExitCode: 0},
		},
	}
}

// A task sits one level deeper than the deepest task it depends on, so every
// phase is an antichain: nothing in a phase depends on anything else in it.
func TestPhasesGroupByDependencyDepth(t *testing.T) {
	tasks := []contracts.TechnicalTask{
		task("T-1", "E1", "ws-a"),
		task("T-2", "E1", "ws-a", "T-1"),
		task("T-3", "E1", "ws-b", "T-1"),
		task("T-4", "E1", "ws-b", "T-2", "T-3"),
		task("T-5", "E2", "ws-c"),
	}
	phases, err := synthesizer.Phases(tasks, []contracts.Epic{{ID: "E1", Title: "Checkout"}})
	if err != nil {
		t.Fatalf("Phases: %v", err)
	}
	if len(phases) != 3 {
		t.Fatalf("want 3 phases, got %d", len(phases))
	}
	want := [][]string{{"T-1", "T-5"}, {"T-2", "T-3"}, {"T-4"}}
	for i, ids := range want {
		if strings.Join(phases[i].TaskIDs, ",") != strings.Join(ids, ",") {
			t.Errorf("phase %d: want %v, got %v", i+1, ids, phases[i].TaskIDs)
		}
	}
	if phases[0].Number != 1 {
		t.Errorf("phases are numbered from 1, got %d", phases[0].Number)
	}
	// The title comes from the epic most of the phase's tasks serve. T-1 (E1)
	// and T-5 (E2) tie at one each, so the lower epic id wins and the title is
	// stable rather than map-order dependent.
	if phases[0].Title != "Checkout" {
		t.Errorf("phase 1 title: want the dominant epic's title, got %q", phases[0].Title)
	}
}

// A phase's gate is the union of its tasks' verification commands: that is
// what makes it independently shippable rather than merely ordered.
func TestPhaseGateCollectsVerification(t *testing.T) {
	phases, err := synthesizer.Phases([]contracts.TechnicalTask{
		task("T-1", "E1", "ws-a"),
		task("T-2", "E1", "ws-a", "T-1"),
	}, nil)
	if err != nil {
		t.Fatalf("Phases: %v", err)
	}
	if len(phases) != 2 {
		t.Fatalf("want 2 phases, got %d", len(phases))
	}
	if len(phases[0].Gate) != 1 || phases[0].Gate[0].ID != "V-T-1" {
		t.Errorf("phase 1 gate: want V-T-1, got %+v", phases[0].Gate)
	}
	// PERT of (1,2,3) is 2 hours; of (6,12,18) is 12 minutes.
	if phases[0].HumanHours != 2 {
		t.Errorf("human hours: want 2, got %v", phases[0].HumanHours)
	}
	if phases[0].AgentMinutes != 12 {
		t.Errorf("agent minutes: want 12, got %v", phases[0].AgentMinutes)
	}
}

// Identical input must produce identical phases, because the compiled spec
// promises byte-identical output.
func TestPhasesAreDeterministic(t *testing.T) {
	tasks := []contracts.TechnicalTask{
		task("T-c", "E1", "ws-a"),
		task("T-a", "E1", "ws-b"),
		task("T-b", "E1", "ws-c", "T-a", "T-c"),
	}
	first, err := synthesizer.Phases(tasks, nil)
	if err != nil {
		t.Fatalf("Phases: %v", err)
	}
	for range 8 {
		again, err := synthesizer.Phases(tasks, nil)
		if err != nil {
			t.Fatalf("Phases: %v", err)
		}
		for i := range first {
			if strings.Join(first[i].TaskIDs, ",") != strings.Join(again[i].TaskIDs, ",") {
				t.Fatalf("phase %d differs between runs: %v vs %v", i+1, first[i].TaskIDs, again[i].TaskIDs)
			}
		}
	}
	if strings.Join(first[0].TaskIDs, ",") != "T-a,T-c" {
		t.Errorf("phase 1 should be id-ordered, got %v", first[0].TaskIDs)
	}
}

// A dependency cycle is a broken plan, not a phase list.
func TestPhasesRejectACycle(t *testing.T) {
	_, err := synthesizer.Phases([]contracts.TechnicalTask{
		task("T-1", "E1", "ws-a", "T-2"),
		task("T-2", "E1", "ws-a", "T-1"),
	}, nil)
	if err == nil {
		t.Fatal("a dependency cycle must be reported, not grouped")
	}
}

func TestPhasesOfNoTasks(t *testing.T) {
	phases, err := synthesizer.Phases(nil, nil)
	if err != nil {
		t.Fatalf("Phases: %v", err)
	}
	if len(phases) != 0 {
		t.Errorf("want no phases, got %d", len(phases))
	}
}
