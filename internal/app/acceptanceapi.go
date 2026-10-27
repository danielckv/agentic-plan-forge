//go:build client && !server

package app

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/acceptance"
	"github.com/danielckv/agentic-plan-forge/internal/planstate"
	"github.com/danielckv/agentic-plan-forge/internal/storage"
)

// Accepting an implementation is a second decision, separate from approving the
// plan. A product sign-off and a qualifying developer review must cover the same
// revision, and an unavailable answer is never turned into approval.

// AcceptanceView is what the page shows about the implementation gate.
type AcceptanceView struct {
	// Available is false when no review client is configured: the page then
	// explains the setup instead of pretending the evidence is missing.
	Available bool                       `json:"available"`
	Setup     string                     `json:"setup,omitempty"`
	Decision  acceptance.Decision        `json:"decision"`
	Record    *planstate.AcceptanceEntry `json:"record,omitempty"`
}

// ImplementationAcceptance reads the review from GitHub and evaluates the gate
// for one approved plan and pull request. A failure is unknown with its reason,
// never accepted.
func (s *appSession) ImplementationAcceptance(ctx context.Context, approvalID, pullRequest, expectedHead string) AcceptanceView {
	client, hint := s.githubClient()
	if client == nil {
		return AcceptanceView{
			Available: false,
			Setup:     hint,
			Decision:  acceptance.Decision{Status: acceptance.StatusUnknown, Reasons: []string{hint}},
		}
	}
	if rejection := acceptancePrecondition(approvalID, pullRequest); rejection != "" {
		return AcceptanceView{
			Available: true,
			Decision:  acceptance.Decision{Status: acceptance.StatusPending, Reasons: []string{rejection}},
		}
	}
	number, err := acceptance.ParsePRNumber(pullRequest)
	if err != nil {
		return AcceptanceView{
			Available: true,
			Decision:  acceptance.Decision{Status: acceptance.StatusPending, Reasons: []string{err.Error()}},
		}
	}
	repo, err := s.githubRepo(ctx)
	if err != nil {
		return AcceptanceView{
			Available: true,
			Decision:  acceptance.Decision{Status: acceptance.StatusPending, Reasons: []string{err.Error()}},
		}
	}

	evidence, err := acceptance.Collect(ctx, client, repo, number)
	if err != nil {
		// CollectError carries ErrUnknown: the API could not answer, so the gate
		// cannot approve and must not reject either.
		return AcceptanceView{Available: true, Decision: acceptance.Decision{
			Status:  acceptance.StatusUnknown,
			Reasons: []string{err.Error()},
		}}
	}
	evidence.ApprovalID = approvalID

	// A record bound to another revision makes the gate stale, not accepted:
	// the evidence carries the recorded head and recipe so Evaluate rejects it.
	view := AcceptanceView{Available: true}
	record, found, stale, rerr := planstate.ImplementationAcceptance(s.repo, approvalID, evidence.HeadSHA)
	switch {
	case rerr != nil:
		view.Decision = acceptance.Decision{Status: acceptance.StatusUnknown,
			Reasons: []string{"the acceptance ledger could not be read: " + rerr.Error()}}
		return view
	case stale:
		evidence.RecordedHeadSHA = record.HeadSHA
		fallthrough
	case found:
		evidence.RecordedRecipeHash = record.RecipeHash
	}
	// The gate needs the product sign-off and the validation receipts, which live
	// in this machine's records rather than in GitHub. Both are read from trusted
	// sources; neither is taken from the request.
	if signoff, ok, serr := planstate.ProductSignoff(s.repo, approvalID, evidence.HeadSHA); serr == nil && ok {
		evidence.PMSignoff = acceptance.Signoff{Actor: signoff.Actor, At: signoff.At}
	}
	if manifest, merr := s.manifestHash(approvalID); merr == nil {
		evidence.ManifestSHA256 = manifest
	}
	evidence.Receipts = s.validationReceipts(approvalID, evidence.HeadSHA)

	view.Decision = acceptance.Evaluate(evidence, expectedHead)
	if found && !stale {
		view.Record = &record
	}
	return view
}

// manifestHash returns the approved manifest's hash for one approval, read from
// the committed plan. It fails closed: without a manifest the gate cannot know
// which plan the implementation belongs to.
func (s *appSession) manifestHash(approvalID string) (string, error) {
	pair, err := storage.ReadPair(s.repo)
	if err != nil {
		return "", err
	}
	if pair.Meta.ApprovalID != "" && pair.Meta.ApprovalID != approvalID {
		return "", errors.New("the committed plan belongs to a different approval")
	}
	return pair.ManifestSHA256, nil
}

