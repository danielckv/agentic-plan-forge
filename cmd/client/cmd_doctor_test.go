//go:build client && !server

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/host"
)

func writeDoctorFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorListsSkillRootsAndFlagsDuplicate(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	claude := filepath.Join(home, ".claude", "skills", "plan-forge", "SKILL.md")
	omp := filepath.Join(home, ".omp", "agent", "skills", "plan-forge", "SKILL.md")
	const front = "---\nname: plan-forge\ndescription: d\n"
	writeDoctorFile(t, claude, front+"---\nclaude body")
	writeDoctorFile(t, omp, front+"---\nomp body")

	render := func() string {
		views, err := host.InspectSkillRoots([]string{"claude", "omp"}, "")
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		printSkillRoots(&out, views)
		return out.String()
	}

	quiet := render()
	for _, want := range []string{"claude reads:", "omp reads:", "~/.omp/agent/skills", "plan-forge"} {
		if !strings.Contains(quiet, want) {
			t.Errorf("roots listing lacks %q:\n%s", want, quiet)
		}
	}
	if strings.Contains(quiet, "DUPLICATE") {
		t.Errorf("duplicate flagged without an OMP opt-in:\n%s", quiet)
	}

	settings := filepath.Join(home, ".omp", "agent", "settings.json")
	writeDoctorFile(t, settings, `{"enabledProviders":["claude"]}`)

	// Marked Claude copy: OMP reads it but skips it.
	writeDoctorFile(t, claude, front+"enabled: false\n---\nclaude body")
	hidden := render()
	if !strings.Contains(hidden, "plan-forge: hidden from oh-my-pi (enabled: false)") {
		t.Errorf("marked copy not labelled hidden:\n%s", hidden)
	}
	if strings.Contains(hidden, "DUPLICATE") {
		t.Errorf("marked copy flagged as duplicate:\n%s", hidden)
	}

	// Unmarked copy stays a duplicate.
	writeDoctorFile(t, claude, front+"---\nclaude body")
	out := render()
	for _, want := range []string{
		"DUPLICATE: plan-forge",
		"~/.claude/skills/plan-forge/SKILL.md",
		"~/.omp/agent/skills/plan-forge/SKILL.md",
		`ignoredSkills: ["plan-forge*"]`,
		"enabledProviders",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
	if raw, _ := os.ReadFile(settings); string(raw) != `{"enabledProviders":["claude"]}` {
		t.Errorf("doctor modified the OMP settings: %s", raw)
	}
}
