//go:build client && !server

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/host"
)

// runConfigSkills prints the skills the harnesses on this machine already offer.
//
// It is a subcommand of `config` rather than a command of its own: the point is
// one entry point with a narrow subcommand, not another top-level verb. It reads
// the same roots the installer reads (internal/host/skills.go), so this listing
// and the installed-skills report can never disagree.
//
// args may start with the literal "skills", which runConfig strips.
func runConfigSkills(args []string) int {
	format := "text"
	only := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--json":
			format = "json"
		case args[i] == "--host":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "config skills: --host needs a host name")
				return exitUsage
			}
			i++
			only = args[i]
		case args[i] == "-h" || args[i] == "--help":
			fmt.Fprint(os.Stdout, configSkillsUsage)
			return exitOK
		default:
			fmt.Fprintf(os.Stderr, "config skills: unexpected argument %q\n\n%s", args[i], configSkillsUsage)
			return exitUsage
		}
	}
	kinds := detectedSkillHosts(only)
	report, err := host.DiscoverSkills(kinds, "")
	if err != nil {
		return fail(err)
	}
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return fail(err)
		}
		return exitOK
	}
	printSkills(os.Stdout, report)
	return exitOK
}

const configSkillsUsage = `usage: plan-forge config skills [--json] [--host NAME]

Lists the skills the coding agents on this machine already offer, with the
one-line description from each SKILL.md. Read-only; Plan Forge edits none of
them. --host limits the scan to one harness (claude, omp, agy, ...).
`

// detectedSkillHosts narrows the scan to one harness when asked, otherwise
// scans every harness this repository knows how to read. It does not probe
// PATH: a harness that is not installed simply has no skill root, which the
// scan reports as an empty list.
func detectedSkillHosts(only string) []string {
	if only != "" {
		return []string{only}
	}
	// The three kinds with a skill surface in this repository. Codex, pi and
	// OpenCode are detected on PATH but have no adapter, so reading a root for
	// them would mean inventing one.
	return []string{string(host.KindClaude), string(host.KindOMP), string(host.KindAGY)}
}

// printSkills writes the human-readable listing. A skill with no description
// says so rather than leaving a blank column, because "no description" is the
// useful fact for judging whether a skill applies.
func printSkills(w io.Writer, report host.SkillScanReport) {
	if len(report.Skills) == 0 && len(report.Unreadable) == 0 {
		fmt.Fprintln(w, "no skills found in the harness roots on this machine")
		return
	}
	byHost := map[string][]host.DiscoveredSkill{}
	order := []string{}
	for _, s := range report.Skills {
		if _, seen := byHost[s.Host]; !seen {
			order = append(order, s.Host)
		}
		byHost[s.Host] = append(byHost[s.Host], s)
	}
	for _, h := range order {
		fmt.Fprintf(w, "%s:\n", h)
		for _, s := range byHost[h] {
			state := ""
			switch {
			case s.Problem != "":
				state = "  [" + s.Problem + "]"
			case !s.Enabled:
				state = "  [disabled]"
			}
			desc := s.Description
			if strings.TrimSpace(desc) == "" {
				desc = "no description"
			}
			fmt.Fprintf(w, "  %-24s %s%s\n", s.Name, desc, state)
			fmt.Fprintf(w, "  %-24s %s (%s)\n", "", s.Root, s.Via)
		}
	}
	for _, p := range report.Unreadable {
		fmt.Fprintf(w, "unreadable root: %s (%s): %s\n", p.Path, p.Via, p.Error)
	}
}
