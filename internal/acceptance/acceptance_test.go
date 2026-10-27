//go:build client && !server

package acceptance

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	head        = "1111111111111111111111111111111111111111"
	staleHead   = "2222222222222222222222222222222222222222"
	recipeHash  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bundleDig   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	manifestSHA = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

var signedAt = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// okEvidence returns fully qualifying evidence: PM sign-off, one approval from
// a write-level developer on the required head, all receipts, identifiers,
// and branch protection. Tests then remove or corrupt one thing at a time.
func okEvidence() Evidence {
	return Evidence{
		ApprovalID:      "apr-2026-10-27-001",
		ManifestSHA256:  manifestSHA,
		Repository:      "danielckv/agentic-plan-forge",
		PR:              "42",
		HeadSHA:         head,
		RecipeHash:      recipeHash,
		BundleDigest:    bundleDig,
		Author:          "dev-alice",
		PMSignoff:       Signoff{Actor: "pm-dana", At: signedAt},
		Reviews:         []Review{{Reviewer: "dev-bob", State: ReviewStateApproved, CommitSHA: head, AuthorPermission: "write"}},
		Receipts:        Receipts{BuildOK: true, TestOK: true, ReadinessOK: true},
		BranchProtected: true,
	}
}

func TestEvaluateStatuses(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*Evidence)
		required   string
		wantStatus string
		wantReason string // substring expected in Reasons; empty skips the check
	}{
		{
			name:       "matching sign-off review and receipts accepted",
			mutate:     func(*Evidence) {},
			wantStatus: StatusAccepted,
		},
		{
			name:       "PM-only pending",
			mutate:     func(ev *Evidence) { ev.Reviews = nil },
			wantStatus: StatusPending,
			wantReason: "no qualifying approval review",
		},
		{
			name: "review-only pending",
			mutate: func(ev *Evidence) {
				ev.PMSignoff = Signoff{}
				ev.Receipts = Receipts{}
				ev.BranchProtected = false
				ev.ApprovalID, ev.ManifestSHA256, ev.Repository, ev.PR = "", "", "", ""
				ev.HeadSHA, ev.RecipeHash, ev.BundleDigest = "", "", ""
			},
			wantStatus: StatusPending,
			wantReason: "PM sign-off is missing",
		},
		{
			name:       "stale head rejected",
			mutate:     func(ev *Evidence) { ev.HeadSHA = staleHead },
			required:   head,
			wantStatus: StatusRejected,
			wantReason: "is not the required head",
		},
		{
			name: "dismissed approval rejected",
			mutate: func(ev *Evidence) {
				ev.Reviews = []Review{{Reviewer: "dev-bob", State: ReviewStateApproved, CommitSHA: head, Dismissed: true, AuthorPermission: "write"}}
			},
			wantStatus: StatusRejected,
			wantReason: "dismissed",
		},
		{
			name: "changes-requested rejected",
			mutate: func(ev *Evidence) {
				ev.Reviews = []Review{{Reviewer: "dev-bob", State: ReviewStateChangesRequested, CommitSHA: head, AuthorPermission: "write"}}
			},
			wantStatus: StatusRejected,
			wantReason: "requests changes",
		},
		{
			name: "changed recipe against recorded hash rejected",
			mutate: func(ev *Evidence) {
				ev.RecordedRecipeHash = recipeHash
				ev.RecipeHash = strings.Repeat("d", 64)
			},
			wantStatus: StatusRejected,
			wantReason: "recipe changed",
		},
		{
			name: "changed bundle against recorded head rejected",
			mutate: func(ev *Evidence) {
				ev.RecordedHeadSHA = head
				ev.HeadSHA = staleHead
			},
			wantStatus: StatusRejected,
			wantReason: "head moved",
		},
		{
			name: "missing tests without waiver pending",
			mutate: func(ev *Evidence) {
				ev.Receipts.TestOK = false
				ev.Receipts.NotConfiguredTests = true
			},
			wantStatus: StatusPending,
			wantReason: "no recorded waiver",
		},
		{
			name: "missing tests with recorded waiver accepted",
			mutate: func(ev *Evidence) {
				ev.Receipts.TestOK = false
				ev.Receipts.NotConfiguredTests = true
				ev.Receipts.Waiver = "waiver-2026-10-27: test harness not yet ported to the sandbox"
			},
			wantStatus: StatusAccepted,
		},
		{
			name: "author's own approval ignored",
			mutate: func(ev *Evidence) {
				ev.Reviews = []Review{{Reviewer: "dev-alice", State: ReviewStateApproved, CommitSHA: head, AuthorPermission: "admin"}}
			},
			wantStatus: StatusPending,
			wantReason: "no qualifying approval review",
		},
		{
			name: "non-write permission ignored",
			mutate: func(ev *Evidence) {
				ev.Reviews = []Review{{Reviewer: "dev-bob", State: ReviewStateApproved, CommitSHA: head, AuthorPermission: "read"}}
			},
			wantStatus: StatusPending,
			wantReason: "not write, maintain, or admin",
		},
		{
			name: "unknown review state produces unknown",
			mutate: func(ev *Evidence) {
				ev.Reviews = []Review{{Reviewer: "dev-bob", State: ReviewStateUnknown, CommitSHA: head, AuthorPermission: "write"}}
			},
			wantStatus: StatusUnknown,
			wantReason: "review state for dev-bob is unavailable",
		},
		{
			name: "indeterminable reviewer permission produces unknown",
			mutate: func(ev *Evidence) {
				ev.Reviews = []Review{{Reviewer: "dev-bob", State: ReviewStateApproved, CommitSHA: head}}
			},
			wantStatus: StatusUnknown,
			wantReason: "permission for dev-bob could not be determined",
		},
		{
			name: "dismissed approval alone rejected even with other gaps",
			mutate: func(ev *Evidence) {
				ev.Receipts = Receipts{}
				ev.Reviews = []Review{{Reviewer: "dev-bob", State: ReviewStateApproved, CommitSHA: head, Dismissed: true, AuthorPermission: "write"}}
			},
			wantStatus: StatusRejected,
			wantReason: "dismissed",
		},
		{
			name: "approval on a stale commit pending",
			mutate: func(ev *Evidence) {
				ev.Reviews = []Review{{Reviewer: "dev-bob", State: ReviewStateApproved, CommitSHA: staleHead, AuthorPermission: "write"}}
			},
			wantStatus: StatusPending,
			wantReason: "is on commit",
		},
		{
			name: "bot reviewer filtered upstream still cannot qualify here",
			mutate: func(ev *Evidence) {
				ev.Reviews = []Review{{Reviewer: "dependabot[bot]", State: ReviewStateApproved, CommitSHA: head, AuthorPermission: "write"}}
			},
			wantStatus: StatusPending,
			wantReason: "no qualifying approval review",
		},
		{
			name:       "missing identifier pending",
			mutate:     func(ev *Evidence) { ev.BundleDigest = "" },
			wantStatus: StatusPending,
			wantReason: "bundle digest is missing",
		},
		{
			name:       "missing readiness receipt pending",
			mutate:     func(ev *Evidence) { ev.Receipts.ReadinessOK = false },
			wantStatus: StatusPending,
			wantReason: "readiness receipt is missing",
		},
		{
			name: "commented review neither qualifies nor blocks",
			mutate: func(ev *Evidence) {
				ev.Reviews = []Review{
					{Reviewer: "dev-carol", State: ReviewStateCommented, CommitSHA: head, AuthorPermission: "write"},
					{Reviewer: "dev-bob", State: ReviewStateApproved, CommitSHA: head, AuthorPermission: "write"},
				}
			},
			wantStatus: StatusAccepted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := okEvidence()
			tt.mutate(&ev)
			got := Evaluate(ev, tt.required)
			if got.Status != tt.wantStatus {
				t.Fatalf("Evaluate status = %q, want %q (reasons: %v)", got.Status, tt.wantStatus, got.Reasons)
			}
			if got.Accepted != (got.Status == StatusAccepted) {
				t.Fatalf("Accepted = %v does not match status %q", got.Accepted, got.Status)
			}
			if tt.wantReason != "" && !slices.ContainsFunc(got.Reasons, func(s string) bool { return strings.Contains(s, tt.wantReason) }) {
				t.Fatalf("reasons %v do not contain %q", got.Reasons, tt.wantReason)
			}
			if len(got.Reasons) == 0 {
				t.Fatal("Decision.Reasons must never be nil or empty")
			}
			if len(got.EvidenceHash) != 64 {
				t.Fatalf("EvidenceHash = %q, want a 64-hex digest", got.EvidenceHash)
			}
		})
	}
}

