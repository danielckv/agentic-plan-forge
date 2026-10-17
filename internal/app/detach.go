//go:build client && !server

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/planstate"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/storage"
	"github.com/danielckv/agentic-plan-forge/internal/window"
)

const (
	// detachedStartupBound is how long the parent of a detached session waits
	// for the child's window to connect before stopping the child and reporting
	// startup_timeout.
	detachedStartupBound = 30 * time.Second
	pollInterval         = 200 * time.Millisecond
)

// DetachResult is the parent's outcome when starting a detached session.
type DetachResult struct {
	SessionID string
	ExitCode  int
	Message   string
}

// StartDetached spawns `__session-child` in its own session/process group,
// waits up to 30 seconds for the dedicated window to connect (PhaseRunning) or
// for the child to reach a terminal state, and returns the session id or the
// terminal failure.
func StartDetached(ctx context.Context, opts Options) DetachResult {
	id := opts.SessionID
	if id == "" {
		id = NewSessionID()
	}
	if err := checkSessionID(id); err != nil {
		return DetachResult{ExitCode: ExitUsage, Message: err.Error()}
	}
	mode := opts.Mode
	if mode == "" {
		mode = relay.ModeSolo
	}
	repo, err := ResolveRepo(ctx, opts.Repo)
	if err != nil {
		res := classify(err)
		return DetachResult{SessionID: id, ExitCode: res.ExitCode, Message: res.Message}
	}
	exe, err := os.Executable()
	if err != nil {
		return DetachResult{SessionID: id, ExitCode: ExitFailure, Message: notice(err)}
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	logPath, err := LogPath(id)
	if err != nil {
		return DetachResult{SessionID: id, ExitCode: ExitFailure, Message: notice(err)}
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return DetachResult{SessionID: id, ExitCode: ExitFailure, Message: notice(err)}
	}

	childArgs := []string{
		"__session-child",
		"--session", id,
		"--repo", repo,
		"--mode", strings.ToLower(string(mode)),
	}
	if opts.Host != "" {
		childArgs = append(childArgs, "--host", opts.Host)
	}
	cmd := exec.Command(exe, childArgs...)
	cmd.Dir = repo
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detachAttrs(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return DetachResult{SessionID: id, ExitCode: ExitFailure, Message: notice(err)}
	}
	_ = logFile.Close()
	childPID := cmd.Process.Pid
	_ = cmd.Process.Release()

	bound := opts.StartupWait
	if bound <= 0 {
		bound = detachedStartupBound
	}
	deadline := time.Now().Add(bound)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		st, rerr := ReadState(id)
		if rerr == nil {
			if st.State == PhaseRunning || st.State == PhaseApproved {
				return DetachResult{SessionID: id, ExitCode: ExitOK}
			}
			if st.State.Terminal() {
				code := st.ExitCode
				if code == 0 {
					code = ExitCodeFor(st.State)
				}
				return DetachResult{SessionID: id, ExitCode: code, Message: st.Message}
			}
		}
		if !PIDAlive(childPID) {
			if st, rerr := ReadState(id); rerr == nil && st.State.Terminal() {
				code := st.ExitCode
				if code == 0 {
					code = ExitCodeFor(st.State)
				}
				return DetachResult{SessionID: id, ExitCode: code, Message: st.Message}
			}
			msg := "the detached session exited before the window connected"
			_ = WriteState(SessionState{
				SessionID: id, PID: childPID, State: PhaseFailed,
				ExitCode: ExitFailure, Message: msg,
			})
			return DetachResult{SessionID: id, ExitCode: ExitFailure, Message: msg}
		}
		if time.Now().After(deadline) {
			killGroup(cmd)
			msg := "startup_timeout: the Plan-Forge window did not connect within 30s"
			_ = WriteState(SessionState{
				SessionID: id, PID: childPID, State: PhaseFailed,
				ExitCode: ExitFailure, Message: msg,
			})
			return DetachResult{SessionID: id, ExitCode: ExitFailure, Message: msg}
		}
		select {
		case <-ctx.Done():
			killGroup(cmd)
			return DetachResult{SessionID: id, ExitCode: ExitCancelled, Message: "interrupted while waiting for the window"}
		case <-ticker.C:
		}
	}
}

