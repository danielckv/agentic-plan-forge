//go:build client && !server

package planstate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/storage"
)

func countOccurrences(haystack, needle string) int {
	return strings.Count(haystack, needle)
}

// newAcceptanceRepo returns a real directory plus an isolated Plan Forge home,
// so the ledger is written somewhere private and the repository resolves.
func newAcceptanceRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("PLAN_FORGE_HOME", t.TempDir())
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func acceptanceFixture(head string) AcceptanceEntry {
	return AcceptanceEntry{
		ApprovalID:      "approval-1",
		ManifestHash:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PullRequest:     "17",
		HeadSHA:         head,
		RecipeHash:      "recipe-1",
		BundleDigest:    "bundle-1",
		ProductOwner:    "pm@example.com",
		Developer:       "dev",
		ValidatedAt:     time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC),
		ProductSignedAt: time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC),
		DeveloperSigned: time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC),
	}
}

func acceptancePathForTest(t *testing.T) string {
	t.Helper()
	path, err := acceptancePath()
	if err != nil {
		t.Fatalf("acceptancePath: %v", err)
	}
	return path
}

// The acceptance ledger is separate from the approval ledger: approving a plan
// says what to build, accepting an implementation says this exact revision was
// reviewed and validated. A head that moved must not inherit the record.
func TestImplementationAcceptanceBindsToTheExactRevision(t *testing.T) {
	repo := newAcceptanceRepo(t)

	if err := RecordAcceptance(repo, acceptanceFixture("head-aaa")); err != nil {
		t.Fatalf("RecordAcceptance: %v", err)
	}
	got, ok, stale, err := ImplementationAcceptance(repo, "approval-1", "head-aaa")
	if err != nil || !ok || stale {
		t.Fatalf("ImplementationAcceptance = %+v, %v, %v, %v; want a live record", got, ok, stale, err)
	}
	if got.HeadSHA != "head-aaa" || got.RecipeHash != "recipe-1" {
		t.Errorf("record = %+v, want the recorded revision and recipe", got)
	}

	// Any new commit makes the earlier acceptance stale rather than inherited.
	_, ok, stale, err = ImplementationAcceptance(repo, "approval-1", "head-bbb")
	if err != nil {
		t.Fatalf("ImplementationAcceptance: %v", err)
	}
	if ok || !stale {
		t.Errorf("a moved head reported found=%v stale=%v, want stale", ok, stale)
	}
}

// Re-checking the same decision is idempotent: the ledger keeps one entry, so a
// repeated acceptance check cannot inflate the file or create two truths.
func TestRecordAcceptanceIsIdempotentOnOneRevision(t *testing.T) {
	repo := newAcceptanceRepo(t)
	entry := acceptanceFixture("head-aaa")
	for range 3 {
		if err := RecordAcceptance(repo, entry); err != nil {
			t.Fatalf("RecordAcceptance: %v", err)
		}
	}
	raw, err := os.ReadFile(acceptancePathForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := countOccurrences(string(raw), "head-aaa"); got != 1 {
		t.Errorf("one acceptance was written %d times, want 1", got)
	}
}

// A different revision of the same approval is a second record, not an update:
// both revisions were genuinely decided on.
func TestAcceptanceOfTwoRevisionsKeepsBoth(t *testing.T) {
	repo := newAcceptanceRepo(t)
	for _, head := range []string{"head-aaa", "head-bbb"} {
		if err := RecordAcceptance(repo, acceptanceFixture(head)); err != nil {
			t.Fatalf("RecordAcceptance(%s): %v", head, err)
		}
	}
	for _, head := range []string{"head-aaa", "head-bbb"} {
		if _, ok, stale, err := ImplementationAcceptance(repo, "approval-1", head); err != nil || !ok || stale {
			t.Errorf("%s: found=%v stale=%v err=%v, want a live record", head, ok, stale, err)
		}
	}
	all, err := Acceptances(repo)
	if err != nil {
		t.Fatalf("Acceptances: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("records = %d, want 2", len(all))
	}
	if all[0].HeadSHA != "head-bbb" {
		t.Errorf("newest record = %s, want the newest first", all[0].HeadSHA)
	}
}

// Asking for a revision that has no record while another revision does reports
// stale, never found: the caller must not accept a revision nobody decided on.
func TestAcceptanceOfAnotherRevisionIsStaleNotFound(t *testing.T) {
	repo := newAcceptanceRepo(t)
	if err := RecordAcceptance(repo, acceptanceFixture("head-aaa")); err != nil {
		t.Fatalf("RecordAcceptance: %v", err)
	}
	if _, found, stale, err := ImplementationAcceptance(repo, "approval-1", "head-zzz"); err != nil {
		t.Fatalf("ImplementationAcceptance: %v", err)
	} else if found || !stale {
		t.Fatalf("found=%v stale=%v, want stale and not found", found, stale)
	}
}

// A record needs both actors and the revision it covers: a half-filled
// acceptance must not be written.
func TestRecordAcceptanceRefusesIncompleteEvidence(t *testing.T) {
	repo := newAcceptanceRepo(t)
	base := acceptanceFixture("head-aaa")
	for _, tc := range []struct {
		name string
		edit func(*AcceptanceEntry)
	}{
		{"no approval", func(e *AcceptanceEntry) { e.ApprovalID = "" }},
		{"bad manifest hash", func(e *AcceptanceEntry) { e.ManifestHash = "short" }},
		{"no head", func(e *AcceptanceEntry) { e.HeadSHA = "" }},
		{"no pull request", func(e *AcceptanceEntry) { e.PullRequest = "" }},
		{"no product owner", func(e *AcceptanceEntry) { e.ProductOwner = "" }},
		{"no developer", func(e *AcceptanceEntry) { e.Developer = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := base
			tc.edit(&entry)
			if err := RecordAcceptance(repo, entry); err == nil {
				t.Fatal("an incomplete acceptance was written")
			}
		})
	}
}

// A ledger written by a newer build is reported, never reinterpreted.
func TestAcceptanceRefusesAnotherVersion(t *testing.T) {
	repo := newAcceptanceRepo(t)
	path := acceptancePathForTest(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":99,"entries":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecordAcceptance(repo, acceptanceFixture("head-aaa")); err == nil {
		t.Fatal("a ledger from another version was written over")
	}
}

// The record is private: it names people and the revision they approved.
func TestAcceptanceLedgerIsOwnerOnly(t *testing.T) {
	repo := newAcceptanceRepo(t)
	if err := RecordAcceptance(repo, acceptanceFixture("head-aaa")); err != nil {
		t.Fatalf("RecordAcceptance: %v", err)
	}
	info, err := os.Stat(acceptancePathForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := storage.ReadFileBounded(acceptancePathForTest(t), 1<<20); err != nil {
		t.Errorf("the record is unreadable: %v", err)
	}
}