// TestEvaluateUnknownNeverAccepted proves the named failure mode: API or
// permission ambiguity degrades to unknown with reasons, never to acceptance.
func TestEvaluateUnknownNeverAccepted(t *testing.T) {
	ev := okEvidence()
	ev.Reviews = []Review{
		{Reviewer: "dev-bob", State: ReviewStateApproved, CommitSHA: head, AuthorPermission: "write"},
		{Reviewer: "dev-carol", State: "", AuthorPermission: "write"},
	}
	got := Evaluate(ev, "")
	if got.Accepted {
		t.Fatal("Evaluate accepted evidence with an unavailable review state")
	}
	if got.Status != StatusUnknown {
		t.Fatalf("status = %q, want %q", got.Status, StatusUnknown)
	}
	if !slices.ContainsFunc(got.Reasons, func(s string) bool { return strings.Contains(s, "review state for dev-carol is unavailable") }) {
		t.Fatalf("reasons %v do not name the unavailable review", got.Reasons)
	}
}

func TestEvidenceHashStability(t *testing.T) {
	// Review order must not change the hash. Both sets hold the same two
	// reviews, listed the other way round.
	first := okEvidence()
	first.Reviews = []Review{
		{Reviewer: "dev-bob", State: ReviewStateApproved, CommitSHA: head, AuthorPermission: "write"},
		{Reviewer: "dev-carol", State: ReviewStateCommented, CommitSHA: head, AuthorPermission: "write"},
	}
	reordered := okEvidence()
	reordered.Reviews = []Review{
		{Reviewer: "dev-carol", State: ReviewStateCommented, CommitSHA: head, AuthorPermission: "write"},
		{Reviewer: "dev-bob", State: ReviewStateApproved, CommitSHA: head, AuthorPermission: "write"},
	}
	a, err := first.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	b, err := reordered.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if a != b {
		t.Fatal("hash changed when review order changed")
	}
	// Re-evaluating identical evidence is stable.
	if c, err := first.Hash(); err != nil || c != a {
		t.Fatal("hash is not stable for identical evidence")
	}
}

