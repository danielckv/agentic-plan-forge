//go:build client && !server

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/host"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/storage"
	"github.com/danielckv/agentic-plan-forge/internal/window"
)

// ErrUsage marks a configuration or usage problem that maps to exit code 2.
var ErrUsage = errors.New("usage error")

// UsageError wraps msg as an ErrUsage.
func UsageError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUsage, fmt.Sprintf(format, args...))
}

// windowHandle is the open Plan-Forge tab; *window.Window implements it.
type windowHandle interface {
	Done() <-chan error
	Terminate(after time.Duration)
}

// Options configures one session.
type Options struct {
	// SessionID is the local UUIDv4 handle (`wait --session`, `invite --session`,
	// sessions/<id>.{json,log,cred}). Empty generates one.
	SessionID string
	// Repo is the repository root or any directory inside it. Empty means the
	// working directory.
	Repo string
	// Host is the --host the invoking skill passed (claude|omp|agy|<registered>).
	Host string
	// Mode selects Solo or Remote hosting.
	Mode   relay.SessionMode
	Config config.Config
	// Brief seeds the shared PRD document in Solo mode: the text an invoking
	// skill agreed with the user before launch, so the ingestion view opens on
	// it instead of a blank page. Remote sessions take their document from the
	// relay, so the caller is refused rather than silently ignored.
	Brief string
	// BriefPath is the file Brief was read from. StartDetached forwards the
	// path, never the text: argv is readable by other local users.
	BriefPath string
	// Detached is true for the __session-child: nothing is printed to a terminal
	// and the window-startup wait outlasts the parent's.
	Detached bool
	// Stdout and Stderr receive terminal output of a foreground run. The invite
	// URL is printed only when InteractiveInvite is set.
	Stdout, Stderr    io.Writer
	InteractiveInvite bool
	Logger            *slog.Logger

	// Seams for tests; production leaves them nil.
	Generator    contracts.Generator
	Hosts        *HostSummary
	LaunchWindow func(url string) (windowHandle, error)
	Scan         func(ctx context.Context, opts ast.ScanOptions) (*contracts.RepoContextGraph, error)
	Grace        time.Duration
	StartupWait  time.Duration
}

// Result is how a session ended.
type Result struct {
	Phase         SessionPhase
	ExitCode      int
	Message       string
	ArtifactPaths []string
}

func (r Result) state(id, relaySessionID string) SessionState {
	return SessionState{
		SessionID: id, RelaySessionID: relaySessionID, PID: os.Getpid(), State: r.Phase,
		ExitCode: r.ExitCode, ArtifactPaths: r.ArtifactPaths, Message: r.Message,
	}
}

func resultFor(phase SessionPhase, message string, artifacts []string) Result {
	return Result{Phase: phase, ExitCode: ExitCodeFor(phase), Message: message, ArtifactPaths: artifacts}
}

// ResolveRepo returns the repository root for dir: the Git top-level when dir is
// inside a work tree, otherwise dir itself (non-Git folders are supported).
func ResolveRepo(ctx context.Context, dir string) (string, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("repository %q: %w", dir, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("repository %q: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository %q is not a directory", dir)
	}
	if top, err := ast.RepoRoot(ctx, real); err == nil && top != "" {
		if r, err := filepath.EvalSymlinks(top); err == nil {
			return r, nil
		}
		return top, nil
	}
	return real, nil
}

// appSession holds what the loopback routes need.
type appSession struct {
	id        string
	opts      Options
	repo      string
	log       *slog.Logger
	cfg       config.Config
	bridge    *relay.Bridge
	exec      *executor
	auth      authority
	room      *room       // Solo
	link      *remoteLink // Remote
	cred      *Credential // Remote owner
	endpoint  relay.Endpoint
	finishCh  chan Result
	finishOne sync.Once
}

func (s *appSession) finish(r Result) {
	s.finishOne.Do(func() { s.finishCh <- r })
}

func (s *appSession) inviteURL() (string, bool) {
	if s.cred == nil || s.cred.InviteSecret == "" {
		return "", false
	}
	return relay.FormatInviteURL(s.endpoint, s.cred.RelaySessionID, s.cred.InviteSecret), true
}

// resolveHosts selects the MAIN and JUDGE hosts before anything opens, so a
// missing sign-in is exit 3 with guidance rather than a failure after the window
// is up.
func resolveHosts(ctx context.Context, router *host.Router, requested string) (HostSummary, error) {
	main, err := router.Resolve(ctx, contracts.SlotMain)
	if err != nil {
		return HostSummary{}, err
	}
	judge, err := router.Resolve(ctx, contracts.SlotJudge)
	if err != nil {
		return HostSummary{}, err
	}
	sum := HostSummary{
		Requested: requested, MainHost: string(main.Kind), MainModel: main.Model,
		JudgeHost: string(judge.Kind), JudgeModel: judge.Model, CriticSharesMain: judge.SharesMain,
		Differs: requested != "" && requested != string(main.Kind),
	}
	return sum, nil
}

