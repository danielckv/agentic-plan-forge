//go:build client && !server

package host

import (
	"context"
	"os/exec"
	"sort"
)

// KnownCLI is a coding-agent command line tool this build knows how to find.
// The list is a catalogue, not a capability claim: a tool is usable only when
// an adapter exists for it and that adapter's Preflight passes. Preflight stays
// the single authority on whether a call can be served (ADR 0012).
type KnownCLI struct {
	// Kind is the adapter kind when Plan Forge already supports the CLI as a
	// generation host; empty for a detected-but-unsupported agent.
	Kind Kind
	// Binaries are the command names to look for, most canonical first.
	Binaries []string
	// Name is what the window calls the tool.
	Name string
	// Purpose explains what Plan Forge does with it.
	Purpose string
	// Supports says how the CLI is integrated when Kind is empty.
	Supports string
}

// KnownCLIs lists the coding-agent CLIs the first-run scan looks for.
//
// Claude and agy are adapters a plan can be generated on; oh-my-pi is
// integration only and never generates (ADR 0027). The rest are
// agents a developer may already have installed. Finding one is reported so the
// owner knows it was seen, and it is never placed in a generation slot until an
// adapter exists for it: reporting a tool as available when Plan Forge cannot
// call it is the "signed in but cannot serve" confusion ADR 0012 exists to
// prevent.
var KnownCLIs = []KnownCLI{
	{Kind: KindClaude, Binaries: []string{"claude"}, Name: "Claude Code", Purpose: "Generate plan stages with your Claude Code sign-in"},
	{Kind: KindOMP, Binaries: []string{"omp", "oh-my-pi"}, Name: "oh-my-pi", Purpose: "Run /plan-forge; integration only, never generates"},
	{Kind: KindAGY, Binaries: []string{"agy"}, Name: "agy", Purpose: "Generate plan stages when its preflight passes"},
	{Binaries: []string{"codex"}, Name: "Codex CLI", Purpose: "A coding agent installed on this machine", Supports: "detected only; Plan Forge has no adapter for it yet"},
	{Binaries: []string{"pi"}, Name: "pi", Purpose: "A coding agent installed on this machine", Supports: "detected only; Plan Forge has no adapter for it yet"},
	{Binaries: []string{"opencode"}, Name: "OpenCode", Purpose: "A coding agent installed on this machine", Supports: "detected only; Plan Forge has no adapter for it yet"},
	{Binaries: []string{"gemini"}, Name: "Gemini CLI", Purpose: "A coding agent installed on this machine", Supports: "detected only; Plan Forge has no adapter for it yet"},
	{Binaries: []string{"cursor-agent"}, Name: "Cursor Agent", Purpose: "A coding agent installed on this machine", Supports: "detected only; no Plan Forge adapter yet"},
}

// DetectedCLI is one entry of the first-run scan.
type DetectedCLI struct {
	Name     string   `json:"name"`
	Binaries []string `json:"binaries"`
	Found    bool     `json:"found"`
	// Path is the resolved executable. It names a path on the owner's machine,
	// so it is shown only through the loopback bridge.
	Path    string `json:"path,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Purpose string `json:"purpose"`
	// Supports says how the CLI is integrated, so a found-but-unsupported tool
	// is not mistaken for a usable host.
	Supports string `json:"supports,omitempty"`
	// Usable is true only for a CLI with an adapter that passed preflight.
	Usable bool `json:"usable"`
	// Blocks says why a found CLI cannot serve a call.
	Blocks string `json:"blocks,omitempty"`
}

// ScanKnownCLIs looks for every known coding-agent CLI. It is read-only and
// runs nothing: only presence on PATH is checked, so a first run costs no model
// call and starts no process. Usability comes from the adapters, in
// Capabilities, where Preflight already decides it.
func ScanKnownCLIs(ctx context.Context) []DetectedCLI {
	out := make([]DetectedCLI, 0, len(KnownCLIs))
	for _, known := range KnownCLIs {
		entry := DetectedCLI{
			Name:     known.Name,
			Binaries: append([]string(nil), known.Binaries...),
			Purpose:  known.Purpose,
			Supports: known.Supports,
			Kind:     string(known.Kind),
		}
		for _, bin := range known.Binaries {
			if ctx.Err() != nil {
				break
			}
			if path, err := exec.LookPath(bin); err == nil {
				entry.Found, entry.Path = true, path
				break
			}
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Found != out[j].Found {
			return out[i].Found
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// CLINames is the catalogue as bare names, for the doctor report and tests.
func CLINames() []string {
	names := make([]string, 0, len(KnownCLIs))
	for _, known := range KnownCLIs {
		names = append(names, known.Name)
	}
	sort.Strings(names)
	return names
}