func TestEvidenceHashSensitivity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Evidence)
	}{
		{"receipt change", func(ev *Evidence) { ev.Receipts.TestOK = false }},
		{"waiver change", func(ev *Evidence) { ev.Receipts.Waiver = "waiver-x" }},
		{"signoff actor change", func(ev *Evidence) { ev.PMSignoff.Actor = "someone-else" }},
		{"signoff time change", func(ev *Evidence) { ev.PMSignoff.At = signedAt.Add(time.Minute) }},
		{"review state change", func(ev *Evidence) { ev.Reviews[0].State = ReviewStateChangesRequested }},
		{"reviewer change", func(ev *Evidence) { ev.Reviews[0].Reviewer = "someone-else" }},
		{"dismissal change", func(ev *Evidence) { ev.Reviews[0].Dismissed = true }},
		{"permission change", func(ev *Evidence) { ev.Reviews[0].AuthorPermission = "admin" }},
		{"head change", func(ev *Evidence) { ev.HeadSHA = staleHead }},
		{"recorded head change", func(ev *Evidence) { ev.RecordedHeadSHA = staleHead }},
		{"bundle change", func(ev *Evidence) { ev.BundleDigest = strings.Repeat("d", 64) }},
		{"branch protection change", func(ev *Evidence) { ev.BranchProtected = false }},
		{"approval id change", func(ev *Evidence) { ev.ApprovalID = "other" }},
	}
	seed, err := okEvidence().Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := okEvidence()
			tt.mutate(&ev)
			h, err := ev.Hash()
			if err != nil {
				t.Fatalf("Hash: %v", err)
			}
			if h == seed {
				t.Fatalf("hash did not change when %s changed", tt.name)
			}
		})
	}
}

// fakeGH is an in-memory GitHub with no network, for Collect tests only.
type fakeGH struct {
	pr      PR
	reviews []Review
	perms   map[string]string

	prErr, reviewErr, permErr error
	permCalls                 []string
}

func (f *fakeGH) PullRequest(context.Context, string, int) (PR, error) {
	if f.prErr != nil {
		return PR{}, f.prErr
	}
	return f.pr, nil
}

func (f *fakeGH) Reviews(context.Context, string, int) ([]Review, error) {
	if f.reviewErr != nil {
		return nil, f.reviewErr
	}
	return f.reviews, nil
}

func (f *fakeGH) Permission(_ context.Context, _, login string) (string, error) {
	if f.permErr != nil {
		return "", f.permErr
	}
	f.permCalls = append(f.permCalls, login)
	return f.perms[login], nil
}

func TestCollectGathersQualifyingEvidence(t *testing.T) {
	gh := &fakeGH{
		pr:      PR{Number: 42, HeadSHA: head, Author: "dev-alice", BaseProtected: true},
		reviews: []Review{{Reviewer: "dev-bob", State: ReviewStateApproved, CommitSHA: head}},
		perms:   map[string]string{"dev-bob": "maintain"},
	}
	ev, err := Collect(context.Background(), gh, "danielckv/agentic-plan-forge", 42)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if ev.HeadSHA != head || ev.Author != "dev-alice" || ev.PR != "42" || !ev.BranchProtected {
		t.Fatalf("Collect lost PR facts: %+v", ev)
	}
	if len(ev.Reviews) != 1 || ev.Reviews[0].AuthorPermission != "maintain" {
		t.Fatalf("Collect did not resolve reviewer permission: %+v", ev.Reviews)
	}
	if len(gh.permCalls) != 1 || gh.permCalls[0] != "dev-bob" {
		t.Fatalf("Permission called %v, want exactly [dev-bob]", gh.permCalls)
	}
	if d := Evaluate(ev, head); d.Status != StatusPending || d.Accepted {
		// Collected evidence carries no receipts or sign-off; it must be
		// pending, never accepted, until the run receipts and PM sign-off
		// are attached.
		t.Fatalf("Evaluate on freshly collected evidence = %q %v, want pending", d.Status, d.Reasons)
	}
}

