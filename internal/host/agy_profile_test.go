//go:build client && !server

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// setupAgyProfileEnv points the Plan Forge home and the host home at separate
// temp directories, so a test can prove which side a child process touches.
func setupAgyProfileEnv(t *testing.T) (pfHome, hostHome string) {
	t.Helper()
	pfHome = t.TempDir()
	hostHome = t.TempDir()
	t.Setenv("PLAN_FORGE_HOME", pfHome)
	t.Setenv("HOME", hostHome)
	t.Setenv("USERPROFILE", hostHome)
	return pfHome, hostHome
}

func TestEnsureAGYProfileCreatesPrivateProfile(t *testing.T) {
	pfHome, hostHome := setupAgyProfileEnv(t)

	isolation, err := ensureAGYProfile()
	if err != nil {
		t.Fatalf("ensureAGYProfile: %v", err)
	}
	wantDir := filepath.Join(pfHome, "profiles", "agy")
	if isolation.homeDir != wantDir {
		t.Fatalf("profile dir = %q, want %q", isolation.homeDir, wantDir)
	}
	if !strings.HasPrefix(isolation.settingsPath, wantDir+string(filepath.Separator)) {
		t.Fatalf("settings path %q escapes the profile", isolation.settingsPath)
	}
	info, err := os.Stat(wantDir)
	if err != nil {
		t.Fatalf("profile dir: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("profile dir mode = %o, want 700", info.Mode().Perm())
	}
	sinfo, err := os.Stat(isolation.settingsPath)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if sinfo.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %o, want 600", sinfo.Mode().Perm())
	}
	raw, err := os.ReadFile(isolation.settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("managed settings are not valid JSON: %v", err)
	}
	denied := map[string]bool{}
	for _, rule := range settings.Permissions.Deny {
		denied[rule] = true
	}
	for _, required := range requiredAGYDenyRules {
		if !denied[required] {
			t.Errorf("managed settings omit deny rule %s", required)
		}
	}
	// The host's own home must be untouched: no ~/.gemini on the host side.
	if _, err := os.Lstat(filepath.Join(hostHome, ".gemini")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host home was touched: .gemini exists or error = %v", err)
	}
	// Re-provisioning is idempotent and keeps the file.
	if _, err := ensureAGYProfile(); err != nil {
		t.Fatalf("second ensureAGYProfile: %v", err)
	}
}

