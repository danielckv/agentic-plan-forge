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

// The default install is Claude Code and Antigravity. Oh-my-pi is never
// selected automatically (ADR 0026): it serves an explicit pin only, so the
// installer's `--host omp` is the way to ask for it.
func TestAutoHostsPreferClaudeAndAntigravityOverOMP(t *testing.T) {
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
	want := []string{string(KindClaude)}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("auto hosts = %v, want %v: OMP must not be chosen beside a ready Claude", got, want)
	}
}

// With only oh-my-pi installed, auto selects nothing: the owner must name it.
func TestAutoHostsNeverSelectsOMPAlone(t *testing.T) {
	in, _ := newTestInstaller(t)
	dir := t.TempDir()
	stubPath(t, dir)
	stubBinary(t, dir, "omp", "0.5.0")

	got, err := in.selectedHosts(context.Background(), "auto")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("auto hosts = %v, want none: OMP serves an explicit pin only", got)
	}
}
