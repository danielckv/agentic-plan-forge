//go:build client && !server

package planstate_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/planstate"
)

func validate(t *testing.T, repo string) planstate.Validation {
	t.Helper()
	v, err := planstate.Validate(context.Background(), repo, planstate.CheckOptions{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return v
}

func TestValidateValid(t *testing.T) {
	isolate(t)
	repo := newRepo(t)
	v1 := verification("V-1", "true")
	sum := commit(t, repo, structuralManifest(t, repo, v1), specText, true)

	v := validate(t, repo)
	if !v.Valid || !v.Approved || v.Status != "UNSTARTED" {
		t.Fatalf("want valid approved UNSTARTED, got %+v", v)
	}
	if v.ManifestSHA256 != sum || v.Title != "Invoice export" || v.Total != 3 || v.Verified != 0 || v.Tasks != 1 {
		t.Fatalf("unexpected counts: %+v", v)
	}
	if v.SpecPath != planstate.SpecRel || v.ManifestPath != planstate.ManifestRel {
		t.Fatalf("paths: %q %q", v.SpecPath, v.ManifestPath)
	}
	if len(v.Pending) == 0 {
		t.Fatalf("an unrun verification must be pending: %+v", v)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"valid", "approved", "status", "title", "manifest_sha256", "verified", "total", "tasks", "pending", "drift", "notes", "spec_path", "manifest_path"} {
		if !strings.Contains(string(raw), `"`+key+`":`) {
			t.Errorf("JSON lacks key %q: %s", key, raw)
		}
	}
	if !strings.Contains(string(raw), `"drift":[]`) || !strings.Contains(string(raw), `"notes":[]`) {
		t.Errorf("empty slices must encode as [], got %s", raw)
	}
}

func TestValidateDriftedIsInvalid(t *testing.T) {
	isolate(t)
	repo := newRepo(t)
	commit(t, repo, structuralManifest(t, repo, verification("V-1", "true")), specText, true)
	if err := os.Remove(filepath.Join(repo, "internal/billing/invoice.go")); err != nil {
		t.Fatal(err)
	}

	v := validate(t, repo)
	if v.Valid || !v.Approved || v.Status != "DRIFTED" {
		t.Fatalf("want invalid approved DRIFTED, got %+v", v)
	}
	if len(v.Drift) == 0 || v.Drift[0].Code != planstate.DriftTargetRemoved || v.Drift[0].Detail == "" {
		t.Fatalf("drift: %+v", v.Drift)
	}
}

func TestValidateUnapprovedIsInvalid(t *testing.T) {
	isolate(t)
	repo := newRepo(t)
	commit(t, repo, structuralManifest(t, repo, verification("V-1", "true")), specText, false)

	v := validate(t, repo)
	if v.Valid || v.Approved || v.Status != "UNKNOWN" {
		t.Fatalf("want invalid unapproved UNKNOWN, got %+v", v)
	}
	if v.Tasks != 1 || len(v.Notes) == 0 {
		t.Fatalf("want a task count and an unapproved note: %+v", v)
	}
}

func TestValidateMissingManifest(t *testing.T) {
	isolate(t)
	repo := newRepo(t)

	v := validate(t, repo)
	if v.Valid || v.Approved {
		t.Fatalf("a missing manifest must be invalid: %+v", v)
	}
	if len(v.Notes) != 1 || v.Notes[0] != "no plan manifest present" {
		t.Fatalf("notes: %+v", v.Notes)
	}
	if v.Pending == nil || v.Drift == nil || v.Notes == nil {
		t.Fatalf("slices must be non-nil: %+v", v)
	}
}

func TestValidateSanitizesTitle(t *testing.T) {
	isolate(t)
	repo := newRepo(t)
	m := structuralManifest(t, repo, verification("V-1", "true"))
	m.Title = "plan\x1b]0;pwned\x07 title"
	commit(t, repo, m, specText, true)

	v := validate(t, repo)
	if strings.ContainsAny(v.Title, "\x1b\x07") {
		t.Fatalf("title keeps control bytes: %q", v.Title)
	}
}