// validationReceipts reports what the prepared run actually proved for this
// revision. Nothing is recorded yet unless the sandbox produced receipts, so the
// answer is empty and the gate stays pending: an absent check is never a pass.
func (s *appSession) validationReceipts(approvalID, headSHA string) acceptance.Receipts {
	if s.sandboxMgr == nil {
		return acceptance.Receipts{}
	}
	return acceptance.Receipts{}
}

// acceptancePrecondition names the first missing input, so the page can ask for
// it instead of showing an unexplained pending gate.
func acceptancePrecondition(approvalID, pullRequest string) string {
	switch {
	case strings.TrimSpace(approvalID) == "":
		return "Approve a plan before accepting an implementation."
	case strings.TrimSpace(pullRequest) == "":
		return "Attach the pull request that implements the plan."
	}
	return ""
}

// githubClient returns the review client, or nil with the reason it is absent.
func (s *appSession) githubClient() (acceptance.GitHub, string) {
	token := strings.TrimSpace(os.Getenv(acceptance.GitHubAPIKeyEnv))
	if token == "" {
		return nil, "Reviewing the implementation needs a GitHub token. Set " + acceptance.GitHubAPIKeyEnv +
			" in the environment that starts Plan Forge. A plan can still be approved and exported without it."
	}
	client, err := acceptance.NewHTTPGitHub(s.cfg.GitHubAPIURL, token)
	if err != nil {
		return nil, "The GitHub review client could not start: " + err.Error()
	}
	return client, ""
}

// githubRepo reports the owner/name pair from the repository's own remote. It
// never guesses a name.
func (s *appSession) githubRepo(ctx context.Context) (string, error) {
	slug, err := githubSlugFromRemote(ctx, s.repo)
	if err != nil {
		return "", err
	}
	return slug, nil
}

// signoffRequest is the product owner's own sign-off for one revision.
type SignoffRequest struct {
	ApprovalID string `json:"approvalId"`
	HeadSHA    string `json:"headSha"`
}

// POST /api/acceptance/signoff — record the product owner's sign-off for the
// revision they reviewed. The actor is this session's owner, never a name the
// browser supplies.
func (s *appSession) handleAcceptanceSignoff(w http.ResponseWriter, r *http.Request) {
	if !s.owns() {
		writeAPIError(w, http.StatusNotFound, "only the session owner can sign off")
		return
	}
	var req SignoffRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.ApprovalID == "" || req.HeadSHA == "" {
		writeAPIError(w, http.StatusBadRequest, "approval and head are required")
		return
	}
	actor := ownerMemberID
	if s.cred != nil && s.cred.MemberID != "" {
		actor = s.cred.MemberID
	}
	if err := planstate.RecordProductSignoff(s.repo, planstate.SignoffEntry{
		ApprovalID: req.ApprovalID, HeadSHA: req.HeadSHA, Actor: actor,
	}); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "the sign-off could not be recorded")
		return
	}
	writeJSON(w, http.StatusOK, s.ImplementationAcceptance(r.Context(), req.ApprovalID, r.URL.Query().Get("pr"), req.HeadSHA))
}

// POST /api/acceptance — record the acceptance when the gate accepts. The gate is
// re-read here rather than trusted from the request: a browser cannot assert its
// own approval.
func (s *appSession) handleAcceptanceRecord(w http.ResponseWriter, r *http.Request) {
	if !s.owns() {
		writeAPIError(w, http.StatusNotFound, "only the session owner can accept an implementation")
		return
	}
	var req struct {
		ApprovalID  string `json:"approvalId"`
		PullRequest string `json:"pullRequest"`
		HeadSHA     string `json:"headSha"`
		RecipeHash  string `json:"recipeHash"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	view := s.ImplementationAcceptance(r.Context(), req.ApprovalID, req.PullRequest, req.HeadSHA)
	if !view.Decision.Accepted {
		writeJSON(w, http.StatusConflict, view)
		return
	}
	if err := planstate.RecordAcceptance(s.repo, planstate.AcceptanceEntry{
		ApprovalID: req.ApprovalID, HeadSHA: req.HeadSHA, PullRequest: req.PullRequest,
		RecipeHash: req.RecipeHash, EvidenceHash: view.Decision.EvidenceHash,
	}); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "the acceptance could not be recorded")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// GET /api/acceptance?approval=…&pr=…&head=… — the implementation gate.
func (s *appSession) handleAcceptance(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	approval, pr, head := q.Get("approval"), q.Get("pr"), q.Get("head")
	if approval == "" {
		writeAPIError(w, http.StatusBadRequest, "approval is required")
		return
	}
	writeJSON(w, http.StatusOK, s.ImplementationAcceptance(r.Context(), approval, pr, head))
}
