//go:build client && !server

package host

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/integrations"
)

// installClaudeSettings writes a settings.json carrying the managed hooks this
// installer would produce, optionally renaming the binary they point at.
func installClaudeSettings(t *testing.T, in *Installer, home, bin string) {
	t.Helper()
	merged, _, err := mergeClaudeHook(map[string]any{},
		strings.ReplaceAll(claudeHookCommand, "{{PLAN_FORGE_BIN}}", bin),
		strings.ReplaceAll(claudeCenterCommand, "{{PLAN_FORGE_BIN}}", bin),
		strings.ReplaceAll(claudeGuardCommand, "{{PLAN_FORGE_BIN}}", bin),
		strings.ReplaceAll(claudeHandoffCommand, "{{PLAN_FORGE_BIN}}", bin),
	)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), string(raw))
}

func TestInspectHooksReportsClaudeInstalledStaleAndMissing(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		in, home := newTestInstaller(t)
		got := statusFor(t, in, home, "", string(KindClaude))
		if got.State != HookMissing {
			t.Errorf("state = %q, want missing", got.State)
		}
		if len(got.Missing) != len(claudeHookEvents) {
			t.Errorf("missing = %v, want every managed event", got.Missing)
		}
		if got.Remedy == "" {
			t.Error("a missing hook has no remedy")
		}
	})

	t.Run("installed", func(t *testing.T) {
		in, home := newTestInstaller(t)
		installClaudeSettings(t, in, home, in.bin)
		got := statusFor(t, in, home, "", string(KindClaude))
		if got.State != HookInstalled {
			t.Fatalf("state = %q (%+v), want installed", got.State, got)
		}
		if len(got.Events) != len(claudeHookEvents) {
			t.Errorf("events = %v, want every managed event", got.Events)
		}
		if got.Remedy != "" {
			t.Errorf("an installed hook carries a remedy: %q", got.Remedy)
		}
	})

	t.Run("stale binary", func(t *testing.T) {
		in, home := newTestInstaller(t)
		installClaudeSettings(t, in, home, "/old/path/plan-forge")
		got := statusFor(t, in, home, "", string(KindClaude))
		if got.State != HookStale {
			t.Fatalf("state = %q, want stale", got.State)
		}
		if got.Bin != "/old/path/plan-forge" {
			t.Errorf("bin = %q, want the stale path", got.Bin)
		}
		if len(got.Stale) != len(claudeHookEvents) {
			t.Errorf("stale = %v, want every event", got.Stale)
		}
	})

	t.Run("unreadable", func(t *testing.T) {
		in, home := newTestInstaller(t)
		writeFile(t, filepath.Join(home, ".claude", "settings.json"), "{not json")
		got := statusFor(t, in, home, "", string(KindClaude))
		if got.State != HookUnreadable {
			t.Errorf("state = %q, want unreadable", got.State)
		}
	})

	// An install that predates the command center has one SessionStart handler
	// where two are wanted, which is stale rather than installed.
	t.Run("one handler where two belong", func(t *testing.T) {
		in, home := newTestInstaller(t)
		handler := map[string]any{
			"type":    "command",
			"command": strings.ReplaceAll(claudeHookCommand, "{{PLAN_FORGE_BIN}}", in.bin) + " # " + managedMarker,
			"timeout": claudeHookTimeout,
		}
		raw, err := json.Marshal(map[string]any{"hooks": map[string]any{
			"SessionStart": []any{map[string]any{"hooks": []any{handler}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(home, ".claude", "settings.json"), string(raw))
		got := statusFor(t, in, home, "", string(KindClaude))
		if got.State != HookStale {
			t.Fatalf("state = %q, want stale", got.State)
		}
		if len(got.Stale) == 0 || got.Stale[0] != "SessionStart" {
			t.Errorf("stale = %v, want SessionStart", got.Stale)
		}
	})
}

// The oh-my-pi remedy has to say what is actually lost, because no skill backs
// those commands up any more.
func TestInspectHooksNamesWhatAMissingOMPHookCosts(t *testing.T) {
	in, home := newTestInstaller(t)
	got := statusFor(t, in, home, "", string(KindOMP))
	if got.State != HookMissing {
		t.Fatalf("state = %q, want missing", got.State)
	}
	for _, want := range []string{"/plan-forge", "/plan-forge-board", "no skills"} {
		if !strings.Contains(got.Remedy, want) {
			t.Errorf("remedy %q does not mention %q", got.Remedy, want)
		}
	}
}

func TestInspectHooksReportsOMPInstalled(t *testing.T) {
	in, home := newTestInstaller(t)
	path := filepath.Join(home, ".omp", "agent", "hooks", "pre", "plan-forge.ts")
	writeFile(t, path, in.substitute(integrations.OMPHook(), string(KindOMP)))
	got := statusFor(t, in, home, "", string(KindOMP))
	if got.State != HookInstalled {
		t.Fatalf("state = %q, want installed", got.State)
	}
	if got.Remedy != "" {
		t.Errorf("an installed hook carries a remedy: %q", got.Remedy)
	}
}

// A hook someone edited is reported as modified, not stale: re-running install
// would refuse to overwrite it, so the remedy has to differ.
func TestInspectHooksDistinguishesAModifiedOMPHook(t *testing.T) {
	in, home := newTestInstaller(t)
	path := filepath.Join(home, ".omp", "agent", "hooks", "pre", "plan-forge.ts")
	writeFile(t, path, "// edited by the user\n")
	manifest, file, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.Rows = []manifestRow{{
		Path: path, Host: string(KindOMP), Kind: kindHookFactory, SHA256: sha256Hex([]byte("original\n")),
	}}
	if err := saveManifest(manifest, file); err != nil {
		t.Fatal(err)
	}
	got := statusFor(t, in, home, "", string(KindOMP))
	if got.State != HookModified {
		t.Fatalf("state = %q, want modified", got.State)
	}
	if !strings.Contains(got.Remedy, "--force") {
		t.Errorf("remedy %q does not name the only thing that works", got.Remedy)
	}
}

// mergeAGYHook returns early on a marker match and so never refreshes a stale
// path; the remedy must not tell the user to do something that cannot work.
func TestInspectHooksAGYRemedyDoesNotPromiseAReinstallFix(t *testing.T) {
	in, home := newTestInstaller(t)
	raw, err := json.Marshal(map[string]any{"SessionStart": []any{map[string]any{
		"type":    "command",
		"command": "/old/path/plan-forge hook check-state --format agy # " + managedMarker,
		"timeout": 2,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".gemini", "config", "hooks.json"), string(raw))
	got := statusFor(t, in, home, "", string(KindAGY))
	if got.State != HookStale {
		t.Fatalf("state = %q, want stale", got.State)
	}
	if !strings.Contains(got.Remedy, "uninstall") {
		t.Errorf("remedy %q should route through uninstall", got.Remedy)
	}
	if got.Bin != "/old/path/plan-forge" {
		t.Errorf("bin = %q", got.Bin)
	}
}

// A host whose binary is gone but whose hook is installed must still appear.
func TestHookHostsIncludesManifestOwnedHosts(t *testing.T) {
	newTestInstaller(t)
	manifest, file, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.Rows = []manifestRow{
		{Path: "/x/settings.json", Host: string(KindClaude), Kind: kindSettings},
		{Path: "/x/plan-forge.ts", Host: string(KindOMP), Kind: kindHookFactory},
		{Path: "/x/SKILL.md", Host: string(KindAGY), Kind: kindSkill},
	}
	if err := saveManifest(manifest, file); err != nil {
		t.Fatal(err)
	}
	got := HookHosts(nil)
	if len(got) != 2 || got[0] != string(KindClaude) || got[1] != string(KindOMP) {
		t.Errorf("hosts = %v, want claude and omp (a skill row is not a hook)", got)
	}
	// A detected host joins the set even with nothing in the manifest.
	if got := HookHosts([]string{string(KindAGY)}); len(got) != 3 {
		t.Errorf("hosts = %v, want the detected host too", got)
	}
}

func TestBinaryOfHandlesQuotedAndBarePaths(t *testing.T) {
	for in, want := range map[string]string{
		`"/opt/plan forge/plan-forge" hook claude`: "/opt/plan forge/plan-forge",
		`/usr/local/bin/plan-forge hook claude`:    "/usr/local/bin/plan-forge",
		`plan-forge`:                               "plan-forge",
		``:                                         "",
		`"unterminated hook claude`:                "",
	} {
		if got := binaryOf(in); got != want {
			t.Errorf("binaryOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// statusFor runs InspectHooks for one kind and returns its status.
func statusFor(t *testing.T, in *Installer, home, repo, kind string) HookStatus {
	t.Helper()
	_ = home // the installer's HOME is already redirected by newTestInstaller
	got, err := in.InspectHooks([]string{kind}, repo)
	if err != nil {
		t.Fatalf("InspectHooks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("statuses = %d, want 1", len(got))
	}
	return got[0]
}
