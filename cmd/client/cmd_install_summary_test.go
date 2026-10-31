//go:build client && !server

package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/host"
)

var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestRenderInstallSummaryPlain(t *testing.T) {
	s := installSummary{
		Version:    "v1.2.3",
		Home:       "/home/u/.plan-forge",
		ConfigPath: "/home/u/.plan-forge/config.json",
		Bin:        "/home/u/.local/bin/plan-forge",
		Hosts:      []string{"claude", "agy"},
		MainHost:   "claude",
		MainModel:  "opus",
		Ready:      true,
	}
	var b strings.Builder
	renderInstallSummary(&b, s, false)
	out := b.String()
	for _, want := range []string{
		"install complete", "/home/u/.plan-forge", "/home/u/.plan-forge/config.json",
		"/home/u/.local/bin/plan-forge", "claude, agy", "main: claude / opus",
		"/plan-forge",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("plain rendering carries ANSI:\n%q", out)
	}
	if lines := strings.Count(out, "\n"); lines > 20 {
		t.Fatalf("summary is %d lines, cap is 20", lines)
	}
}

func TestRenderInstallSummaryColorStripsToPlain(t *testing.T) {
	s := installSummary{Version: "dev", Home: "/h", ConfigPath: "/h/config.json", Bin: "/b", Hosts: []string{"agy"}}
	var colored, plain strings.Builder
	renderInstallSummary(&colored, s, true)
	renderInstallSummary(&plain, s, false)
	if !strings.Contains(colored.String(), "\x1b[") {
		t.Fatal("coloured rendering has no ANSI")
	}
	if ansiEscape.ReplaceAllString(colored.String(), "") != plain.String() {
		t.Fatalf("stripping colour must reproduce the plain rendering\ncolored: %q\nplain:    %q", colored.String(), plain.String())
	}
}

func TestGatherInstallSummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PLAN_FORGE_HOME", home)
	cfg := config.Default()
	cfg.MainHost = "claude"
	cfg.DefaultMainModel = "opus"
	if err := config.Save("", cfg); err != nil {
		t.Fatal(err)
	}
	reports := []host.HostReport{
		{Host: "claude", Found: true, Auth: "ready"},
		{Host: "omp", Found: false},
	}
	s := gatherInstallSummary(reports, "/bin/plan-forge")
	if s.Home != home || s.ConfigPath != filepath.Join(home, "config.json") {
		t.Errorf("paths = %q, %q under home %q", s.Home, s.ConfigPath, home)
	}
	if len(s.Hosts) != 1 || s.Hosts[0] != "claude" {
		t.Errorf("hosts = %v, want [claude]", s.Hosts)
	}
	if s.MainHost != "claude" || s.MainModel != "opus" {
		t.Errorf("models = %q/%q", s.MainHost, s.MainModel)
	}
	if !s.Ready {
		t.Error("a ready host must mark the summary ready")
	}
}

func TestRunInstallPrintsSummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PLAN_FORGE_HOME", filepath.Join(home, ".plan-forge"))
	// The agy host is present when its config directory exists; no binary or
	// credential is needed for the skills install to run.
	if err := os.MkdirAll(filepath.Join(home, ".gemini", "config"), 0o755); err != nil {
		t.Fatal(err)
	}

	var code int
	out := captureStdout(t, func() {
		code = runInstall(context.Background(), []string{"--host", "agy", "--yes"})
	})
	if code != exitOK {
		t.Fatalf("exit = %d, want 0\n%s", code, out)
	}
	for _, want := range []string{"install complete", ".plan-forge", "config.json", "binary", "agy"} {
		if !strings.Contains(out, want) {
			t.Errorf("install output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatal("piped stdout must not carry ANSI colour")
	}
}
