//go:build client && !server

package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer/synthtest"
)

// exportSession is a session whose executor holds an approved plan, over a
// repository that has the documentation directories a real project has.
func exportSession(t *testing.T) *appSession {
	t.Helper()
	repo := t.TempDir()
	exec := plannedExecutor(t)
	// The synthesizer's own fixture, so a failure here is an export failure
	// rather than an input the compiler would reject.
	in := synthtest.Sample().Input()
	in.Decisions = []contracts.Decision{{
		ID:               "D1",
		Question:         "Where should a large export be written?",
		ResolvedOptionID: "stream",
		Options: []contracts.DecisionOption{
			{ID: "stream", Label: "Stream it"},
			{ID: "buffer", Label: "Buffer in memory"},
		},
	}}
	in.Decisions[0].ResolvedOptionID = "stream"
	in.Decisions[0].Options[0].ID = "stream"
	in.Decisions[0].Options[1].ID = "buffer"
	exec.synthesized = &in
	exec.approvalID = "APR-EXPORT-1"
	return &appSession{repo: repo, exec: exec}
}

// plannedPaths is the plan and specification an export of this session would
// write, so a test can name the file it expects to collide without hard-coding
// a title from a fixture.
func plannedPaths(t *testing.T, s *appSession) (plan, spec string) {
	t.Helper()
	input, meta, ok := s.exec.approvedInput()
	if !ok {
		t.Fatal("the session has no approved input")
	}
	bundle, err := synthesizer.BuildBundle(input, meta, s.bundleOptions())
	if err != nil {
		t.Fatalf("BuildBundle: %v", err)
	}
	for _, entry := range bundle.Entries {
		switch entry.Kind {
		case synthesizer.KindPlan:
			plan = entry.Path
		case synthesizer.KindSpec:
			spec = entry.Path
		}
	}
	if plan == "" || spec == "" {
		t.Fatalf("bundle has no plan/spec: %+v", bundle.Entries)
	}
	return plan, spec
}

// readExported returns every generated document the report names.
func readExported(t *testing.T, repo string, report ExportReport) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, rel := range report.Committed {
		raw, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		out[rel] = string(raw)
	}
	return out
}

// The export writes the plan, the specification and one record per decision
// that was actually made. It is the one path that puts generated files into the
// repository, so it must land them where a reader expects them.
func TestExportWritesTheApprovedPlan(t *testing.T) {
	s := exportSession(t)
	planPath, specPath := plannedPaths(t, s)

	report := s.ExportDocuments(context.Background(), ExportRequest{Confirmed: true})
	if report.State != "exported" {
		t.Fatalf("state = %q (%s), want exported", report.State, report.Problem)
	}
	files := readExported(t, s.repo, report)
	if len(files) != 3 {
		t.Fatalf("wrote %d documents, want plan, spec and one decision record: %v", len(files), report.Committed)
	}
	if _, ok := files[planPath]; !ok {
		t.Errorf("no plan at %q: %v", planPath, report.Committed)
	}
	if _, ok := files[specPath]; !ok {
		t.Errorf("no specification at %q: %v", specPath, report.Committed)
	}
	if !strings.Contains(files[planPath], "## Tasks") {
		t.Error("the plan carries no task section")
	}

	// The decision record carries the question that was decided, and is
	// numbered after the last record the repository already had.
	var adrPath string
	for rel := range files {
		if strings.HasPrefix(rel, "docs/adrs/") {
			adrPath = rel
		}
	}
	if adrPath == "" {
		t.Fatalf("no decision record was written: %v", report.Committed)
	}
	if !strings.Contains(files[adrPath], "Where should a large export be written?") {
		t.Error("the decision record does not carry the question that was decided")
	}
}

// Nothing is written without an explicit confirmation: the export is a
// deliberate act, never a side effect of approving a plan.
func TestExportNeedsConfirmation(t *testing.T) {
	s := exportSession(t)

	report := s.ExportDocuments(context.Background(), ExportRequest{})
	if report.State != "confirm" {
		t.Fatalf("state = %q, want confirm", report.State)
	}
	if len(report.Committed) != 0 {
		t.Errorf("wrote %v without confirmation", report.Committed)
	}
	if entries, _ := os.ReadDir(filepath.Join(s.repo, "docs")); len(entries) != 0 {
		t.Errorf("created %d directories without confirmation", len(entries))
	}
}

