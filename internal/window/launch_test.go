//go:build client && !server

package window

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeExecs creates executable stubs and returns name -> absolute path.
func fakeExecs(t *testing.T, names ...string) map[string]string {
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

// finderFor builds a Finder that sees only the stubs in found, on any machine:
// a real /usr/bin/open on the developer's Mac must not change what CI resolves.
func finderFor(goos string, found map[string]string, env map[string]string) Finder {
	known := map[string]bool{}
	for _, p := range found {
		known[p] = true
	}
	return Finder{
		GOOS:   goos,
		Getenv: func(k string) string { return env[k] },
		Stat: func(p string) (fs.FileInfo, error) {
			if !known[p] {
				return nil, fs.ErrNotExist
			}
			return os.Stat(p)
		},
		LookPath: func(name string) (string, error) {
			if p, ok := found[name]; ok {
				return p, nil
			}
			return "", exec.ErrNotFound
		},
	}
}

func TestArgsPutTheURLLastAndNeverWeakenSandbox(t *testing.T) {
	url := "http://127.0.0.1:4000/#n=abc"
	cases := map[string]struct {
		browser Browser
		want    []string
	}{
		"platform opener": {Browser{Prefix: []string{"--"}}, []string{"--", url}},
		"protocol handler": {
			Browser{Prefix: []string{"url.dll,FileProtocolHandler"}},
			[]string{"url.dll,FileProtocolHandler", url},
		},
		"browser executable": {Browser{}, []string{url}},
	}
	for name, tc := range cases {
		got := Args(url, tc.browser)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: args = %q, want %q", name, got, tc.want)
		}
		for _, a := range got {
			if strings.HasPrefix(a, "--app=") || strings.HasPrefix(a, "--user-data-dir=") {
				t.Errorf("%s: a tab must not be opened with a Chromium app-window flag: %q", name, a)
			}
			for _, bad := range forbiddenArgs {
				if strings.HasPrefix(a, bad) {
					t.Errorf("%s: forbidden argument %s present", name, bad)
				}
			}
		}
	}
}

func TestFinderResolvesThePlatformOpener(t *testing.T) {
	cases := []struct {
		goos, stub, wantName string
		wantPrefix           []string
	}{
		{"darwin", "open", "open", []string{"--"}},
		{"linux", "xdg-open", "xdg-open", nil},
		{"windows", "rundll32.exe", "the Windows protocol handler", []string{"url.dll,FileProtocolHandler"}},
	}
	for _, tc := range cases {
		found := fakeExecs(t, tc.stub)
		got, err := finderFor(tc.goos, found, nil).Find("")
		if err != nil {
			t.Fatalf("%s: %v", tc.goos, err)
		}
		if got.Name != tc.wantName || got.Path != found[tc.stub] {
			t.Errorf("%s: got %+v, want %s at %s", tc.goos, got, tc.wantName, found[tc.stub])
		}
		if !slices.Equal(got.Prefix, tc.wantPrefix) {
			t.Errorf("%s: prefix = %q, want %q", tc.goos, got.Prefix, tc.wantPrefix)
		}
	}
}