// wireSolo connects a Solo room to its executor: every document change
// schedules a re-interpretation.
func wireSolo(rm *room, exec *executor) {
	rm.docChanged = exec.DocChanged
}

// classify maps a startup failure onto the lifecycle: a missing host, token or
// browser is a prerequisite (exit 3); anything else is a failure (exit 1).
func classify(err error) Result {
	var noHost *host.NoHostError
	switch {
	case errors.As(err, &noHost):
		return resultFor(PhasePrereq, noHost.Error(), nil)
	case errors.Is(err, window.ErrNoBrowser):
		return resultFor(PhasePrereq, err.Error(), nil)
	case errors.Is(err, errNoRelayToken):
		return resultFor(PhasePrereq, err.Error(), nil)
	}
	var capErr *contracts.ErrCapability
	if errors.As(err, &capErr) {
		return resultFor(PhasePrereq, capErr.Error(), nil)
	}
	if errors.Is(err, ErrUsage) {
		return Result{Phase: PhaseFailed, ExitCode: ExitUsage, Message: notice(err)}
	}
	return resultFor(PhaseFailed, notice(err), nil)
}

var errNoRelayToken = errors.New("no relay token is stored: run `plan-forge config --set-relay-token` in your own terminal, or set PLAN_FORGE_RELAY_TOKEN")

