//go:build client && !server

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielckv/agentic-plan-forge/internal/app"
	"github.com/danielckv/agentic-plan-forge/internal/planstate"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

func init() {
	register("run", "open the Plan-Forge tab and run a planning session", runRun)
	register("wait", "wait for a detached session to finish (exit 75 on timeout)", runWait)
	register("join", "join a remote collaboration session with an invite URL", runJoin)
	register("invite", "mint or revoke an invite for a running remote session", runInvite)
	register("__session-child", "hidden: detached session worker process", runSessionChild)
}

func parseSessionMode(raw string) (relay.SessionMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "solo":
		return relay.ModeSolo, nil
	case "remote":
		return relay.ModeRemote, nil
	default:
		return "", fmt.Errorf("unknown --mode %q (supported: solo, remote)", raw)
	}
}

func runRun(ctx context.Context, args []string) int {
	var (
		repoFlag  string
		hostFlag  string
		modeFlag  string
		briefFlag string
		detach    bool
	)
	fs, err := parseFlags("run", args, func(fs *flag.FlagSet) {
		fs.StringVar(&repoFlag, "repo", "", "repository path (default: cwd's repository root)")
		fs.StringVar(&hostFlag, "host", "", "invoking host kind (claude, omp, agy)")
		fs.StringVar(&modeFlag, "mode", "solo", "session mode: solo|remote")
		fs.StringVar(&briefFlag, "brief", "", "Markdown file seeding the shared PRD document (solo mode only)")
		fs.BoolVar(&detach, "detach", false, "run in a background session and print {\"session_id\":\"...\"}")
	})
	if err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "run takes no positional arguments")
		return exitUsage
	}
	mode, err := parseSessionMode(modeFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		return exitUsage
	}
	brief, err := readBrief(briefFlag, mode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		return exitUsage
	}
	var briefPath string
	if brief != "" {
		if briefPath, err = filepath.Abs(briefFlag); err != nil {
			fmt.Fprintf(os.Stderr, "run: resolve brief path: %v\n", err)
			return exitUsage
		}
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	opts := app.Options{
		Repo:              repoFlag,
		Host:              strings.ToLower(strings.TrimSpace(hostFlag)),
		Mode:              mode,
		Brief:             brief,
		BriefPath:         briefPath,
		Config:            cfg,
		Stdout:            os.Stdout,
		Stderr:            os.Stderr,
		InteractiveInvite: !detach && isTTY(os.Stdin) && isTTY(os.Stderr),
	}
	if detach {
		res := app.StartDetached(ctx, opts)
		if res.ExitCode != exitOK {
			if res.Message != "" {
				fmt.Fprintf(os.Stderr, "run: %s\n", res.Message)
			}
			return res.ExitCode
		}
		out, _ := json.Marshal(struct {
			SessionID string `json:"session_id"`
		}{SessionID: res.SessionID})
		fmt.Println(string(out))
		return exitOK
	}
	res := app.Run(ctx, opts)
	if res.ExitCode != exitOK && res.Message != "" {
		fmt.Fprintf(os.Stderr, "run: %s\n", res.Message)
	}
	return res.ExitCode
}

func runSessionChild(ctx context.Context, args []string) int {
	var (
		sessionID string
		repoFlag  string
		hostFlag  string
		modeFlag  string
		briefFlag string
	)
	fs, err := parseFlags("__session-child", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session", "", "session UUIDv4")
		fs.StringVar(&repoFlag, "repo", "", "repository path")
		fs.StringVar(&hostFlag, "host", "", "invoking host kind")
		fs.StringVar(&modeFlag, "mode", "solo", "session mode: solo|remote")
		fs.StringVar(&briefFlag, "brief", "", "Markdown file seeding the shared PRD document")
	})
	if err != nil || fs.NArg() != 0 || !app.ValidSessionID(sessionID) {
		return exitUsage
	}
	mode, err := parseSessionMode(modeFlag)
	if err != nil {
		return exitUsage
	}
	brief, err := readBrief(briefFlag, mode)
	if err != nil {
		return fail(err)
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	var logger *slog.Logger
	if logPath, lerr := app.LogPath(sessionID); lerr == nil {
		if f, ferr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); ferr == nil {
			defer f.Close()
			logger = slog.New(slog.NewTextHandler(f, nil))
		}
	}
	res := app.Run(ctx, app.Options{
		SessionID: sessionID,
		Repo:      repoFlag,
		Host:      strings.ToLower(strings.TrimSpace(hostFlag)),
		Mode:      mode,
		Brief:     brief,
		Config:    cfg,
		Detached:  true,
		Logger:    logger,
	})
	return res.ExitCode
}

func runWait(ctx context.Context, args []string) int {
	var (
		sessionID  string
		timeoutRaw string
	)
	fs, err := parseFlags("wait", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session", "", "session UUIDv4 from `plan-forge run --detach` (required)")
		fs.StringVar(&timeoutRaw, "timeout", "50s", "poll timeout before exiting 75 (for example: 50s, 100s)")
	})
	if err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || sessionID == "" {
		fmt.Fprintln(os.Stderr, "usage: plan-forge wait --session <id> [--timeout 50s]")
		return exitUsage
	}
	timeout, err := time.ParseDuration(timeoutRaw)
	if err != nil || timeout <= 0 {
		fmt.Fprintf(os.Stderr, "wait: invalid --timeout %q\n", timeoutRaw)
		return exitUsage
	}
	st, code := app.Wait(ctx, sessionID, timeout)
	if code == exitWaitTimeout {
		return exitWaitTimeout
	}
	if code != exitOK {
		if st.Message != "" {
			fmt.Fprintf(os.Stderr, "wait: %s\n", st.Message)
		}
		return code
	}
	out, _ := json.Marshal(struct {
		SessionID     string               `json:"session_id"`
		State         string               `json:"state"`
		ArtifactPaths []string             `json:"artifact_paths"`
		Validation    planstate.Validation `json:"validation"`
	}{
		SessionID:     st.SessionID,
		State:         string(st.State),
		ArtifactPaths: st.ArtifactPaths,
		Validation:    waitValidation(ctx, st.ArtifactPaths),
	})
	fmt.Println(string(out))
	return exitOK
}

