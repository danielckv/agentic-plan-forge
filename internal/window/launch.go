//go:build client && !server

// Package window launches and supervises Plan-Forge's dedicated application
// window: a Chromium-family browser started with --app against the loopback
// bridge, in a throwaway profile so the launched process is the browser process
// and cmd.Wait() returns when the window closes.
//
// It never falls back to the user's default browser or to a tab: a missing
// browser is exit code 3, and the window is the session — closing it without an
// approval ends the session with exit code 130.
package window

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Exit codes the supervising command maps window outcomes to.
const (
	ExitFailure      = 1
	ExitNoBrowser    = 3
	ExitWindowClosed = 130
)

// Timings from the plan.
const (
	// DefaultGrace is how long the UI WebSocket may stay disconnected before the
	// window counts as closed. A refresh, a crash and a close are
	// indistinguishable at the socket, so the window gets a moment to return.
	DefaultGrace = 10 * time.Second
	// DefaultStartupWait bounds how long the page may take to open its first
	// WebSocket after the browser started.
	DefaultStartupWait = 30 * time.Second
	// PostApprovalClose is how long the window may show "Approved — safe to
	// close" before the binary terminates the browser process it owns.
	PostApprovalClose = 5 * time.Second
	terminateGrace    = 3 * time.Second
)

// ConfigKey names the configuration key that selects a browser explicitly.
const ConfigKey = "browser_path"

// Errors.
var (
	// ErrNoBrowser means no Chromium-family browser could be launched.
	ErrNoBrowser = errors.New("no Chromium-family browser found")
	// ErrWindowClosed means the window went away without an approval.
	ErrWindowClosed = errors.New("the Plan-Forge window was closed")
	// ErrNeverConnected means the page never opened its WebSocket.
	ErrNeverConnected = errors.New("the Plan-Forge window never connected to the local session")
	// ErrBadURL means the launch URL is not a loopback http URL.
	ErrBadURL = errors.New("window URL must be http://127.0.0.1:<port>/...")
)

// NoBrowserError carries the guidance printed with exit code 3.
type NoBrowserError struct {
	// Detail says what was tried, or why an explicit browser_path was refused.
	Detail string
}

func (e *NoBrowserError) Error() string {
	var b strings.Builder
	b.WriteString("no Chromium-family browser found")
	if e.Detail != "" {
		b.WriteString(": " + e.Detail)
	}
	b.WriteString(". Install Google Chrome, Microsoft Edge, Brave or Chromium, or set " + ConfigKey +
		" with: plan-forge config --help")
	return b.String()
}

func (e *NoBrowserError) Is(target error) bool { return target == ErrNoBrowser }

// ExitCode maps a window error to the process exit code: 3 when no browser is
// available, 130 when the window closed without approval, otherwise 1.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrNoBrowser):
		return ExitNoBrowser
	case errors.Is(err, ErrWindowClosed):
		return ExitWindowClosed
	default:
		return ExitFailure
	}
}

// --- browser discovery ----------------------------------------------------------

// Browser is a discovered executable.
type Browser struct {
	Name string
	Path string
}

// Finder discovers browsers. Every environment dependency is injectable so the
// search order is testable on any machine.
type Finder struct {
	GOOS     string
	Home     string
	Getenv   func(string) string
	Stat     func(string) (fs.FileInfo, error)
	LookPath func(string) (string, error)
}

// DefaultFinder reads the real machine.
func DefaultFinder() Finder {
	home, _ := os.UserHomeDir()
	return Finder{GOOS: runtime.GOOS, Home: home, Getenv: os.Getenv, Stat: os.Stat, LookPath: exec.LookPath}
}

type candidate struct {
	name  string
	paths []string // absolute install locations
	names []string // executable names resolved through PATH
}

