//go:build client && !server

package planstate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/host"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
	"github.com/danielckv/agentic-plan-forge/internal/storage"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

const (
	// MaxVerifyTimeout caps a command's timeout_seconds.
	MaxVerifyTimeout = 600
	// MaxVerifyOutput caps the output Verify captures per command.
	MaxVerifyOutput = 1 << 20
	// verifyScanTimeout bounds the source-fingerprint scans, which are not on a
	// hook's two-second budget.
	verifyScanTimeout = 60 * time.Second
	// outputTailBytes is how much of the output is echoed to the terminal.
	outputTailBytes = 4 << 10
	// waitDelay bounds how long Wait lingers on output pipes after a kill.
	waitDelay = 2 * time.Second
	// maxAnswerBytes bounds a confirmation answer.
	maxAnswerBytes = 256
)

// VerifyOptions configures Verify.
type VerifyOptions struct {
	// TaskID is the manifest task whose verification commands run.
	TaskID string
	// In answers the confirmation prompt. Out receives the display; Err receives
	// warnings. Nil writers discard.
	In  io.Reader
	Out io.Writer
	Err io.Writer
	// Interactive reports a terminal on stdin and stdout. Without one the
	// caller has already required an explicit --yes, which is enough for an
	// approved manifest and never enough for an unapproved one.
	Interactive bool
	// Env overrides the parent environment filtered for the child. Nil selects
	// os.Environ().
	Env []string
	// ScanTimeout bounds each source-fingerprint scan. Zero selects 60 s.
	ScanTimeout time.Duration
}

// CommandReport is the outcome of one verification command.
type CommandReport struct {
	VerificationID   string
	Argv             []string
	Cwd              string
	ExitCode         int
	ExpectedExitCode int
	Duration         time.Duration
	OutputSHA256     string
	OutputBytes      int64
	Truncated        bool
	TimedOut         bool
	// Passed reports an exit status equal to the expected one, in time.
	Passed bool
	// ReceiptWritten reports that a receipt now vouches for this run.
	ReceiptWritten bool
	// Problem says why a passed run has no receipt, or why a run failed.
	Problem string
	// Output is the sanitized tail of the captured output, for display.
	Output string
}

// Report is the outcome of a Verify call.
type Report struct {
	TaskID   string
	Approved bool
	Commands []CommandReport
}

// OK reports that every command ran, passed and left a receipt.
func (r Report) OK() bool {
	if len(r.Commands) == 0 {
		return false
	}
	for _, c := range r.Commands {
		if !c.Passed || !c.ReceiptWritten {
			return false
		}
	}
	return true
}

// capture keeps at most MaxVerifyOutput bytes of a command's combined output.
type capture struct {
	mu    sync.Mutex
	buf   []byte
	total int64
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total += int64(len(p))
	if room := MaxVerifyOutput - len(c.buf); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		c.buf = append(c.buf, p...)
	}
	return len(p), nil
}

