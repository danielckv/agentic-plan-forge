//go:build client && !server

package app

import (
	"context"
	"net/http"
	"os/exec"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/host"
)

// What this installation can actually do, in one answer.
//
// All of it existed already, in `plan-forge doctor`, on a terminal the person
// writing the brief is not looking at. That is where "agy is signed in but
// cannot serve a call" lived while every avatar quietly went somewhere else
// (ADR 0012): true, printed, and never read. The window asks the same
// questions and shows them beside the work.

// CapabilityHost is one generation host.
type CapabilityHost struct {
	Host    string `json:"host"`
	Found   bool   `json:"found"`
	Version string `json:"version,omitempty"`
	Ready   bool   `json:"ready"`
	Method  string `json:"method,omitempty"`
	SignIn  string `json:"sign_in,omitempty"`
	// Serves is false when the host is signed in and still cannot take a
	// call. Blocks says why.
	Serves bool   `json:"serves"`
	Blocks string `json:"blocks,omitempty"`
	Models int    `json:"models"`
}

// CapabilityTool is an external program a feature needs.
type CapabilityTool struct {
	Name string `json:"name"`
	// What the tool is for, in the window's own words.
	Purpose   string `json:"purpose"`
	Found     bool   `json:"found"`
	Path      string `json:"path,omitempty"`
	Remedy    string `json:"remedy,omitempty"`
	Essential bool   `json:"essential"`
}

// CapabilitiesView is the whole answer.
type CapabilitiesView struct {
	Hosts  []CapabilityHost     `json:"hosts"`
	Tools  []CapabilityTool     `json:"tools"`
	Skills []host.SkillRootView `json:"skills"`
	// Discovered lists every skill on disk in a harness root with its
	// one-line description, so the owner can see what a harness already
	// offers. It is read-only: Plan Forge never edits a foreign skill.
	Discovered host.SkillScanReport `json:"discovered_skills"`
	Hooks      []host.HookStatus    `json:"hooks"`
	// CLIs is the first-run scan of installed coding agents. A tool that is
	// found but has no adapter says so; only a host whose preflight passes is
	// reported usable.
	CLIs []host.DetectedCLI `json:"clis"`
	// Pinned names a configured main_host or judge_host that cannot serve, so
	// the window can say the choice is being ignored rather than leaving the
	// owner to infer it from the placements.
	Pinned []string `json:"pinned"`
	// Providers is the API key state of each HTTP generation provider. It is
	// what the setup window reads to decide whether this installation has a
	// usable provider at all.
	Providers []ProviderStatus `json:"providers"`
}

// ProviderStatus is one HTTP provider's credential state.
//
// It never carries the key. Masked holds the first and last few characters so
// the owner can tell which key is installed, and Source names where it came
// from, because a stored key silently overridden by an environment variable is
// the commonest confusing state.
type ProviderStatus struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// Configured is true when a usable key is present, from either source.
	Configured bool `json:"configured"`
	// Source is the environment variable name, or "credentials.json".
	Source string `json:"source,omitempty"`
	Masked string `json:"masked,omitempty"`
	// EnvVar and ConsoleURL tell the owner how to supply a key without
	// leaving the window to look either one up.
	EnvVar     string `json:"env_var"`
	ConsoleURL string `json:"console_url"`
	// BaseURL is a configured endpoint override, empty for the public one.
	BaseURL string `json:"base_url,omitempty"`
}

// providerStatuses reports the credential state of every API provider. It
// reads the key file and nothing else: no network call and no provider probe,
// so the window can ask "is a provider configured?" cheaply and often.
func providerStatuses() []ProviderStatus {
	creds, err := config.LoadCredentials()
	out := make([]ProviderStatus, 0, len(config.ProviderNames()))
	for _, name := range config.ProviderNames() {
		status := ProviderStatus{
			Name:       name,
			Label:      config.ProviderLabel(name),
			EnvVar:     config.ProviderKeyEnv(name),
			ConsoleURL: config.ProviderConsoleURL(name),
		}
		if err == nil {
			key, source := creds.Key(name)
			status.Configured = key != ""
			status.Source = source
			status.Masked = config.MaskKey(key)
			status.BaseURL = creds.BaseURL(name)
		}
		out = append(out, status)
	}
	return out
}

