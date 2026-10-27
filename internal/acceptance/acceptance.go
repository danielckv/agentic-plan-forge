//go:build client && !server

// Package acceptance decides whether an approved plan's implementation may be
// accepted. Acceptance is a distinct decision from plan approval (the ledger
// in internal/planstate records that a manifest was approved): this package
// gates the implementation PR against PM sign-off, a qualifying developer
// review on the exact reviewed revision, and validation receipts.
//
// Evaluate is pure and deterministic; Collect is the only place that talks to
// GitHub. Every absence is a pending reason, every mismatch is a rejection,
// and ambiguity is surfaced as unknown — never as acceptance.
package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Status values reported by Evaluate.
const (
	StatusAccepted = "accepted"
	StatusPending  = "pending"
	StatusRejected = "rejected"
	StatusUnknown  = "unknown"
)

// Review states used in Review.State. These mirror the GitHub review states
// that matter for acceptance; ReviewStateUnknown marks evidence the API could
// not determine.
const (
	ReviewStateApproved         = "APPROVED"
	ReviewStateChangesRequested = "CHANGES_REQUESTED"
	ReviewStateCommented        = "COMMENTED"
	ReviewStateUnknown          = "UNKNOWN"
)

// Evidence is everything the acceptance decision may consider. The zero value
// is pending, never accepted: every field must be present for acceptance.
type Evidence struct {
	ApprovalID      string   `json:"approval_id"`
	ManifestSHA256  string   `json:"manifest_sha256"`
	Repository      string   `json:"repository"`
	PR              string   `json:"pr"`
	HeadSHA         string   `json:"head_sha"`
	RecipeHash      string   `json:"recipe_hash"`
	BundleDigest    string   `json:"bundle_digest"`
	Author          string   `json:"author"`
	PMSignoff       Signoff  `json:"pm_signoff"`
	Reviews         []Review `json:"reviews"`
	Receipts        Receipts `json:"receipts"`
	BranchProtected bool     `json:"branch_protected"`
	// RecordedHeadSHA and RecordedRecipeHash carry the head and recipe an
	// earlier acceptance record was bound to. Any drift from the evidence in
	// hand invalidates that record outright.
	RecordedHeadSHA    string `json:"recorded_head_sha"`
	RecordedRecipeHash string `json:"recorded_recipe_hash"`
}

// Signoff records the authenticated PM actor who signed off. The actor is a
// server-side identity, never a browser-claimed name.
type Signoff struct {
	Actor string    `json:"actor"`
	At    time.Time `json:"at"`
}

// Review is one GitHub review as seen by the acceptance decision.
// AuthorPermission is the reviewer's current repository permission; an empty
// value means the permission could not be determined.
type Review struct {
	Reviewer         string `json:"reviewer"`
	State            string `json:"state"`
	CommitSHA        string `json:"commit_sha"`
	Dismissed        bool   `json:"dismissed"`
	AuthorPermission string `json:"author_permission"`
}

// Receipts are the validation receipts collected from the prepared run.
// NotConfiguredTests with a non-empty Waiver stands in for a missing test
// receipt only when a waiver was explicitly recorded; missing tests are never
// reported as passed.
type Receipts struct {
	BuildOK            bool   `json:"build_ok"`
	TestOK             bool   `json:"test_ok"`
	ReadinessOK        bool   `json:"readiness_ok"`
	NotConfiguredTests bool   `json:"not_configured_tests"`
	Waiver             string `json:"waiver"`
}

// Decision is the outcome of Evaluate. Reasons are specific and ordered by
// severity: rejections first, then unknowns, then pending gaps.
type Decision struct {
	Accepted     bool     `json:"accepted"`
	Status       string   `json:"status"`
	Reasons      []string `json:"reasons"`
	EvidenceHash string   `json:"evidence_hash"`
}

// GitHub is the authenticated server-side client surface Collect needs.
// Implementations must resolve the review and permission state for the
// current head at call time; a caller-supplied reviewer identity is never
// evidence.
type GitHub interface {
	PullRequest(ctx context.Context, repo string, number int) (PR, error)
	Reviews(ctx context.Context, repo string, number int) ([]Review, error)
	Permission(ctx context.Context, repo, login string) (string, error)
}

