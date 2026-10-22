//go:build client && !server

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/planstate"
	"github.com/danielckv/agentic-plan-forge/internal/storage"
)

const pullSpec = "# Invoice export\n\n## Executive summary\n\nExports invoices.\n"

func pullWrite(t *testing.T, repo, rel, body string) {
	t.Helper()
	p := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pullRepo returns a symlink-free repository holding a two-task plan. The plan
// is recorded in the approval ledger when approved is true. PLAN_FORGE_HOME is
// isolated.
func pullRepo(t *testing.T, approved bool) (repo, sha string) {
	t.Helper()
	t.Setenv("PLAN_FORGE_HOME", t.TempDir())
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	targets := []contracts.Target{{Path: "internal/billing/export.go", Operation: contracts.OpAdd}}
	m := contracts.PlanManifest{
		SchemaVersion: contracts.SchemaVersion, ApprovalID: "approval-1", Title: "Invoice export",
		CreatedAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC), Targets: targets,
		VerificationCommands: []contracts.VerificationCommand{}, EstimateAssumptions: []string{},
		SpecSHA256: storage.SHA256Hex([]byte(pullSpec)),
		TaskDAG: []contracts.TechnicalTask{
			{ID: "T-2", EpicID: "E1", WorkstreamID: "billing", Title: "Document export", Steps: []string{"write"}},
			{ID: "T-1", EpicID: "E1", WorkstreamID: "billing", Title: "Export invoices", Steps: []string{"do it"}, Targets: targets},
		},
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	pullWrite(t, repo, planstate.SpecRel, pullSpec)
	pullWrite(t, repo, planstate.ManifestRel, string(raw))
	sha = storage.SHA256Hex(raw)
	if approved {
		if err := planstate.RecordApproval(repo, sha, m.ApprovalID); err != nil {
			t.Fatal(err)
		}
	}
	return repo, sha
}
