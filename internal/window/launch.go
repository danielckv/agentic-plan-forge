//go:build client && !server

// Package window opens and supervises Plan-Forge's view of a session: a tab in
// the user's default browser, pointed at the loopback bridge.
//
// It hands the URL to the platform opener (`open` on macOS, `xdg-open` and
// friends on Linux, the protocol handler on Windows), or to the executable
// browser_path names. It never starts a Chromium --app window and never
// creates a throwaway profile: the tab belongs to the user's everyday browser,
// with their extensions, their sessions and their history.
//
// The opener process is therefore not the browser. It exits within
// milliseconds of handing the URL over, so it says nothing about whether the
// tab is still on screen, and Done() deliberately never fires. The UI
// WebSocket is the only close signal: the tab is the session, and dropping
// that socket for longer than the grace period ends the session with exit
// code 130.
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
	// tab counts as closed. A refresh, a crash and a close are
	// indistinguishable at the socket, so the tab gets a moment to return.
	DefaultGrace = 10 * time.Second
	// DefaultStartupWait bounds how long the page may take to open its first
	// WebSocket after the opener ran. It covers a cold browser start, not only
	// a new tab in a browser that is already up.
	DefaultStartupWait = 30 * time.Second
	// PostApprovalClose is how long the binary stays up after an approval so
	// the tab receives the "Approved — safe to close" frame. Nothing closes the
	// tab: it is the user's, in the user's browser.
	PostApprovalClose = 5 * time.Second
)

// ConfigKey names the configuration key that selects a browser explicitly.
const ConfigKey = "browser_path"

// Errors.
var (
	// ErrNoBrowser means no way to open a URL could be resolved.
	ErrNoBrowser = errors.New("no way to open a browser tab was found")
	// ErrWindowClosed means the tab went away without an approval.
	ErrWindowClosed = errors.New("the Plan-Forge tab was closed")
	// ErrNeverConnected means the page never opened its WebSocket.
	ErrNeverConnected = errors.New("the Plan-Forge tab never connected to the local session")
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
	b.WriteString("no way to open a browser tab was found")
	if e.Detail != "" {
		b.WriteString(": " + e.Detail)
	}
	b.WriteString(". Set " + ConfigKey + " to a browser executable with: plan-forge config --help")
	return b.String()
}

func (e *NoBrowserError) Is(target error) bool { return target == ErrNoBrowser }

// ExitCode maps a window error to the process exit code: 3 when no opener is
// available, 130 when the tab closed without approval, otherwise 1.
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

// --- opener discovery -----------------------------------------------------------

// Browser is the resolved executable that opens the tab: a platform opener, or
// the browser named by browser_path.
type Browser struct {
	// Name is what the user is told was used.
	Name string
	// Path is the absolute executable.
	Path string
	// Prefix are the arguments that must precede the URL. The platform openers
	// need one (an end-of-flags marker, a handler entry point); a browser
	// executable takes the URL on its own.
	Prefix []string
}

// Finder discovers the opener. Every environment dependency is injectable so
// the search is testable on any machine.
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

// opener is one candidate platform opener.
type opener struct {
	name   string
	paths  []string // absolute locations tried before PATH
	names  []string // executable names resolved through PATH
	prefix []string
}

// openers lists the platform openers in the order they are tried. The first
// entry of each platform is the one a stock machine has.
func (f Finder) openers() []opener {
	switch f.GOOS {
	case "darwin":
		// `open` without -n reuses the running default browser, so the URL
		// lands as a new tab rather than a second browser instance. "--" stops
		// a URL beginning with a dash being read as a flag.
		return []opener{{
			name: "open", paths: []string{"/usr/bin/open"}, names: []string{"open"}, prefix: []string{"--"},
		}}
	case "windows":
		var paths []string
		if root := f.env("SystemRoot"); root != "" {
			paths = []string{filepath.Join(root, "System32", "rundll32.exe")}
		}
		return []opener{{
			name:  "the Windows protocol handler",
			paths: paths, names: []string{"rundll32.exe", "rundll32"},
			prefix: []string{"url.dll,FileProtocolHandler"},
		}}
	default:
		return []opener{
			{name: "xdg-open", names: []string{"xdg-open"}},
			{name: "gio", names: []string{"gio"}, prefix: []string{"open"}},
			{name: "x-www-browser", names: []string{"x-www-browser"}},
			{name: "sensible-browser", names: []string{"sensible-browser"}},
		}
	}
}