// PR is the pull-request facts acceptance depends on.
type PR struct {
	Number        int
	HeadSHA       string
	Author        string
	Merged        bool
	BaseProtected bool
}

// ErrUnknown marks evidence that could not be collected. Callers surface it
// as StatusUnknown: an API failure must never read as approval or rejection.
var ErrUnknown = errors.New("acceptance evidence could not be collected")

// CollectError wraps any GitHub failure during Collect. errors.Is matches
// both ErrUnknown and the underlying error.
type CollectError struct {
	Op  string
	Err error
}

func (e *CollectError) Error() string {
	return fmt.Sprintf("acceptance: %s: %v", e.Op, e.Err)
}

func (e *CollectError) Unwrap() []error { return []error{ErrUnknown, e.Err} }

// Evaluate applies the acceptance gate to the evidence in hand.
//
// Accepted only when a PM signed off, exactly one qualifying approval review
// exists (APPROVED, not dismissed, on the required head, from a reviewer other
// than the PR author holding write, maintain, or admin permission), the build
// and test receipts hold (or tests are unconfigured under a recorded waiver),
// readiness holds, branch protection is on, and every identifier is present.
// Anything absent is pending with a specific reason. A stale head, a changed
// recipe or bundle, a dismissed review, or changes-requested review is a
// rejection. Unavailable review or permission data is unknown. API failures
// never produce accepted.
func Evaluate(ev Evidence, requiredHead string) Decision {
	d := Decision{Status: StatusPending, Reasons: []string{}}
	h, err := ev.Hash()
	if err != nil {
		d.Status = StatusUnknown
		d.Reasons = append(d.Reasons, "acceptance evidence could not be hashed")
		return d
	}
	d.EvidenceHash = h

	var rejected, unknown, pending []string

	// A recorded acceptance binds to one revision and one recipe. Any drift
	// invalidates the record before anything else is considered.
	if ev.RecordedHeadSHA != "" && ev.RecordedHeadSHA != ev.HeadSHA {
		rejected = append(rejected, fmt.Sprintf("head moved since the recorded acceptance: recorded %s, evidence now has %s", ev.RecordedHeadSHA, ev.HeadSHA))
	}
	if ev.RecordedRecipeHash != "" && ev.RecordedRecipeHash != ev.RecipeHash {
		rejected = append(rejected, fmt.Sprintf("recipe changed since the recorded acceptance: recorded %s, evidence now has %s", ev.RecordedRecipeHash, ev.RecipeHash))
	}
	if requiredHead != "" && requiredHead != ev.HeadSHA {
		rejected = append(rejected, fmt.Sprintf("evidence head %s is not the required head %s", ev.HeadSHA, requiredHead))
	}

	// An empty requiredHead means the caller pins nothing; the evidence head
	// is then the anchor the reviews must match.
	effective := requiredHead
	if effective == "" {
		effective = ev.HeadSHA
	}

	qualifying := false
	for _, r := range ev.Reviews {
		switch {
		case r.Dismissed:
			rejected = append(rejected, fmt.Sprintf("review from %s was dismissed; a dismissed review is failed evidence", reviewerLabel(r.Reviewer)))
		case r.State == ReviewStateChangesRequested:
			rejected = append(rejected, fmt.Sprintf("review from %s requests changes", reviewerLabel(r.Reviewer)))
		case r.State == "" || r.State == ReviewStateUnknown:
			unknown = append(unknown, fmt.Sprintf("review state for %s is unavailable", reviewerLabel(r.Reviewer)))
		case r.State != ReviewStateApproved:
			// Commented and unrecognized states never qualify and never block.
		case r.Reviewer == "":
			pending = append(pending, "an approval review carries no reviewer identity")
		case isBot(r.Reviewer):
			// A bot or app account is not a developer. Counting it would let
			// an automation approve its own change.
		case r.Reviewer == ev.Author:
			// The PR author's own approval is not independent evidence; it is
			// ignored rather than counted.
		case r.AuthorPermission == "":
			unknown = append(unknown, fmt.Sprintf("repository permission for %s could not be determined", r.Reviewer))
		case !qualifyingPermission(r.AuthorPermission):
			pending = append(pending, fmt.Sprintf("reviewer %s holds permission %q, not write, maintain, or admin", r.Reviewer, r.AuthorPermission))
		case r.CommitSHA != effective || ev.HeadSHA != effective:
			pending = append(pending, fmt.Sprintf("approval from %s is on commit %s, not the current head %s", r.Reviewer, r.CommitSHA, effective))
		default:
			qualifying = true
		}
	}
	if !qualifying {
		pending = append(pending, "no qualifying approval review on the current head from a developer other than the PR author")
	}

	for _, id := range []struct{ name, value string }{
		{"approval ID", ev.ApprovalID},
		{"manifest hash", ev.ManifestSHA256},
		{"repository", ev.Repository},
		{"pull request", ev.PR},
		{"head SHA", ev.HeadSHA},
		{"recipe hash", ev.RecipeHash},
		{"bundle digest", ev.BundleDigest},
	} {
		if strings.TrimSpace(id.value) == "" {
			pending = append(pending, id.name+" is missing")
		}
	}

	if strings.TrimSpace(ev.PMSignoff.Actor) == "" {
		pending = append(pending, "PM sign-off is missing")
	}

	if !ev.Receipts.BuildOK {
		pending = append(pending, "build receipt is missing")
	}
	testsOK := ev.Receipts.TestOK
	if !testsOK && ev.Receipts.NotConfiguredTests {
		if strings.TrimSpace(ev.Receipts.Waiver) != "" {
			testsOK = true
		} else {
			pending = append(pending, "tests are not configured and no recorded waiver exists")
		}
	} else if !testsOK {
		pending = append(pending, "test receipt is missing")
	}
	if !ev.Receipts.ReadinessOK {
		pending = append(pending, "readiness receipt is missing")
	}

	if !ev.BranchProtected {
		pending = append(pending, "branch protection is not enabled for the base branch")
	}

	switch {
	case len(rejected) > 0:
		d.Status = StatusRejected
		d.Reasons = rejected
	case len(unknown) > 0:
		d.Status = StatusUnknown
		d.Reasons = unknown
	case len(pending) > 0:
		d.Status = StatusPending
		d.Reasons = pending
	default:
		d.Status = StatusAccepted
		d.Reasons = []string{"PM sign-off, a qualifying developer approval on the current head, and all receipts are present"}
	}
	d.Accepted = d.Status == StatusAccepted
	return d
}

