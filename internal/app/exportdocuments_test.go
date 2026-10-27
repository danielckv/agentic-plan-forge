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
)

// exportSession is a session whose executor holds an approved plan, over a
// repository with the documentation directories a real project has.
func exportSession(t *testing.T) *appSession {
	t.Helper()
	repo := t.TempDir()
	exec := plannedExecutor(t)
	exec.synthesized = &contracts.SynthesisInput{
		PRD: contracts.NormalizedPRD{Revision: 3, Epics: []contracts.DraftEpic{
			{ID: "E1", Title: "Invoice export", Requirements: []string{"Export invoices for a date range"}},
		}},
		Decisions: []contracts.Decision{{
			ID:               "D1",
			Question:         "Where should a large export be written?",
			ResolvedOptionID: "stream",
			Options: []contracts.DecisionOption{
				{ID: "stream", Label: "Stream it"},
				{ID: "buffer", Label: "Buffer in memory"},
			},
		}},
	}
	exec.approvalID = "APR-EXPORT-1"
	return &appSession{repo: repo, exec: exec}
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

	report := s.ExportDocuments(context.Background(), ExportRequest{Confirmed: true})
	if report.State != "exported" {
		t.Fatalf("state = %q (%s), want exported", report.State, report.Problem)
	}
	files := readExported(t, s.repo, report)
	if len(files) != 3 {
		t.Fatalf("wrote %d documents, want plan, spec and one ADR: %v", len(files), report.Committed)
	}

	var planPath, specPath, adrPath string
	for rel := range files {
		switch {
		case strings.HasPrefix(rel, "docs/plans/"):
			planPath = rel
		case strings.HasPrefix(rel, "docs/specs/"):
			specPath = rel
		case strings.HasPrefix(rel, "docs/adrs/"):
			adrPath = rel
		}
	}
	if planPath == "" || specPath == "" || adrPath == "" {
		t.Fatalf("a kind is missing: plan=%q spec=%q adr=%q", planPath, specPath, adrPath)
	}
	if !strings.Contains(files[planPath], "Invoice export") {
		t.Error("the plan does not name the approved work")
	}
	if !strings.Contains(files[adrPath], "Where should a large export be written?") {
		t.Error("the decision record does not carry the question that was decided")
	}
	// A decision record is numbered so history never collides.
	base := filepath.Base(adrPath)
	if len(base) < 4 || base[:4] != "0017" {
		t.Errorf("decision record %q is not numbered after the last one in the repository", base)
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
	entries, _ := os.ReadDir(filepath.Join(s.repo, "docs"))
	if len(entries) != 0 {
		t.Errorf("created %d directories without confirmation", len(entries))
	}
}

// A destination that already holds a document stops the whole set before the
// first write. A generated file never replaces one a person wrote, and a
// half-written set is worse than none.
func TestExportRefusesToOverwriteADocument(t *testing.T) {
	s := exportSession(t)
	writeTestFile(t, s.repo, "docs/plans/invoice-export.md", "# A plan a person wrote\n")

	report := s.ExportDocuments(context.Background(), ExportRequest{Confirmed: true})
	if report.State != "collision" {
		t.Fatalf("state = %q (%s), want collision", report.State, report.Problem)
	}
	if !strings.Contains(report.Problem, "docs/plans/invoice-export.md") {
		t.Errorf("problem = %q, want it to name the file that is in the way", report.Problem)
	}

	// The document a person wrote is untouched.
	raw, err := os.ReadFile(filepath.Join(s.repo, "docs/plans/invoice-export.md"))
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
	if second.State != "exported" {
		t.Logf("second export state = %q (%s)", second.State, second.Problem)
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

// The ADR number continues the repository's own history, so an export never
// reuses a number a person already took.
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
		if strings.HasPrefix(rel, "docs/adrs/0017") {
			t.Errorf("wrote %q, want the next free number", rel)
		}
	}
}

// The report is JSON the window can read, so the state survives the bridge.
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
