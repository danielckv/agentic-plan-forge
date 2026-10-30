//go:build client && !server

package host

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DiscoveredSkill is one skill found on disk in a harness root. It is read-only
// product information: Plan Forge shows it so the owner can see what a harness
// already offers, and never edits a foreign skill.
//
// The scan is deliberately shallow. It reads the named roots only, one level
// below each, and never walks the home directory: a planning run must not pay
// for a filesystem crawl, and an unreadable directory is a fact to report, not
// a reason to fail.
type DiscoveredSkill struct {
	// Name is the directory name, which is the skill id a harness resolves.
	Name string `json:"name"`
	// Title is the `name:` of the SKILL.md frontmatter when it differs.
	Title string `json:"title,omitempty"`
	// Description is the one-line `description:` from the frontmatter: what
	// the skill does and when to use it. Empty when the file states none.
	Description string `json:"description,omitempty"`
	// Host and Surface name the harness and the surface (cli, app) that reads
	// the root, so the UI can group by harness.
	Host    string `json:"host"`
	Surface string `json:"surface,omitempty"`
	// Root is the directory the skill was found in; Via says why that root is
	// read (user, project, opted in, a custom directory).
	Root string `json:"root"`
	Via  string `json:"via"`
	// Managed reports whether this is a skill Plan Forge installs.
	Managed bool `json:"managed"`
	// Enabled is false when the SKILL.md sets `enabled: false`, the same rule
	// the harness applies.
	Enabled bool `json:"enabled"`
	// Problem records why this entry could not be read as a skill: a missing
	// or malformed SKILL.md. The entry is still listed so the UI can say so.
	Problem string `json:"problem,omitempty"`
}

// SkillScanReport is the whole discovery result: every skill found, in a stable
// order, plus the roots that could not be read at all.
type SkillScanReport struct {
	Skills []DiscoveredSkill `json:"skills"`
	// Unreadable names a root and why it could not be listed. A root that does
	// not exist is not an error: a harness the owner never used has none.
	Unreadable []SkillRootProblem `json:"unreadable,omitempty"`
}

// SkillRootProblem is one root that could not be scanned.
type SkillRootProblem struct {
	Path  string `json:"path"`
	Via   string `json:"via"`
	Error string `json:"error"`
}

// DiscoverSkills scans the skill roots of the named harness kinds and returns
// every skill found, with its frontmatter description. It reuses the root list
// the installer already reads (`hostSurfaces`), so the product and the
// installer cannot disagree about where a harness keeps its skills.
func DiscoverSkills(kinds []string, repo string) (SkillScanReport, error) {
	home, err := homeDir()
	if err != nil {
		return SkillScanReport{}, err
	}
	settings := readOMPSkillSettings(home, repo)
	report := SkillScanReport{Skills: []DiscoveredSkill{}}
	seen := map[string]bool{}
	for _, kind := range kinds {
		for _, sf := range hostSurfaces(kind, home, repo, settings) {
			for _, root := range sf.Roots {
				// EvalSymlinks collapses a root that is a link to another root,
				// so the same skill is not reported twice under two paths.
				key := root.Path
				if resolved, err := filepath.EvalSymlinks(root.Path); err == nil {
					key = resolved
				}
				if seen[key] {
					continue
				}
				seen[key] = true
				skills, problem := scanSkillRoot(kind, sf.Name, root)
				if problem != nil {
					report.Unreadable = append(report.Unreadable, *problem)
				}
				report.Skills = append(report.Skills, skills...)
			}
		}
	}
	sort.SliceStable(report.Skills, func(i, j int) bool {
		a, b := report.Skills[i], report.Skills[j]
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.Root != b.Root {
			return a.Root < b.Root
		}
		return a.Name < b.Name
	})
	return report, nil
}

// scanSkillRoot reads one root, one directory level deep. A missing root is not
// a problem: the harness simply has no skills there. Any other read error is
// reported and the scan carries on, so one bad directory cannot blank the view.
func scanSkillRoot(kind, surface string, root readRoot) ([]DiscoveredSkill, *SkillRootProblem) {
	entries, err := os.ReadDir(root.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, &SkillRootProblem{Path: root.Path, Via: root.Via, Error: err.Error()}
	}
	out := make([]DiscoveredSkill, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		skill := DiscoveredSkill{
			Name: name, Host: kind, Surface: surface, Root: root.Path, Via: root.Via,
			Managed: isManagedSkillName(name), Enabled: true,
		}
		body, err := os.ReadFile(filepath.Join(root.Path, name, "SKILL.md"))
		switch {
		case os.IsNotExist(err):
			skill.Problem = "no SKILL.md"
			skill.Enabled = false
		case err != nil:
			skill.Problem = err.Error()
			skill.Enabled = false
		default:
			applySkillFrontmatter(&skill, body)
		}
		out = append(out, skill)
	}
	return out, nil
}

// isManagedSkillName reports whether the directory is a skill Plan Forge writes.
func isManagedSkillName(name string) bool {
	for _, managed := range managedSkillNames {
		if managed == name {
			return true
		}
	}
	return false
}

// applySkillFrontmatter fills the title, description and enabled flag from a
// SKILL.md body. It reads only the leading `---` block, one `key: value` per
// line, which is the frontmatter every harness in this repository uses. A file
// without frontmatter keeps its directory name and no description: that is a
// fact about the file, not a parse error.
func applySkillFrontmatter(skill *DiscoveredSkill, body []byte) {
	text := string(body)
	if !strings.HasPrefix(text, "---") {
		return
	}
	rest := text[3:]
	rest = strings.TrimPrefix(rest, "\r")
	rest = strings.TrimPrefix(rest, "\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		// An unterminated block still carries readable keys up to the end.
		end = len(rest)
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		switch key {
		case "name":
			skill.Title = val
		case "description":
			skill.Description = val
		case "enabled":
			skill.Enabled = !strings.EqualFold(val, "false")
		}
	}
}
