package config_test

import (
	"os"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/config"
)

// A fresh install has no prepared VM. Launches must be impossible until the
// owner names a provider, so no code path can spend money by default.
func TestSandboxIsDisabledByDefault(t *testing.T) {
	cfg := config.Default()
	if cfg.Sandbox.Enabled() {
		t.Fatal("a default config enables the sandbox")
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("default config is invalid: %v", err)
	}
	if _, _, ok := cfg.Sandbox.EstimatedRuntimeCost(); ok {
		t.Error("a disabled sandbox quoted a cost")
	}
}

// Defaults fill in a bounded lifetime and modest resources, so a launch without
// explicit numbers still expires within the hour.
func TestSandboxDefaultsAreBounded(t *testing.T) {
	got := config.SandboxConfig{Provider: config.SandboxProviderE2B}.Resolved()
	if got.TimeoutSeconds != config.DefaultSandboxTimeoutSeconds {
		t.Errorf("timeout = %d, want %d", got.TimeoutSeconds, config.DefaultSandboxTimeoutSeconds)
	}
	if got.TimeoutSeconds > config.MaxSandboxTimeoutSeconds {
		t.Errorf("default lifetime %ds exceeds the provider ceiling", got.TimeoutSeconds)
	}
	if got.VCPU != config.DefaultSandboxVCPU || got.MemoryMB != config.DefaultSandboxMemoryMB {
		t.Errorf("resources = %d vCPU / %d MB, want the documented defaults", got.VCPU, got.MemoryMB)
	}
	if got.APIKeyEnv != config.DefaultSandboxAPIKeyEnv {
		t.Errorf("key env = %q, want %q", got.APIKeyEnv, config.DefaultSandboxAPIKeyEnv)
	}
	if got.DomainSuffix != config.DefaultE2BDomainSuffix {
		t.Errorf("domain suffix = %q, want %q", got.DomainSuffix, config.DefaultE2BDomainSuffix)
	}
}

// Validation is a closed set: an unknown provider disables nothing silently.
func TestValidateSandboxRejectsUnknownProviderAndBounds(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.SandboxConfig
	}{
		{"unknown provider", config.SandboxConfig{Provider: "fly"}},
		{"zero lifetime below floor", config.SandboxConfig{Provider: config.SandboxProviderE2B, TimeoutSeconds: 59}},
		{"lifetime above ceiling", config.SandboxConfig{Provider: config.SandboxProviderE2B, TimeoutSeconds: 3601}},
		{"key env with a value", config.SandboxConfig{Provider: config.SandboxProviderE2B, APIKeyEnv: "E2B_API_KEY=secret"}},
		{"template path", config.SandboxConfig{Provider: config.SandboxProviderE2B, Template: "../escape"}},
		{"domain suffix without dot", config.SandboxConfig{Provider: config.SandboxProviderE2B, DomainSuffix: "e2b.app"}},
		{"negative memory", config.SandboxConfig{Provider: config.SandboxProviderE2B, MemoryMB: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := config.ValidateSandbox(tc.cfg); err == nil {
				t.Fatal("validation accepted an invalid sandbox setting")
			}
		})
	}
	if err := config.ValidateSandbox(config.SandboxConfig{}); err != nil {
		t.Fatalf("a disabled sandbox must always validate: %v", err)
	}
}

// A cost is quoted only when real rates are configured. Guessing a price is
// worse than saying it is unavailable.
func TestSandboxCostNeedsBothRates(t *testing.T) {
	s := config.SandboxConfig{
		Provider:       config.SandboxProviderE2B,
		TimeoutSeconds: 3600,
		VCPU:           2,
		MemoryMB:       4096,
		HourlyCPUCents: 5,
	}
	if _, _, ok := s.EstimatedRuntimeCost(); ok {
		t.Fatal("a half-configured price produced an estimate")
	}

	s.HourlyMemoryCents = 2
	lifetime, cost, ok := s.EstimatedRuntimeCost()
	if !ok {
		t.Fatal("both rates were configured and no estimate was produced")
	}
	if lifetime != time.Hour {
		t.Errorf("lifetime = %v, want one hour", lifetime)
	}
	// 1h * (2 vCPU * 5c + 4096 MB / 1024 * 2c) = 10c + 8c.
	if cost < 0.1799 || cost > 0.1801 {
		t.Errorf("cost = %f, want 0.18", cost)
	}
}

// Extra work-view roots are repository-relative directories only: an absolute
// path or a traversal would read outside the project.
func TestValidateRepositoryWorkRoots(t *testing.T) {
	if err := config.ValidateRepositoryWorkRoots(nil); err != nil {
		t.Fatalf("no extra roots must validate: %v", err)
	}
	if err := config.ValidateRepositoryWorkRoots([]string{"planning/plans", "docs/epics"}); err != nil {
		t.Fatalf("relative roots must validate: %v", err)
	}
	for _, bad := range []string{"", "/etc", "../secrets", "planning/../../etc", ".git", "a\\b"} {
		if err := config.ValidateRepositoryWorkRoots([]string{bad}); err == nil {
			t.Errorf("root %q was accepted", bad)
		}
	}
}

// The new settings survive a save/load round trip and are recognised, so a
// second run of the same build does not treat them as another build's keys.
func TestSandboxSettingsRoundTrip(t *testing.T) {
	dir := isolateHome(t)
	path := dir + "/config.json"
	cfg := config.Default()
	cfg.RepositoryWorkRoots = []string{"planning/plans"}
	cfg.Sandbox = config.SandboxConfig{Provider: config.SandboxProviderE2B, TimeoutSeconds: 1800}
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Sandbox.Provider != config.SandboxProviderE2B || got.Sandbox.TimeoutSeconds != 1800 {
		t.Errorf("sandbox settings were lost: %+v", got.Sandbox)
	}
	if len(got.RepositoryWorkRoots) != 1 || got.RepositoryWorkRoots[0] != "planning/plans" {
		t.Errorf("work roots were lost: %v", got.RepositoryWorkRoots)
	}
	if err := config.Validate(got); err != nil {
		t.Errorf("a round-tripped config does not validate: %v", err)
	}
}

// A malformed sandbox block is a reported config error, never a silent fallback
// to a launch with defaults the owner did not choose.
func TestLoadRejectsAnInvalidSandboxBlock(t *testing.T) {
	dir := isolateHome(t)
	path := dir + "/config.json"
	write := `{"schema_version":1,"sandbox":{"provider":"unknown-cloud"}}`
	if err := os.WriteFile(path, []byte(write), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("an unknown provider passed validation")
	}
}
