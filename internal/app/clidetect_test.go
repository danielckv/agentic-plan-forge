//go:build client && !server

package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
)

// The first run must name the coding agents it found, because every plan stage
// runs on one. A found-but-unsupported CLI is reported as found and not usable,
// never hidden and never promoted to a host.
func TestCapabilitiesScansInstalledCodingAgents(t *testing.T) {
	dir := t.TempDir()
	for _, bin := range []string{"codex", "claude"} {
		path := filepath.Join(dir, bin)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)

	view := Capabilities(context.Background(), config.Config{}, t.TempDir(), "test")
	if len(view.CLIs) == 0 {
		t.Fatal("the first-run scan reported no coding agents")
	}
	byName := map[string]bool{}
	for _, cli := range view.CLIs {
		byName[cli.Name] = cli.Found
		if cli.Found && cli.Purpose == "" {
			t.Errorf("%s was found and says nothing about what it is for", cli.Name)
		}
	}
	if !byName["Codex CLI"] {
		t.Error("an installed Codex CLI was not reported")
	}
	if !byName["Claude Code"] {
		t.Error("an installed Claude Code was not reported")
	}

	// Unsupported agents are found but never usable: no adapter means no call.
	for _, cli := range view.CLIs {
		if cli.Kind == "" && cli.Usable {
			t.Errorf("%s is reported usable with no adapter", cli.Name)
		}
	}

	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) == "" {
		t.Fatal("the capabilities answer did not encode")
	}
}

// An absent adapter is reported as not installed with a reason, so the window
// can say what to install instead of showing an empty list.
func TestCapabilitiesExplainsAnAbsentHost(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	view := Capabilities(context.Background(), config.Config{}, t.TempDir(), "test")
	var claude *hostCLI
	for i := range view.CLIs {
		if view.CLIs[i].Name == "Claude Code" {
			claude = &hostCLI{Blocks: view.CLIs[i].Blocks, Found: view.CLIs[i].Found, Usable: view.CLIs[i].Usable}
		}
	}
	if claude == nil {
		t.Fatal("the scan dropped the Claude Code entry")
	}
	if claude.Found || claude.Usable {
		t.Errorf("claude = %+v on an empty PATH, want absent and unusable", *claude)
	}
	if claude.Blocks == "" {
		t.Error("an absent host gives no reason")
	}
}

// hostCLI is the three fields this test reads, so it does not depend on the
// wire type's other members.
type hostCLI struct {
	Blocks string
	Found  bool
	Usable bool
}
