//go:build client && !server

package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/integrations"
)

const testBin = "/opt/plan forge/plan-forge"

// newTestInstaller isolates HOME and PLAN_FORGE_HOME in temp directories and
// returns an installer whose binary path contains a space, so quoting bugs in
// the rendered commands surface.
func newTestInstaller(t *testing.T) (*Installer, string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pfHome, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PLAN_FORGE_HOME", filepath.Join(pfHome, "pf"))
	return &Installer{bin: testBin, version: "test"}, home
}

func planFor(t *testing.T, in *Installer, kind, home string, opts InstallOptions) []artifact {
	t.Helper()
	var arts []artifact
	var err error
	switch kind {
	case string(KindClaude):
		arts, err = in.planClaude(home, opts)
	case string(KindOMP):
		arts, err = in.planOMP(home, opts)
	case string(KindAGY):
		arts, err = in.planAGY(home, opts)
	default:
		t.Fatalf("unknown host %q", kind)
	}
	if err != nil {
		t.Fatalf("plan %s: %v", kind, err)
	}
	return arts
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPlanModeStepSubstitutionPerHost(t *testing.T) {
	in, home := newTestInstaller(t)
	want := map[string][]string{
		string(KindClaude): {
			"`EnterPlanMode`", "`ExitPlanMode`", `"` + testBin + `" validate --repo`,
		},
		string(KindOMP): {"managed hook sees the approved", "docs/plans/.plan/synthesized_spec.md"},
		string(KindAGY): {"Immediately enter Plan Mode", "switch to Plan Mode"},
	}
	for _, kind := range []string{string(KindClaude), string(KindOMP), string(KindAGY)} {
		t.Run(kind, func(t *testing.T) {
			arts := planFor(t, in, kind, home, InstallOptions{Scope: "user"})
			sawForge := false
			for _, art := range arts {
				for label, body := range map[string]string{"content": art.content, "handoff": art.handoff} {
					if strings.Contains(body, "{{") {
						t.Errorf("%s (%s): unresolved template variable in %s", art.path, label, body)
					}
				}
				if filepath.Base(filepath.Dir(art.path)) != "plan-forge" || filepath.Base(art.path) != "SKILL.md" {
					continue
				}
				sawForge = true
				for _, frag := range want[kind] {
					if !strings.Contains(art.content, frag) {
						t.Errorf("%s plan-forge skill missing %q", kind, frag)
					}
				}
				if strings.Contains(art.content, "PLAN_MODE_STEP") {
					t.Errorf("%s plan-forge skill still carries the PLAN_MODE_STEP variable", kind)
				}
			}
			if len(want[kind]) == 0 {
				if sawForge {
					t.Fatalf("%s: planned a plan-forge SKILL.md, but this host is command-only", kind)
				}
				return
			}
			if !sawForge {
				t.Fatalf("%s: no plan-forge SKILL.md planned", kind)
			}
		})
	}
}

func TestSubstituteResolvesPlanModeStepBinary(t *testing.T) {
	in, _ := newTestInstaller(t)
	for _, kind := range []string{string(KindClaude), string(KindOMP), string(KindAGY)} {
		got := in.substitute("a {{PLAN_MODE_STEP}} b {{PLAN_MODE_HINT}} c {{HOST}} {{WAIT_TIMEOUT}} {{PLAN_FORGE_BIN}}", kind)
		if strings.Contains(got, "{{") {
			t.Errorf("%s: unresolved variable left in %q", kind, got)
		}
	}
}

// The plan-forge-board skill was retired. An install sweeps the copy an older
// version wrote, but only while its bytes match the recorded hash.
func TestClaudeInstallRemovesRetiredBoardSkill(t *testing.T) {
	for _, tc := range []struct {
		name     string
		edited   bool
		wantGone bool
	}{{"owned", false, true}, {"edited by the user", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			in, home := newTestInstaller(t)
			retired := filepath.Join(home, ".claude", "skills", "plan-forge-board", "SKILL.md")
			body := "---\nname: plan-forge-board\n---\nold board skill\n"
			writeFile(t, retired, body)
			_, manifestFile, err := loadManifest()
			if err != nil {
				t.Fatal(err)
			}
			if err := saveManifest(installManifest{Rows: []manifestRow{{
				Path: retired, Host: string(KindClaude), Kind: kindSkill, Scope: "user", SHA256: sha256Hex([]byte(body)),
			}}}, manifestFile); err != nil {
				t.Fatal(err)
			}
			if tc.edited {
				writeFile(t, retired, body+"my notes\n")
			}

			opts := InstallOptions{Scope: "user"}
			if err := in.Apply(planFor(t, in, string(KindClaude), home, opts), opts); err != nil {
				t.Fatalf("apply: %v", err)
			}
			_, statErr := os.Stat(retired)
			if gone := errors.Is(statErr, os.ErrNotExist); gone != tc.wantGone {
				t.Errorf("retired skill gone = %v, want %v", gone, tc.wantGone)
			}
			for _, art := range planFor(t, in, string(KindClaude), home, opts) {
				if strings.Contains(art.path, "plan-forge-board") {
					t.Errorf("install still plans the retired skill: %s", art.path)
				}
			}
		})
	}
}

