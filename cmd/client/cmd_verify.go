//go:build client && !server

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/danielckv/agentic-plan-forge/internal/planstate"
)

func init() {
	register("verify", "run one manifest verification task explicitly, with confirmation", runVerify)
}

// runVerify implements `plan-forge verify --repo PATH --task ID`: it shows
// argv/cwd, requires explicit interactive confirmation, and never runs from
// hooks. planstate.Verify enforces the untrusted-manifest rules: commands from
// a manifest whose hash is not in the local approval ledger print `commands
// come from an unapproved file` and require typing `run`; verification runs
// without a shell; receipts land in ignored `.plan/verification/`.
func runVerify(ctx context.Context, args []string) int {
	var repoFlag, taskID string
	var yes bool
	fs, err := parseFlags("verify", args, func(fs *flag.FlagSet) {
		fs.StringVar(&repoFlag, "repo", "", "repository path (default: cwd's repository root)")
		fs.StringVar(&taskID, "task", "", "manifest task ID to verify (required)")
		fs.BoolVar(&yes, "yes", false, "skip the interactive confirmation (non-interactive use)")
	})
	if err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "verify takes no arguments")
		return exitUsage
	}
	if taskID == "" {
		fmt.Fprintln(os.Stderr, "verify: --task is required")
		return exitUsage
	}
	// Never run from hooks: a non-TTY stdin without --yes refuses.
	interactive := isTTY(os.Stdin) && isTTY(os.Stdout)
	if !interactive && !yes {
		fmt.Fprintln(os.Stderr, "verify: requires an interactive terminal or --yes")
		return exitUsage
	}
	repo, code := resolveRepoArg(repoFlag)
	if code != exitOK {
		return code
	}

	opts := planstate.VerifyOptions{
		TaskID:      taskID,
		In:          os.Stdin,
		Out:         os.Stdout,
		Err:         os.Stderr,
		Interactive: interactive,
	}
	report, err := planstate.Verify(ctx, repo, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: %v\n", err)
		return exitRuntimeError
	}
	if !report.OK() {
		return exitRuntimeError
	}
	return exitOK
}
