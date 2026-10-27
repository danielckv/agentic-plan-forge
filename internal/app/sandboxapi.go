//go:build client && !server

package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/recipe"
	"github.com/danielckv/agentic-plan-forge/internal/sandbox"
)

// The prepared sandbox is an explicit, paid action.
//
// Every rule here keeps that action honest: it needs a configured provider, a
// reviewed recipe, an approved plan and a confirmation that names the cost and
// the expiry; a create that may or may not have happened is reconciled by
// operation identity instead of retried; and the window is never told a build
// passed without a receipt that says so.

// sandboxManager owns the one live lease of a session. One sandbox at a time is
// deliberate: two paid VMs from one click is the failure this prevents.
type sandboxManager struct {
	mu    sync.Mutex
	repo  string
	cfg   sandbox.Settings
	lease sandbox.Lifecycle
	gate  *sandbox.AccessGate
	prov  sandbox.Provider
	// configured is false when no provider or key is available. Every launch
	// then reports a setup problem, and nothing else in the product changes.
	configured bool
	setupHint  string

	// appPort is the port inside the sandbox the preview serves. The lifecycle
	// record does not carry it because it belongs to this side of the proxy.
	appPort int
	// drive runs the recorded commands inside the lease. It is a field so a test
	// can substitute a driver that never reaches a provider.
	drive sandboxRun
}

// sandboxRun runs one recipe in a created sandbox and returns its receipts.
type sandboxRun interface {
	RunRecipe(ctx context.Context, sandboxID string, r recipe.Recipe, leaseExpiry, now time.Time) ([]sandbox.Receipt, error)
}

// newSandboxManager resolves the provider settings once per session.
func newSandboxManager(repo string, cfg sandbox.Settings, configured bool, hint string) *sandboxManager {
	return &sandboxManager{repo: repo, cfg: cfg, configured: configured, setupHint: hint}
}

// SessionSandboxSettings resolves the prepared-VM settings from the configuration
// and the environment. A missing provider or key disables launching with a
// reason the window can show; it never fails the session.
func (s *appSession) SessionSandboxSettings() (sandbox.Settings, bool, string) {
	resolved := s.cfg.Sandbox.Resolved()
	if strings.TrimSpace(resolved.Provider) == "" {
		return sandbox.Settings{}, false,
			"No prepared-VM provider is configured. Set sandbox.provider to e2b to enable a remote build and preview; documents, implementation and acceptance work without it."
	}
	key := strings.TrimSpace(os.Getenv(resolved.APIKeyEnv))
	if key == "" {
		return sandbox.Settings{}, false,
			"The sandbox provider key is missing. Set " + resolved.APIKeyEnv + " in the environment that starts Plan Forge."
	}
	return sandbox.Settings{
		BaseURL:        resolved.BaseURL,
		APIKey:         key,
		Template:       resolved.Template,
		TimeoutSeconds: resolved.TimeoutSeconds,
		VCPU:           resolved.VCPU,
		MemoryMB:       resolved.MemoryMB,
		DomainSuffix:   resolved.DomainSuffix,
	}, true, ""
}

// LaunchRequest is what the owner confirmed before any money is spent.
type LaunchRequest struct {
	// Confirmed is explicit: a launch never happens as a side effect of
	// approving a plan or opening the page.
	Confirmed bool `json:"confirmed"`
	// AppPort is the port inside the sandbox the preview serves.
	AppPort int `json:"appPort"`
	// RecipeHash pins the recipe the owner reviewed; a mismatch refuses.
	RecipeHash string `json:"recipeHash"`
	// ApprovalID pins the approved plan the launch belongs to.
	ApprovalID string `json:"approvalId"`
	// CostAcknowledged names the estimate the owner saw. Empty means they saw
	// "unavailable", which is allowed only when no rates are configured.
	CostAcknowledged string `json:"costAcknowledged"`
}

// LaunchReport is the state the window renders.
type LaunchReport struct {
	State     string            `json:"state"`
	SandboxID string            `json:"sandboxId,omitempty"`
	ExpiresAt time.Time         `json:"expiresAt,omitempty"`
	Preview   string            `json:"preview,omitempty"`
	Problem   string            `json:"problem,omitempty"`
	Cleanup   string            `json:"cleanup,omitempty"`
	Receipts  []sandbox.Receipt `json:"receipts,omitempty"`
}