// Wait polls a session's state file until it reaches a terminal state, its
// process disappears, or timeout elapses (ExitWaitTimeout = 75).
func Wait(ctx context.Context, sessionID string, timeout time.Duration) (SessionState, int) {
	if !ValidSessionID(sessionID) {
		return SessionState{Message: fmt.Sprintf("invalid session id %q", sessionID)}, ExitUsage
	}
	if timeout <= 0 {
		timeout = 50 * time.Second
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		st, err := ReadState(sessionID)
		if errors.Is(err, ErrNoSession) {
			return SessionState{Message: fmt.Sprintf("no session %s found", sessionID)}, ExitFailure
		}
		if err != nil {
			return SessionState{Message: notice(err)}, ExitFailure
		}
		if st.State.Terminal() {
			code := st.ExitCode
			if code == 0 && st.State != PhaseApproved {
				code = ExitCodeFor(st.State)
			}
			return st, code
		}
		if st.PID > 0 && !PIDAlive(st.PID) {
			if latest, rerr := ReadState(sessionID); rerr == nil && latest.State.Terminal() {
				code := latest.ExitCode
				if code == 0 && latest.State != PhaseApproved {
					code = ExitCodeFor(latest.State)
				}
				return latest, code
			}
			failed := SessionState{
				SessionID: sessionID, RelaySessionID: st.RelaySessionID, PID: st.PID,
				State: PhaseFailed, ExitCode: ExitFailure,
				Message: "the session process exited unexpectedly",
			}
			_ = WriteState(failed)
			return failed, ExitFailure
		}
		if time.Now().After(deadline) {
			return st, ExitWaitTimeout
		}
		select {
		case <-ctx.Done():
			return SessionState{SessionID: sessionID, State: PhaseCancelled, ExitCode: ExitCancelled, Message: "wait interrupted"}, ExitCancelled
		case <-ticker.C:
		}
	}
}

// JoinOptions configures a follower session (`plan-forge join`).
type JoinOptions struct {
	InviteURL    string
	Repo         string
	Config       config.Config
	Stdout       io.Writer
	Stderr       io.Writer
	Logger       *slog.Logger
	LaunchWindow func(url string) (windowHandle, error)
	Grace        time.Duration
	StartupWait  time.Duration
}

// Join connects a follower to a Remote session using an invite URL, opens the
// local window, and writes verified artifacts when the plan is approved.
func Join(ctx context.Context, opts JoinOptions) Result {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	ep, relaySessionID, inviteSecret, err := relay.ParseInviteURL(opts.InviteURL)
	if err != nil {
		return Result{Phase: PhaseFailed, ExitCode: ExitUsage, Message: err.Error()}
	}
	var repo string
	if opts.Repo != "" {
		resolved, rerr := ResolveRepo(ctx, opts.Repo)
		if rerr != nil {
			return classify(rerr)
		}
		repo = resolved
	} else if wd, werr := os.Getwd(); werr == nil {
		if resolved, rerr := ResolveRepo(ctx, wd); rerr == nil {
			repo = resolved
		}
	}

	client := relay.NewRelayClient(ep, nil)
	joined, err := client.Join(ctx, relaySessionID, inviteSecret)
	if err != nil {
		return classify(fmt.Errorf("join session: %w", err))
	}
	id := NewSessionID()
	cred := Credential{
		SessionID: id, Origin: ep.Origin(), RelaySessionID: relaySessionID,
		Role: "member", Secret: joined.MemberSecret, MemberID: joined.MemberID,
	}
	_ = SaveCredential(cred)
	defer DeleteCredential(id)

	s := &appSession{
		id: id, repo: repo, log: opts.Logger, cfg: opts.Config,
		cred: &cred, endpoint: ep, finishCh: make(chan Result, 1),
	}
	var bridge *relay.Bridge
	deliver := func(raw []byte) error {
		if bridge == nil {
			return relay.ErrUINotConnected
		}
		return bridge.Send(raw)
	}
	var roots []string
	if repo != "" {
		roots = relay.DefaultEgressRoots(repo)
	}
	link, err := newRemoteLink(remoteLinkConfig{
		RelaySessionID: relaySessionID, Client: client, Secret: joined.MemberSecret,
		Owner: false, Deliver: deliver, Guard: relay.NewEgressGuard(roots...), Logger: opts.Logger,
		OnFatal: func(err error) { s.finish(classifyRelayFatal(err)) },
	})
	if err != nil {
		return classify(err)
	}
	s.link, s.auth = link, link
	defer link.close()

	bridge, err = relay.NewBridge(relay.BridgeConfig{
		Assets: UIAssets(), Logger: opts.Logger, OnMessage: link.HandleUI, Upload: s.uploadHandler,
	})
	if err != nil {
		return classify(err)
	}
	s.bridge = bridge
	defer bridge.Close()
	s.routes()

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go link.Run(cctx)
	go func() { _ = bridge.Serve(cctx) }()
	go s.followEvents(cctx, client, relaySessionID, joined.MemberSecret)

	win, outcome, err := startWindow(cctx, windowParams{
		Bridge: bridge, Launch: opts.LaunchWindow, BrowserPath: opts.Config.BrowserPath,
		Logger: opts.Logger, Grace: opts.Grace, StartupWait: opts.StartupWait,
		OnWindowGone: link.windowGone,
	})
	if err != nil {
		return classify(err)
	}
	defer win.Terminate(0)

	fmt.Fprintln(opts.Stderr, "Joined Plan-Forge session. Window opened.")
	select {
	case r := <-s.finishCh:
		if r.Phase == PhaseApproved {
			win.Terminate(window.PostApprovalClose)
		}
		return r
	case err := <-bridge.Fatal():
		return resultFor(PhaseFailed, notice(err), nil)
	case r := <-outcome:
		return r
	case <-ctx.Done():
		return resultFor(PhaseCancelled, "the session was interrupted", nil)
	}
}

