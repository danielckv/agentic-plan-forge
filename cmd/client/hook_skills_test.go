//go:build client && !server

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/host"
)

// fakeSkill writes one skill directory with a SKILL.md body.
func fakeSkill(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The session-start note names what exists and stops. It must not read as an
// instruction: a hook that tells the agent to do something is a task nobody
// asked for.
func TestSkillHintIsInformationalOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fakeSkill(t, filepath.Join(home, ".claude", "skills"), "graphify", "---\ndescription: Query a code graph.\n---\n")

	hint := skillHint()
	if hint == "" {
		t.Fatal("no hint for a home with one readable skill")
	}
	for _, want := range []string{"1 skill", "graphify", "Informational only"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint lacks %q: %s", want, hint)
		}
	}
	for _, banned := range []string{"you must", "you should", "run ", "please"} {
		if strings.Contains(strings.ToLower(hint), banned) {
			t.Errorf("hint reads as an instruction (%q): %s", banned, hint)
		}
	}
}

// An unreadable entry never makes the hint claim a skill that cannot be used,
// and a home with no skills produces no line at all.
func TestSkillHintSkipsBrokenEntriesAndEmptyHomes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if hint := skillHint(); hint != "" {
		t.Fatalf("hint on an empty home = %q, want empty", hint)
	}
	dir := filepath.Join(home, ".claude", "skills", "broken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if hint := skillHint(); hint != "" {
		t.Fatalf("hint with only a broken skill = %q, want empty", hint)
	}
}

// The listing prints the description, which is the whole reason for the
// feature: a name alone does not tell the owner whether a skill applies.
func TestPrintSkillsShowsDescriptionAndSource(t *testing.T) {
	report := host.SkillScanReport{Skills: []host.DiscoveredSkill{{
		Name: "pdf-tools", Description: "Extract text from PDFs.", Host: "claude",
		Root: "/home/x/.claude/skills", Via: "user", Enabled: true,
	}}}
	var out bytes.Buffer
	printSkills(&out, report)
	got := out.String()
	for _, want := range []string{"claude:", "pdf-tools", "Extract text from PDFs.", "/home/x/.claude/skills", "user"} {
		if !strings.Contains(got, want) {
			t.Errorf("listing lacks %q:\n%s", want, got)
		}
	}
}

// A skill with no SKILL.md is shown with the reason rather than dropped: an
// empty list would read as "this machine has nothing", which is a different
// fact.
func TestPrintSkillsReportsProblemEntries(t *testing.T) {
	report := host.SkillScanReport{Skills: []host.DiscoveredSkill{{
		Name: "half-installed", Host: "claude", Root: "/r", Via: "user", Problem: "no SKILL.md",
	}}}
	var out bytes.Buffer
	printSkills(&out, report)
	if !strings.Contains(out.String(), "no SKILL.md") {
		t.Fatalf("problem entry not reported:\n%s", out.String())
	}
}

// The JSON shape is the scriptable one, so it must carry the description and
// the root, not just names.
func TestConfigSkillsJSONCarriesDescriptions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fakeSkill(t, filepath.Join(home, ".claude", "skills"), "graphify", "---\ndescription: Query a code graph.\n---\n")

	report, err := host.DiscoverSkills([]string{"claude"}, "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var back host.SkillScanReport
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Skills) != 1 || back.Skills[0].Description != "Query a code graph." {
		t.Fatalf("round trip lost the description: %+v", back.Skills)
	}
}
