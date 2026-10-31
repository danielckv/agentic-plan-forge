//go:build client && !server

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/host"
	"github.com/danielckv/agentic-plan-forge/internal/window"
)

func init() {
	register("doctor", "report prerequisites, host capabilities, models, browser and OCR status", runDoctor)
}

// doctorReport is the data behind both output formats. It carries only
// redacted values: host AuthState detail strings are the sign-in commands by
// contract, never credentials, account identifiers or raw command output.
type doctorReport struct {
	Version string            `json:"version"`
	Browser doctorBrowser     `json:"browser"`
	Hosts   []doctorHost      `json:"hosts"`
	OCR     map[string]string `json:"ocr"`

	SkillRoots []host.SkillRootView `json:"skill_roots"`
	// DiscoveredSkills is every skill found on disk with its description, so
	// the terminal report says what a harness already offers, not only which
	// roots exist.
	DiscoveredSkills host.SkillScanReport `json:"discovered_skills"`
	Hooks            []host.HookStatus    `json:"hooks"`
}

type doctorBrowser struct {
	Found  string `json:"found"`
	Name   string `json:"name,omitempty"`
	Path   string `json:"path,omitempty"`
	Remedy string `json:"remedy,omitempty"`
}

type doctorHost struct {
	Host    string     `json:"host"`
	Found   bool       `json:"found"`
	Version string     `json:"version,omitempty"`
	Auth    doctorAuth `json:"auth"`
	Models  []string   `json:"models,omitempty"`
	// Usable is false when the host is signed in but cannot serve a call:
	// AGY before its isolated profile passes policy validation is the live
	// case. Reporting auth alone hid it, and a main_host pinned to such a
	// host was dropped without a word while every avatar went somewhere else.
	Usable bool   `json:"usable"`
	Blocks string `json:"blocks,omitempty"`
}

type doctorAuth struct {
	Ready  bool   `json:"ready"`
	Method string `json:"method,omitempty"`
	SignIn string `json:"sign_in,omitempty"`
}