func TestEnsureAGYProfileTightensLooseDirectory(t *testing.T) {
	pfHome, _ := setupAgyProfileEnv(t)
	dir := filepath.Join(pfHome, "profiles", "agy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureAGYProfile(); err != nil {
		t.Fatalf("ensureAGYProfile: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("profile dir mode = %o, want group/other bits cleared", info.Mode().Perm())
	}
}

func TestEnsureAGYProfilePreservesExistingSettings(t *testing.T) {
	pfHome, _ := setupAgyProfileEnv(t)
	dir := filepath.Join(pfHome, "profiles", "agy")
	settingsPath := agyProfileSettingsPath(dir)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// A settings file that satisfies the policy but carries an extra rule the
	// owner added must survive provisioning untouched.
	custom := []byte(`{"permissions":{"deny":["read_file(*)","write_file(*)","read_url(*)","execute_url(*)","command(*)","unsandboxed(*)","mcp(*)","browser(*)"]}}` + "\n")
	if err := os.WriteFile(settingsPath, custom, 0o600); err != nil {
		t.Fatal(err)
	}
	isolation, err := ensureAGYProfile()
	if err != nil {
		t.Fatalf("ensureAGYProfile: %v", err)
	}
	raw, err := os.ReadFile(isolation.settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(custom) {
		t.Fatalf("existing settings were rewritten:\n%s", raw)
	}
	if _, err := validateAGYIsolation(isolation); err != nil {
		t.Fatalf("owner-extended settings must pass validation: %v", err)
	}
}

func TestAGYPreflightProvisionsProfileAndPasses(t *testing.T) {
	pfHome, _ := setupAgyProfileEnv(t)
	dir := t.TempDir()
	stubPath(t, dir)
	stubBinary(t, dir, "agy", "1.2.3")

	adapter := &AGY{binary: "agy"}
	if err := adapter.Preflight(context.Background()); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if adapter.isolation == nil || !adapter.isolation.selectionVerified {
		t.Fatal("Preflight did not attach the verified isolated profile")
	}
	want := filepath.Join(pfHome, "profiles", "agy")
	if adapter.isolation.homeDir != want {
		t.Fatalf("isolated home = %q, want %q", adapter.isolation.homeDir, want)
	}
	// A second Preflight reuses the profile and still passes.
	if err := adapter.Preflight(context.Background()); err != nil {
		t.Fatalf("second Preflight: %v", err)
	}
}

func TestAGYPreflightFailsClosedOnPolicyDrift(t *testing.T) {
	setupAgyProfileEnv(t)
	dir := t.TempDir()
	stubPath(t, dir)
	stubBinary(t, dir, "agy", "1.2.3")
	adapter := &AGY{binary: "agy"}
	if err := adapter.Preflight(context.Background()); err != nil {
		t.Fatalf("initial Preflight: %v", err)
	}

	// Weaken the settings: drop the deny list. The gate must fail closed
	// rather than generate against a tool-capable profile.
	if err := os.WriteFile(adapter.isolation.settingsPath, []byte(`{"permissions":{"deny":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := adapter.Preflight(context.Background())
	var capability *contracts.ErrCapability
	if !errors.As(err, &capability) || !strings.Contains(err.Error(), "deny rule") {
		t.Fatalf("Preflight after drift = %v, want a deny-rule capability error", err)
	}
}

func TestAGYPreflightFailsClosedOnWorldReadableSettings(t *testing.T) {
	setupAgyProfileEnv(t)
	dir := t.TempDir()
	stubPath(t, dir)
	stubBinary(t, dir, "agy", "1.2.3")
	adapter := &AGY{binary: "agy"}
	if err := adapter.ensureIsolation(); err != nil {
		t.Fatalf("ensureIsolation: %v", err)
	}
	if err := os.Chmod(adapter.isolation.settingsPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Preflight(context.Background()); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("Preflight with 0644 settings = %v, want a privacy error", err)
	}
}

func TestAGYProvisioningFailureFailsClosed(t *testing.T) {
	t.Setenv("PLAN_FORGE_HOME", "relative/path")
	adapter := &AGY{binary: "agy"}
	err := adapter.Preflight(context.Background())
	var capability *contracts.ErrCapability
	if !errors.As(err, &capability) {
		t.Fatalf("expected ErrCapability, got %v", err)
	}
	if !strings.Contains(capability.Reason, "isolated agy profile") {
		t.Fatalf("unexpected reason: %s", capability.Reason)
	}
}

func TestAGYChildEnvPointsIntoProfile(t *testing.T) {
	pfHome, hostHome := setupAgyProfileEnv(t)
	adapter := &AGY{binary: "agy"}
	env, err := adapter.childEnv(t.TempDir())
	if err != nil {
		t.Fatalf("childEnv: %v", err)
	}
	want := filepath.Join(pfHome, "profiles", "agy")
	var home, userprofile string
	for _, entry := range env {
		if v, ok := strings.CutPrefix(entry, "HOME="); ok {
			home = v
		}
		if v, ok := strings.CutPrefix(entry, "USERPROFILE="); ok {
			userprofile = v
		}
	}
	if home != want || userprofile != want {
		t.Fatalf("child HOME=%q USERPROFILE=%q, want both %q", home, userprofile, want)
	}
	if home == hostHome {
		t.Fatal("child HOME still points at the host home")
	}
}

// The probe stub writes a credential file under its $HOME, the way a real
// sign-in would. With the isolated profile it must land inside the Plan Forge
// home, and the host's ~/.gemini must never be created. No real agy binary is
// involved.
func TestAGYProbeWritesOnlyInsideProfile(t *testing.T) {
	pfHome, hostHome := setupAgyProfileEnv(t)
	dir := t.TempDir()
	stubPath(t, dir)
	stub := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then printf '1.2.3\\n'; exit 0; fi\n" +
		"if [ \"$1\" = \"models\" ]; then\n" +
		"  mkdir -p \"$HOME/.gemini\"\n" +
		"  printf '{}' > \"$HOME/.gemini/oauth_creds.json\"\n" +
		"  printf 'gemini-3-pro\\n'\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "agy"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	adapter := &AGY{binary: "agy"}
	state, err := adapter.Auth(context.Background())
	if err != nil {
		t.Fatalf("Auth: %v", err)
	}
	if !state.Ready {
		t.Fatalf("Auth not ready against the writing stub: %+v", state)
	}
	models, err := adapter.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "gemini-3-pro" {
		t.Fatalf("Models = %v, %v", models, err)
	}

	creds := filepath.Join(pfHome, "profiles", "agy", ".gemini", "oauth_creds.json")
	if _, err := os.Stat(creds); err != nil {
		t.Fatalf("child did not write into the isolated profile: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(hostHome, ".gemini")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host ~/.gemini was touched: err = %v", err)
	}
}

func TestAGYAuthSignInHintTargetsIsolatedProfile(t *testing.T) {
	pfHome, _ := setupAgyProfileEnv(t)
	dir := t.TempDir()
	stubPath(t, dir)
	stub := "#!/bin/sh\nif [ \"$1\" = \"models\" ]; then exit 1; fi\nprintf '1.2.3\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "agy"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	adapter := &AGY{binary: "agy"}
	state, err := adapter.Auth(context.Background())
	if err != nil {
		t.Fatalf("Auth: %v", err)
	}
	if state.Ready {
		t.Fatal("Auth ready against a signed-out stub")
	}
	want := filepath.Join(pfHome, "profiles", "agy")
	if !strings.Contains(state.Detail, want) {
		t.Fatalf("sign-in hint %q does not point at the isolated profile %q", state.Detail, want)
	}
}
