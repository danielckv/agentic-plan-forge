//go:build client && !server

package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/host"
)

// installSummary is everything the closing block of `plan-forge install`
// prints. Gathered once so the renderer stays pure and testable.
type installSummary struct {
	Version    string
	Home       string
	ConfigPath string
	Bin        string
	// Hosts are the installed host names, in the order the installer
	// reported them.
	Hosts []string
	// MainHost and MainModel are the configured generation pair, empty when
	// the owner has not pinned one.
	MainHost   string
	MainModel  string
	JudgeHost  string
	JudgeModel string
	// Ready reports that at least one generation host is signed in, which
	// decides whether the next-steps line can say /plan-forge works now.
	Ready bool
}

// gatherInstallSummary collects the summary inputs. A configuration that
// cannot be read does not fail the install: the model line simply shows the
// defaults note instead of invented values.
func gatherInstallSummary(reports []host.HostReport, bin string) installSummary {
	s := installSummary{Version: versionDisplay(), Bin: bin, Hosts: []string{}}
	if home, err := config.Home(); err == nil {
		s.Home = home
	}
	if path, err := config.DefaultPath(); err == nil {
		s.ConfigPath = path
	}
	for _, r := range reports {
		if r.Found {
			s.Hosts = append(s.Hosts, r.Host)
		}
		if r.Found && r.Auth == "ready" {
			s.Ready = true
		}
	}
	if cfg, err := config.Load(""); err == nil {
		s.MainHost = cfg.MainHost
		s.MainModel = cfg.DefaultMainModel
		s.JudgeHost = cfg.JudgeHost
		s.JudgeModel = cfg.DefaultJudgeModel
	}
	return s
}

// ANSI styles for the summary. Kept as constants so the plain rendering is
// the coloured one with the escapes stripped, never a second layout.
const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
	ansiGreen = "\x1b[32m"
	ansiCyan  = "\x1b[36m"
)

// wantColor reports whether stdout should carry ANSI colour: a terminal, and
// NO_COLOR absent or empty (the convention treats a set-but-empty NO_COLOR as
// "colour is fine", which is what the other tools on the machine do).
func wantColor(f *os.File) bool {
	if !isTTY(f) {
		return false
	}
	v, ok := os.LookupEnv("NO_COLOR")
	return !ok || v == ""
}

// renderInstallSummary writes the closing block, at most a dozen lines, so
// the rows the installer printed above it stay on screen.
func renderInstallSummary(w io.Writer, s installSummary, color bool) {
	style := func(code, text string) string {
		if !color {
			return text
		}
		return code + text + ansiReset
	}
	row := func(label, value string) {
		fmt.Fprintf(w, "  %s %s\n", style(ansiCyan, padRight(label, 8)), value)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, style(ansiBold+ansiGreen, "Plan Forge "+s.Version+" — install complete"))
	fmt.Fprintln(w)
	row("home", s.Home)
	row("config", s.ConfigPath)
	row("binary", s.Bin)
	row("hosts", strings.Join(s.Hosts, ", "))
	row("models", modelLine(s))
	fmt.Fprintln(w)
	if s.Ready {
		fmt.Fprintln(w, "Next: restart your agent, then type /plan-forge")
	} else {
		fmt.Fprintln(w, "Next: sign in a generation host (plan-forge doctor lists how), restart your agent, then type /plan-forge")
	}
}

// modelLine renders the model row: the pinned pairs, or the spread note a
// default install runs with.
func modelLine(s installSummary) string {
	pair := func(hostName, model string) string {
		switch {
		case hostName == "" && model == "":
			return ""
		case model == "":
			return hostName + " (host default)"
		case hostName == "":
			return model
		default:
			return hostName + " / " + model
		}
	}
	main, judge := pair(s.MainHost, s.MainModel), pair(s.JudgeHost, s.JudgeModel)
	if main == "" && judge == "" {
		return "spread across every signed-in host (pin with main_host, default_main_model)"
	}
	if judge == "" {
		judge = "spread"
	}
	if main == "" {
		main = "spread"
	}
	return "main: " + main + " · judge: " + judge
}