// runDoctor reports prerequisites: host detection, auth state and models per
// host, how a browser tab will be opened (honoring browser_path), and the
// optional OCR tools. Secrets are redacted by construction: nothing
// here reads or prints credentials or raw probe output.
func runDoctor(ctx context.Context, args []string) int {
	var asJSON bool
	fs, err := parseFlags("doctor", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&asJSON, "json", false, "machine-readable output")
	})
	if err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "doctor takes no arguments")
		return exitUsage
	}

	report := doctorReport{Version: version, Hosts: []doctorHost{}, OCR: map[string]string{}}

	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	if browser, berr := window.DefaultFinder().Find(cfg.BrowserPath); berr == nil {
		report.Browser = doctorBrowser{Found: "yes", Name: browser.Name, Path: browser.Path}
	} else {
		report.Browser = doctorBrowser{
			Found:  "none",
			Remedy: "no platform URL opener was found; set browser_path to a browser executable",
		}
	}

	for _, adapter := range host.Registered() {
		entry := doctorHost{Host: string(adapter.Kind()), Auth: doctorAuth{}}
		v, verr := adapter.Detect(ctx)
		if verr != nil {
			entry.Found = false
			report.Hosts = append(report.Hosts, entry)
			continue
		}
		entry.Found = true
		entry.Version = v
		if state, aerr := adapter.Auth(ctx); aerr == nil {
			entry.Auth = doctorAuth{Ready: state.Ready, Method: state.Method}
			if !state.Ready && state.Detail != "" {
				entry.Auth.SignIn = state.Detail
			}
		}
		if models, merr := adapter.Models(ctx); merr == nil && len(models) > 0 {
			ids := make([]string, 0, len(models))
			for _, m := range models {
				ids = append(ids, m.ID)
			}
			sort.Strings(ids)
			entry.Models = ids
		}
		if perr := adapter.Preflight(ctx); perr != nil {
			entry.Blocks = perr.Error()
		} else {
			entry.Usable = entry.Auth.Ready
		}
		report.Hosts = append(report.Hosts, entry)
	}

	for _, tool := range []string{"pdftoppm", "tesseract"} {
		if path, lerr := exec.LookPath(tool); lerr == nil {
			report.OCR[tool] = "found (" + path + ")"
		} else {
			report.OCR[tool] = "not found"
		}
	}

	var found []string
	for _, entry := range report.Hosts {
		if entry.Found {
			found = append(found, entry.Host)
		}
	}
	repo, _ := os.Getwd()
	report.SkillRoots = []host.SkillRootView{}
	if views, serr := host.InspectSkillRoots(found, repo); serr == nil {
		report.SkillRoots = views
	}
	if scan, serr := host.DiscoverSkills(found, repo); serr == nil {
		report.DiscoveredSkills = scan
	}
	// Hooks are checked for every host that was detected or that the install
	// manifest knows about, so a host whose binary is absent but whose hook is
	// installed is never silently skipped.
	report.Hooks = []host.HookStatus{}
	if in, ierr := host.NewInstaller(version); ierr == nil {
		if statuses, herr := in.InspectHooks(host.HookHosts(found), repo); herr == nil {
			report.Hooks = statuses
		}
	}

	if asJSON {
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return exitRuntimeError
		}
		fmt.Println(string(raw))
		return exitOK
	}

	fmt.Printf("plan-forge %s doctor\n\n", version)
	if report.Browser.Found == "yes" {
		fmt.Printf("tab opener: %s (%s)\n", report.Browser.Name, report.Browser.Path)
	} else {
		fmt.Printf("tab opener: none — %s\n", report.Browser.Remedy)
	}
	for _, entry := range report.Hosts {
		name := padRight(entry.Host, 10)
		if !entry.Found {
			fmt.Printf("%s not found\n", name)
			continue
		}
		fmt.Printf("%s found %s\n", name, entry.Version)
		if entry.Auth.Ready {
			fmt.Printf("  auth:   ready (%s)\n", entry.Auth.Method)
		} else if entry.Auth.SignIn != "" {
			fmt.Printf("  auth:   not signed in — run: %s\n", entry.Auth.SignIn)
		} else {
			fmt.Println("  auth:   unknown")
		}
		if entry.Blocks != "" {
			fmt.Printf("  serves: NO — %s\n", entry.Blocks)
		}
		if len(entry.Models) > 0 {
			fmt.Printf("  models: %s\n", strings.Join(entry.Models, ", "))
		}
	}
	printPinnedHosts(os.Stdout, cfg, report.Hosts)
	fmt.Println()
	printHookStatuses(os.Stdout, report.Hooks)
	printSkillRoots(os.Stdout, report.SkillRoots, report.DiscoveredSkills)
	fmt.Println()
	fmt.Printf("ocr (optional, scanned PDFs only): pdftoppm %s; tesseract %s\n",
		report.OCR["pdftoppm"], report.OCR["tesseract"])
	return exitOK
}

// printPinnedHosts warns when main_host or judge_host names a host that cannot
// serve a call. The router silently walks past such a pin and spreads the roles
// over whatever is left, so without this line the owner reads "auth ready" and
// never learns their choice was dropped.
func printPinnedHosts(w io.Writer, cfg config.Config, hosts []doctorHost) {
	serves := map[string]doctorHost{}
	for _, entry := range hosts {
		serves[entry.Host] = entry
	}
	for _, pin := range []struct{ key, value string }{
		{"main_host", cfg.MainHost},
		{"judge_host", cfg.JudgeHost},
	} {
		if pin.value == "" {
			continue
		}
		entry, known := serves[pin.value]
		switch {
		case !known || !entry.Found:
			fmt.Fprintf(w, "\n%s = %s IGNORED: that host is not installed\n", pin.key, pin.value)
		case !entry.Usable:
			reason := entry.Blocks
			if reason == "" {
				reason = "that host is not signed in"
			}
			fmt.Fprintf(w, "\n%s = %s IGNORED: %s\n", pin.key, pin.value, reason)
		}
	}
}

