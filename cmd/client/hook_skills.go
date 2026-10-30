//go:build client && !server

package main

import (
	"fmt"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/host"
)

// skillHintLimit is how many names the one-line note lists before it says
// "and more". The hint is a pointer, not an inventory.
const skillHintLimit = 3

// skillHint is the one-line session-start note about skills already on this
// machine. It is informational: it names what exists and stops. It never asks
// the agent to run anything, because a session-start hook that issues
// instructions turns a notice into a task the user did not request.
//
// It returns "" when nothing is found or the scan fails: a plan banner must
// never be held up by an environment question.
func skillHint() string {
	report, err := host.DiscoverSkills(
		[]string{string(host.KindClaude), string(host.KindOMP), string(host.KindAGY)}, "",
	)
	if err != nil || len(report.Skills) == 0 {
		return ""
	}
	seen := map[string]bool{}
	names := make([]string, 0, 4)
	for _, s := range report.Skills {
		if seen[s.Name] || s.Problem != "" {
			continue
		}
		seen[s.Name] = true
		names = append(names, s.Name)
	}
	if len(names) == 0 {
		return ""
	}
	shown, suffix := names, ""
	if len(names) > skillHintLimit {
		shown = names[:skillHintLimit]
		suffix = ", and more"
	}
	noun := "skills"
	if len(seen) == 1 {
		noun = "skill"
	}
	// No call to action: the note names what exists and stops. "plan-forge
	// config skills" is the deeper listing, but pointing at it here would turn
	// a session-start notice into a task.
	return fmt.Sprintf("[Plan-Forge] %d %s already available on this machine: %s%s. Informational only.",
		len(seen), noun, strings.Join(shown, ", "), suffix)
}
