//go:build client && !server

package app

import (
	"context"
	"net/http"
	"os/exec"
	"sort"

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
	Hooks  []host.HookStatus    `json:"hooks"`
	// Pinned names a configured main_host or judge_host that cannot serve, so
	// the window can say the choice is being ignored rather than leaving the
	// owner to infer it from the placements.
	Pinned []string `json:"pinned"`
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
	if views, err := host.InspectSkillRoots(detected, repo); err == nil {
		view.Skills = views
	}
	if in, err := host.NewInstaller(version); err == nil {
		if statuses, herr := in.InspectHooks(host.HookHosts(detected), repo); herr == nil {
			view.Hooks = statuses
		}
	}
	return view
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
