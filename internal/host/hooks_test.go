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
		"PostToolUse": []any{map[string]any{
			"matcher": claudePostToolUseMatcher,
			"hooks": []any{
				handler("echo foreign"),
				handler(handoffCmd + " # " + managedMarker),
			},
		}},
		"UserPromptSubmit": []any{map[string]any{"hooks": []any{
			handler(handoffCmd + " # " + managedMarker),
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

// t2 is a dummy *testing.T used by installedSettings so the fixture helper can
// stay a plain function; roundTrip only fails on impossible marshals.

func TestMergeClaudeHookRefreshesStaleBinaryPath(t *testing.T) {
	settings := installedSettings()
	merged, changed, err := mergeClaudeHook(settings, newStartCmd, newHandoffCmd)
	if err != nil || !changed {
		t.Fatalf("merge = changed %v, err %v; want a refresh", changed, err)
	}
	want := map[string]string{
		"SessionStart":     newStartCmd + " # " + managedMarker,
		"PostToolUse":      newHandoffCmd + " # " + managedMarker,
		"UserPromptSubmit": newHandoffCmd + " # " + managedMarker,
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
	merged, changed, err := mergeClaudeHook(settings, startCmd, handoffCmd)
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
	merged, changed, err := mergeClaudeHook(settings, startCmd, handoffCmd)
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

func TestMergeClaudeHookFreshInstallUsesEditMatcher(t *testing.T) {
	merged, changed, err := mergeClaudeHook(map[string]any{}, startCmd, handoffCmd)
	if err != nil || !changed {
		t.Fatalf("merge = changed %v, err %v", changed, err)
	}
	if got := postToolUseMatcher(t, merged); got != "Bash|Edit|Write|MultiEdit|NotebookEdit" {
		t.Errorf("matcher = %q", got)
	}
	if !strings.Contains(manualHookSnippet, `"matcher": "Bash|Edit|Write|MultiEdit|NotebookEdit"`) {
		t.Error("manual snippet lacks the new matcher")
	}
	// Idempotent.
	_, changed, err = mergeClaudeHook(merged, startCmd, handoffCmd)
	if err != nil || changed {
		t.Errorf("second merge = changed %v, err %v; want no change", changed, err)
	}
}

func TestMergeClaudeHookUpgradesOldBashMatcherInPlace(t *testing.T) {
	settings := installedSettings() // same commands as startCmd/handoffCmd
	// Managed-only entry (no foreign handler): its matcher is rewritten in place.
	pre := settings["hooks"].(map[string]any)["PostToolUse"].([]any)[0].(map[string]any)
	pre["matcher"] = "Bash"
	pre["hooks"] = pre["hooks"].([]any)[1:]
	if got := postToolUseMatcher(t, settings); got != "Bash" {
		t.Fatalf("fixture matcher = %q", got)
	}
	merged, changed, err := mergeClaudeHook(settings, startCmd, handoffCmd)
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
	if _, ours := stripManagedEntry(merged["hooks"].(map[string]any)["UserPromptSubmit"].([]any)[0]); !ours {
		t.Error("UserPromptSubmit managed entry lost")
	}
	if _, has := merged["hooks"].(map[string]any)["UserPromptSubmit"].([]any)[0].(map[string]any)["matcher"]; has {
		t.Error("UserPromptSubmit must not gain a matcher")
	}
	_, changed, err = mergeClaudeHook(merged, startCmd, handoffCmd)
	if err != nil || changed {
		t.Errorf("second merge = changed %v, err %v; want idempotent", changed, err)
	}
}

func TestMergeClaudeHookSharedEntryKeepsForeignMatcher(t *testing.T) {
	settings := installedSettings() // PostToolUse entry shares foreign + managed handlers
	settings["hooks"].(map[string]any)["PostToolUse"].([]any)[0].(map[string]any)["matcher"] = "Bash"
	original, _, err := splitManagedHook(mustJSON(t, settings))
	if err != nil {
		t.Fatal(err)
	}

	merged, changed, err := mergeClaudeHook(settings, startCmd, handoffCmd)
	if err != nil || !changed {
		t.Fatalf("merge = changed %v, err %v; want a split", changed, err)
	}
	post := merged["hooks"].(map[string]any)["PostToolUse"].([]any)
	if len(post) != 2 {
		t.Fatalf("PostToolUse entries = %v, want foreign entry + new managed entry", post)
	}
	foreign := post[0].(map[string]any)
	if foreign["matcher"] != "Bash" {
		t.Errorf("foreign handler matcher = %v, want Bash", foreign["matcher"])
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
	again, changed, err := mergeClaudeHook(merged, startCmd, handoffCmd)
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
