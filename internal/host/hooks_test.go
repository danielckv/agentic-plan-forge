//go:build client && !server

package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// managedHandler returns the managed handler of the single managed entry for
// event, or nil.
func managedHandler(t *testing.T, settings map[string]any, event string) map[string]any {
	t.Helper()
	hooks, _ := settings["hooks"].(map[string]any)
	entries, _ := hooks[event].([]any)
	for _, entry := range entries {
		if _, ours := stripManagedEntry(entry); ours {
			m, _ := entry.(map[string]any)
			if m == nil {
				return nil
			}
			handlers, _ := m["hooks"].([]any)
			for _, h := range handlers {
				hm, _ := h.(map[string]any)
				if hm != nil && entryHasMarker(hm, claudeCommandKey) {
					return hm
				}
			}
		}
	}
	return nil
}

const (
	newStartCmd   = `"/new/path/plan-forge" hook check-state --repo "$CLAUDE_PROJECT_DIR" --format claude`
	newGuardCmd   = `"/new/path/plan-forge" hook claude-guard`
	newHandoffCmd = `"/new/path/plan-forge" hook claude`
)

// installedSettings simulates a completed install with the old binary path,
// including a foreign handler sharing the PostToolUse entry.
func installedSettings() map[string]any {
	handler := func(cmd string) map[string]any {
		return map[string]any{"type": "command", "command": cmd, "timeout": claudeHookTimeout}
	}
	raw, err := json.Marshal(map[string]any{"hooks": map[string]any{
		"SessionStart": []any{map[string]any{"hooks": []any{
			handler("echo start"),
			handler(startCmd + " # " + managedMarker),
		}}},
		"PreToolUse": []any{map[string]any{
			"matcher": claudePreToolUseMatcher,
			"hooks": []any{
				handler(guardCmd + " # " + managedMarker),
			},
		}},
		"PostToolUse": []any{map[string]any{
			"matcher": claudePostToolUseMatcher,
			"hooks": []any{
				handler("echo foreign"),
				handler(handoffCmd + " # " + managedMarker),
			},
		}},
	}})
	if err != nil {
		panic(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}

// previousInstallSettings is what 0.3.0 wrote: a second SessionStart handler
// that started the command center daemon, a PostToolUse matcher that also
// caught edit tools, and a UserPromptSubmit entry for the plan board. A
// foreign UserPromptSubmit handler shares the event.
func previousInstallSettings(t *testing.T) map[string]any {
	t.Helper()
	handler := func(cmd string) map[string]any {
		return map[string]any{"type": "command", "command": cmd, "timeout": claudeHookTimeout}
	}
	return roundTrip(t, map[string]any{"hooks": map[string]any{
		"SessionStart": []any{map[string]any{"hooks": []any{
			handler(startCmd + " # " + managedMarker),
			handler(retiredCenterCmd + " # " + managedMarker),
		}}},
		"PreToolUse": []any{map[string]any{
			"matcher": claudePreToolUseMatcher,
			"hooks":   []any{handler(guardCmd + " # " + managedMarker)},
		}},
		"PostToolUse": []any{map[string]any{
			"matcher": retiredPostToolUseMatcher,
			"hooks":   []any{handler(handoffCmd + " # " + managedMarker)},
		}},
		"UserPromptSubmit": []any{
			map[string]any{"hooks": []any{handler("echo prompt")}},
			map[string]any{"hooks": []any{handler(handoffCmd + " # " + managedMarker)}},
		},
	}})
}

const (
	retiredCenterCmd          = `"/opt/plan forge/plan-forge" center ensure --repo "$CLAUDE_PROJECT_DIR"`
	retiredPostToolUseMatcher = "Bash|Edit|Write|MultiEdit|NotebookEdit"
)

func TestMergeClaudeHookRefreshesStaleBinaryPath(t *testing.T) {
	settings := installedSettings()
	merged, changed, err := mergeClaudeHook(settings, newStartCmd, newGuardCmd, newHandoffCmd)
	if err != nil || !changed {
		t.Fatalf("merge = changed %v, err %v; want a refresh", changed, err)
	}
	want := map[string]string{
		"SessionStart": newStartCmd + " # " + managedMarker,
		"PreToolUse":   newGuardCmd + " # " + managedMarker,
		"PostToolUse":  newHandoffCmd + " # " + managedMarker,
	}
	for event, cmd := range want {
		h := managedHandler(t, merged, event)
		if h == nil {
			t.Fatalf("%s: no managed handler after refresh", event)
		}
		if got := h["command"].(string); got != cmd {
			t.Errorf("%s command = %q, want %q", event, got, cmd)
		}
		if got, ok := h["timeout"].(float64); ok && int(got) == claudeHookTimeout {
			// decoded JSON number
		} else if got, ok := h["timeout"].(int); ok && got == claudeHookTimeout {
			// freshly built handler
		} else {
			t.Errorf("%s timeout = %v (%T), want %d", event, h["timeout"], h["timeout"], claudeHookTimeout)
		}
	}

	// Foreign handlers in the same entries survive and order is kept.
	post := merged["hooks"].(map[string]any)["PostToolUse"].([]any)
	handlers := post[0].(map[string]any)["hooks"].([]any)
	if len(handlers) != 2 {
		t.Fatalf("PostToolUse handlers = %v, want foreign + managed", handlers)
	}
	if handlers[0].(map[string]any)["command"] != "echo foreign" {
		t.Errorf("foreign handler lost or reordered: %v", handlers)
	}
	if !entryHasMarker(handlers[1], claudeCommandKey) {
		t.Errorf("managed handler not in place: %v", handlers)
	}
	start := merged["hooks"].(map[string]any)["SessionStart"].([]any)
	startHandlers := start[0].(map[string]any)["hooks"].([]any)
	// The foreign handler keeps its place in front of the plan status banner.
	if len(startHandlers) != 2 || startHandlers[0].(map[string]any)["command"] != "echo start" {
		t.Errorf("SessionStart foreign handler lost: %v", startHandlers)
	}

	// No entry was appended: still exactly one managed entry per event.
	for _, event := range claudeHookEvents {
		if got := managedCount(t, merged, event); got != 1 {
			t.Errorf("%s: %d managed entries after refresh, want 1", event, got)
		}
	}

	// Uninstall still restores the pre-install content.
	stripped, ours, err := splitManagedHook(mustJSON(t, merged))
	if err != nil || !ours {
		t.Fatalf("split = ours %v, err %v", ours, err)
	}
	original, _, err := splitManagedHook(mustJSON(t, installedSettings()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stripped, original) {
		t.Errorf("uninstall did not restore foreign handlers:\n got %v\nwant %v", stripped, original)
	}
}

func TestMergeClaudeHookRefreshIsIdempotentWhenNothingDiffers(t *testing.T) {
	settings := installedSettings()
	// First pass with the same commands already present: nothing differs.
	merged, changed, err := mergeClaudeHook(settings, startCmd, guardCmd, handoffCmd)
	if err != nil || changed {
		t.Fatalf("merge with identical commands = changed %v, err %v; want unchanged", changed, err)
	}
	if !reflect.DeepEqual(merged, settings) {
		t.Errorf("idempotent merge rewrote the settings")
	}
}

func legacySettings() map[string]any {
	raw, err := json.Marshal(map[string]any{"hooks": map[string]any{
		"SessionStart": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": startCmd + " # " + managedMarker},
		}}},
	}})
	if err != nil {
		panic(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}

func TestMergeClaudeHookAddsTimeoutToEveryManagedHandler(t *testing.T) {
	// Even a legacy install without any timeout field is refreshed.
	settings := legacySettings()
	merged, changed, err := mergeClaudeHook(settings, startCmd, guardCmd, handoffCmd)
	if err != nil || !changed {
		t.Fatalf("merge = changed %v, err %v; want timeout refresh", changed, err)
	}
	for _, event := range claudeHookEvents {
		h := managedHandler(t, merged, event)
		if h == nil {
			t.Fatalf("%s: no managed handler", event)
		}
		if got, ok := h["timeout"].(float64); ok && int(got) == claudeHookTimeout {
			// decoded JSON number
		} else if got, ok := h["timeout"].(int); ok && got == claudeHookTimeout {
			// freshly built handler
		} else {
			t.Errorf("%s timeout = %v (%T), want %d", event, h["timeout"], h["timeout"], claudeHookTimeout)
		}
	}
}

func TestManualHookSnippetCarriesTimeouts(t *testing.T) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(manualHookSnippet), &parsed); err != nil {
		t.Fatalf("snippet is not valid JSON: %v", err)
	}
	hooks, _ := parsed["hooks"].(map[string]any)
	for _, event := range claudeHookEvents {
		entries, _ := hooks[event].([]any)
		if len(entries) == 0 {
			t.Fatalf("%s: snippet has no entry", event)
		}
		handlers, _ := entries[0].(map[string]any)["hooks"].([]any)
		if len(handlers) == 0 {
			t.Fatalf("%s: snippet has no handler", event)
		}
		got, ok := handlers[0].(map[string]any)["timeout"].(float64)
		if !ok || int(got) != claudeHookTimeout {
			t.Errorf("%s snippet timeout = %v, want %d", event, handlers[0].(map[string]any)["timeout"], claudeHookTimeout)
		}
	}
}

