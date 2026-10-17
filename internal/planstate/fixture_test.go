//go:build client && !server

package planstate_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/planstate"
	"github.com/danielckv/agentic-plan-forge/internal/storage"
)

const invoiceSrc = `package billing

func ListInvoices(org string) []string { return nil }

func Legacy() {}
`

const specText = "# Invoice export\n\n## Executive summary\n\nExports invoices for finance.\n\n## Next\n\nDetails.\n"

// isolate points the Plan-Forge home and cache at a temporary directory.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("PLAN_FORGE_HOME", t.TempDir())
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell scripts")
	}
}

func write(t *testing.T, repo, rel, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// newRepo returns a symlink-resolved repository holding one Go file.
func newRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "internal/billing/invoice.go", invoiceSrc, 0o644)
	return dir
}

func scan(t *testing.T, repo string) *contracts.RepoContextGraph {
	t.Helper()
	g, err := ast.Scan(context.Background(), ast.ScanOptions{Root: repo, SkipDocDrift: true})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func symbol(t *testing.T, g *contracts.RepoContextGraph, name string) contracts.Symbol {
	t.Helper()
	for _, syms := range g.Symbols {
		for _, s := range syms {
			if s.Name == name {
				return s
			}
		}
	}
	t.Fatalf("symbol %s not found in the scan", name)
	return contracts.Symbol{}
}

func verification(id string, argv ...string) contracts.VerificationCommand {
	return contracts.VerificationCommand{ID: id, Argv: argv, Cwd: ".", TimeoutSeconds: 30, ExpectedExitCode: 0}
}

// structuralManifest plans three structural changes recorded against the base
// scan: add a file, modify ListInvoices and remove Legacy. One task owns them
// and carries one verification command.
func structuralManifest(t *testing.T, repo string, v contracts.VerificationCommand) contracts.PlanManifest {
	t.Helper()
	g := scan(t, repo)
	list, legacy := symbol(t, g, "ListInvoices"), symbol(t, g, "Legacy")
	targets := []contracts.Target{
		{Path: "internal/billing/export.go", Operation: contracts.OpAdd},
		{Path: list.Path, Operation: contracts.OpModify, SymbolID: list.ID, ExpectedSignatureSHA256: list.SignatureSHA256},
		{Path: legacy.Path, Operation: contracts.OpRemove, SymbolID: legacy.ID, ExpectedSignatureSHA256: legacy.SignatureSHA256},
	}
	return contracts.PlanManifest{
		SchemaVersion: contracts.SchemaVersion, ApprovalID: "approval-1", Title: "Invoice export",
		CreatedAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC), WorktreeHash: g.WorktreeHash,
		Targets: targets, VerificationCommands: []contracts.VerificationCommand{v}, EstimateAssumptions: []string{},
		TaskDAG: []contracts.TechnicalTask{{
			ID: "T-1", EpicID: "E1", WorkstreamID: "billing", Title: "Export invoices", Steps: []string{"do it"},
			Targets: targets, AcceptanceCriteria: []string{"it works"}, Verification: []contracts.VerificationCommand{v},
			HumanEstimate: contracts.Estimate{Optimistic: 1, Likely: 2, Pessimistic: 3, Basis: "similar work"},
			AgentEstimate: contracts.Estimate{Optimistic: 5, Likely: 8, Pessimistic: 12, Basis: "one file"},
		}},
	}
}

// commit writes the manifest and spec to docs/plans/.plan/ and returns the manifest hash.
// It records the approval in the local ledger when approved is true.
func commit(t *testing.T, repo string, m contracts.PlanManifest, spec string, approved bool) string {
	t.Helper()
	m.SpecSHA256 = storage.SHA256Hex([]byte(spec))
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	write(t, repo, planstate.SpecRel, spec, 0o644)
	write(t, repo, planstate.ManifestRel, string(raw), 0o644)
	sum := storage.SHA256Hex(raw)
	if approved {
		if err := planstate.RecordApproval(repo, sum, m.ApprovalID); err != nil {
			t.Fatal(err)
		}
	}
	return sum
}

func check(t *testing.T, repo string) planstate.PlanStatus {
	t.Helper()
	st, err := planstate.CheckState(context.Background(), repo)
	if err != nil {
		t.Fatalf("CheckState: %v", err)
	}
	return st
}

func hasDrift(st planstate.PlanStatus, code string) bool {
	for _, d := range st.Drift {
		if d.Code == code {
			return true
		}
	}
	return false
}

// receipt records a verification receipt for the task bound to the current
// sources.
func receipt(t *testing.T, repo string, v contracts.VerificationCommand, exit int) {
	t.Helper()
	fp, err := planstate.Fingerprint(context.Background(), repo, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = storage.WriteReceipt(context.Background(), repo, storage.Receipt{
		TaskID: "T-1", VerificationID: v.ID, CommandSHA256: planstate.CommandHash(v), SourceFingerprint: fp,
		ExitCode: exit, ExpectedExitCode: v.ExpectedExitCode, OutputSHA256: "00", At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

// completeStructure performs the three planned structural changes.
func completeStructure(t *testing.T, repo string) {
	t.Helper()
	write(t, repo, "internal/billing/export.go", "package billing\n\nfunc Export() {}\n", 0o644)
	write(t, repo, "internal/billing/invoice.go", "package billing\n\nfunc ListInvoices(ctx string, org string) []string { return nil }\n", 0o644)
}

// tree lists every file under dir with its size and modification time.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, _ := d.Info()
		rel, _ := filepath.Rel(dir, p)
		out[rel] = info.ModTime().String() + "/" + string(rune(info.Size()))
		return nil
	})
	return out
}