// A bot approval that reaches Evaluate directly never qualifies: the filter in
// Collect is one layer, and evidence can also arrive from a stored record.
func TestEvaluateRefusesABotApproval(t *testing.T) {
	ev := okEvidence()
	ev.Reviews = []Review{{Reviewer: "dependabot[bot]", State: ReviewStateApproved, CommitSHA: head, AuthorPermission: "write"}}
	d := Evaluate(ev, head)
	if d.Status != StatusPending || d.Accepted {
		t.Fatalf("Evaluate = %q %v, want pending", d.Status, d.Reasons)
	}
}

// TestCollectFiltersBotsAndAuthorApprovals proves bot reviews and the PR
// author's own approval are filtered out of qualifying evidence.
func TestCollectFiltersBotsAndAuthorApprovals(t *testing.T) {
	gh := &fakeGH{
		pr: PR{Number: 7, HeadSHA: head, Author: "dev-alice"},
		reviews: []Review{
			{Reviewer: "dependabot[bot]", State: ReviewStateApproved, CommitSHA: head},
			{Reviewer: "dev-alice", State: ReviewStateApproved, CommitSHA: head},
			{Reviewer: "dev-bob", State: ReviewStateApproved, CommitSHA: head},
		},
		perms: map[string]string{"dev-bob": "write", "dev-alice": "admin"},
	}
	ev, err := Collect(context.Background(), gh, "danielckv/agentic-plan-forge", 7)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(ev.Reviews) != 1 || ev.Reviews[0].Reviewer != "dev-bob" {
		t.Fatalf("Collect kept %d reviews, want only dev-bob: %+v", len(ev.Reviews), ev.Reviews)
	}
	if d := Evaluate(ev, head); d.Status != StatusPending || d.Accepted {
		t.Fatalf("Evaluate = %q %v; author-only approvals must not qualify", d.Status, d.Reasons)
	}
}

// TestCollectMapsAPIFailureToUnknown proves the named failure mode: a GitHub
// API failure returns a CollectError carrying ErrUnknown so callers surface
// unknown, never accepted.
func TestCollectMapsAPIFailureToUnknown(t *testing.T) {
	tests := []struct {
		name string
		gh   *fakeGH
	}{
		{
			name: "pull request fetch fails",
			gh:   &fakeGH{prErr: errors.New("rate limited")},
		},
		{
			name: "reviews fetch fails",
			gh:   &fakeGH{pr: PR{Number: 42, HeadSHA: head, Author: "dev-alice"}, reviewErr: errors.New("connection reset")},
		},
		{
			name: "permission fetch fails",
			gh: &fakeGH{
				pr:      PR{Number: 42, HeadSHA: head, Author: "dev-alice"},
				reviews: []Review{{Reviewer: "dev-bob", State: ReviewStateApproved, CommitSHA: head}},
				permErr: errors.New("403 insufficient scope"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Collect(context.Background(), tt.gh, "danielckv/agentic-plan-forge", 42)
			if err == nil {
				t.Fatal("Collect succeeded despite an API failure")
			}
			var ce *CollectError
			if !errors.As(err, &ce) {
				t.Fatalf("error %v is not a *CollectError", err)
			}
			if !errors.Is(err, ErrUnknown) {
				t.Fatalf("error %v does not carry ErrUnknown", err)
			}
		})
	}
}

func TestCollectRejectsMergedAndInvalidInput(t *testing.T) {
	gh := &fakeGH{pr: PR{Number: 42, HeadSHA: head, Author: "dev-alice", Merged: true}}
	if _, err := Collect(context.Background(), gh, "danielckv/agentic-plan-forge", 42); err == nil {
		t.Fatal("Collect accepted a merged PR")
	}
	if _, err := Collect(context.Background(), gh, "", 42); err == nil {
		t.Fatal("Collect accepted an empty repository")
	}
	if _, err := Collect(context.Background(), gh, "danielckv/agentic-plan-forge", 0); err == nil {
		t.Fatal("Collect accepted pull request number 0")
	}
	if _, err := Collect(context.Background(), nil, "danielckv/agentic-plan-forge", 42); err == nil {
		t.Fatal("Collect accepted a nil client")
	}
}