func TestOMPWritesNoSlashCommands(t *testing.T) {
	in, home := newTestInstaller(t)
	repo := t.TempDir()
	for _, opts := range []InstallOptions{{Scope: "user"}, {Scope: "project", Repo: repo}} {
		for _, art := range planFor(t, in, string(KindOMP), home, opts) {
			if art.kind == kindCommand {
				t.Errorf("%s scope: OMP must not write the command %s; the skills are the entry point", opts.Scope, art.path)
			}
		}
	}
}

func TestOMPInstallRemovesManagedLegacyCommands(t *testing.T) {
	in, home := newTestInstaller(t)
	commands := filepath.Join(home, ".omp", "agent", "commands")
	var rows []manifestRow
	legacy := map[string]string{
		filepath.Join(commands, "plan-forge.md"):  "legacy managed command\n",
		filepath.Join(commands, "plan-config.md"): "legacy managed config command\n",
	}
	for path, body := range legacy {
		writeFile(t, path, body)
		rows = append(rows, manifestRow{
			Path: path, Host: string(KindOMP), Kind: kindCommand, Scope: "user", SHA256: sha256Hex([]byte(body)),
		})
	}
	_, manifestFile, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveManifest(installManifest{Rows: rows}, manifestFile); err != nil {
		t.Fatal(err)
	}

	opts := InstallOptions{Scope: "user"}
	if err := in.Apply(planFor(t, in, string(KindOMP), home, opts), opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for path := range legacy {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("managed legacy %s must be removed, stat err = %v", filepath.Base(path), err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".omp", "agent", "skills", "plan-forge", "SKILL.md")); err != nil {
		t.Errorf("the plan-forge skill that replaces them is missing: %v", err)
	}
	manifest, _, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range manifest.Rows {
		if _, stale := legacy[row.Path]; stale {
			t.Errorf("legacy row still in manifest: %+v", row)
		}
	}
}

func TestOMPInstallLeavesUnownedAndDivergedLegacyCommand(t *testing.T) {
	t.Run("unowned", func(t *testing.T) {
		in, home := newTestInstaller(t)
		legacy := filepath.Join(home, ".omp", "agent", "commands", "plan-forge.md")
		writeFile(t, legacy, "my own command\n")
		opts := InstallOptions{Scope: "user"}
		if err := in.Apply(planFor(t, in, string(KindOMP), home, opts), opts); err != nil {
			t.Fatalf("apply: %v", err)
		}
		raw, err := os.ReadFile(legacy)
		if err != nil || string(raw) != "my own command\n" {
			t.Errorf("unowned commands/plan-forge.md must be untouched, got %q, %v", raw, err)
		}
		manifest, _, err := loadManifest()
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range manifest.Rows {
			if row.Path == legacy {
				t.Errorf("unowned file gained a manifest row: %+v", row)
			}
		}
	})
	t.Run("diverged managed copy", func(t *testing.T) {
		in, home := newTestInstaller(t)
		legacy := filepath.Join(home, ".omp", "agent", "commands", "plan-forge.md")
		writeFile(t, legacy, "edited by the user\n")
		_, manifestFile, err := loadManifest()
		if err != nil {
			t.Fatal(err)
		}
		if err := saveManifest(installManifest{Rows: []manifestRow{{
			Path: legacy, Host: string(KindOMP), Kind: kindCommand, Scope: "user", SHA256: sha256Hex([]byte("original\n")),
		}}}, manifestFile); err != nil {
			t.Fatal(err)
		}
		opts := InstallOptions{Scope: "user"}
		if err := in.Apply(planFor(t, in, string(KindOMP), home, opts), opts); err != nil {
			t.Fatalf("apply: %v", err)
		}
		raw, err := os.ReadFile(legacy)
		if err != nil || string(raw) != "edited by the user\n" {
			t.Errorf("diverged copy must stay, got %q, %v", raw, err)
		}
		manifest, _, err := loadManifest()
		if err != nil {
			t.Fatal(err)
		}
		kept := false
		for _, row := range manifest.Rows {
			kept = kept || row.Path == legacy
		}
		if !kept {
			t.Error("diverged copy must keep its manifest row")
		}
	})
}

// foreignSettings is a Claude settings file with foreign hooks on all three
// managed events plus unrelated keys.
func foreignSettings() map[string]any {
	return map[string]any{
		"permissions": map[string]any{"allow": []any{"Bash(go test:*)"}},
		"env":         map[string]any{"FOO": "bar"},
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo start"}}}},
			"PreToolUse": []any{map[string]any{
				"matcher": "Read",
				"hooks":   []any{map[string]any{"type": "command", "command": "echo reading"}},
			}},
			"PostToolUse": []any{map[string]any{
				"matcher": "Write",
				"hooks":   []any{map[string]any{"type": "command", "command": "echo wrote"}},
			}},
			"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo prompt"}}}},
			"Stop":             []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo stop"}}}},
		},
	}
}