func TestFinderWindowsPrefersSystemRoot(t *testing.T) {
	dir := t.TempDir()
	sys := filepath.Join(dir, "System32")
	if err := os.MkdirAll(sys, 0o755); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(sys, "rundll32.exe")
	if err := os.WriteFile(abs, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	onPath := fakeExecs(t, "rundll32.exe")
	found := map[string]string{"rundll32.exe": onPath["rundll32.exe"], abs: abs}
	got, err := finderFor("windows", found, map[string]string{"SystemRoot": dir}).Find("")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != abs {
		t.Fatalf("path = %s, want the SystemRoot copy %s", got.Path, abs)
	}
}

func TestFinderLinuxFallsBackPastXdgOpen(t *testing.T) {
	found := fakeExecs(t, "x-www-browser", "gio")
	got, err := finderFor("linux", found, nil).Find("")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "gio" || !slices.Equal(got.Prefix, []string{"open"}) {
		t.Fatalf("got %+v, want gio with an `open` prefix (gio outranks x-www-browser)", got)
	}
}

func TestFinderExplicitBrowserPathWinsAndTakesABareURL(t *testing.T) {
	found := fakeExecs(t, "xdg-open", "mybrowser")
	got, err := finderFor("linux", found, nil).Find(found["mybrowser"])
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != found["mybrowser"] {
		t.Fatalf("browser_path must win over the platform opener, got %+v", got)
	}
	if len(got.Prefix) != 0 {
		t.Fatalf("a browser executable takes the URL alone, got prefix %q", got.Prefix)
	}
}

func TestFinderBadBrowserPathIsExit3AndNeverFallsBack(t *testing.T) {
	found := fakeExecs(t, "xdg-open")
	missing := filepath.Join(t.TempDir(), "nope")
	_, err := finderFor("linux", found, nil).Find(missing)
	if !errors.Is(err, ErrNoBrowser) || ExitCode(err) != 3 {
		t.Fatalf("a bad browser_path must be exit 3 and must not fall back, got %v", err)
	}
	if !strings.Contains(err.Error(), ConfigKey) {
		t.Fatalf("the message must name %s, got %q", ConfigKey, err)
	}
}

func TestFinderNoOpenerIsExit3(t *testing.T) {
	_, err := finderFor("linux", nil, nil).Find("")
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

func TestLaunchWithoutAnOpenerOpensNothing(t *testing.T) {
	f := finderFor("linux", nil, nil)
	_, err := Launch(Options{URL: "http://127.0.0.1:4000/", Finder: &f})
	if ExitCode(err) != ExitNoBrowser {
		t.Fatalf("got %v, want exit code 3", err)
	}
}

func TestLaunchRunsTheOpenerWithOnlyTheURL(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "argv")
	stub := filepath.Join(dir, "xdg-open")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record + "\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f := finderFor("linux", map[string]string{"xdg-open": stub}, nil)
	url := "http://127.0.0.1:4100/#n=tok"
	w, err := Launch(Options{URL: url, Finder: &f})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Terminate(0)
	if !slices.Equal(w.Args, []string{url}) {
		t.Fatalf("args = %q, want just the URL", w.Args)
	}
	deadline := time.Now().Add(5 * time.Second)
	var argv []byte
	for time.Now().Before(deadline) {
		if argv, err = os.ReadFile(record); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the opener never ran: %v", err)
	}
	if got := strings.TrimSpace(string(argv)); got != url {
		t.Fatalf("the opener received %q, want %q", got, url)
	}
}

// A tab is not a process this binary owns, so the opener's exit must never be
// published as a close signal: a page refresh after it exits would otherwise
// end the session.
func TestLaunchedWindowPublishesNoProcessSignal(t *testing.T) {
	found := fakeExecs(t, "xdg-open")
	f := finderFor("linux", found, nil)
	w, err := Launch(Options{URL: "http://127.0.0.1:4200/", Finder: &f})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Terminate(0)
	if w.Done() != nil {
		t.Fatal("Done() must be nil for a tab: the opener is not the browser")
	}
}

func TestSuperviseSurvivesARefreshWithNoProcessSignal(t *testing.T) {
	ui := make(chan bool, 4)
	res, cancel := superviseAsync(t, Signals{UI: ui, Process: nil}, 300*time.Millisecond, time.Second)
	ui <- true
	ui <- false
	time.Sleep(50 * time.Millisecond)
	ui <- true
	select {
	case o := <-res:
		t.Fatalf("a refresh must not end a tab session, got %v", o)
	case <-time.After(500 * time.Millisecond):
	}
	cancel()
	if o := <-res; o != OutcomeContextDone {
		t.Fatalf("outcome %v", o)
	}
}

func TestTerminateWaitsOutTheDelayAndIsIdempotent(t *testing.T) {
	found := fakeExecs(t, "xdg-open")
	f := finderFor("linux", found, nil)
	w, err := Launch(Options{URL: "http://127.0.0.1:4300/", Finder: &f})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	w.Terminate(80 * time.Millisecond)
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("Terminate returned after %v, want at least the delay", elapsed)
	}
	start = time.Now()
	w.Terminate(time.Hour) // the second call is a no-op, not another wait
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Terminate waited %v on the second call, want a no-op", elapsed)
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