// Run hosts one session and returns how it ended. The caller maps Result onto an
// exit code and prints its message. Run writes the session-state file as the
// session moves through starting, running and its terminal state.
func Run(ctx context.Context, opts Options) Result {
	if opts.SessionID == "" {
		opts.SessionID = NewSessionID()
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Mode == "" {
		opts.Mode = relay.ModeSolo
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	id := opts.SessionID
	if err := checkSessionID(id); err != nil {
		return resultFor(PhaseFailed, err.Error(), nil)
	}
	relayID := ""
	write := func(st SessionPhase, msg string) {
		if err := WriteState(SessionState{SessionID: id, RelaySessionID: relayID, PID: os.Getpid(), State: st, Message: msg}); err != nil {
			opts.Logger.Warn("could not write the session state", "err", err)
		}
	}
	write(PhaseStarting, "")

	res := runSession(ctx, opts, &relayID, write)
	if err := WriteState(res.state(id, relayID)); err != nil {
		opts.Logger.Warn("could not write the final session state", "err", err)
	}
	DeleteCredential(id)
	return res
}

func runSession(ctx context.Context, opts Options, relayID *string, write func(SessionPhase, string)) (res Result) {
	id := opts.SessionID
	log := opts.Logger
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	repo, err := ResolveRepo(ctx, opts.Repo)
	if err != nil {
		return classify(err)
	}

	// Generation host. Resolving it now makes a missing sign-in exit 3 before a
	// window opens; the router caches the result for the run.
	gen := opts.Generator
	var hosts HostSummary
	switch {
	case gen != nil:
		if opts.Hosts != nil {
			hosts = *opts.Hosts
		}
	default:
		router := host.NewRouter(opts.Config, host.Kind(opts.Host))
		hosts, err = resolveHosts(ctx, router, opts.Host)
		if err != nil {
			return classify(err)
		}
		gen = router
	}

	store, err := storage.NewArtifactStore(repo)
	if err != nil {
		return classify(err)
	}

	s := &appSession{id: id, opts: opts, repo: repo, log: log, cfg: opts.Config, finishCh: make(chan Result, 1)}

	var bridge *relay.Bridge
	deliver := func(raw []byte) error {
		if bridge == nil {
			return relay.ErrUINotConnected
		}
		return bridge.Send(raw)
	}

	execCfg := executorConfig{
		Repo: repo, Config: opts.Config, Gen: gen, Store: store, Hosts: hosts, Logger: log, Scan: opts.Scan,
		Finish: func(phase SessionPhase, message string, artifacts []string) {
			s.finish(resultFor(phase, message, artifacts))
		},
	}

	var onMessage func([]byte)
	var windowGone func()
	var onWindowLeft func()
	switch opts.Mode {
	case relay.ModeRemote:
		link, cred, err := startRemoteOwner(ctx, s, deliver)
		if err != nil {
			return classify(err)
		}
		*relayID = cred.RelaySessionID
		s.link, s.cred, s.auth = link, cred, link
		defer link.close()
		execCfg.Upload = link.Upload
		execCfg.WaitForSync = true
		execCfg.Auth = link
		s.exec = newExecutor(execCfg)
		link.cfg.DocChanged = s.exec.DocChanged
		onMessage, windowGone, onWindowLeft = link.HandleUI, link.windowGone, link.Leave
		go link.Run(ctx)
	default:
		rm := newRoom(id, nil, deliver, log, opts.Brief)
		defer rm.close()
		s.room, s.auth = rm, soloAuthority{rm}
		execCfg.Auth = s.auth
		s.exec = newExecutor(execCfg)
		wireSolo(rm, s.exec)
		onMessage, windowGone = rm.HandleUI, rm.windowGone
	}

	bridge, err = relay.NewBridge(relay.BridgeConfig{
		Assets: UIAssets(), Logger: log, OnMessage: onMessage, Upload: s.uploadHandler,
	})
	if err != nil {
		return classify(err)
	}
	s.bridge = bridge
	defer bridge.Close()
	s.routes()
	go func() { _ = bridge.Serve(ctx) }()

	if err := s.exec.Start(ctx); err != nil {
		return classify(err)
	}

	win, outcome, err := startWindow(ctx, windowParams{
		Bridge: bridge, Launch: opts.LaunchWindow, BrowserPath: opts.Config.BrowserPath, Logger: log,
		Detached: opts.Detached, Grace: opts.Grace, StartupWait: opts.StartupWait,
		OnRunning:    func() { write(PhaseRunning, "") },
		OnWindowGone: windowGone,
	})
	if err != nil {
		return classify(err)
	}
	defer win.Terminate(0)

	if opts.InteractiveInvite {
		if url, ok := s.inviteURL(); ok {
			fmt.Fprintf(opts.Stderr, "Invite teammates (visible only in this terminal and the Plan-Forge tab):\n  %s\n", url)
		}
	}
	if !opts.Detached {
		fmt.Fprintln(opts.Stderr, "Plan-Forge opened a browser tab. Approve the plan there to continue.")
	}

	for {
		select {
		case r := <-s.finishCh:
			return s.conclude(ctx, r, win)
		case err := <-bridge.Fatal():
			return resultFor(PhaseFailed, notice(err), nil)
		case r := <-outcome:
			// A window that closes while an approval is being written must not
			// cancel it: the commit is transactional and finishes on its own.
			switch s.exec.phaseNow() {
			case relay.PhaseApproving, relay.PhaseApprovingUnknown, relay.PhaseApproved:
				select {
				case fin := <-s.finishCh:
					return s.conclude(ctx, fin, win)
				case <-time.After(30 * time.Second):
				}
			}
			s.exec.stopRun()
			if s.room != nil {
				_ = s.room.ReleaseLease()
			}
			if onWindowLeft != nil {
				onWindowLeft()
			}
			return r
		case <-ctx.Done():
			s.exec.stopRun()
			if onWindowLeft != nil {
				onWindowLeft()
			}
			return resultFor(PhaseCancelled, "the session was interrupted", nil)
		}
	}
}

// conclude ends a session that reached a terminal state through the executor:
// after an approval the window is told it is safe to close and then the browser
// the binary owns is terminated.
func (s *appSession) conclude(ctx context.Context, r Result, win windowHandle) Result {
	if r.Phase == PhaseApproved {
		win.Terminate(window.PostApprovalClose)
	}
	return r
}

// --- relay owner startup -----------------------------------------------------------------------

func startRemoteOwner(ctx context.Context, s *appSession, deliver func([]byte) error) (*remoteLink, *Credential, error) {
	cfg := s.cfg
	if cfg.RelayServerURL == "" {
		return nil, nil, UsageError("--mode remote needs a relay URL: run `plan-forge config --relay-url <url>`")
	}
	ep, err := relay.ParseRelayURL(cfg.RelayServerURL)
	if err != nil {
		return nil, nil, UsageError("%v", err)
	}
	tok, err := config.LoadRelayToken(cfg.RelayServerURL)
	if err != nil {
		return nil, nil, fmt.Errorf("read the relay token: %w", err)
	}
	if strings.TrimSpace(tok.Token) == "" {
		return nil, nil, errNoRelayToken
	}
	client := relay.NewRelayClient(ep, nil)
	created, err := client.CreateSession(ctx, tok.Token, &relay.InviteOptions{Role: string(relay.RoleEditor)})
	if err != nil {
		var se *relay.StatusError
		if errors.As(err, &se) && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden) {
			return nil, nil, fmt.Errorf("%w (the relay rejected the stored token)", errNoRelayToken)
		}
		return nil, nil, fmt.Errorf("create the relay session: %w", err)
	}
	cred := &Credential{
		SessionID: s.id, Origin: ep.Origin(), RelaySessionID: created.RelaySessionID, Role: "owner",
		Secret: created.OwnerSecret, InviteSecret: created.InviteSecret,
	}
	if err := SaveCredential(*cred); err != nil {
		return nil, nil, fmt.Errorf("store the session credential: %w", err)
	}
	s.endpoint = ep
	link, err := newRemoteLink(remoteLinkConfig{
		RelaySessionID: created.RelaySessionID, Client: client, Secret: created.OwnerSecret, Owner: true,
		Deliver: deliver, Guard: relay.NewEgressGuard(relay.DefaultEgressRoots(s.repo)...), Logger: s.log,
		OnFatal: func(err error) { s.finish(classifyRelayFatal(err)) },
	})
	if err != nil {
		return nil, nil, err
	}
	return link, cred, nil
}

// classifyRelayFatal maps an error that ended the relay connection for good.
func classifyRelayFatal(err error) Result {
	switch {
	case errors.Is(err, relay.ErrRoomClosed):
		return resultFor(PhaseCancelled, "the relay session was closed", nil)
	default:
		return resultFor(PhaseFailed, notice(err), nil)
	}
}