// waitValidationTimeout bounds the scan `wait` runs after approval.
const waitValidationTimeout = 20 * time.Second

// waitValidation validates the repository derived from the spec artifact path.
// Approval already happened, so a failure never changes wait's exit code: it
// reports status UNKNOWN with the run-validate note and zero-valued fields.
func waitValidation(ctx context.Context, artifactPaths []string) planstate.Validation {
	unknown := planstate.Validation{
		Status: string(planstate.StatusUnknown), Pending: []string{}, Drift: []planstate.ValidationDrift{},
		Notes: []string{"validation timed out; run plan-forge validate"},
	}
	if len(artifactPaths) == 0 {
		return unknown
	}
	suffix := filepath.FromSlash("/" + planstate.SpecRel)
	repo := strings.TrimSuffix(artifactPaths[0], suffix)
	if repo == artifactPaths[0] || repo == "" {
		return unknown
	}
	v, err := planstate.Validate(ctx, repo, planstate.CheckOptions{ScanTimeout: waitValidationTimeout})
	if err != nil {
		return unknown
	}
	return v
}

func runJoin(ctx context.Context, args []string) int {
	var (
		repoFlag  string
		inviteArg string
	)
	// Allow `plan-forge join <invite-url> [--repo PATH]` or flags first.
	flagArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		inviteArg = args[0]
		flagArgs = args[1:]
	}
	fs, err := parseFlags("join", flagArgs, func(fs *flag.FlagSet) {
		fs.StringVar(&repoFlag, "repo", "", "repository path to sync approved artifacts into (optional)")
		fs.StringVar(&inviteArg, "invite", inviteArg, "invite URL (https://<relay>/join/<session>#<secret>)")
	})
	if err != nil {
		return exitUsage
	}
	if inviteArg == "" && fs.NArg() == 1 {
		inviteArg = fs.Arg(0)
	} else if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: plan-forge join [<invite-url>] [--repo PATH]")
		return exitUsage
	}
	if strings.TrimSpace(inviteArg) == "" {
		if !isTTY(os.Stdin) {
			fmt.Fprintln(os.Stderr, "join: an invite URL is required")
			return exitUsage
		}
		fmt.Fprint(os.Stderr, "Invite URL: ")
		line, rerr := bufio.NewReader(os.Stdin).ReadString('\n')
		if rerr != nil && strings.TrimSpace(line) == "" {
			fmt.Fprintln(os.Stderr, "join: could not read the invite URL")
			return exitUsage
		}
		inviteArg = strings.TrimSpace(line)
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	res := app.Join(ctx, app.JoinOptions{
		InviteURL: inviteArg,
		Repo:      repoFlag,
		Config:    cfg,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
	})
	if res.ExitCode != exitOK && res.Message != "" {
		fmt.Fprintf(os.Stderr, "join: %s\n", res.Message)
	}
	return res.ExitCode
}

func runInvite(ctx context.Context, args []string) int {
	var (
		sessionID string
		role      string
		maxUses   int
		ttlRaw    string
		revokeID  string
	)
	fs, err := parseFlags("invite", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session", "", "local session UUIDv4 (required)")
		fs.StringVar(&role, "role", "editor", "invite role: editor|viewer")
		fs.IntVar(&maxUses, "max-uses", 1, "maximum uses (1-10)")
		fs.StringVar(&ttlRaw, "ttl", "24h", "invite time-to-live (up to 24h)")
		fs.StringVar(&revokeID, "revoke", "", "invite ID to revoke instead of minting")
	})
	if err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || sessionID == "" {
		fmt.Fprintln(os.Stderr, "usage: plan-forge invite --session <id> [--role editor|viewer] [--max-uses N] [--ttl 24h] [--revoke <invite-id>]")
		return exitUsage
	}
	ttl, err := time.ParseDuration(ttlRaw)
	if err != nil || ttl <= 0 {
		fmt.Fprintf(os.Stderr, "invite: invalid --ttl %q\n", ttlRaw)
		return exitUsage
	}
	url, err := app.MintInvite(ctx, sessionID, relay.InviteOptions{
		Role:       role,
		MaxUses:    maxUses,
		TTLSeconds: int(ttl.Seconds()),
	}, revokeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invite: %v\n", err)
		return exitRuntimeError
	}
	if url != "" {
		fmt.Println(url)
	}
	return exitOK
}

// maxBriefBytes bounds a --brief file. The brief is a page of prose agreed
// with the user, not a requirements corpus: documents belong in the ingestion
// view, which parses and normalizes them.
const maxBriefBytes = 128 << 10

// readBrief loads the --brief file. Remote sessions take their document from
// the relay, so a brief there is refused rather than silently dropped.
func readBrief(path string, mode relay.SessionMode) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	if mode != relay.ModeSolo {
		return "", errors.New("--brief applies to solo sessions; a remote session takes its document from the relay")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read brief: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("read brief: %s is a directory", path)
	}
	if info.Size() > maxBriefBytes {
		return "", fmt.Errorf("read brief: %s is %d bytes, over the %d byte limit", path, info.Size(), maxBriefBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read brief: %w", err)
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("read brief: %s is not valid UTF-8", path)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", fmt.Errorf("read brief: %s is empty", path)
	}
	return string(raw), nil
}