// candidates lists browsers in the plan's order: Chrome, Edge, Brave, Chromium.
func (f Finder) candidates() []candidate {
	env := func(k string) string {
		if f.Getenv == nil {
			return ""
		}
		return f.Getenv(k)
	}
	switch f.GOOS {
	case "darwin":
		app := func(bundle, exe string) []string {
			rel := filepath.Join("Applications", bundle+".app", "Contents", "MacOS", exe)
			out := []string{filepath.Join("/", rel)}
			if f.Home != "" {
				out = append(out, filepath.Join(f.Home, rel))
			}
			return out
		}
		// The bundle executable is run directly, not through `open`, so that
		// cmd.Wait() tracks the window.
		return []candidate{
			{name: "Google Chrome", paths: app("Google Chrome", "Google Chrome")},
			{name: "Microsoft Edge", paths: app("Microsoft Edge", "Microsoft Edge")},
			{name: "Brave", paths: app("Brave Browser", "Brave Browser")},
			{name: "Chromium", paths: app("Chromium", "Chromium")},
		}
	case "windows":
		join := func(root string, parts ...string) []string {
			if root == "" {
				return nil
			}
			return []string{filepath.Join(append([]string{root}, parts...)...)}
		}
		pf, pf86, local := env("ProgramFiles"), env("ProgramFiles(x86)"), env("LocalAppData")
		chrome := []string{}
		for _, r := range []string{pf, pf86, local} {
			chrome = append(chrome, join(r, "Google", "Chrome", "Application", "chrome.exe")...)
		}
		edge := append(join(pf86, "Microsoft", "Edge", "Application", "msedge.exe"),
			join(pf, "Microsoft", "Edge", "Application", "msedge.exe")...)
		brave := []string{}
		for _, r := range []string{pf, pf86, local} {
			brave = append(brave, join(r, "BraveSoftware", "Brave-Browser", "Application", "brave.exe")...)
		}
		chromium := join(local, "Chromium", "Application", "chrome.exe")
		return []candidate{
			{name: "Google Chrome", paths: chrome, names: []string{"chrome.exe"}},
			{name: "Microsoft Edge", paths: edge, names: []string{"msedge.exe"}},
			{name: "Brave", paths: brave, names: []string{"brave.exe"}},
			{name: "Chromium", paths: chromium, names: []string{"chromium.exe"}},
		}
	default:
		return []candidate{
			{name: "Google Chrome", names: []string{"google-chrome", "google-chrome-stable"}},
			{name: "Microsoft Edge", names: []string{"microsoft-edge", "microsoft-edge-stable"}},
			{name: "Brave", names: []string{"brave-browser", "brave"}},
			{name: "Chromium", names: []string{"chromium", "chromium-browser"}, paths: []string{"/snap/bin/chromium"}},
		}
	}
}

func (f Finder) isExecutable(path string) bool {
	if f.Stat == nil {
		return false
	}
	info, err := f.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return f.GOOS == "windows" || info.Mode().Perm()&0o111 != 0
}

// Find resolves the browser to launch: an explicit browser_path first, then
// Chrome, Edge, Brave and Chromium from their known install locations and PATH.
// An explicit path that is unusable is an error, never a silent fall-through to
// a browser the user did not choose.
func (f Finder) Find(browserPath string) (Browser, error) {
	if browserPath != "" {
		if !filepath.IsAbs(browserPath) {
			resolved, err := f.LookPath(browserPath)
			if err != nil {
				return Browser{}, &NoBrowserError{Detail: fmt.Sprintf("%s %q is not an absolute path or a command on PATH", ConfigKey, browserPath)}
			}
			browserPath = resolved
		}
		if !f.isExecutable(browserPath) {
			return Browser{}, &NoBrowserError{Detail: fmt.Sprintf("%s %q is not an executable file", ConfigKey, browserPath)}
		}
		return Browser{Name: filepath.Base(browserPath), Path: browserPath}, nil
	}
	var tried []string
	for _, c := range f.candidates() {
		for _, p := range c.paths {
			tried = append(tried, p)
			if f.isExecutable(p) {
				return Browser{Name: c.name, Path: p}, nil
			}
		}
		for _, n := range c.names {
			tried = append(tried, n)
			if f.LookPath == nil {
				continue
			}
			if p, err := f.LookPath(n); err == nil && f.isExecutable(p) {
				return Browser{Name: c.name, Path: p}, nil
			}
		}
	}
	return Browser{}, &NoBrowserError{Detail: "looked for " + strings.Join(tried, ", ")}
}

// --- launching --------------------------------------------------------------------

// forbiddenArgs must never reach the browser: each one weakens the sandbox or
// opens a control channel to another process.
var forbiddenArgs = []string{
	"--no-sandbox", "--disable-web-security", "--remote-debugging-port", "--remote-debugging-pipe",
}

// Args returns the exact Chromium arguments. A dedicated --user-data-dir is
// what makes the launched process the browser process: Chromium's process
// singleton is keyed by profile directory, so a second window in the user's
// everyday profile would hand off to the existing process and exit at once.
func Args(appURL, profileDir string) []string {
	return []string{
		"--app=" + appURL,
		"--new-window",
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--no-default-browser-check",
	}
}

func validateLaunchURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Hostname() != "127.0.0.1" || u.Port() == "" {
		return ErrBadURL
	}
	return nil
}

// Options configures Launch.
type Options struct {
	// URL is the loopback launch URL, fragment included.
	URL string
	// BrowserPath is the browser_path config value; empty searches.
	BrowserPath string
	// ProfileRoot is where the throwaway profile directory is created; empty
	// uses os.TempDir().
	ProfileRoot string
	// Finder overrides discovery (tests).
	Finder *Finder
	Logger *slog.Logger
}

// Window is a running dedicated window and the process that owns it.
type Window struct {
	Browser    Browser
	Args       []string
	ProfileDir string

	cmd    *exec.Cmd
	done   chan error
	logger *slog.Logger

	closeOnce sync.Once
}