func (f Finder) env(key string) string {
	if f.Getenv == nil {
		return ""
	}
	return f.Getenv(key)
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

func (f Finder) lookPath(name string) (string, error) {
	if f.LookPath == nil {
		return "", exec.ErrNotFound
	}
	return f.LookPath(name)
}

// Find resolves what opens the tab: an explicit browser_path first, then the
// platform opener. An explicit path that is unusable is an error, never a
// silent fall-through to an opener the user did not choose.
func (f Finder) Find(browserPath string) (Browser, error) {
	if browserPath != "" {
		if !filepath.IsAbs(browserPath) {
			resolved, err := f.lookPath(browserPath)
			if err != nil {
				return Browser{}, &NoBrowserError{Detail: fmt.Sprintf("%s %q is not an absolute path or a command on PATH", ConfigKey, browserPath)}
			}
			browserPath = resolved
		}
		if !f.isExecutable(browserPath) {
			return Browser{}, &NoBrowserError{Detail: fmt.Sprintf("%s %q is not an executable file", ConfigKey, browserPath)}
		}
		// A browser executable handed a bare URL opens it in the instance the
		// user already has running, as a tab.
		return Browser{Name: filepath.Base(browserPath), Path: browserPath}, nil
	}
	var tried []string
	for _, c := range f.openers() {
		for _, p := range c.paths {
			tried = append(tried, p)
			if f.isExecutable(p) {
				return Browser{Name: c.name, Path: p, Prefix: c.prefix}, nil
			}
		}
		for _, n := range c.names {
			tried = append(tried, n)
			p, err := f.lookPath(n)
			if err != nil || !f.isExecutable(p) {
				continue
			}
			return Browser{Name: c.name, Path: p, Prefix: c.prefix}, nil
		}
	}
	return Browser{}, &NoBrowserError{Detail: "looked for " + strings.Join(tried, ", ")}
}

// --- launching --------------------------------------------------------------------

// forbiddenArgs must never reach a browser: each one weakens the sandbox or
// opens a control channel to another process. Only a URL is passed today, so
// this guards a browser_path that smuggles arguments in.
var forbiddenArgs = []string{
	"--no-sandbox", "--disable-web-security", "--remote-debugging-port", "--remote-debugging-pipe",
}

// Args returns the exact argv after the executable: the opener's prefix, then
// the URL.
func Args(rawURL string, b Browser) []string {
	args := make([]string, 0, len(b.Prefix)+1)
	args = append(args, b.Prefix...)
	return append(args, rawURL)
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
	// BrowserPath is the browser_path config value; empty uses the platform opener.
	BrowserPath string
	// Finder overrides discovery (tests).
	Finder *Finder
	Logger *slog.Logger
}

// Window is the opened tab, as far as this process can see it.
type Window struct {
	Browser Browser
	Args    []string

	cmd    *exec.Cmd
	logger *slog.Logger

	closeOnce sync.Once
}

// Launch opens opts.URL as a tab. It returns ErrNoBrowser (exit code 3) when
// no opener resolves.
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
	args := Args(opts.URL, browser)
	for _, a := range args {
		for _, bad := range forbiddenArgs {
			if strings.HasPrefix(a, bad) {
				return nil, fmt.Errorf("refusing to launch with %s", bad)
			}
		}
	}

	cmd := exec.Command(browser.Path, args...)
	cmd.Env = browserEnv(os.Environ())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	setProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", browser.Name, err)
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	w := &Window{Browser: browser, Args: args, cmd: cmd, logger: logger}
	// Reap the opener. Its exit says nothing about the tab, so nothing is
	// published: leaving a zombie for the life of a session would be the only
	// effect of not waiting.
	go func() {
		if werr := cmd.Wait(); werr != nil {
			logger.Debug("the tab opener exited with an error", "opener", browser.Name, "err", werr)
		}
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

// Done never delivers. The opener exits as soon as the browser has the URL, so
// its exit is not the tab closing; treating it as one would end a session on
// the first page refresh. Supervise reads a nil channel as "this signal does
// not exist" and decides on the WebSocket alone.
func (w *Window) Done() <-chan error { return nil }

// Terminate waits out the given delay and returns. A tab in the user's own
// browser is theirs to close: this process never kills it, which is also why
// an explicit browser_path cannot be turned into a way to shut the user's
// browser down. After an approval the page shows "Approved — safe to close",
// and the delay is how long the binary stays up to deliver that frame.
func (w *Window) Terminate(after time.Duration) {
	w.closeOnce.Do(func() {
		if after > 0 {
			time.Sleep(after)
		}
	})
}

// --- supervision --------------------------------------------------------------------

// Outcome is why Supervise returned.
type Outcome int

// Outcomes.
const (
	// OutcomeContextDone means the caller's context ended first.
	OutcomeContextDone Outcome = iota
	// OutcomeWindowGone means the tab closed and did not come back.
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

// Signals are the inputs to close detection.
type Signals struct {
	// UI carries the loopback bridge's WebSocket state: true when the UI
	// connected, false when it disconnected. This is the only signal a tab has.
	UI <-chan bool
	// Process receives a launched process's exit. Launch leaves it nil, because
	// the opener is not the browser; a caller that owns a browser process
	// itself may still supply one.
	Process <-chan error
}

// Supervise decides when the tab is gone. The WebSocket is the source of
// truth: it must stay disconnected for the whole grace period, because a
// refresh, a crash and a close all look the same at the socket. A process exit
// while the socket is down needs no grace — nothing can reconnect — and a
// process exit while the socket is still up (a launcher that handed off)
// changes nothing until the socket drops.
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
			// Before the first connect the socket has not had its say: a
			// launcher that hands off exits before the page connects, and
			// the startup timer already covers a tab that never shows.
			if everConnected && !connected {
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
