package synthesizer_test

import (
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer/synthtest"
)

// sampleReady is the shared fixture. Its only finding is a MEDIUM accepted
// under a tradeoff, so the base plan is ready.
func sampleReady() contracts.SynthesisInput {
	return synthtest.Sample().Input()
}

func TestReadinessReadyWhenEveryGateHolds(t *testing.T) {
	rd := synthesizer.ReadinessOf(sampleReady())
	if rd.Verdict != synthesizer.ReadinessReady {
		t.Fatalf("verdict = %q, want %q (blockers %v)", rd.Verdict, synthesizer.ReadinessReady, rd.Blockers)
	}
	if len(rd.Blockers) != 0 || len(rd.Waived) != 0 || len(rd.Integrated) != 0 {
		t.Fatalf("ready verdict carries blockers %v, %d waived, %d integrated", rd.Blockers, len(rd.Waived), len(rd.Integrated))
	}
}

// A human waiver is resolved for the gate and still listed: the plan is
// produced, and it is never called ready while the risk stands.
func TestReadinessProvisionalListsEveryWaivedFinding(t *testing.T) {
	in := synthtest.Sample().Input()
	in.Critiques = append(in.Critiques, contracts.Critique{
		ID: "C-3", Severity: contracts.SeverityHigh, Evidence: "The retry budget is unbounded.",
		RequiredChange: "Cap the retries.", Status: contracts.CritiqueResolved,
		ResolutionRef: contracts.RecordedRefPrefix + "owner waived: export volume is known",
	})
	rd := synthesizer.ReadinessOf(in)
	if rd.Verdict != synthesizer.ReadinessProvisional {
		t.Fatalf("verdict = %q, want %q", rd.Verdict, synthesizer.ReadinessProvisional)
	}
	if len(rd.Waived) != 1 || rd.Waived[0].ID != "C-3" {
		t.Fatalf("waived = %+v, want C-3 present", rd.Waived)
	}
	if len(rd.Blockers) != 0 {
		t.Fatalf("blockers = %v, want none", rd.Blockers)
	}
	spec := string(compile(t, in, contracts.ApprovalMeta{}).SpecMarkdown)
	if !strings.Contains(spec, "C-3") {
		t.Fatal("the waived finding is missing from the written readiness section")
	}
}

func TestReadinessProvisionalOnLeaderIntegration(t *testing.T) {
	in := synthtest.Sample().Input()
	in.Critiques = append(in.Critiques, contracts.Critique{
		ID: "C-4", Severity: contracts.SeverityHigh, Evidence: "The export ignores the cancellation context.",
		RequiredChange: "Thread ctx.", Status: contracts.CritiqueResolved,
		ResolutionRef: synthesizer.IntegratedRefPrefix + "the handler already threads ctx",
	})
	rd := synthesizer.ReadinessOf(in)
	if rd.Verdict != synthesizer.ReadinessProvisional {
		t.Fatalf("verdict = %q, want %q", rd.Verdict, synthesizer.ReadinessProvisional)
	}
	if len(rd.Integrated) != 1 || rd.Integrated[0].ID != "C-4" {
		t.Fatalf("integrated = %+v, want C-4", rd.Integrated)
	}
}

func TestReadinessBlockedOnOpenBlockerAndCompileRefusesIt(t *testing.T) {
	in := synthtest.Sample().Input()
	in.Critiques = append(in.Critiques, contracts.Critique{
		ID: "C-5", Severity: contracts.SeverityCritical, Evidence: "The export writes outside the run directory.",
		RequiredChange: "Confine the path.", Status: contracts.CritiqueOpen,
	})
	rd := synthesizer.ReadinessOf(in)
	if rd.Verdict != synthesizer.ReadinessBlocked {
		t.Fatalf("verdict = %q, want %q", rd.Verdict, synthesizer.ReadinessBlocked)
	}
	if len(rd.Blockers) != 1 || !strings.Contains(rd.Blockers[0], "C-5") {
		t.Fatalf("blockers = %v, want C-5 named", rd.Blockers)
	}
	if _, err := synthesizer.Compile(in, contracts.ApprovalMeta{}); err == nil {
		t.Fatal("Compile accepted a blocked candidate")
	}
}

func TestReadinessBlockedOnUnresolvedDecision(t *testing.T) {
	in := synthtest.Sample().Input()
	in.Decisions = []contracts.Decision{{ID: "D-1", Question: "Which storage backs the export?"}}
	rd := synthesizer.ReadinessOf(in)
	if rd.Verdict != synthesizer.ReadinessBlocked {
		t.Fatalf("verdict = %q, want %q", rd.Verdict, synthesizer.ReadinessBlocked)
	}
	if len(rd.Blockers) != 1 || !strings.Contains(rd.Blockers[0], "D-1") {
		t.Fatalf("blockers = %v, want D-1 named", rd.Blockers)
	}
}

// Verification is specified by the plan and executed by nobody during
// compilation: the two lists must never be confused.
func TestReadinessVerificationIsSpecifiedNotExecuted(t *testing.T) {
	rd := synthesizer.ReadinessOf(synthtest.Sample().Input())
	if len(rd.VerificationSpecified) == 0 {
		t.Fatal("the sample plan names verification commands; none were recorded as specified")
	}
	if len(rd.VerificationExecuted) != 0 {
		t.Fatalf("compilation executed %v; it runs no command", rd.VerificationExecuted)
	}
}

func TestCompiledPlanCarriesTheSameVerdictItWrote(t *testing.T) {
	in := synthtest.Sample().Input()
	plan := compile(t, in, contracts.ApprovalMeta{})
	want := synthesizer.ReadinessOf(in)
	if plan.Readiness.Verdict != want.Verdict {
		t.Fatalf("compiled verdict = %q, ReadinessOf = %q", plan.Readiness.Verdict, want.Verdict)
	}
	if !strings.Contains(string(plan.SpecMarkdown), "**Verdict: `"+want.Verdict+"`.**") {
		t.Fatalf("spec does not state the verdict %q", want.Verdict)
	}
}