// resolveExecutable returns the absolute program to run for argv[0]. A bare name
// is looked up on PATH. A name with a path separator is relative to the
// command's cwd and must stay local to the repository with no symlink
// component; an absolute path is refused, because a manifest never carries
// machine paths.
func resolveExecutable(real, cwdRel, argv0 string) (string, error) {
	if !strings.ContainsAny(argv0, `/\`) {
		p, err := exec.LookPath(argv0)
		if err != nil {
			return "", fmt.Errorf("executable %s was not found on PATH", shown(argv0))
		}
		return p, nil
	}
	if strings.Contains(argv0, `\`) || filepath.IsAbs(argv0) || path.IsAbs(argv0) {
		return "", fmt.Errorf("executable %s must be a bare command name or a repository-relative path", shown(argv0))
	}
	if cwdRel == "" {
		cwdRel = "."
	}
	rel := path.Join(cwdRel, argv0)
	if err := checkLocalPath(real, rel, false); err != nil {
		return "", fmt.Errorf("executable %w", err)
	}
	return filepath.Join(real, filepath.FromSlash(rel)), nil
}

type preparedCommand struct {
	v    contracts.VerificationCommand
	exe  string
	dir  string
	cwd  string
	wait time.Duration
}

// prepare validates one command: bounded, shell-free, with a local cwd and a
// program that resolves inside the rules. A timeout above the ceiling is capped.
func prepare(real string, v contracts.VerificationCommand) (preparedCommand, error) {
	pc := preparedCommand{v: v}
	capped := v
	if capped.TimeoutSeconds > MaxVerifyTimeout {
		capped.TimeoutSeconds = MaxVerifyTimeout
	}
	pc.v.TimeoutSeconds = capped.TimeoutSeconds
	if err := synthesizer.ValidateVerification(capped); err != nil {
		return pc, errors.New(sanitize.Terminal(err.Error()))
	}
	if v.ExpectedExitCode < 0 || v.ExpectedExitCode > 255 {
		return pc, fmt.Errorf("verification %s expected_exit_code must be 0-255", shown(v.ID))
	}
	if err := checkLocalPath(real, v.Cwd, true); err != nil {
		return pc, fmt.Errorf("verification %s cwd: %w", shown(v.ID), err)
	}
	pc.cwd = v.Cwd
	if pc.cwd == "" {
		pc.cwd = "."
	}
	pc.dir = filepath.Join(real, filepath.FromSlash(pc.cwd))
	info, err := os.Stat(pc.dir)
	if err != nil || !info.IsDir() {
		return pc, fmt.Errorf("verification %s cwd %s is not a directory", shown(v.ID), shown(pc.cwd))
	}
	if pc.exe, err = resolveExecutable(real, pc.cwd, v.Argv[0]); err != nil {
		return pc, fmt.Errorf("verification %s: %w", shown(v.ID), err)
	}
	pc.wait = time.Duration(capped.TimeoutSeconds) * time.Second
	return pc, nil
}

func ask(in io.Reader, out io.Writer, prompt string) string {
	fmt.Fprint(out, prompt)
	if in == nil {
		return ""
	}
	line, _ := bufio.NewReader(io.LimitReader(in, maxAnswerBytes)).ReadString('\n')
	return strings.TrimSpace(line)
}

func tail(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[len(b)-n:]
}

// Verify runs the verification commands of one manifest task, explicitly.
//
// It prints the task ID, every argv element and the cwd with %q, one per line,
// and requires confirmation first: `y` for a manifest approved on this machine,
// typing `run` after `commands come from an unapproved file` otherwise. Without
// a terminal (Interactive false) an approved manifest proceeds because the
// caller required --yes, and an unapproved one is refused. Commands run without
// a shell, in their own process group, with the filtered child environment, the
// timeout capped at 600 s and at most 1 MiB of output captured. A timeout or
// cancellation kills the whole process tree and waits for it.
//
// Each command that ran to completion leaves a receipt in the ignored
// .plan/verification/, bound to the source fingerprint measured before and after
// the run: if the sources changed while it ran, no receipt is written. A failed
// command records a failing receipt, never a successful one. Hooks never call
// Verify.
func Verify(ctx context.Context, repo string, opts VerifyOptions) (Report, error) {
	out, errw := opts.Out, opts.Err
	if out == nil {
		out = io.Discard
	}
	if errw == nil {
		errw = io.Discard
	}
	report := Report{TaskID: sanitize.Terminal(opts.TaskID)}
	real, err := realRepo(repo)
	if err != nil {
		return report, err
	}
	pair, err := storage.ReadPair(real)
	switch {
	case errors.Is(err, storage.ErrNoPlan):
		return report, errors.New("no plan manifest is present in this repository")
	case err != nil:
		return report, fmt.Errorf("the plan cannot be read: %w", err)
	}
	m := pair.Meta
	if m.SchemaVersion != contracts.SchemaVersion {
		return report, fmt.Errorf("unsupported manifest schema_version %d", m.SchemaVersion)
	}
	if err := validateManifestPaths(real, m); err != nil {
		return report, fmt.Errorf("refusing to run: %w", err)
	}
	var task *contracts.TechnicalTask
	for i := range m.TaskDAG {
		if m.TaskDAG[i].ID == opts.TaskID {
			task = &m.TaskDAG[i]
			break
		}
	}
	if task == nil {
		return report, fmt.Errorf("the manifest has no task %s", shown(opts.TaskID))
	}
	if len(task.Verification) == 0 {
		return report, fmt.Errorf("task %s has no verification commands", shown(task.ID))
	}
	cmds := make([]preparedCommand, 0, len(task.Verification))
	for _, v := range task.Verification {
		pc, err := prepare(real, v)
		if err != nil {
			return report, fmt.Errorf("refusing to run: %w", err)
		}
		cmds = append(cmds, pc)
	}
	approved, _ := IsApproved(real, pair.ManifestSHA256)
	report.Approved = approved

	if !approved {
		fmt.Fprintln(out, "commands come from an unapproved file")
	}
	fmt.Fprintf(out, "task: %q\n", sanitize.Terminal(task.ID))
	for _, pc := range cmds {
		fmt.Fprintf(out, "verification: %q\n", sanitize.Terminal(pc.v.ID))
		for _, a := range pc.v.Argv {
			fmt.Fprintf(out, "  argv: %q\n", sanitize.Terminal(a))
		}
		fmt.Fprintf(out, "  cwd: %q\n", sanitize.Terminal(pc.cwd))
		fmt.Fprintf(out, "  timeout: %ds, expected exit code: %d\n", pc.v.TimeoutSeconds, pc.v.ExpectedExitCode)
	}
	switch {
	case opts.Interactive && approved:
		if a := strings.ToLower(ask(opts.In, out, "Run these commands? [y/N]: ")); a != "y" && a != "yes" {
			return report, errors.New("not confirmed; nothing was run")
		}
	case opts.Interactive:
		if ask(opts.In, out, `Type "run" to execute commands from this unapproved file: `) != "run" {
			return report, errors.New("not confirmed; nothing was run")
		}
	case !approved:
		return report, errors.New("commands come from an unapproved file; run interactively and type run to execute them")
	}

	timeout := opts.ScanTimeout
	if timeout <= 0 {
		timeout = verifyScanTimeout
	}
	env := opts.Env
	if env == nil {
		env = os.Environ()
	}
	before, ferr := Fingerprint(ctx, real, timeout)
	for _, pc := range cmds {
		cr := runCommand(ctx, pc, env)
		after, aerr := Fingerprint(ctx, real, timeout)
		switch {
		case !cr.ranToCompletion():
			// Nothing completed, so there is nothing to vouch for.
		case ferr != nil || aerr != nil:
			cr.Problem = "no receipt: the source fingerprint could not be measured (" + sanitize.Terminal(errString(ferr, aerr)) + ")"
		case before != after:
			cr.Problem = "no receipt: the sources changed while the command ran"
		default:
			rerr := storage.WriteReceipt(ctx, real, storage.Receipt{
				TaskID: task.ID, VerificationID: pc.v.ID, CommandSHA256: CommandHash(pc.v),
				SourceFingerprint: after, ExitCode: cr.ExitCode, ExpectedExitCode: pc.v.ExpectedExitCode,
				OutputSHA256: cr.OutputSHA256, DurationMS: cr.Duration.Milliseconds(), At: time.Now().UTC(),
			})
			if rerr != nil {
				cr.Problem = "no receipt: " + sanitize.Terminal(rerr.Error())
			} else {
				cr.ReceiptWritten = true
			}
		}
		before, ferr = after, aerr
		report.Commands = append(report.Commands, cr)
		printResult(out, errw, cr)
		if err := ctx.Err(); err != nil {
			return report, err
		}
	}
	return report, nil
}

func errString(errs ...error) string {
	for _, e := range errs {
		if e != nil {
			return e.Error()
		}
	}
	return ""
}

// ranToCompletion reports that the process exited on its own, so its exit code
// means something.
func (c CommandReport) ranToCompletion() bool { return !c.TimedOut && c.ExitCode >= 0 }

func printResult(out, errw io.Writer, c CommandReport) {
	status := "passed"
	if !c.Passed {
		status = "FAILED"
	}
	fmt.Fprintf(out, "%s: %s (exit %d, expected %d, %s)\n", sanitize.Terminal(c.VerificationID), status,
		c.ExitCode, c.ExpectedExitCode, c.Duration.Round(time.Millisecond))
	fmt.Fprintf(out, "  output: %d byte(s), sha256 %s", c.OutputBytes, c.OutputSHA256)
	if c.Truncated {
		fmt.Fprintf(out, " (captured the first %d bytes)", MaxVerifyOutput)
	}
	fmt.Fprintln(out)
	if c.Output != "" {
		fmt.Fprintln(out, "  output tail:")
		for _, line := range strings.Split(c.Output, "\n") {
			fmt.Fprintf(out, "    %s\n", line)
		}
	}
	if c.ReceiptWritten {
		fmt.Fprintln(out, "  receipt recorded")
	}
	if c.Problem != "" {
		fmt.Fprintf(errw, "  %s\n", c.Problem)
	}
}

// runCommand executes one prepared command and measures it.
func runCommand(ctx context.Context, pc preparedCommand, parentEnv []string) CommandReport {
	cr := CommandReport{
		VerificationID: pc.v.ID, Argv: append([]string(nil), pc.v.Argv...), Cwd: pc.cwd,
		ExpectedExitCode: pc.v.ExpectedExitCode, ExitCode: -1,
	}
	runCtx, cancel := context.WithTimeout(ctx, pc.wait)
	defer cancel()

	cmd := exec.Command(pc.exe, pc.v.Argv[1:]...)
	cmd.Dir = pc.dir
	cmd.Env = host.BuildEnvironment(parentEnv, pc.dir, nil, nil)
	cmd.WaitDelay = waitDelay
	out := &capture{}
	cmd.Stdout, cmd.Stderr = out, out

	started := time.Now()
	group, err := startGroup(cmd)
	if err != nil {
		cr.Problem = "the command could not be started: " + sanitize.Terminal(err.Error())
		cr.Duration = time.Since(started)
		return cr
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var werr error
	select {
	case werr = <-done:
	case <-runCtx.Done():
		group.Kill()
		werr = <-done // wait for the whole tree to terminate
		cr.TimedOut = ctx.Err() == nil
	}
	group.Close()
	cr.Duration = time.Since(started)

	out.mu.Lock()
	sum := sha256.Sum256(out.buf)
	cr.OutputSHA256, cr.OutputBytes, cr.Truncated = hex.EncodeToString(sum[:]), out.total, out.total > int64(len(out.buf))
	shownOut := append([]byte(nil), tail(out.buf, outputTailBytes)...)
	out.mu.Unlock()
	cr.Output = sanitize.Lines(string(shownOut))

	switch {
	case cr.TimedOut:
		cr.Problem = fmt.Sprintf("the command exceeded its %s timeout and was killed", pc.wait)
	case ctx.Err() != nil:
		cr.Problem = "the command was cancelled"
	case cmd.ProcessState != nil:
		cr.ExitCode = cmd.ProcessState.ExitCode()
		cr.Passed = cr.ExitCode == pc.v.ExpectedExitCode
		var ee *exec.ExitError
		if werr != nil && !errors.As(werr, &ee) {
			cr.Problem = "waiting for the command failed: " + sanitize.Terminal(werr.Error())
		}
	}
	return cr
}
