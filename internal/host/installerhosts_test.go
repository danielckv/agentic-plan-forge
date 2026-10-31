//go:build client && !server

package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// stubBinary writes an executable that prints a version, so hostPresent's
// real Detect path can be exercised without the tool installed.
func stubBinary(t *testing.T, dir, name, out string) {
	t.Helper()
	path := filepath.Join(dir, name)
	body := "#!/bin/sh\nprintf '%s\\n' \"" + out + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// stubPath replaces PATH with the stub directory, so a tool installed on the
// machine under test cannot answer for one the test did not provide.
func stubPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
}

// Installing is not generating. Every agent found on the machine gets the
// skills and hooks; which host generates the plan is a separate decision
// (ADR 0026 keeps oh-my-pi out of generation unless it is pinned).
func TestAutoHostsSelectEveryAgentFound(t *testing.T) {
	in, _ := newTestInstaller(t)
	dir := t.TempDir()
	stubPath(t, dir)
	// omp --version and claude --version both answer; agy has no binary, so it
	// is detected by nothing here.
	stubBinary(t, dir, "omp", "0.5.0")
	stubBinary(t, dir, "claude", "2.1.0")

	got, err := in.selectedHosts(context.Background(), "auto")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{string(KindClaude), string(KindOMP)}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("auto hosts = %v, want %v: both agents are installed", got, want)
	}
}

// An oh-my-pi-only machine still gets the integration: the owner types
// /plan-forge there, whatever host ends up generating the plan.
func TestAutoHostsSelectOMPAlone(t *testing.T) {
	in, _ := newTestInstaller(t)
	dir := t.TempDir()
	stubPath(t, dir)
	stubBinary(t, dir, "omp", "0.5.0")

	got, err := in.selectedHosts(context.Background(), "auto")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != string(KindOMP) {
		t.Fatalf("auto hosts = %v, want [omp]: the only agent installed still gets the skills", got)
	}
}
