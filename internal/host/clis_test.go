//go:build client && !server

package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The first run has to say which coding agents it found, because every plan
// stage runs on one of them. A missing adapter is not a crash; it is a scan
// result the window explains.
func TestScanKnownCLIsReportsPresenceWithoutRunning(t *testing.T) {
	dir := t.TempDir()
	// A script named like a coding agent: present on PATH, never executed.
	bin := filepath.Join(dir, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	got := ScanKnownCLIs(context.Background())
	var codex *DetectedCLI
	for i := range got {
		if got[i].Name == "Codex CLI" {
			codex = &got[i]
		}
	}
	if codex == nil {
		t.Fatal("the catalogue has no Codex entry")
	}
	if !codex.Found || codex.Path != bin {
		t.Errorf("codex = %+v, want it found at %s", *codex, bin)
	}
	// Detection is presence only: an unsupported CLI is never reported usable.
	if codex.Usable {
		t.Error("an unsupported CLI was reported as a usable host")
	}
	if codex.Supports == "" {
		t.Error("an unsupported CLI says nothing about how it is integrated")
	}
}

// A supported adapter that is absent must be reported as absent, not omitted:
// the window names what is missing and how to install it.
func TestScanKnownCLIsKeepsAbsentHosts(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	got := ScanKnownCLIs(context.Background())
	if len(got) != len(KnownCLIs) {
		t.Fatalf("scan returned %d entries, want the whole catalogue (%d)", len(got), len(KnownCLIs))
	}
	for _, entry := range got {
		if entry.Found {
			t.Errorf("%s was reported found on an empty PATH", entry.Name)
		}
		if entry.Usable {
			t.Errorf("%s was reported usable with nothing installed", entry.Name)
		}
	}
}

// The catalogue names every agent the first run promises to look for, once.
func TestKnownCLIsCoversTheDocumentedAgentsAndHasNoDuplicates(t *testing.T) {
	found := map[string]bool{}
	for _, name := range CLINames() {
		found[name] = true
	}
	for _, want := range []string{"Claude Code", "oh-my-pi", "agy", "Codex CLI", "pi"} {
		if !found[want] {
			t.Errorf("the catalogue does not look for %s", want)
		}
	}
	seen := map[string]bool{}
	for _, known := range KnownCLIs {
		if len(known.Binaries) == 0 {
			t.Errorf("%s has no binary name", known.Name)
		}
		for _, bin := range known.Binaries {
			if bin == "" || bin != filepath.Base(bin) {
				t.Errorf("%s names %q, which is not a bare command", known.Name, bin)
			}
		}
		key := known.Name
		if seen[key] {
			t.Errorf("%s appears twice in the catalogue", key)
		}
		seen[key] = true
	}
}