// printHookStatuses reports whether each host's managed hooks are installed
// and point at this binary. A state that needs attention is uppercase, the
// same convention printSkillRoots uses for a duplicate, and carries its
// remedy on the next line.
func printHookStatuses(w io.Writer, statuses []host.HookStatus) {
	if len(statuses) == 0 {
		return
	}
	fmt.Fprintln(w, "managed hooks:")
	for _, st := range statuses {
		label := st.State
		if st.State != host.HookInstalled {
			label = strings.ToUpper(st.State)
		}
		detail := ""
		switch {
		case len(st.Stale) > 0 && st.Bin != "":
			detail = "  " + strings.Join(st.Stale, ", ") + " points at " + st.Bin
		case len(st.Stale) > 0:
			detail = "  " + strings.Join(st.Stale, ", ") + " is out of date"
		case len(st.Missing) > 0:
			detail = "  missing: " + strings.Join(st.Missing, ", ")
		case len(st.Events) > 0:
			detail = "  " + strings.Join(st.Events, ", ")
		}
		fmt.Fprintf(w, "  %-8s %-38s %s%s\n", st.Host, st.Path, label, detail)
		if st.Remedy != "" {
			fmt.Fprintf(w, "           %s\n", st.Remedy)
		}
	}
	fmt.Fprintln(w)
}

// printSkillRoots lists, per detected host, the skill roots it reads and
// flags every Plan Forge skill present in more than one of them. Read-only.
func printSkillRoots(w io.Writer, views []host.SkillRootView, report host.SkillScanReport) {
	home, _ := os.UserHomeDir()
	short := func(p string) string {
		if home != "" && strings.HasPrefix(p, home+string(os.PathSeparator)) {
			return "~" + strings.TrimPrefix(p, home)
		}
		return p
	}
	fmt.Fprintln(w, "skill roots (per host):")
	anyVisible := false
	for _, view := range views {
		label := view.Host
		if view.Surface != "" {
			label += " (" + view.Surface + ")"
		}
		fmt.Fprintf(w, "  %s reads:\n", label)
		for _, root := range view.Roots {
			var parts []string
			if len(root.Skills) > 0 {
				parts = append(parts, strings.Join(root.Skills, ", "))
			}
			if len(root.Hidden) > 0 {
				parts = append(parts, strings.Join(root.Hidden, ", ")+": hidden from oh-my-pi (enabled: false)")
			}
			present := "no plan-forge skills"
			if len(parts) > 0 {
				present = strings.Join(parts, "; ")
			}
			fmt.Fprintf(w, "    %s [%s] %s\n", short(root.Path), root.Via, present)
		}
		for _, dup := range view.Duplicates {
			paths := make([]string, len(dup.Paths))
			for i, p := range dup.Paths {
				paths[i] = short(p)
			}
			if dup.Identical {
				fmt.Fprintf(w, "    duplicate: %s in %s (identical bodies; OMP collapses them)\n", dup.Name, strings.Join(paths, " and "))
				continue
			}
			anyVisible = true
			fmt.Fprintf(w, "    DUPLICATE: %s in %s (bodies differ)\n", dup.Name, strings.Join(paths, " and "))
		}
	}
	if anyVisible {
		fmt.Fprintf(w, "  remedy: %s\n", host.SkillDuplicateRemedy)
	}
	if len(report.Skills) == 0 && len(report.Unreadable) == 0 {
		return
	}
	fmt.Fprintln(w, "  skills found on this machine:")
	for _, s := range report.Skills {
		state := ""
		if !s.Enabled {
			state = " (disabled)"
		}
		if s.Problem != "" {
			state += " (" + s.Problem + ")"
		}
		desc := s.Description
		if desc == "" {
			desc = "no description"
		}
		fmt.Fprintf(w, "    %-24s %-10s %s%s\n           %s\n", s.Name, s.Host, short(s.Root), state, desc)
	}
	for _, p := range report.Unreadable {
		fmt.Fprintf(w, "    unreadable: %s [%s] %s\n", short(p.Path), p.Via, p.Error)
	}
}