// Capabilities probes the hosts, the optional tools, the installed skills and
// the managed hooks. Every probe is read-only; nothing here writes.
func Capabilities(ctx context.Context, cfg config.Config, repo, version string) CapabilitiesView {
	view := CapabilitiesView{
		Hosts:  []CapabilityHost{},
		Tools:  []CapabilityTool{},
		Skills: []host.SkillRootView{},
		Hooks:  []host.HookStatus{},
		Pinned: []string{},
	}

	var detected []string
	serves := map[string]CapabilityHost{}
	for _, adapter := range host.Registered() {
		entry := CapabilityHost{Host: string(adapter.Kind())}
		v, err := adapter.Detect(ctx)
		if err != nil {
			view.Hosts = append(view.Hosts, entry)
			serves[entry.Host] = entry
			continue
		}
		entry.Found, entry.Version = true, v
		detected = append(detected, entry.Host)
		if state, aerr := adapter.Auth(ctx); aerr == nil {
			entry.Ready, entry.Method = state.Ready, state.Method
			if !state.Ready {
				entry.SignIn = state.Detail
			}
		}
		if models, merr := adapter.Models(ctx); merr == nil {
			entry.Models = len(models)
		}
		if perr := adapter.Preflight(ctx); perr != nil {
			entry.Blocks = perr.Error()
		} else {
			entry.Serves = entry.Ready
		}
		view.Hosts = append(view.Hosts, entry)
		serves[entry.Host] = entry
	}
	sort.Slice(view.Hosts, func(i, j int) bool { return view.Hosts[i].Host < view.Hosts[j].Host })

	for _, pin := range []struct{ key, value string }{
		{"main_host", cfg.MainHost},
		{"judge_host", cfg.JudgeHost},
	} {
		if pin.value == "" {
			continue
		}
		if entry, ok := serves[pin.value]; !ok || !entry.Serves {
			view.Pinned = append(view.Pinned, pin.key+" = "+pin.value)
		}
	}

	view.Tools = probeTools()
	// Rediscovery on every call is deliberate: a person installs a CLI and
	// reloads the window, and a cached first-run result would keep saying it is
	// missing until the process restarted.
	view.Providers = providerStatuses()
	view.CLIs = host.ScanKnownCLIs(ctx)
	for i := range view.CLIs {
		entry, ok := serves[view.CLIs[i].Kind]
		switch {
		case strings.TrimSpace(view.CLIs[i].Kind) == "":
			// No adapter: presence is all this scan can honestly report.
		case !view.CLIs[i].Found:
			view.CLIs[i].Blocks = "not installed"
		case !ok || !entry.Serves:
			// A host can be signed in and still refuse a call; Blocks carries
			// that reason when the adapter gave one.
			view.CLIs[i].Blocks = blockedReason(entry)
		default:
			view.CLIs[i].Usable = true
		}
	}

	// Both inspectors return a nil slice when there is nothing to report, and
	// assigning that over the empty one puts `null` on the wire for the
	// window's `.map` to throw on. A machine with no host installed is the
	// case: CI has none, this developer machine has three, which is why it
	// only failed once it left the machine it was written on.
	if views, err := host.InspectSkillRoots(detected, repo); err == nil && views != nil {
		view.Skills = views
	}
	if report, err := host.DiscoverSkills(detected, repo); err == nil {
		view.Discovered = report
	}
	if in, err := host.NewInstaller(version); err == nil {
		if statuses, herr := in.InspectHooks(host.HookHosts(detected), repo); herr == nil && statuses != nil {
			view.Hooks = statuses
		}
	}
	return view.withoutNilLists()
}

// blockedReason explains an installed host that cannot serve a call. The
// adapter's own reason wins: it is specific ("preflight failed", "no isolated
// settings") where a generic sentence would hide the fix.
func blockedReason(entry CapabilityHost) string {
	switch {
	case entry.Blocks != "":
		return entry.Blocks
	case entry.SignIn != "":
		return entry.SignIn
	default:
		return "installed but cannot serve a call"
	}
}

// withoutNilLists is the last word on the no-null rule, so a future assignment
// cannot reintroduce the fault the guards above fix one call site at a time.
func (v CapabilitiesView) withoutNilLists() CapabilitiesView {
	if v.Hosts == nil {
		v.Hosts = []CapabilityHost{}
	}
	if v.Tools == nil {
		v.Tools = []CapabilityTool{}
	}
	if v.Skills == nil {
		v.Skills = []host.SkillRootView{}
	}
	if v.Hooks == nil {
		v.Hooks = []host.HookStatus{}
	}
	if v.CLIs == nil {
		v.CLIs = []host.DetectedCLI{}
	}
	if v.Pinned == nil {
		v.Pinned = []string{}
	}
	if v.Providers == nil {
		v.Providers = []ProviderStatus{}
	}
	return v
}

// probeTools reports the optional programs, each with what it is for. A tool
// that is missing is not an error: every one of these powers a feature the
// window works without, and saying which feature is the only useful part.
func probeTools() []CapabilityTool {
	tools := []CapabilityTool{
		{
			Name:    "docker",
			Purpose: "Build and run an approved plan as a container on the Preview tab",
			Remedy:  "install Docker Desktop or the Docker engine",
		},
		{
			Name:    "agentbox",
			Purpose: "Deploy an approved plan to an Agent Box sandbox",
			Remedy:  "npm -g i @madarco/agentbox && agentbox install",
		},
		{
			Name:    "git",
			Purpose: "Open the worktree an approved plan is written in",
			Remedy:  "install git",
			// Without git there is no worktree, so no coding run at all.
			Essential: true,
		},
		{
			Name:    "pdftoppm",
			Purpose: "Read a scanned PDF dropped into the composer",
			Remedy:  "install poppler",
		},
		{
			Name:    "tesseract",
			Purpose: "Read text out of a scanned page",
			Remedy:  "install tesseract",
		},
	}
	for i := range tools {
		if path, err := exec.LookPath(tools[i].Name); err == nil {
			tools[i].Found, tools[i].Path = true, path
		}
	}
	return tools
}

// GET /api/capabilities — the hosts, tools, skills and hooks behind this
// session. Owner-only: it names paths on the owner's machine.
func (s *appSession) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if s.exec == nil {
		writeAPIError(w, http.StatusNotFound, "this session has no local capabilities")
		return
	}
	writeJSON(w, http.StatusOK, Capabilities(r.Context(), s.cfg, s.repo, s.opts.Version))
}