// roundTrip deep-copies a settings map through JSON, as the installer sees it.
func roundTrip(t *testing.T, settings map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func managedCount(t *testing.T, settings map[string]any, event string) int {
	t.Helper()
	hooks, _ := settings["hooks"].(map[string]any)
	entries, _ := hooks[event].([]any)
	n := 0
	for _, entry := range entries {
		if _, ours := stripManagedEntry(entry); ours {
			n++
		}
	}
	return n
}

const (
	startCmd   = `"/opt/plan forge/plan-forge" hook check-state --repo "$CLAUDE_PROJECT_DIR" --format claude`
	guardCmd   = `"/opt/plan forge/plan-forge" hook claude-guard`
	handoffCmd = `"/opt/plan forge/plan-forge" hook claude`
)

func TestMergeClaudeHookAddsAllThreeEvents(t *testing.T) {
	merged, changed, err := mergeClaudeHook(map[string]any{}, startCmd, guardCmd, handoffCmd)
	if err != nil || !changed {
		t.Fatalf("merge = changed %v, err %v", changed, err)
	}
	for _, event := range claudeHookEvents {
		if got := managedCount(t, merged, event); got != 1 {
			t.Errorf("%s: %d managed entries, want 1", event, got)
		}
	}
	hooks := merged["hooks"].(map[string]any)
	post := hooks["PostToolUse"].([]any)[0].(map[string]any)
	if post["matcher"] != "Bash" {
		t.Errorf("PostToolUse matcher = %v, want Bash", post["matcher"])
	}
	if _, has := hooks["UserPromptSubmit"]; has {
		t.Error("a fresh install must not add a UserPromptSubmit entry")
	}
	command := func(event string) string {
		entry := hooks[event].([]any)[0].(map[string]any)
		return entry["hooks"].([]any)[0].(map[string]any)["command"].(string)
	}
	if got, want := command("SessionStart"), startCmd+" # "+managedMarker; got != want {
		t.Errorf("SessionStart command = %q, want %q", got, want)
	}
	if got, want := command("PostToolUse"), handoffCmd+" # "+managedMarker; got != want {
		t.Errorf("PostToolUse command = %q, want %q", got, want)
	}
}

func TestMergeClaudeHookIsIdempotent(t *testing.T) {
	first, changed, err := mergeClaudeHook(roundTrip(t, foreignSettings()), startCmd, guardCmd, handoffCmd)
	if err != nil || !changed {
		t.Fatalf("first merge = changed %v, err %v", changed, err)
	}
	encoded, err := encodeSettings(first)
	if err != nil {
		t.Fatal(err)
	}
	var reparsed map[string]any
	if err := json.Unmarshal(encoded, &reparsed); err != nil {
		t.Fatal(err)
	}
	second, changed, err := mergeClaudeHook(reparsed, startCmd, guardCmd, handoffCmd)
	if err != nil || changed {
		t.Fatalf("second merge = changed %v, err %v; want unchanged", changed, err)
	}
	again, err := encodeSettings(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, again) {
		t.Errorf("second merge rewrote the settings:\n%s\n---\n%s", encoded, again)
	}
	for _, event := range claudeHookEvents {
		if got := managedCount(t, second, event); got != 1 {
			t.Errorf("%s: %d managed entries after re-merge, want 1", event, got)
		}
	}
}

func TestMergeClaudeHookUpgradesSessionStartOnlyInstall(t *testing.T) {
	old := map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": startCmd + " # " + managedMarker}},
	}}}}
	merged, changed, err := mergeClaudeHook(old, startCmd, guardCmd, handoffCmd)
	if err != nil || !changed {
		t.Fatalf("merge = changed %v, err %v", changed, err)
	}
	for _, event := range claudeHookEvents {
		if got := managedCount(t, merged, event); got != 1 {
			t.Errorf("%s: %d managed entries, want exactly 1 (no duplicate SessionStart)", event, got)
		}
	}
}

func TestMergeClaudeHookPreservesForeignEntries(t *testing.T) {
	original := foreignSettings()
	merged, _, err := mergeClaudeHook(roundTrip(t, original), startCmd, guardCmd, handoffCmd)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(merged["permissions"], original["permissions"]) || !reflect.DeepEqual(merged["env"], original["env"]) {
		t.Errorf("unrelated keys changed: %v", merged)
	}
	hooks := merged["hooks"].(map[string]any)
	originalHooks := original["hooks"].(map[string]any)
	if !reflect.DeepEqual(hooks["Stop"], originalHooks["Stop"]) {
		t.Errorf("unmanaged event rewritten: %v", hooks["Stop"])
	}
	for _, event := range claudeHookEvents {
		entries := hooks[event].([]any)
		if len(entries) != 2 {
			t.Fatalf("%s: %d entries, want foreign + managed", event, len(entries))
		}
		if !reflect.DeepEqual(entries[0], originalHooks[event].([]any)[0]) {
			t.Errorf("%s: foreign entry changed or reordered: %v", event, entries[0])
		}
	}

	stripped, ours, err := splitManagedHook(mustJSON(t, merged))
	if err != nil || !ours {
		t.Fatalf("split = ours %v, err %v", ours, err)
	}
	if !reflect.DeepEqual(stripped, roundTrip(t, original)) {
		t.Errorf("uninstall did not restore the original settings:\n got %v\nwant %v", stripped, original)
	}
}

