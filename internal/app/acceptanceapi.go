//go:build client && !server

package app

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/acceptance"
	"github.com/danielckv/agentic-plan-forge/internal/planstate"
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
	view.Decision = acceptance.Evaluate(evidence, expectedHead)
	if found && !stale {
		view.Record = &record
	}
	return view
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
