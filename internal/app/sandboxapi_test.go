//go:build client && !server

package app

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/recipe"
	"github.com/danielckv/agentic-plan-forge/internal/sandbox"
)

// Launching spends money, so every precondition is checked before a provider is
// contacted. Each case is a state the window can show, and none may create a
// sandbox.
func TestLaunchRefusesWithoutAProvider(t *testing.T) {
	m := &sandboxManager{repo: t.TempDir(), configured: false, setupHint: "no provider configured"}
	report := m.Launch(context.Background(), LaunchRequest{Confirmed: true, ApprovalID: "a", AppPort: 8080})
	if report.State != "setup" {
		t.Errorf("state = %q, want setup", report.State)
	}
	if report.Problem == "" {
		t.Error("a refused launch gave no reason")
	}
	if report.SandboxID != "" {
		t.Error("a refused launch reported a sandbox")
	}
}

func configuredManager(t *testing.T) *sandboxManager {
	t.Helper()
	return &sandboxManager{
		repo:       t.TempDir(),
		configured: true,
		cfg:        sandbox.Settings{Template: "base", TimeoutSeconds: 3600, VCPU: 2, MemoryMB: 4096, APIKey: "k"},
	}
}

// Approving a plan must never start a paid VM as a side effect: a launch needs
// its own confirmation.
func TestLaunchRefusesWithoutConfirmation(t *testing.T) {
	report := configuredManager(t).Launch(t.Context(), LaunchRequest{ApprovalID: "a", AppPort: 8080})
	if report.State != "setup" {
		t.Fatalf("state = %q, want setup", report.State)
	}
	if report.Problem == "" {
		t.Fatal("an unconfirmed launch gave no reason")
	}
}

// Every other precondition names itself, so the owner can fix it.
func TestLaunchNamesTheMissingPrecondition(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  LaunchRequest
	}{
		{"no approval", LaunchRequest{Confirmed: true, AppPort: 8080}},
		{"no port", LaunchRequest{Confirmed: true, ApprovalID: "a", AppPort: 0}},
		{"no recipe", LaunchRequest{Confirmed: true, ApprovalID: "a", AppPort: 8080}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := configuredManager(t).Launch(t.Context(), tc.req)
			if report.State != "setup" {
				t.Fatalf("state = %q, want setup", report.State)
			}
			if report.Problem == "" {
				t.Fatal("a refused launch gave no reason")
			}
		})
	}
}

// Ready is never reached without receipts: a run that fails is a failure the
// page can show, never a green preview.
func TestRunWithoutReceiptsRefusesReady(t *testing.T) {
	m := configuredManager(t)
	m.lease = sandbox.Lifecycle{SandboxID: "sb-1", State: sandbox.StateBuilding}
	m.drive = stubDriver{err: errors.New("build exited 2")}

	report := m.Run(t.Context(), recipe.Recipe{})
	if report.State == "ready" {
		t.Fatal("a failing run reported ready")
	}
	if report.Problem == "" {
		t.Error("a failed run gave no reason")
	}
}

// A successful run with every receipt is the only path to ready.
func TestRunWithEveryReceiptReachesReady(t *testing.T) {
	m := configuredManager(t)
	m.lease = sandbox.Lifecycle{SandboxID: "sb-1", State: sandbox.StateTesting}
	m.drive = stubDriver{receipts: []sandbox.Receipt{
		{Kind: sandbox.ReceiptBuild, ExitCode: 0},
		{Kind: sandbox.ReceiptTest, ExitCode: 0},
		{Kind: sandbox.ReceiptReadiness, ExitCode: 0},
	}}

	report := m.Run(t.Context(), recipe.Recipe{})
	if report.State != "ready" {
		t.Fatalf("state = %q, want ready", report.State)
	}
	if report.Preview == "" {
		t.Error("a ready sandbox offers no preview")
	}
}

// A lease that cannot be deleted is reported as pending cleanup, never as
// stopped: the provider's expiry is then the only backstop.
func TestStopReportsPendingCleanup(t *testing.T) {
	m := &sandboxManager{repo: t.TempDir(), configured: true, prov: failingProvider{}}
	m.lease = sandbox.Lifecycle{SandboxID: "sb-1", State: sandbox.StateReady}

	report := m.Stop(t.Context())
	if report.State != string(sandbox.StateUnknown) {
		t.Errorf("state = %q, want unknown after a failed deletion", report.State)
	}
	if report.Cleanup == "" {
		t.Error("a failed deletion did not say cleanup is pending")
	}
}

// Two clicks must not reconcile to one sandbox by accident, and an identity is
// never empty.
func TestOperationIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 64 {
		id := newOperationID()
		if id == "" {
			t.Fatal("an empty operation id was minted")
		}
		if seen[id] {
			t.Fatal("the operation id repeated")
		}
		seen[id] = true
	}
}

// The preview origin check admits its own origin and refuses another page.
func TestPreviewOriginAdmitsOnlyItsOwnOrigin(t *testing.T) {
	check := previewOrigin("127.0.0.1:9111")
	for _, tc := range []struct {
		origin string
		want   bool
	}{
		{"", true},
		{"http://127.0.0.1:9111", true},
		{"http://127.0.0.1:1111", false},
		{"https://evil.example", false},
	} {
		req, err := http.NewRequest(http.MethodGet, "/preview/", nil)
		if err != nil {
			t.Fatal(err)
		}
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if got := check(req); got != tc.want {
			t.Errorf("origin %q admitted = %v, want %v", tc.origin, got, tc.want)
		}
	}
}

// stubDriver is a driver that never reaches a provider.
type stubDriver struct {
	receipts []sandbox.Receipt
	err      error
}

func (d stubDriver) RunRecipe(context.Context, string, recipe.Recipe, time.Time, time.Time) ([]sandbox.Receipt, error) {
	return d.receipts, d.err
}

// failingProvider is a provider whose deletion always fails.
type failingProvider struct{}

func (failingProvider) Create(context.Context, sandbox.CreateRequest) (sandbox.Lease, error) {
	return sandbox.Lease{}, errors.New("not used")
}

func (failingProvider) Kill(context.Context, string) error { return errors.New("provider refused") }

func (failingProvider) Inspect(context.Context, string) (sandbox.Info, error) {
	return sandbox.Info{}, nil
}

func (failingProvider) PreviewURL(string, int) (string, error) { return "", nil }

func (failingProvider) InspectOperation(context.Context, string) (sandbox.Lease, error) {
	return sandbox.Lease{}, nil
}