func TestSplitManagedHookKeepsForeignHandlerInSharedEntry(t *testing.T) {
	settings := map[string]any{"hooks": map[string]any{"PostToolUse": []any{map[string]any{
		"matcher": "Bash",
		"hooks": []any{
			map[string]any{"type": "command", "command": "echo mine"},
			map[string]any{"type": "command", "command": handoffCmd + " # " + managedMarker},
		},
	}}}}
	stripped, ours, err := splitManagedHook(mustJSON(t, settings))
	if err != nil || !ours {
		t.Fatalf("split = ours %v, err %v", ours, err)
	}
	entries := stripped["hooks"].(map[string]any)["PostToolUse"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}
	handlers := entries[0].(map[string]any)["hooks"].([]any)
	if len(handlers) != 1 || handlers[0].(map[string]any)["command"] != "echo mine" {
		t.Errorf("foreign handler lost or managed handler kept: %v", handlers)
	}
	if entries[0].(map[string]any)["matcher"] != "Bash" {
		t.Errorf("matcher dropped: %v", entries[0])
	}
}

func TestSplitManagedHookIgnoresMarkerInForeignFields(t *testing.T) {
	settings := map[string]any{"hooks": map[string]any{"UserPromptSubmit": []any{map[string]any{
		"statusMessage": "plan-forge managed? no",
		"hooks":         []any{map[string]any{"type": "command", "command": "echo hi"}},
	}}}}
	_, ours, err := splitManagedHook(mustJSON(t, settings))
	if err != nil || ours {
		t.Errorf("split = ours %v, err %v; a marker outside a command is not ours", ours, err)
	}
}