// Hash returns the SHA-256 of the canonical JSON encoding of every
// decision-affecting field. Reviews are sorted so review order never changes
// the hash; any change to a receipt, review, sign-off, or identifier does.
func (ev Evidence) Hash() (string, error) {
	reviews := make([]Review, len(ev.Reviews))
	copy(reviews, ev.Reviews)
	sort.Slice(reviews, func(i, j int) bool {
		a, b := reviews[i], reviews[j]
		if a.Reviewer != b.Reviewer {
			return a.Reviewer < b.Reviewer
		}
		if a.State != b.State {
			return a.State < b.State
		}
		if a.CommitSHA != b.CommitSHA {
			return a.CommitSHA < b.CommitSHA
		}
		if a.AuthorPermission != b.AuthorPermission {
			return a.AuthorPermission < b.AuthorPermission
		}
		return !a.Dismissed && b.Dismissed
	})
	raw, err := json.Marshal(hashEvidence{
		ApprovalID:         ev.ApprovalID,
		ManifestSHA256:     ev.ManifestSHA256,
		Repository:         ev.Repository,
		PR:                 ev.PR,
		HeadSHA:            ev.HeadSHA,
		RecipeHash:         ev.RecipeHash,
		BundleDigest:       ev.BundleDigest,
		Author:             ev.Author,
		PMSignoff:          hashSignoff{Actor: ev.PMSignoff.Actor, At: ev.PMSignoff.At},
		Reviews:            reviews,
		Receipts:           ev.Receipts,
		BranchProtected:    ev.BranchProtected,
		RecordedHeadSHA:    ev.RecordedHeadSHA,
		RecordedRecipeHash: ev.RecordedRecipeHash,
	})
	if err != nil {
		return "", fmt.Errorf("acceptance: canonical evidence encoding: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

type hashEvidence struct {
	ApprovalID         string      `json:"approval_id"`
	ManifestSHA256     string      `json:"manifest_sha256"`
	Repository         string      `json:"repository"`
	PR                 string      `json:"pr"`
	HeadSHA            string      `json:"head_sha"`
	RecipeHash         string      `json:"recipe_hash"`
	BundleDigest       string      `json:"bundle_digest"`
	Author             string      `json:"author"`
	PMSignoff          hashSignoff `json:"pm_signoff"`
	Reviews            []Review    `json:"reviews"`
	Receipts           Receipts    `json:"receipts"`
	BranchProtected    bool        `json:"branch_protected"`
	RecordedHeadSHA    string      `json:"recorded_head_sha"`
	RecordedRecipeHash string      `json:"recorded_recipe_hash"`
}

type hashSignoff struct {
	Actor string    `json:"actor"`
	At    time.Time `json:"at"`
}

// Collect gathers acceptance evidence for one pull request through the
// authenticated server-side client. Bot reviews are filtered out. Any GitHub
// failure is returned as a CollectError carrying ErrUnknown so callers
// surface unknown; it is never treated as approval or rejection.
func Collect(ctx context.Context, gh GitHub, repo string, number int) (Evidence, error) {
	if gh == nil {
		return Evidence{}, &CollectError{Op: "GitHub client", Err: errors.New("nil client")}
	}
	if strings.TrimSpace(repo) == "" || number <= 0 {
		return Evidence{}, errors.New("acceptance: repository and pull request number are required")
	}
	pr, err := gh.PullRequest(ctx, repo, number)
	if err != nil {
		return Evidence{}, &CollectError{Op: "fetch pull request", Err: err}
	}
	if pr.Merged {
		// A merged PR is a definitive state, not an API ambiguity: report it
		// directly instead of masking it as unknown.
		return Evidence{}, fmt.Errorf("acceptance: pull request %d is already merged", number)
	}
	rs, err := gh.Reviews(ctx, repo, number)
	if err != nil {
		return Evidence{}, &CollectError{Op: "fetch reviews", Err: err}
	}
	ev := Evidence{
		Repository:      repo,
		PR:              strconv.Itoa(number),
		HeadSHA:         pr.HeadSHA,
		Author:          pr.Author,
		BranchProtected: pr.BaseProtected,
	}
	for _, r := range rs {
		if isBot(r.Reviewer) {
			continue
		}
		// The author's own approval is not independent evidence. It is dropped
		// here rather than carried and ignored later, so no caller can count it.
		if r.Reviewer == pr.Author {
			continue
		}
		if r.Reviewer != "" && r.AuthorPermission == "" {
			perm, perr := gh.Permission(ctx, repo, r.Reviewer)
			if perr != nil {
				return Evidence{}, &CollectError{Op: "fetch permission for " + r.Reviewer, Err: perr}
			}
			r.AuthorPermission = perm
		}
		ev.Reviews = append(ev.Reviews, r)
	}
	return ev, nil
}

// isBot reports whether a login belongs to a bot or app account. Bot
// approvals never qualify as developer review.
func isBot(login string) bool {
	return strings.HasSuffix(strings.ToLower(login), "[bot]")
}

// qualifyingPermission matches the GitHub permission levels that count as
// repository write access.
func qualifyingPermission(p string) bool {
	switch p {
	case "write", "maintain", "admin":
		return true
	}
	return false
}

func reviewerLabel(login string) string {
	if strings.TrimSpace(login) == "" {
		return "unknown reviewer"
	}
	return login
}