// Launch creates one sandbox. It refuses without a confirmation, an approved
// plan, a reviewed recipe or a provider, because each refusal is a state the
// owner can act on.
func (m *sandboxManager) Launch(ctx context.Context, req LaunchRequest) LaunchReport {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.configured {
		return LaunchReport{State: "setup", Problem: m.setupHint}
	}
	if m.lease.State != "" && !m.lease.State.Terminal() {
		return m.report()
	}
	switch {
	case !req.Confirmed:
		return LaunchReport{State: "setup", Problem: "A launch spends money and must be confirmed first."}
	case req.ApprovalID == "":
		return LaunchReport{State: "setup", Problem: "Approve a plan before launching a sandbox."}
	case req.AppPort < 1 || req.AppPort > 65535:
		return LaunchReport{State: "setup", Problem: "The preview needs a port inside the sandbox."}
	}

	stored, ok := LoadStoredRecipe(m.repo)
	if !ok {
		return LaunchReport{
			State:   "setup",
			Problem: "No execution recipe is recorded yet. Review the build and test commands before launching.",
		}
	}
	if req.RecipeHash != "" {
		hash, err := stored.Hash()
		if err != nil {
			return LaunchReport{State: "setup", Problem: "The recorded recipe could not be hashed."}
		}
		if hash != req.RecipeHash {
			return LaunchReport{
				State:   "setup",
				Problem: "The recipe changed since it was reviewed. Review it again before spending money.",
			}
		}
	}

	operationID := newOperationID()
	if operationID == "" {
		return LaunchReport{State: "setup", Problem: "A launch needs a system random source for its operation identity."}
	}
	m.lease = sandbox.Lifecycle{
		OperationID: operationID,
		ApprovalID:  req.ApprovalID,
		RecipeHash:  req.RecipeHash,
		State:       sandbox.StatePreparing,
	}
	lease, err := m.provider().Create(ctx, sandbox.CreateRequest{
		Template:       m.cfg.Template,
		TimeoutSeconds: m.cfg.TimeoutSeconds,
		VCPU:           m.cfg.VCPU,
		MemoryMB:       m.cfg.MemoryMB,
		OperationID:    operationID,
		// The preview is reachable only through this process's authenticated
		// proxy, so the provider URL itself stays private.
		AllowPublicTraffic: false,
		Metadata:           map[string]string{"operation_id": operationID, "approval_id": req.ApprovalID},
	})
	switch {
	case errors.Is(err, sandbox.ErrAmbiguous):
		// The sandbox may exist. Reconcile by operation identity before any
		// retry, so a paid VM is never created twice.
		m.lease = m.lease.ReconcileUnknown()
		m.lease.LastError = "the create outcome is unknown; reconciling by operation id"
		return m.report()
	case err != nil:
		m.lease = m.lease.MarkFailed(err.Error())
		return m.report()
	}

	m.lease.SandboxID = lease.SandboxID
	m.lease.ExpiresAt = lease.ExpiresAt
	m.lease.State = sandbox.StateBuilding
	m.appPort = req.AppPort
	return m.report()
}

// Run executes the recorded recipe in the created sandbox and records each
// receipt on the lease. It is the only path that can move a lease to ready, and
// it refuses to do so without a successful build, test and readiness receipt.
func (m *sandboxManager) Run(ctx context.Context, r recipe.Recipe) LaunchReport {
	m.mu.Lock()
	if m.lease.SandboxID == "" {
		m.mu.Unlock()
		return LaunchReport{State: "setup", Problem: "There is no sandbox to run in."}
	}
	driver := m.drive
	sandboxID, expiry := m.lease.SandboxID, m.lease.ExpiresAt
	m.lease.State = sandbox.StateTesting
	m.mu.Unlock()

	if driver == nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.lease = m.lease.MarkFailed("no execution driver is configured for this build")
		return m.report()
	}
	receipts, err := driver.RunRecipe(ctx, sandboxID, r, expiry, time.Now())

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, receipt := range receipts {
		m.lease = m.lease.AddReceipt(receipt)
	}
	if err != nil {
		m.lease = m.lease.MarkFailed(err.Error())
		return m.report()
	}
	m.lease.State = sandbox.StateReady
	if verr := m.lease.Validate(); verr != nil {
		// Receipts that do not add up are a failure, not a ready page.
		m.lease = m.lease.MarkFailed(verr.Error())
	}
	return m.report()
}

// Stop requests deletion. An unconfirmed deletion is reported as pending rather
// than assumed: the provider's own expiry is then the backstop.
func (m *sandboxManager) Stop(ctx context.Context) LaunchReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lease.SandboxID == "" {
		return LaunchReport{State: string(sandbox.StateStopped)}
	}
	m.lease.State = sandbox.StateStopping
	if err := m.provider().Kill(ctx, m.lease.SandboxID); err != nil {
		m.lease.State = sandbox.StateUnknown
		m.lease.LastError = err.Error()
		report := m.report()
		report.Cleanup = "Deletion could not be confirmed; the provider will expire this sandbox."
		return report
	}
	m.lease.State = sandbox.StateStopped
	if m.gate != nil {
		m.gate.RevokeAll()
	}
	return m.report()
}

// Report is the current state, safe to call from a handler.
func (m *sandboxManager) Report() LaunchReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.report()
}

func (m *sandboxManager) report() LaunchReport {
	r := LaunchReport{
		State:     string(m.lease.State),
		SandboxID: m.lease.SandboxID,
		ExpiresAt: m.lease.ExpiresAt,
		Problem:   m.lease.LastError,
		Receipts:  m.lease.Receipts,
	}
	if m.lease.State == sandbox.StateReady && m.lease.SandboxID != "" {
		r.Preview = "/preview/"
	}
	return r
}

// provider builds the adapter once. It holds no state, so building one per call
// would only spread configuration.
func (m *sandboxManager) provider() sandbox.Provider {
	if m.prov == nil {
		m.prov = sandbox.NewHTTPProvider(m.cfg)
	}
	return m.prov
}

// newOperationID mints the reconciliation identity for one create. A
// predictable identity is worse than none, so a failure to read the system
// random source returns empty and the caller refuses the launch.
func newOperationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// previewOrigin allows only the preview's own origin. A navigation carries no
// Origin header, so the live cookie remains the gate for it.
func previewOrigin(host string) func(*http.Request) bool {
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return strings.EqualFold(u.Host, host)
	}
}
