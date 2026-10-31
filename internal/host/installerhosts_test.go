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
// (ADR 0027: oh-my-pi gets the integration and never generates).
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

// The oh-my-pi installer puts `omp` in ~/.local/bin, which a curl-piped
// install or a fresh shell often lacks on PATH. The agent directory
// ~/.omp/agent (singular, where the skills land) must still select it.
func TestAutoHostsSelectOMPByItsAgentDirectory(t *testing.T) {
	in, home := newTestInstaller(t)
	stubPath(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".omp", "agent"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := in.selectedHosts(context.Background(), "auto")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != string(KindOMP) {
		t.Fatalf("auto hosts = %v, want [omp]: ~/.omp/agent exists, omp is not on PATH", got)
	}
}

// Selecting oh-my-pi must also write to it. Plan once listed omp as found and
// printed its targets while planning no files, so the skills and hook never
// reached ~/.omp/agent.
func TestPlanWritesOMPSkillsAndHook(t *testing.T) {
	in, home := newTestInstaller(t)
	stubPath(t, t.TempDir())
	root := filepath.Join(home, ".omp", "agent")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}

	_, arts, err := in.Plan(context.Background(), InstallOptions{Host: "auto", Scope: "user"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		filepath.Join(root, "skills", "plan-forge", "SKILL.md"):  false,
		filepath.Join(root, "skills", "plan-config", "SKILL.md"): false,
		filepath.Join(root, "hooks", "pre", "plan-forge.ts"):     false,
	}
	for _, art := range arts {
		if _, ok := want[art.path]; ok && art.host == string(KindOMP) {
			want[art.path] = true
		}
	}
	for path, planned := range want {
		if !planned {
			t.Errorf("omp artifact %s not planned", path)
		}
	}
}