func TestMergeClaudeHookRefusesForeignShapes(t *testing.T) {
	cases := map[string]map[string]any{
		"hooks not an object":  {"hooks": []any{"x"}},
		"event not an array":   {"hooks": map[string]any{"PostToolUse": "echo oops"}},
		"session start a map":  {"hooks": map[string]any{"SessionStart": map[string]any{"a": 1}}},
		"pre tool use numeric": {"hooks": map[string]any{"PreToolUse": 3}},
	}
	for name, settings := range cases {
		t.Run(name, func(t *testing.T) {
			_, changed, err := mergeClaudeHook(settings, startCmd, guardCmd, handoffCmd)
			var mergeErr *settingsMergeError
			if !errors.As(err, &mergeErr) || changed {
				t.Fatalf("merge = changed %v, err %v; want settingsMergeError", changed, err)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestClaudeApplyAndUninstallRoundTrip(t *testing.T) {
	in, home := newTestInstaller(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	original := foreignSettings()
	writeFile(t, settingsPath, string(mustJSON(t, original)))
	opts := InstallOptions{Scope: "user", Host: "claude"}

	if err := in.Apply(planFor(t, in, string(KindClaude), home, opts), opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	installed := readJSON(t, settingsPath)
	for _, event := range claudeHookEvents {
		if got := managedCount(t, installed, event); got != 1 {
			t.Errorf("%s: %d managed entries after install, want 1", event, got)
		}
	}
	hooks := installed["hooks"].(map[string]any)["PostToolUse"].([]any)
	if hooks[len(hooks)-1].(map[string]any)["matcher"] != claudePostToolUseMatcher {
		t.Errorf("managed PostToolUse entry lost its matcher: %v", hooks)
	}
	for _, name := range []string{"plan-forge", "plan-config"} {
		if _, err := os.Stat(filepath.Join(home, ".claude", "skills", name, "SKILL.md")); err != nil {
			t.Errorf("skill %s missing: %v", name, err)
		}
	}

	// A second install is byte-for-byte idempotent.
	before, _ := os.ReadFile(settingsPath)
	if err := in.Apply(planFor(t, in, string(KindClaude), home, opts), opts); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	after, _ := os.ReadFile(settingsPath)
	if !bytes.Equal(before, after) {
		t.Errorf("second install changed settings.json:\n%s\n---\n%s", before, after)
	}

	// A dry-run uninstall reports but changes nothing.
	dry, err := in.Uninstall(InstallOptions{Host: "claude", DryRun: true})
	if err != nil || len(dry) == 0 {
		t.Fatalf("dry-run uninstall = %v, %v", dry, err)
	}
	if now, _ := os.ReadFile(settingsPath); !bytes.Equal(now, after) {
		t.Error("dry-run uninstall modified settings.json")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "plan-forge", "SKILL.md")); err != nil {
		t.Errorf("dry-run uninstall removed a skill: %v", err)
	}

	// The real uninstall removes only the managed entries and files.
	removed, err := in.Uninstall(InstallOptions{Host: "claude"})
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if len(removed) == 0 {
		t.Fatal("uninstall reported nothing")
	}
	if got := readJSON(t, settingsPath); !reflect.DeepEqual(got, roundTrip(t, original)) {
		t.Errorf("settings after uninstall differ from the original:\n got %v\nwant %v", got, original)
	}
	for _, name := range []string{"plan-forge", "plan-config"} {
		if _, err := os.Stat(filepath.Join(home, ".claude", "skills", name, "SKILL.md")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("skill %s must be removed, stat err = %v", name, err)
		}
	}

	// The manifest is pruned, so a reinstall starts clean instead of failing
	// on the settings file the uninstall legitimately changed.
	if err := in.Apply(planFor(t, in, string(KindClaude), home, opts), opts); err != nil {
		t.Fatalf("reinstall after uninstall: %v", err)
	}
	if got := managedCount(t, readJSON(t, settingsPath), "PostToolUse"); got != 1 {
		t.Errorf("reinstall: %d managed PostToolUse entries, want 1", got)
	}
}

func TestClaudeForceRemergesDivergedSettings(t *testing.T) {
	in, home := newTestInstaller(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	opts := InstallOptions{Scope: "user", Host: "claude"}
	if err := in.Apply(planFor(t, in, string(KindClaude), home, opts), opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// The user edits settings.json after the install and the managed events
	// are lost, so the next install has something to merge.
	edited := foreignSettings()
	edited["model"] = "opus"
	writeFile(t, settingsPath, string(mustJSON(t, edited)))

	err := in.Apply(planFor(t, in, string(KindClaude), home, opts), opts)
	var modified *ErrInstallModified
	if !errors.As(err, &modified) {
		t.Fatalf("apply on diverged settings = %v, want ErrInstallModified", err)
	}

	opts.Force = true
	if err := in.Apply(planFor(t, in, string(KindClaude), home, opts), opts); err != nil {
		t.Fatalf("forced apply: %v", err)
	}
	got := readJSON(t, settingsPath)
	if got["model"] != "opus" || got["permissions"] == nil {
		t.Errorf("--force overwrote the user's settings: %v", got)
	}
	for _, event := range claudeHookEvents {
		if n := managedCount(t, got, event); n != 1 {
			t.Errorf("%s: %d managed entries after --force, want 1", event, n)
		}
	}
}

func TestClaudeApplyRefusesMalformedSettings(t *testing.T) {
	in, home := newTestInstaller(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	const body = `{"hooks": {"PostToolUse": "oops"}}`
	writeFile(t, settingsPath, body)
	opts := InstallOptions{Scope: "user", Host: "claude"}
	err := in.Apply(planFor(t, in, string(KindClaude), home, opts), opts)
	var mergeErr *settingsMergeError
	if !errors.As(err, &mergeErr) {
		t.Fatalf("apply = %v, want settingsMergeError", err)
	}
	if raw, _ := os.ReadFile(settingsPath); string(raw) != body {
		t.Errorf("a refused merge must leave the file untouched, got %s", raw)
	}
}

// dupFixture plans Claude and OMP user-scope skills for a fresh HOME.
func dupFixture(t *testing.T) (*Installer, string, []artifact) {
	t.Helper()
	in, home := newTestInstaller(t)
	opts := InstallOptions{Scope: "user"}
	arts := append(planFor(t, in, string(KindClaude), home, opts), planFor(t, in, string(KindOMP), home, opts)...)
	return in, home, arts
}

func assertNoDuplicateRoots(t *testing.T, label, home, repo string, kinds []string, arts []artifact) {
	t.Helper()
	planned := plannedSkillBodies(arts)
	settings := readOMPSkillSettings(home, repo)
	for _, kind := range kinds {
		for _, sf := range hostSurfaces(kind, home, repo, settings) {
			for _, dup := range inspectSurface(kind, sf, planned).Duplicates {
				t.Errorf("%s: host %s surface %q reads %s from %v", label, kind, sf.Name, dup.Name, dup.Paths)
			}
		}
	}
}

func TestInstallWritesEachSkillIntoOneRootPerHost(t *testing.T) {
	in, home := newTestInstaller(t)
	user := InstallOptions{Scope: "user"}
	var all []artifact
	for _, kind := range []string{string(KindClaude), string(KindOMP), string(KindAGY)} {
		all = append(all, planFor(t, in, kind, home, user)...)
	}
	assertNoDuplicateRoots(t, "--host all", home, "", []string{"claude", "agy"}, all)
	assertNoDuplicateRoots(t, "--host omp", home, "", []string{"omp"}, planFor(t, in, string(KindOMP), home, user))

	// Project scope: OMP reads .claude/skills and .agents/skills by default.
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proj := InstallOptions{Scope: "project", Repo: repo}
	assertNoDuplicateRoots(t, "--host omp project", home, repo, []string{"omp"}, planFor(t, in, string(KindOMP), home, proj))

	var projAll []artifact
	for _, kind := range []string{string(KindClaude), string(KindOMP), string(KindAGY)} {
		projAll = append(projAll, planFor(t, in, kind, home, proj)...)
	}
	// OMP reads .omp/skills (its own copy) and .agents/skills (AGY's) by
	// default. The bodies differ per host, so each managed name is reported
	// once.
	ompSkills := 0
	for _, art := range projAll {
		if art.host == string(KindOMP) && art.kind == kindSkill {
			ompSkills++
		}
	}
	if ompSkills != 2 {
		t.Errorf("OMP planned %d skills, want plan-forge and plan-config", ompSkills)
	}
	// plan-config renders the same bytes for every host, so OMP collapses its
	// two copies silently. plan-forge carries a per-host plan-mode step, so
	// that one differs and is reported.
	lines := ompDuplicateLines(home, repo, projAll)
	if len(lines) != 1 || !strings.Contains(lines[0], `"plan-forge"`) {
		t.Fatalf("project --host all duplicate lines = %v, want one for plan-forge", lines)
	}
}

func TestOMPDuplicateWarningRequiresOptIn(t *testing.T) {
	_, home, marked := dupFixture(t)
	arts := unmarkedClaude(marked)
	settings := filepath.Join(home, ".omp", "agent", "settings.json")

	if lines := ompDuplicateLines(home, "", arts); len(lines) != 0 {
		t.Fatalf("warned without an opt-in: %v", lines)
	}

	cases := map[string]struct {
		file, body string
		want       bool
	}{
		"enabledProviders json":   {"settings.json", `{"enabledProviders":["claude"]}`, true},
		"enableClaudeUser json":   {"settings.json", `{"skills":{"enableClaudeUser":true}}`, true},
		"enableClaudeUser yaml":   {"config.yml", "skills:\n  enableClaudeUser: true\n", true},
		"enabledProviders yaml":   {"config.yml", "enabledProviders:\n  - claude\n", true},
		"other provider only":     {"settings.json", `{"enabledProviders":["opencode"]}`, false},
		"claude toggle false":     {"config.yml", "skills:\n  enableClaudeUser: false\n", false},
		"commands section toggle": {"config.yml", "commands:\n  enableClaudeUser: true\n", false},
		"agents opt-in, not ours": {"settings.json", `{"enableAgentsUser":true}`, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_ = os.Remove(settings)
			_ = os.Remove(filepath.Join(home, ".omp", "agent", "config.yml"))
			path := filepath.Join(home, ".omp", "agent", tc.file)
			writeFile(t, path, tc.body)
			lines := ompDuplicateLines(home, "", arts)
			if tc.want {
				// plan-config renders identically for every host, so its two
				// copies collapse silently; plan-forge differs and is reported.
				if len(lines) != 1 {
					t.Fatalf("lines = %v, want one for plan-forge", lines)
				}
				forge := ""
				for _, l := range lines {
					if strings.Contains(l, `"plan-forge"`) {
						forge = l
					}
				}
				if forge == "" {
					t.Fatalf("no plan-forge duplicate line in %v", lines)
				}
				for _, part := range []string{
					filepath.Join(home, ".claude", "skills", "plan-forge", "SKILL.md"),
					"with different bodies",
					`ignoredSkills: ["plan-forge*"]`, "enabledProviders",
				} {
					if !strings.Contains(forge, part) {
						t.Errorf("warning %q lacks %q", forge, part)
					}
				}
			} else if len(lines) != 0 {
				t.Errorf("warned without opt-in: %v", lines)
			}
			after, _ := os.ReadFile(path)
			if string(after) != tc.body {
				t.Errorf("settings file was modified: %q", after)
			}
		})
	}
}

func TestDuplicateLinesAttachToReports(t *testing.T) {
	reports := []HostReport{{Host: "claude", Found: true}, {Host: "omp", Found: true, Note: "n"}}
	attachNotes(reports, []string{"a", "b"})
	if reports[0].Note != "" || reports[1].Note != "n\n    a\n    b" {
		t.Errorf("notes = %q / %q", reports[0].Note, reports[1].Note)
	}
	only := []HostReport{{Host: "agy"}, {Host: "claude", Found: true}}
	attachNotes(only, []string{"x"})
	if only[1].Note != "x" {
		t.Errorf("fallback note = %q", only[1].Note)
	}
}

// plan-config renders identically for every host apart from the Claude copy's
// enabled: false marker. OMP carries it as a command rather than a skill, so
// the lookup accepts either kind.
func TestSkillBodyIdentityAcrossHosts(t *testing.T) {
	in, home := newTestInstaller(t)
	body := func(kind, name string) string {
		for _, art := range planFor(t, in, kind, home, InstallOptions{Scope: "user"}) {
			switch art.kind {
			case kindSkill:
				if strings.HasSuffix(art.path, filepath.Join(name, "SKILL.md")) {
					return art.content
				}
			case kindCommand:
				if strings.HasSuffix(art.path, name+".md") {
					return art.content
				}
			}
		}
		t.Fatalf("%s %s not planned", kind, name)
		return ""
	}
	claudeConf := body("claude", "plan-config")
	if claudeConf == body("omp", "plan-config") {
		t.Error("Claude plan-config is not marked enabled: false")
	}
	if strings.Replace(claudeConf, "enabled: false\n", "", 1) != body("omp", "plan-config") {
		t.Error("plan-config differs between Claude and OMP beyond the enabled key")
	}
	// OMP has no plan-forge body of its own: the hook factory registers that
	// command, so nothing else under the OMP roots may carry the name.
	for _, art := range planFor(t, in, string(KindOMP), home, InstallOptions{Scope: "user"}) {
		if strings.Contains(filepath.Base(art.path), "plan-forge") && art.kind != kindHookFactory {
			t.Errorf("OMP planned %s; only the hook factory carries plan-forge", art.path)
		}
	}
}

func TestUninstallLeavesForeignHostCopies(t *testing.T) {
	for _, remove := range []string{"omp", "claude"} {
		t.Run("uninstall "+remove, func(t *testing.T) {
			in, home, arts := dupFixture(t)
			opts := InstallOptions{Scope: "user", Host: "all"}
			if err := in.Apply(arts, opts); err != nil {
				t.Fatalf("apply: %v", err)
			}
			claude := filepath.Join(home, ".claude", "skills", "plan-forge", "SKILL.md")
			omp := filepath.Join(home, ".omp", "agent", "skills", "plan-forge", "SKILL.md")
			// A same-named file nobody installed (different root) stays too.
			foreign := filepath.Join(home, ".agents", "skills", "plan-forge", "SKILL.md")
			writeFile(t, foreign, "not ours")

			if _, err := in.Uninstall(InstallOptions{Host: remove}); err != nil {
				t.Fatalf("uninstall: %v", err)
			}
			gone, kept := omp, claude
			if remove == "claude" {
				gone, kept = claude, omp
			}
			if _, err := os.Stat(gone); !os.IsNotExist(err) {
				t.Errorf("%s still present after uninstalling %s", gone, remove)
			}
			if _, err := os.Stat(kept); err != nil {
				t.Errorf("uninstalling %s removed the other host's copy: %v", remove, err)
			}
			ompBody, _ := os.ReadFile(omp)
			claudeBody, _ := os.ReadFile(claude)
			if !hiddenFromOMP(claudeBody) && remove == "omp" {
				t.Error("Claude copy lost its enabled: false marker")
			}
			if hiddenFromOMP(ompBody) && remove == "claude" {
				t.Error("OMP copy carries enabled: false")
			}
			if raw, err := os.ReadFile(foreign); err != nil || string(raw) != "not ours" {
				t.Errorf("unowned copy touched: %q %v", raw, err)
			}
		})
	}
}

// unmarkedClaude returns the artifacts with the Claude enabled: false key
// removed, as an older install or a hand-copied skill would look.
func unmarkedClaude(arts []artifact) []artifact {
	out := make([]artifact, len(arts))
	copy(out, arts)
	for i := range out {
		if out[i].host == string(KindClaude) && out[i].kind == kindSkill {
			out[i].content = strings.Replace(out[i].content, "enabled: false\n", "", 1)
		}
	}
	return out
}

func TestOnlyClaudeSkillCopiesAreHiddenFromOMP(t *testing.T) {
	in, home := newTestInstaller(t)
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, opts := range []InstallOptions{{Scope: "user"}, {Scope: "project", Repo: repo}} {
		for _, kind := range []string{string(KindClaude), string(KindOMP), string(KindAGY)} {
			skills := 0
			for _, art := range planFor(t, in, kind, home, opts) {
				if art.kind != kindSkill {
					continue
				}
				skills++
				if got := hiddenFromOMP([]byte(art.content)); got != (kind == string(KindClaude)) {
					t.Errorf("%s %s %s: enabled: false = %v", opts.Scope, kind, art.path, got)
				}
			}
			if skills == 0 {
				t.Errorf("%s %s planned no skills", opts.Scope, kind)
			}
		}
	}
}

func TestEnabledFalseLeavesFrontmatterAndBodyIntact(t *testing.T) {
	in, _ := newTestInstaller(t)
	for _, tmpl := range []string{
		integrations.SkillPlanForge(), integrations.SkillPlanConfig(),
	} {
		original := in.substitute(tmpl, string(KindClaude))
		marked := markHiddenFromOMP(original)
		if marked == original || !hiddenFromOMP([]byte(marked)) {
			t.Fatalf("not marked:\n%.200s", marked)
		}
		if strings.Replace(marked, "enabled: false\n", "", 1) != original {
			t.Error("marking changed more than the one frontmatter line")
		}
		front, body, _ := strings.Cut(marked[4:], "\n---\n")
		lines := strings.Split(front, "\n")
		if !strings.HasPrefix(lines[0], "name: ") || !strings.HasPrefix(lines[1], "description: ") || lines[len(lines)-1] != "enabled: false" {
			t.Errorf("frontmatter shape = %q", lines)
		}
		_, origBody, _ := strings.Cut(original[4:], "\n---\n")
		if body != origBody {
			t.Error("body changed")
		}
		if markHiddenFromOMP(marked) != marked {
			t.Error("marking is not idempotent")
		}
	}
}

func TestOMPDuplicateWarningSilentOnceForeignCopiesAreMarked(t *testing.T) {
	_, home, marked := dupFixture(t)
	writeFile(t, filepath.Join(home, ".omp", "agent", "settings.json"), `{"enabledProviders":["claude"],"skills":{"customDirectories":["~/.claude/skills"]}}`)
	if lines := ompDuplicateLines(home, "", marked); len(lines) != 0 {
		t.Errorf("warned although the Claude copies are enabled: false: %v", lines)
	}
	if lines := ompDuplicateLines(home, "", unmarkedClaude(marked)); len(lines) != 1 {
		t.Errorf("unmarked copies produced %v, want one for plan-forge", lines)
	}
}

// TestBackupIsIdempotentForTheSamePreImage covers the upgrade path: the backup
// path is the content hash of the pre-image, so preserving unchanged content a
// second time names the same file and must be success, not a conflict.
func TestBackupIsIdempotentForTheSamePreImage(t *testing.T) {
	newTestInstaller(t)
	raw := []byte("{\n  \"hooks\": {}\n}\n")
	if err := backup(raw); err != nil {
		t.Fatalf("first backup: %v", err)
	}
	if err := backup(raw); err != nil {
		t.Fatalf("second backup of the same pre-image: %v", err)
	}
	dir, err := backupPath(sha256Hex(raw))
	if err != nil {
		t.Fatal(err)
	}
	pre := filepath.Join(dir, "pre")
	got, err := os.ReadFile(pre)
	if err != nil {
		t.Fatalf("read preserved pre-image: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("preserved content = %q, want %q", got, raw)
	}
	if info, err := os.Stat(pre); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("backup mode = %o, want 600", perm)
	}
	// A tampered or foreign file at that path is still a conflict.
	writeFile(t, pre, "not the pre-image")
	if err := backup(raw); err == nil {
		t.Error("backup accepted a path holding different content")
	}
}

// TestUninstallTwiceSucceeds is the real scenario: uninstall leaves the
// settings file stripped, so the second uninstall preserves the same pre-image
// it already has. It used to fail with "pre: file exists".
func TestUninstallTwiceSucceeds(t *testing.T) {
	in, _, arts := dupFixture(t)
	opts := InstallOptions{Scope: "user", Host: "all"}
	if err := in.Apply(arts, opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := in.Uninstall(InstallOptions{Host: "claude"}); err != nil {
			t.Fatalf("uninstall attempt %d: %v", attempt, err)
		}
	}
}

// OMP reaches Plan Forge through the same two skills as every other host.
// The hook adds only what a skill cannot do there: the status banner and the
// plan-mode handoff. A command of its own would shadow the skill name.
func TestOMPPlansSkillsAndHook(t *testing.T) {
	in, home := newTestInstaller(t)
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, opts := range []InstallOptions{{Scope: "user"}, {Scope: "project", Repo: repo}} {
		kinds := map[manifestKind][]string{}
		for _, art := range planFor(t, in, string(KindOMP), home, opts) {
			kinds[art.kind] = append(kinds[art.kind], art.path)
		}
		var names []string
		for _, path := range kinds[kindSkill] {
			names = append(names, filepath.Base(filepath.Dir(path)))
		}
		sort.Strings(names)
		if len(names) != 2 || names[0] != "plan-config" || names[1] != "plan-forge" {
			t.Errorf("%s: OMP skills = %v, want plan-config and plan-forge", opts.Scope, names)
		}
		if got := kinds[kindCommand]; len(got) != 0 {
			t.Errorf("%s: OMP commands = %v, want none", opts.Scope, got)
		}
		if got := kinds[kindHookFactory]; len(got) != 1 || filepath.Base(got[0]) != "plan-forge.ts" {
			t.Errorf("%s: OMP hook factories = %v, want one plan-forge.ts", opts.Scope, got)
		}
	}
}

// An install from before the skills existed left slash commands under the OMP
// commands directory. Apply owns those rows, so re-running the installer
// removes them and drops the rows. A copy whose bytes diverged from the
// recorded hash is not ours to delete: TestOMPInstallLeavesUnownedAndDiverged\
// LegacyCommand covers that side.
func TestApplyRemovesCommandsFromAnEarlierOMPInstall(t *testing.T) {
	in, home := newTestInstaller(t)
	opts := InstallOptions{Scope: "user", Host: "omp"}
	stale := filepath.Join(home, ".omp", "agent", "commands", "plan-config.md")
	body := "legacy plan-config command\n"
	writeFile(t, stale, body)
	manifest, file, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.Rows = []manifestRow{
		{Path: stale, Host: string(KindOMP), Kind: kindCommand, SHA256: sha256Hex([]byte(body))},
	}
	if err := saveManifest(manifest, file); err != nil {
		t.Fatal(err)
	}

	// --dry-run removes nothing: the sweep sits after Apply's dry-run return.
	dry := opts
	dry.DryRun = true
	if err := in.Apply(planFor(t, in, string(KindOMP), home, dry), dry); err != nil {
		t.Fatalf("dry-run apply: %v", err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("--dry-run removed the stale command: %v", err)
	}

	if err := in.Apply(planFor(t, in, string(KindOMP), home, opts), opts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale OMP command still present: %v", err)
	}
	after, _, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range after.Rows {
		if row.Path == stale {
			t.Errorf("manifest still carries the removed command row %s", row.Path)
		}
	}
}