// Launch starts the browser against opts.URL. It returns ErrNoBrowser (exit
// code 3) when none is installed and never opens a tab in another browser.
func Launch(opts Options) (*Window, error) {
	if err := validateLaunchURL(opts.URL); err != nil {
		return nil, err
	}
	finder := DefaultFinder()
	if opts.Finder != nil {
		finder = *opts.Finder
	}
	browser, err := finder.Find(opts.BrowserPath)
	if err != nil {
		return nil, err
	}
	root := opts.ProfileRoot
	if root == "" {
		root = os.TempDir()
	}
	profile, err := os.MkdirTemp(root, "plan-forge-window-*") // 0700
	if err != nil {
		return nil, fmt.Errorf("create window profile: %w", err)
	}
	args := Args(opts.URL, profile)
	for _, a := range args {
		for _, bad := range forbiddenArgs {
			if strings.HasPrefix(a, bad) {
				_ = os.RemoveAll(profile)
				return nil, fmt.Errorf("refusing to launch with %s", bad)
			}
		}
	}

	cmd := exec.Command(browser.Path, args...)
	cmd.Env = browserEnv(os.Environ())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	setProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(profile)
		return nil, fmt.Errorf("start %s: %w", browser.Name, err)
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	w := &Window{Browser: browser, Args: args, ProfileDir: profile, cmd: cmd, done: make(chan error, 1), logger: logger}
	go func() {
		w.done <- cmd.Wait()
		close(w.done)
	}()
	return w, nil
}

// browserEnv strips Plan-Forge variables: the relay token must never reach a
// browser process.
func browserEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "PLAN_FORGE_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Done receives the browser process's exit status once, then closes.
func (w *Window) Done() <-chan error { return w.done }

// Terminate stops the browser after the given delay (use PostApprovalClose
// after an approval, zero to stop at once), then removes the profile.
func (w *Window) Terminate(after time.Duration) {
	w.closeOnce.Do(func() {
		if after > 0 {
			select {
			case <-w.done:
				w.removeProfile()
				return
			case <-time.After(after):
			}
		}
		terminate(w.cmd, terminateGrace, w.done)
		w.removeProfile()
	})
}

// removeProfile deletes the throwaway profile, retrying briefly because the
// browser may still be flushing files (and Windows holds locks) as it exits.
func (w *Window) removeProfile() {
	var err error
	for range 10 {
		if err = os.RemoveAll(w.ProfileDir); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	w.logger.Warn("could not remove window profile", "err", err)
}

// --- supervision --------------------------------------------------------------------

// Outcome is why Supervise returned.
type Outcome int

// Outcomes.
const (
	// OutcomeContextDone means the caller's context ended first.
	OutcomeContextDone Outcome = iota
	// OutcomeWindowGone means the window closed and did not come back.
	OutcomeWindowGone
	// OutcomeNeverConnected means the page never opened its WebSocket.
	OutcomeNeverConnected
)

func (o Outcome) String() string {
	switch o {
	case OutcomeContextDone:
		return "context-done"
	case OutcomeWindowGone:
		return "window-gone"
	case OutcomeNeverConnected:
		return "never-connected"
	}
	return "unknown"
}

// Signals are the two inputs to close detection.
type Signals struct {
	// UI carries the loopback bridge's WebSocket state: true when the UI
	// connected, false when it disconnected. This is the primary signal.
	UI <-chan bool
	// Process receives the browser process's exit (Window.Done). This is the
	// secondary signal.
	Process <-chan error
}

// Supervise decides when the window is gone. The WebSocket is the source of
// truth: it must stay disconnected for the whole grace period, because a
// refresh, a crash and a close all look the same at the socket. A process exit
// while the socket is down needs no grace — nothing can reconnect — and a
// process exit while the socket is still up (a browser that handed off to
// another process) changes nothing until the socket drops.
//
// The returned error is nil only for OutcomeContextDone with a live context.
func Supervise(ctx context.Context, sig Signals, grace, startupWait time.Duration) (Outcome, error) {
	var (
		connected, everConnected, procGone bool
		graceTimer                         *time.Timer
		graceC                             <-chan time.Time
	)
	startup := time.NewTimer(startupWait)
	defer startup.Stop()
	stopGrace := func() {
		if graceTimer != nil {
			graceTimer.Stop()
			graceTimer, graceC = nil, nil
		}
	}
	defer stopGrace()
	ui, proc := sig.UI, sig.Process
	for {
		select {
		case <-ctx.Done():
			return OutcomeContextDone, ctx.Err()
		case up, ok := <-ui:
			if !ok {
				ui = nil
				up = false
			}
			if up {
				connected, everConnected = true, true
				startup.Stop()
				stopGrace()
				continue
			}
			connected = false
			if !everConnected {
				continue
			}
			if procGone {
				return OutcomeWindowGone, ErrWindowClosed
			}
			if graceTimer == nil {
				graceTimer = time.NewTimer(grace)
				graceC = graceTimer.C
			}
		case _, ok := <-proc:
			proc = nil
			_ = ok
			procGone = true
			if !connected {
				return OutcomeWindowGone, ErrWindowClosed
			}
		case <-graceC:
			return OutcomeWindowGone, ErrWindowClosed
		case <-startup.C:
			if !everConnected {
				return OutcomeNeverConnected, ErrNeverConnected
			}
		}
	}
}
