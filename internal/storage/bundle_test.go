//go:build client && !server

package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

func bundleEntries() []synthesizer.BundleEntry {
	return []synthesizer.BundleEntry{
		{Path: "docs/plans/approved.md", Kind: synthesizer.KindPlan, Title: "Approved", Body: []byte("# Approved\n"), SHA256: "aaa"},
		{Path: "docs/specs/approved.md", Kind: synthesizer.KindSpec, Title: "Approved", Body: []byte("# Spec\n"), SHA256: "bbb"},
	}
}

func bundleStore(t *testing.T) (*ArtifactStore, string) {
	t.Helper()
	repo := testRepo(t)
	store, err := NewArtifactStore(repo)
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	return store, repo
}

// A bundle lands as real files and the marker records every one of them.
func TestCommitBundleWritesTheDocumentsAndTheMarker(t *testing.T) {
	store, repo := bundleStore(t)
	meta := contracts.ApprovalMeta{ApprovalID: "a-1", CreatedAt: time.Unix(0, 0).UTC()}

	commit, err := store.CommitBundle(context.Background(), meta, bundleEntries())
	if err != nil {
		t.Fatalf("CommitBundle: %v", err)
	}
	if len(commit.Committed) != 2 {
		t.Fatalf("committed = %v, want two documents", commit.Committed)
	}
	for _, rel := range commit.Committed {
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s was not written: %v", rel, err)
		}
	}
	marker := filepath.Join(repo, filepath.FromSlash(commit.ManifestPath))
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the marker was not written: %v", err)
	}
}

// A destination that already exists is a document a person wrote. A generated
// file never replaces one, and nothing is written when it would.
func TestCommitBundleRefusesAnExistingDocument(t *testing.T) {
	store, repo := bundleStore(t)
	path := filepath.Join(repo, "docs", "plans", "approved.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("a document a person wrote"), 0o644); err != nil {
		t.Fatal(err)
	}

	meta := contracts.ApprovalMeta{ApprovalID: "a-1", CreatedAt: time.Unix(0, 0).UTC()}
	if _, err := store.CommitBundle(context.Background(), meta, bundleEntries()); err == nil {
		t.Fatal("an existing document was overwritten")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "a document a person wrote" {
		t.Error("the existing document was changed")
	}
	if _, err := os.Stat(filepath.Join(repo, "docs", "specs", "approved.md")); err == nil {
		t.Error("a partial bundle was written after the refusal")
	}
}

// Re-running the same commit is success, and publishes nothing twice.
func TestCommitBundleIsIdempotent(t *testing.T) {
	store, _ := bundleStore(t)
	meta := contracts.ApprovalMeta{ApprovalID: "a-1", CreatedAt: time.Unix(0, 0).UTC()}
	first, err := store.CommitBundle(context.Background(), meta, bundleEntries())
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	second, err := store.CommitBundle(context.Background(), meta, bundleEntries())
	if err != nil {
		t.Fatalf("second commit: %v", err)
	}
	if len(first.Committed) != len(second.Committed) {
		t.Errorf("a repeat commit changed the set: %v vs %v", first.Committed, second.Committed)
	}
}

// A different generation for one approval is a mismatch, never a merge: mixing
// two generations of one plan on disk is unrecoverable.
func TestCommitBundleRefusesASecondGeneration(t *testing.T) {
	store, _ := bundleStore(t)
	meta := contracts.ApprovalMeta{ApprovalID: "a-1", CreatedAt: time.Unix(0, 0).UTC()}
	if _, err := store.CommitBundle(context.Background(), meta, bundleEntries()); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	changed := bundleEntries()
	changed[0].SHA256 = "different"
	if _, err := store.CommitBundle(context.Background(), meta, changed); err == nil {
		t.Fatal("a second generation was merged into the first")
	}
}

// The documented bounds are enforced before anything is written.
func TestCommitBundleBounds(t *testing.T) {
	store, _ := bundleStore(t)
	meta := contracts.ApprovalMeta{ApprovalID: "a-1"}
	for _, tc := range []struct {
		name    string
		entries []synthesizer.BundleEntry
	}{
		{"empty", nil},
		{"no path", []synthesizer.BundleEntry{{Body: []byte("x"), SHA256: "a"}}},
		{"empty body", []synthesizer.BundleEntry{{Path: "docs/plans/a.md", SHA256: "a"}}},
		{"duplicate path", []synthesizer.BundleEntry{
			{Path: "docs/plans/a.md", Body: []byte("x"), SHA256: "a"},
			{Path: "docs/plans/a.md", Body: []byte("y"), SHA256: "b"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.CommitBundle(context.Background(), meta, tc.entries); !bundleRefused(err) {
				t.Fatalf("error = %v, want a refusal", err)
			}
		})
	}
}

// A path that escapes the repository is refused by the plan-directory guard.
func TestCommitBundleRefusesAnEscapingPath(t *testing.T) {
	store, _ := bundleStore(t)
	meta := contracts.ApprovalMeta{ApprovalID: "a-1"}
	entries := []synthesizer.BundleEntry{{Path: "../escape.md", Body: []byte("x"), SHA256: "a"}}
	if _, err := store.CommitBundle(context.Background(), meta, entries); err == nil {
		t.Fatal("an escaping path was written")
	}
}

// The canonical approved pair is untouched: a bundle is additive.
func TestCommitBundleLeavesTheApprovedPairAlone(t *testing.T) {
	store, repo := bundleStore(t)
	meta := contracts.ApprovalMeta{ApprovalID: "a-1", CreatedAt: time.Unix(0, 0).UTC()}
	if _, err := store.CommitBundle(context.Background(), meta, bundleEntries()); err != nil {
		t.Fatalf("CommitBundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(PlanDirName), SpecFileName)); err == nil {
		t.Error("the canonical spec was created by a bundle commit")
	}
}

func bundleRefused(err error) bool {
	var coded *contracts.CodedError
	var bound *contracts.ErrBound
	return errors.As(err, &coded) || errors.As(err, &bound)
}