// TestReinstallWithMovedBinaryUpdatesSettingsWithoutDivergence covers the
// installer path: the manifest records the settings hash from the first
// install; when the binary moves, the refresh rewrites settings.json and the
// manifest, so the next install with the same binary must not trip
// ErrInstallModified and must keep exactly one managed entry per event.
func TestReinstallWithMovedBinaryUpdatesSettingsWithoutDivergence(t *testing.T) {
	in, home := newTestInstaller(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	original := foreignSettings()
	writeFile(t, settingsPath, string(mustJSON(t, original)))
	opts := InstallOptions{Scope: "user", Host: "claude"}

	if err := in.Apply(planFor(t, in, string(KindClaude), home, opts), opts); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	first := readJSON(t, settingsPath)

	// The binary moved: a new installer with a different bin path plans the
	// same settings artifact with the new path.
	moved := &Installer{bin: "/new/path/plan-forge", version: "test"}
	if err := moved.Apply(planFor(t, moved, string(KindClaude), home, opts), opts); err != nil {
		var mod *ErrInstallModified
		if errors.As(err, &mod) {
			t.Fatalf("reinstall after binary move wrongly refused: %v", err)
		}
		t.Fatalf("reinstall after move: %v", err)
	}
	refreshed := readJSON(t, settingsPath)
	h := managedHandler(t, refreshed, "SessionStart")
	if h == nil {
		t.Fatal("SessionStart managed handler missing after reinstall")
	}
	if got := h["command"].(string); !bytes.Contains([]byte(got), []byte("/new/path/plan-forge")) {
		t.Errorf("SessionStart command kept a stale path: %q", got)
	}
	for _, event := range claudeHookEvents {
		if got := managedCount(t, refreshed, event); got != 1 {
			t.Errorf("%s: %d managed entries after reinstall, want 1", event, got)
		}
	}

	// Third install with the same (moved) binary: no divergence, byte-stable.
	before, _ := os.ReadFile(settingsPath)
	if err := moved.Apply(planFor(t, moved, string(KindClaude), home, opts), opts); err != nil {
		var mod *ErrInstallModified
		if errors.As(err, &mod) {
			t.Fatalf("third install wrongly refused: %v", err)
		}
		t.Fatalf("third install: %v", err)
	}
	after, _ := os.ReadFile(settingsPath)
	if !bytes.Equal(before, after) {
		t.Errorf("third install changed settings.json:\n%s\n---\n%s", before, after)
	}

	// Sanity: the first install's commands really used the old path.
	if old := managedHandler(t, first, "SessionStart"); old == nil || !bytes.Contains([]byte(old["command"].(string)), []byte(testBin)) {
		t.Fatalf("first install did not use the old binary path: %v", first)
	}
}

func postToolUseMatcher(t *testing.T, settings map[string]any) string {
	t.Helper()
	for _, entry := range settings["hooks"].(map[string]any)["PostToolUse"].([]any) {
		m := entry.(map[string]any)
		for _, h := range m["hooks"].([]any) {
			if entryHasMarker(h, claudeCommandKey) {
				got, _ := m["matcher"].(string)
				return got
			}
		}
	}
	t.Fatal("no managed PostToolUse entry")
	return ""
}

func TestMergeClaudeHookFreshInstallUsesBashMatcher(t *testing.T) {
	merged, changed, err := mergeClaudeHook(map[string]any{}, startCmd, guardCmd, handoffCmd)
	if err != nil || !changed {
		t.Fatalf("merge = changed %v, err %v", changed, err)
	}
	if got := postToolUseMatcher(t, merged); got != "Bash" {
		t.Errorf("matcher = %q", got)
	}
	if !strings.Contains(manualHookSnippet, `"matcher": "Bash"`) {
		t.Error("manual snippet lacks the new matcher")
	}
	// Idempotent.
	_, changed, err = mergeClaudeHook(merged, startCmd, guardCmd, handoffCmd)
	if err != nil || changed {
		t.Errorf("second merge = changed %v, err %v; want no change", changed, err)
	}
}

func TestMergeClaudeHookNarrowsTheEditMatcherInPlace(t *testing.T) {
	settings := installedSettings() // same commands as startCmd/handoffCmd
	// Managed-only entry (no foreign handler): its matcher is rewritten in place.
	pre := settings["hooks"].(map[string]any)["PostToolUse"].([]any)[0].(map[string]any)
	pre["matcher"] = retiredPostToolUseMatcher
	pre["hooks"] = pre["hooks"].([]any)[1:]
	if got := postToolUseMatcher(t, settings); got != retiredPostToolUseMatcher {
		t.Fatalf("fixture matcher = %q", got)
	}
	merged, changed, err := mergeClaudeHook(settings, startCmd, guardCmd, handoffCmd)
	if err != nil || !changed {
		t.Fatalf("merge = changed %v, err %v; want matcher refresh", changed, err)
	}
	if got := postToolUseMatcher(t, merged); got != claudePostToolUseMatcher {
		t.Errorf("matcher = %q, want %q", got, claudePostToolUseMatcher)
	}
	post := merged["hooks"].(map[string]any)["PostToolUse"].([]any)
	if len(post) != 1 {
		t.Fatalf("PostToolUse entries = %d, want 1 (refresh in place)", len(post))
	}
	handlers := post[0].(map[string]any)["hooks"].([]any)
	if len(handlers) != 1 || !entryHasMarker(handlers[0], claudeCommandKey) {
		t.Errorf("managed handler not kept in place: %v", handlers)
	}
	_, changed, err = mergeClaudeHook(merged, startCmd, guardCmd, handoffCmd)
	if err != nil || changed {
		t.Errorf("second merge = changed %v, err %v; want idempotent", changed, err)
	}
}

func TestMergeClaudeHookSharedEntryKeepsForeignMatcher(t *testing.T) {
	settings := installedSettings() // PostToolUse entry shares foreign + managed handlers
	settings["hooks"].(map[string]any)["PostToolUse"].([]any)[0].(map[string]any)["matcher"] = "Bash|Read"
	original, _, err := splitManagedHook(mustJSON(t, settings))
	if err != nil {
		t.Fatal(err)
	}

	merged, changed, err := mergeClaudeHook(settings, startCmd, guardCmd, handoffCmd)
	if err != nil || !changed {
		t.Fatalf("merge = changed %v, err %v; want a split", changed, err)
	}
	post := merged["hooks"].(map[string]any)["PostToolUse"].([]any)
	if len(post) != 2 {
		t.Fatalf("PostToolUse entries = %v, want foreign entry + new managed entry", post)
	}
	foreign := post[0].(map[string]any)
	if foreign["matcher"] != "Bash|Read" {
		t.Errorf("foreign handler matcher = %v, want Bash|Read", foreign["matcher"])
	}
	fh := foreign["hooks"].([]any)
	if len(fh) != 1 || fh[0].(map[string]any)["command"] != "echo foreign" {
		t.Errorf("foreign entry changed: %v", fh)
	}
	managedEntry := post[1].(map[string]any)
	if managedEntry["matcher"] != claudePostToolUseMatcher {
		t.Errorf("managed entry matcher = %v, want %q", managedEntry["matcher"], claudePostToolUseMatcher)
	}
	if got := managedCount(t, merged, "PostToolUse"); got != 1 {
		t.Errorf("managed PostToolUse entries = %d, want 1", got)
	}
	count := 0
	for _, entry := range post {
		for _, h := range entry.(map[string]any)["hooks"].([]any) {
			if entryHasMarker(h, claudeCommandKey) {
				count++
			}
		}
	}
	if count != 1 {
		t.Errorf("managed PostToolUse handlers = %d, want exactly 1", count)
	}

	before := mustJSON(t, merged)
	again, changed, err := mergeClaudeHook(merged, startCmd, guardCmd, handoffCmd)
	if err != nil || changed {
		t.Fatalf("second merge = changed %v, err %v; want idempotent", changed, err)
	}
	if string(mustJSON(t, again)) != string(before) {
		t.Error("second merge rewrote the settings")
	}

	stripped, ours, err := splitManagedHook(mustJSON(t, merged))
	if err != nil || !ours {
		t.Fatalf("split = ours %v, err %v", ours, err)
	}
	if !reflect.DeepEqual(stripped, original) {
		t.Errorf("uninstall did not restore the foreign entry:\n got %v\nwant %v", stripped, original)
	}
}

// Reinstalling over 0.3.0 removes the command center handler and the plan
// board's UserPromptSubmit entry, narrows the PostToolUse matcher, and keeps
// every foreign handler. A second pass changes nothing.
func TestMergeClaudeHookUpgradesAPreviousInstall(t *testing.T) {
	merged, changed, err := mergeClaudeHook(previousInstallSettings(t), startCmd, guardCmd, handoffCmd)
	if err != nil || !changed {
		t.Fatalf("upgrade = changed %v err %v", changed, err)
	}
	hooks := merged["hooks"].(map[string]any)
	start := hooks["SessionStart"].([]any)
	if handlers := start[0].(map[string]any)["hooks"].([]any); len(handlers) != 1 {
		t.Errorf("SessionStart handlers = %v, want the status banner only", handlers)
	}
	if got := postToolUseMatcher(t, merged); got != claudePostToolUseMatcher {
		t.Errorf("PostToolUse matcher = %q, want %q", got, claudePostToolUseMatcher)
	}
	prompt, _ := hooks["UserPromptSubmit"].([]any)
	wantPrompt := roundTrip(t, map[string]any{"v": []any{map[string]any{"hooks": []any{
		map[string]any{"type": "command", "command": "echo prompt", "timeout": claudeHookTimeout},
	}}}})["v"]
	if !reflect.DeepEqual(any(prompt), wantPrompt) {
		t.Errorf("UserPromptSubmit = %v, want only the foreign handler", prompt)
	}
	if encoded := string(mustJSON(t, merged)); strings.Contains(encoded, "center ensure") {
		t.Errorf("the command center handler survived the upgrade: %s", encoded)
	}

	if _, changed, err := mergeClaudeHook(roundTrip(t, merged), startCmd, guardCmd, handoffCmd); err != nil || changed {
		t.Fatalf("second pass = changed %v err %v; want idempotent", changed, err)
	}
}

// Uninstall also strips the retired events, so a 0.3.0 install leaves no
// managed residue behind and keeps foreign handlers.
func TestSplitManagedHookRemovesRetiredHandlers(t *testing.T) {
	stripped, ours, err := splitManagedHook(mustJSON(t, previousInstallSettings(t)))
	if err != nil || !ours {
		t.Fatalf("split = ours %v err %v", ours, err)
	}
	encoded := string(mustJSON(t, stripped))
	if strings.Contains(encoded, managedMarker) {
		t.Errorf("managed residue after uninstall: %s", encoded)
	}
	if !strings.Contains(encoded, "echo prompt") {
		t.Errorf("uninstall removed a foreign handler: %s", encoded)
	}
}

// A session starting must not start a daemon or open a tab. No managed
// handler, and no snippet the user is told to paste, may do either.
func TestManagedHooksStartNothingAtSessionStart(t *testing.T) {
	bin := "/opt/plan-forge"
	sub := func(tmpl string) string { return strings.ReplaceAll(tmpl, "{{PLAN_FORGE_BIN}}", bin) }
	merged, _, err := mergeClaudeHook(map[string]any{},
		sub(claudeHookCommand), sub(claudeGuardCommand), sub(claudeHandoffCommand))
	if err != nil {
		t.Fatal(err)
	}
	for name, settings := range map[string]string{"merged settings": string(mustJSON(t, merged)), "manual snippet": manualHookSnippet} {
		for _, banned := range []string{"--open", "center", "UserPromptSubmit"} {
			if strings.Contains(settings, banned) {
				t.Errorf("%s carries %q", name, banned)
			}
		}
	}
}