// followEvents watches for PLAN_APPROVED on a follower connection, downloads
// and verifies the artifacts when a local repository is available, and reports
// ARTIFACT_SYNCED to the room.
func (s *appSession) followEvents(ctx context.Context, client *relay.RelayClient, relaySessionID, secret string) {
	for {
		env, ok := s.link.Events().next(ctx)
		if !ok {
			return
		}
		if env.EventType != relay.EventPlanApproved {
			continue
		}
		var p relay.PlanApprovedPayload
		if json.Unmarshal(env.Payload, &p) != nil {
			continue
		}
		var paths []string
		if s.repo != "" {
			paths = s.syncFollowerArtifacts(ctx, client, relaySessionID, secret, p)
		}
		s.finish(resultFor(PhaseApproved, "approved", paths))
		return
	}
}

func (s *appSession) syncFollowerArtifacts(ctx context.Context, client *relay.RelayClient, relaySessionID, secret string, p relay.PlanApprovedPayload) []string {
	spec, _, err := client.GetArtifact(ctx, relaySessionID, secret, p.ApprovalID, "synthesized_spec.md")
	if err != nil {
		_ = s.link.SendSynced(false, relay.ArtifactEOTHER, artifactSpecRel)
		return nil
	}
	manifest, _, err := client.GetArtifact(ctx, relaySessionID, secret, p.ApprovalID, "plan_manifest.json")
	if err != nil {
		_ = s.link.SendSynced(false, relay.ArtifactEOTHER, artifactManifestRel)
		return nil
	}
	if sha256Hex(spec) != p.SpecSHA256 || sha256Hex(manifest) != p.ManifestSHA256 {
		_ = s.link.SendSynced(false, relay.ArtifactEHASH, artifactSpecRel)
		return nil
	}
	store, err := storage.NewArtifactStore(s.repo)
	if err != nil {
		_ = s.link.SendSynced(false, storage.ArtifactCode(err), artifactSpecRel)
		return nil
	}
	meta := contracts.ApprovalMeta{ApprovalID: p.ApprovalID, CreatedAt: time.Now().UTC()}
	res, err := store.Commit(ctx, meta, spec, manifest)
	if err != nil {
		_ = s.link.SendSynced(false, storage.ArtifactCode(err), artifactSpecRel)
		return nil
	}
	_ = planstate.RecordApproval(s.repo, res.ManifestSHA256, p.ApprovalID)
	_ = s.link.SendSynced(true, "", "")
	return []string{
		filepath.Join(s.repo, filepath.FromSlash(artifactSpecRel)),
		filepath.Join(s.repo, filepath.FromSlash(artifactManifestRel)),
	}
}

// MintInvite mints a new invite URL (or revokes an existing invite ID) for a
// running Remote owner session.
func MintInvite(ctx context.Context, sessionID string, opts relay.InviteOptions, revokeID string) (string, error) {
	cred, err := LoadCredential(sessionID)
	if err != nil {
		return "", err
	}
	if cred.Role != "owner" {
		return "", errors.New("only the session owner can manage invites")
	}
	ep, err := relay.ParseRelayURL(cred.Origin)
	if err != nil {
		return "", err
	}
	client := relay.NewRelayClient(ep, nil)
	if revokeID != "" {
		if err := client.RevokeInvite(ctx, cred.RelaySessionID, cred.Secret, revokeID); err != nil {
			return "", err
		}
		return "", nil
	}
	res, err := client.CreateInvite(ctx, cred.RelaySessionID, cred.Secret, opts)
	if err != nil {
		return "", err
	}
	return relay.FormatInviteURL(ep, cred.RelaySessionID, res.InviteSecret), nil
}