// A destination that already holds a document stops the whole set before the
// first write. A generated file never replaces one a person wrote, and a
// half-written set is worse than none.
func TestExportRefusesToOverwriteADocument(t *testing.T) {
	s := exportSession(t)
	planPath, _ := plannedPaths(t, s)
	writeTestFile(t, s.repo, planPath, "# A plan a person wrote\n")

	report := s.ExportDocuments(context.Background(), ExportRequest{Confirmed: true})
	if report.State != "collision" {
		t.Fatalf("state = %q (%s), want collision", report.State, report.Problem)
	}
	if !strings.Contains(report.Problem, planPath) {
		t.Errorf("problem = %q, want it to name %q", report.Problem, planPath)
	}

	// The document a person wrote is untouched.
	raw, err := os.ReadFile(filepath.Join(s.repo, filepath.FromSlash(planPath)))
	if err != nil || string(raw) != "# A plan a person wrote\n" {
		t.Fatalf("the existing document was modified: %q err=%v", raw, err)
	}
	// And no other document was written either.
	if entries, _ := os.ReadDir(filepath.Join(s.repo, "docs/specs")); len(entries) != 0 {
		t.Errorf("wrote %d documents before refusing", len(entries))
	}
}

// A second export of the same approval is success, not a second set: re-running
// after a crash finishes the job rather than doubling it.
func TestExportIsIdempotent(t *testing.T) {
	s := exportSession(t)

	first := s.ExportDocuments(context.Background(), ExportRequest{Confirmed: true})
	if first.State != "exported" {
		t.Fatalf("first export: %q (%s)", first.State, first.Problem)
	}
	second := s.ExportDocuments(context.Background(), ExportRequest{Confirmed: true})
	if second.State == "exported" && len(second.Committed) > 0 {
		t.Errorf("a re-run wrote %v again, want it recognised as already committed", second.Committed)
	}
}

// An approval from another session is refused: the window cannot ask one
// session to write another's plan.
func TestExportRefusesAnotherApproval(t *testing.T) {
	s := exportSession(t)

	report := s.ExportDocuments(context.Background(), ExportRequest{Confirmed: true, ApprovalID: "APR-OTHER"})
	if report.State != "mismatch" {
		t.Fatalf("state = %q, want mismatch", report.State)
	}
}

// Without an approved plan there is nothing to write, and the report says so
// instead of creating empty documents.
func TestExportWithoutAnApprovedPlan(t *testing.T) {
	s := exportSession(t)
	s.exec.synthesized = nil

	report := s.ExportDocuments(context.Background(), ExportRequest{Confirmed: true})
	if report.State != "unavailable" {
		t.Fatalf("state = %q, want unavailable", report.State)
	}
	if !strings.Contains(report.Problem, "approved first") {
		t.Errorf("problem = %q, want it to name the missing approval", report.Problem)
	}
}

// The decision-record number continues the repository's own history, so an
// export never reuses a number a person already took.
func TestExportSkipsTakenADRNumber(t *testing.T) {
	s := exportSession(t)
	writeTestFile(t, s.repo, "docs/adrs/0017-stream-it.md", "# Taken\n")

	report := s.ExportDocuments(context.Background(), ExportRequest{Confirmed: true})
	if report.State != "exported" {
		t.Fatalf("state = %q (%s)", report.State, report.Problem)
	}
	for _, rel := range report.Committed {
		if rel == "docs/adrs/0017-stream-it.md" {
			t.Fatal("reused a decision-record number a person already wrote")
		}
	}
}

// The report is JSON the window can read, so its state survives the bridge.
func TestExportReportMarshals(t *testing.T) {
	report := ExportReport{State: "exported", Committed: []string{"docs/plans/a.md"}}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\"state\":\"exported\"") {
		t.Errorf("marshalled report = %s", raw)
	}
}
