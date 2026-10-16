//go:build client && !server

package window

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func fakeBrowsers(t *testing.T, names ...string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	out := map[string]string{}
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		out[n] = p
	}
	return out
}

func linuxFinder(found map[string]string) Finder {
	return Finder{
		GOOS: "linux",
		Stat: os.Stat,
		LookPath: func(name string) (string, error) {
			if p, ok := found[name]; ok {
				return p, nil
			}
			return "", exec.ErrNotFound
		},
	}
}

func TestArgsExactAndNeverWeakenSandbox(t *testing.T) {
	args := Args("http://127.0.0.1:4000/#n=abc", "/tmp/profile")
	want := []string{
		"--app=http://127.0.0.1:4000/#n=abc", "--new-window", "--user-data-dir=/tmp/profile",
		"--no-first-run", "--no-default-browser-check",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("args = %q, want %q", args, want)
	}
	for _, a := range args {
		for _, bad := range forbiddenArgs {
			if strings.HasPrefix(a, bad) {
				t.Fatalf("forbidden argument %s present", bad)
			}
		}
	}
}

func TestFinderOrderChromeEdgeBraveChromium(t *testing.T) {
	found := fakeBrowsers(t, "chromium", "brave-browser", "microsoft-edge")
	got, err := linuxFinder(found).Find("")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Microsoft Edge" || got.Path != found["microsoft-edge"] {
		t.Fatalf("got %+v, want Microsoft Edge (Edge outranks Brave and Chromium)", got)
	}
	found = fakeBrowsers(t, "chromium", "google-chrome-stable")
	got, err = linuxFinder(found).Find("")
	if err != nil || got.Name != "Google Chrome" {
		t.Fatalf("got %+v, %v; want Google Chrome first", got, err)
	}
}

func TestFinderExplicitBrowserPathWinsAndFailsLoudly(t *testing.T) {
	found := fakeBrowsers(t, "google-chrome", "mybrowser")
	got, err := linuxFinder(found).Find(found["mybrowser"])
	if err != nil || got.Path != found["mybrowser"] {
		t.Fatalf("explicit path: got %+v, %v", got, err)
	}
	_, err = linuxFinder(found).Find(filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, ErrNoBrowser) || ExitCode(err) != ExitNoBrowser {
		t.Fatalf("a bad browser_path must be exit 3 and must not fall back to Chrome, got %v", err)
	}
	if !strings.Contains(err.Error(), ConfigKey) {
		t.Fatalf("guidance must name %s: %v", ConfigKey, err)
	}
}

func TestFinderNoBrowserIsExit3(t *testing.T) {
	_, err := linuxFinder(nil).Find("")
	if !errors.Is(err, ErrNoBrowser) || ExitCode(err) != 3 {
		t.Fatalf("got %v, code %d", err, ExitCode(err))
	}
}

func TestLaunchRejectsNonLoopbackURL(t *testing.T) {
	for _, u := range []string{
		"https://127.0.0.1:1/", "http://example.com:1/", "http://user@127.0.0.1:1/", "http://127.0.0.1/", "javascript:alert(1)",
	} {
		if _, err := Launch(Options{URL: u}); !errors.Is(err, ErrBadURL) {
			t.Fatalf("Launch(%q) = %v, want ErrBadURL", u, err)
		}
	}
}

func TestLaunchWithoutBrowserOpensNothing(t *testing.T) {
	f := linuxFinder(nil)
	_, err := Launch(Options{URL: "http://127.0.0.1:4000/", Finder: &f, ProfileRoot: t.TempDir()})
	if ExitCode(err) != ExitNoBrowser {
		t.Fatalf("got %v, want exit code 3", err)
	}
}

func TestBrowserEnvDropsPlanForgeVariables(t *testing.T) {
	got := browserEnv([]string{"PATH=/bin", "PLAN_FORGE_RELAY_TOKEN=secret", "plan_forge_x=1", "HOME=/h"})
	if !slices.Equal(got, []string{"PATH=/bin", "HOME=/h"}) {
		t.Fatalf("env = %q", got)
	}
}

func TestExitCodeMapping(t *testing.T) {
	cases := map[error]int{
		nil: 0, ErrWindowClosed: 130, &NoBrowserError{}: 3, ErrNeverConnected: 1, errors.New("x"): 1,
	}
	for err, want := range cases {
		if got := ExitCode(err); got != want {
			t.Errorf("ExitCode(%v) = %d, want %d", err, got, want)
		}
	}
}

func superviseAsync(t *testing.T, sig Signals, grace, startup time.Duration) (<-chan Outcome, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	res := make(chan Outcome, 1)
	go func() {
		o, _ := Supervise(ctx, sig, grace, startup)
		res <- o
	}()
	return res, cancel
}

func TestSuperviseDisconnectPastGraceIsWindowGone(t *testing.T) {
	ui := make(chan bool, 4)
	res, cancel := superviseAsync(t, Signals{UI: ui}, 60*time.Millisecond, time.Second)
	defer cancel()
	ui <- true
	ui <- false
	select {
	case o := <-res:
		if o != OutcomeWindowGone {
			t.Fatalf("outcome %v", o)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no outcome after the grace period")
	}
}

func TestSuperviseReconnectWithinGraceKeepsWindow(t *testing.T) {
	ui := make(chan bool, 4)
	res, cancel := superviseAsync(t, Signals{UI: ui}, 300*time.Millisecond, time.Second)
	ui <- true
	ui <- false
	time.Sleep(50 * time.Millisecond)
	ui <- true // a refresh: back inside the grace period
	select {
	case o := <-res:
		t.Fatalf("refresh within grace must not end the window, got %v", o)
	case <-time.After(500 * time.Millisecond):
	}
	cancel()
	if o := <-res; o != OutcomeContextDone {
		t.Fatalf("outcome %v", o)
	}
}

func TestSuperviseProcessExitWithSocketDownNeedsNoGrace(t *testing.T) {
	ui := make(chan bool, 4)
	proc := make(chan error, 1)
	res, cancel := superviseAsync(t, Signals{UI: ui, Process: proc}, time.Hour, time.Second)
	defer cancel()
	ui <- true
	ui <- false
	proc <- nil
	select {
	case o := <-res:
		if o != OutcomeWindowGone {
			t.Fatalf("outcome %v", o)
		}
	case <-time.After(time.Second):
		t.Fatal("a dead process with a dead socket must end the window at once")
	}
}

func TestSuperviseProcessExitWhileSocketUpIsIgnored(t *testing.T) {
	ui := make(chan bool, 4)
	proc := make(chan error, 1)
	res, cancel := superviseAsync(t, Signals{UI: ui, Process: proc}, 50*time.Millisecond, time.Second)
	ui <- true
	proc <- nil
	select {
	case o := <-res:
		t.Fatalf("a hand-off exit with a live socket must not end the window, got %v", o)
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	<-res
}

func TestSuperviseNeverConnected(t *testing.T) {
	res, cancel := superviseAsync(t, Signals{UI: make(chan bool)}, time.Second, 50*time.Millisecond)
	defer cancel()
	if o := <-res; o != OutcomeNeverConnected {
		t.Fatalf("outcome %v", o)
	}
}
