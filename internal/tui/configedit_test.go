//go:build client && !server

package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
)

// editScript runs the editor against a config file at path over the recorded
// keys and returns the frames it drew.
func editScript(t *testing.T, path string, keys ...keyMsg) []string {
	t.Helper()
	term := &scriptTerminal{keys: keys}
	if err := EditConfig(context.Background(), term, path); err != nil {
		t.Fatalf("EditConfig: %v", err)
	}
	return term.frames
}

func TestEditConfigSavesModelFromModelsTab(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	keys := []keyMsg{{Special: "tab"}, {Special: "enter"}}
	keys = append(keys, typed("kimi-k2")...)
	keys = append(keys, keyMsg{Special: "enter"}, keyMsg{Rune: 's'}, keyMsg{Rune: 'q'})
	editScript(t, path, keys...)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if cfg.DefaultMainModel != "kimi-k2" {
		t.Fatalf("default_main_model = %q, want kimi-k2", cfg.DefaultMainModel)
	}
}

func TestEditConfigEditsRoleModelKeepsHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Default()
	cfg.RoleModels = map[string]config.RoleBinding{"architect": {Host: "claude", Model: "opus"}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	// Models tab, third row (after the two defaults) is role_models.architect.
	keys := []keyMsg{{Special: "tab"}, {Special: "down"}, {Special: "down"}, {Special: "enter"}}
	keys = append(keys, typed("sonnet")...)
	keys = append(keys, keyMsg{Special: "enter"}, keyMsg{Rune: 's'}, keyMsg{Rune: 'q'})
	frames := editScript(t, path, keys...)

	saved, err := config.Load(path)
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	b := saved.RoleModels["architect"]
	if b.Host != "claude" || b.Model != "sonnet" {
		t.Fatalf("role_models.architect = %+v, want host claude model sonnet", b)
	}
	joined := strings.Join(frames, "")
	if !strings.Contains(joined, "role_models.architect") {
		t.Fatal("no frame listed the pinned role")
	}
	if !strings.Contains(joined, "General") || !strings.Contains(joined, "Models") {
		t.Fatal("no frame drew the tab bar")
	}
}

func TestEditConfigRejectsBadHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	// A rejected value keeps the edit open with the error as the notice; Esc
	// cancels the edit, q quits a session that never became dirty.
	keys := []keyMsg{{Special: "enter"}}
	keys = append(keys, typed("bogus")...)
	keys = append(keys, keyMsg{Special: "enter"}, keyMsg{Special: "esc"}, keyMsg{Rune: 'q'})
	frames := editScript(t, path, keys...)

	if !strings.Contains(strings.Join(frames, ""), "not one of") {
		t.Fatalf("no frame showed the host error:\n%s", strings.Join(frames, "\n---\n"))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a rejected edit must not create the file")
	}
}

func TestEditConfigDirtyQuitAsksTwice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	keys := []keyMsg{{Special: "enter"}}
	keys = append(keys, typed("claude")...)
	// q once warns, q again discards; nothing was saved.
	keys = append(keys, keyMsg{Special: "enter"}, keyMsg{Rune: 'q'}, keyMsg{Rune: 'q'})
	frames := editScript(t, path, keys...)

	joined := strings.Join(frames, "")
	if !strings.Contains(joined, "Unsaved changes") {
		t.Fatal("first q on a dirty session did not warn")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("discarded changes must not create the file")
	}
}

func TestEditConfigEmptyDraftKeepsValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Default()
	cfg.MainHost = "claude"
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	// Enter, straight Enter again, save, quit: main_host must survive.
	editScript(t, path,
		keyMsg{Special: "enter"}, keyMsg{Special: "enter"}, keyMsg{Rune: 's'}, keyMsg{Rune: 'q'})
	saved, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.MainHost != "claude" {
		t.Fatalf("main_host = %q, want claude", saved.MainHost)
	}
}
