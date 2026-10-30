//go:build client && !server

package host

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSkill creates one skill directory with a SKILL.md body under root.
func writeSkill(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A skill in the user's own Claude directory is discovered with its one-line
// description, which is the whole point: the owner can see what is available
// without opening every file.
func TestDiscoverSkillsReadsFrontmatterDescription(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSkill(t, filepath.Join(home, ".claude", "skills"), "pdf-tools", `---
name: pdf-tools
description: Extract text and tables from PDF files, fill forms, merge documents.
---

# pdf-tools

Body text that is not frontmatter.
`)

	report, err := DiscoverSkills([]string{string(KindClaude)}, "")
	if err != nil {
		t.Fatalf("DiscoverSkills: %v", err)
	}
	var found *DiscoveredSkill
	for i := range report.Skills {
		if report.Skills[i].Name == "pdf-tools" {
			found = &report.Skills[i]
		}
	}
	if found == nil {
		t.Fatalf("pdf-tools was not discovered; got %+v", report.Skills)
	}
	if want := "Extract text and tables from PDF files, fill forms, merge documents."; found.Description != want {
		t.Errorf("description = %q, want %q", found.Description, want)
	}
	if !found.Enabled {
		t.Error("a skill without enabled: false must be enabled")
	}
	if found.Via != "user" {
		t.Errorf("via = %q, want the user root", found.Via)
	}
}

// A directory with no SKILL.md is reported, not silently dropped and not fatal:
// the owner sees a broken entry instead of an empty list.
func TestDiscoverSkillsReportsMissingSKILLMarkdown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills", "empty-skill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	report, err := DiscoverSkills([]string{string(KindClaude)}, "")
	if err != nil {
		t.Fatalf("DiscoverSkills: %v", err)
	}
	if len(report.Skills) != 1 || report.Skills[0].Problem == "" {
		t.Fatalf("skills = %+v, want one entry with a recorded problem", report.Skills)
	}
	if report.Skills[0].Enabled {
		t.Error("a skill with no SKILL.md must not report as enabled")
	}
}

// A harness the owner never used has no root. That is an empty result, not an
// error, and it must not fail a planning run.
func TestDiscoverSkillsMissingRootIsNotAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	report, err := DiscoverSkills([]string{string(KindClaude), string(KindAGY)}, "")
	if err != nil {
		t.Fatalf("DiscoverSkills on an empty home: %v", err)
	}
	if len(report.Skills) != 0 || len(report.Unreadable) != 0 {
		t.Fatalf("report = %+v, want empty and no problems", report)
	}
}

// enabled: false is the harness's own rule for hiding a skill, and the product
// reports the same answer rather than guessing.
func TestDiscoverSkillsHonoursEnabledFalse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSkill(t, filepath.Join(home, ".claude", "skills"), "retired", `---
description: An old skill.
enabled: false
---
`)

	report, err := DiscoverSkills([]string{string(KindClaude)}, "")
	if err != nil {
		t.Fatalf("DiscoverSkills: %v", err)
	}
	if len(report.Skills) != 1 {
		t.Fatalf("skills = %+v, want one", report.Skills)
	}
	if report.Skills[0].Enabled {
		t.Fatal("enabled: false was ignored")
	}
}

// The Antigravity roots are the Gemini ones; a skill there is discovered like
// any other, which is the case the user asked for.
func TestDiscoverSkillsReadsGeminiRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSkill(t, filepath.Join(home, ".gemini", "config", "skills"), "gemini-helper", `---
description: Help with Gemini API calls.
---
`)

	report, err := DiscoverSkills([]string{string(KindAGY)}, "")
	if err != nil {
		t.Fatalf("DiscoverSkills: %v", err)
	}
	if len(report.Skills) != 1 || report.Skills[0].Name != "gemini-helper" {
		t.Fatalf("skills = %+v, want gemini-helper", report.Skills)
	}
	if report.Skills[0].Description != "Help with Gemini API calls." {
		t.Errorf("description = %q", report.Skills[0].Description)
	}
}
