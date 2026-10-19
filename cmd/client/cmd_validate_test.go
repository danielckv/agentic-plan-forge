//go:build client && !server

package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/planstate"
	"github.com/danielckv/agentic-plan-forge/internal/storage"
)

// captureStdout runs fn and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

func writePlan(t *testing.T, repo string, approved bool) {
	t.Helper()
	spec := "# Plan\n\n## Executive summary\n\nHello.\n"
	m := contracts.PlanManifest{
		SchemaVersion: contracts.SchemaVersion, ApprovalID: "approval-1", Title: "Export invoices",
		CreatedAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC), SpecSHA256: storage.SHA256Hex([]byte(spec)),
		Targets: []contracts.Target{}, VerificationCommands: []contracts.VerificationCommand{}, EstimateAssumptions: []string{},
		TaskDAG: []contracts.TechnicalTask{},
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	for rel, body := range map[string][]byte{planstate.SpecRel: []byte(spec), planstate.ManifestRel: raw} {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if approved {
		if err := planstate.RecordApproval(repo, storage.SHA256Hex(raw), m.ApprovalID); err != nil {
			t.Fatal(err)
		}
	}
}

func newValidateRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("PLAN_FORGE_HOME", t.TempDir())
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestValidateExitCodes(t *testing.T) {
	ctx := context.Background()

	repo := newValidateRepo(t)
	writePlan(t, repo, true)
	if code := runValidate(ctx, []string{"--repo", repo}); code != exitOK {
		t.Fatalf("approved plan: exit %d, want %d", code, exitOK)
	}

	unapproved := newValidateRepo(t)
	writePlan(t, unapproved, false)
	if code := runValidate(ctx, []string{"--repo", unapproved}); code != exitValidationFailed {
		t.Fatalf("unapproved plan: exit %d, want %d", code, exitValidationFailed)
	}

	empty := newValidateRepo(t)
	if code := runValidate(ctx, []string{"--repo", empty}); code != exitValidationFailed {
		t.Fatalf("missing manifest: exit %d, want %d", code, exitValidationFailed)
	}

	if code := runValidate(ctx, []string{"--repo", filepath.Join(empty, "nope")}); code != exitRuntimeError {
		t.Fatalf("missing repo dir: exit %d, want %d", code, exitRuntimeError)
	}
	if code := runValidate(ctx, []string{"--repo", repo, "extra"}); code != exitUsage {
		t.Fatalf("positional arg: exit %d, want %d", code, exitUsage)
	}
	if code := runValidate(ctx, []string{"--repo", repo, "--timeout", "soon"}); code != exitUsage {
		t.Fatalf("bad timeout: exit %d, want %d", code, exitUsage)
	}
	if code := runValidate(ctx, []string{"--bogus"}); code != exitUsage {
		t.Fatalf("bad flag: exit %d, want %d", code, exitUsage)
	}
	if exitValidationFailed != 6 {
		t.Fatalf("exitValidationFailed = %d, want 6", exitValidationFailed)
	}
}

func TestValidateJSONShape(t *testing.T) {
	repo := newValidateRepo(t)
	writePlan(t, repo, true)
	var code int
	out := captureStdout(t, func() { code = runValidate(context.Background(), []string{"--repo", repo, "--json"}) })
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not one JSON object: %q: %v", out, err)
	}
	for _, key := range []string{"valid", "approved", "status", "title", "manifest_sha256", "verified", "total", "tasks", "pending", "drift", "notes", "spec_path", "manifest_path"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing key %q in %s", key, out)
		}
	}
	if string(got["valid"]) != "true" || string(got["pending"]) != "[]" || string(got["drift"]) != "[]" {
		t.Errorf("unexpected values: %s", out)
	}
}

func TestValidateTextOutput(t *testing.T) {
	repo := newValidateRepo(t)
	writePlan(t, repo, true)
	out := captureStdout(t, func() { runValidate(context.Background(), []string{"--repo", repo}) })
	want := "Plan validation: VALID — UNSTARTED · 0/0 AST targets present · 0 tasks\n" +
		"Spec: docs/plans/.plan/synthesized_spec.md\nDrift: (none)\nPending verification: (none)\n"
	if out != want {
		t.Fatalf("text output:\n%q\nwant:\n%q", out, want)
	}
}

func TestWaitValidationFallback(t *testing.T) {
	v := waitValidation(context.Background(), nil)
	if v.Valid || v.Status != "UNKNOWN" || len(v.Notes) != 1 || v.Notes[0] != "validation timed out; run plan-forge validate" {
		t.Fatalf("fallback: %+v", v)
	}

	repo := newValidateRepo(t)
	writePlan(t, repo, true)
	v = waitValidation(context.Background(), []string{filepath.Join(repo, filepath.FromSlash(planstate.SpecRel)), filepath.Join(repo, filepath.FromSlash(planstate.ManifestRel))})
	if !v.Valid || !v.Approved || v.Title != "Export invoices" {
		t.Fatalf("validation derived from artifact path: %+v", v)
	}
}
