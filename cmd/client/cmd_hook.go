//go:build client && !server

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/planstate"
)

func init() {
	register("hook", "agent session-start hook: report plan status (check-state)", runHook)
}

// runHook implements `plan-forge hook check-state --repo PATH --format
// text|claude|omp|agy`. It is non-interactive and read-only against
// repository source: no manifest means no output and exit 0; the banner and
// context come from planstate.Render, including the fixed unapproved-manifest
// text; a 2-second bound prints nothing on timeout. Notice state lives in
// PlanForgeCache()/notices/ via planstate.ShouldNotify/RecordNotice; nothing
// is written inside the repository.
func runHook(ctx context.Context, args []string) int {
	sub := ""
	flagArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		flagArgs = args[1:]
	}
	var repoFlag, format string
	fs, err := parseFlags("hook", flagArgs, func(fs *flag.FlagSet) {
		fs.StringVar(&repoFlag, "repo", "", "repository path (default: the cwd's repository root)")
		fs.StringVar(&format, "format", "text", "text|claude|omp|agy")
	})
	if err != nil {
		return exitUsage
	}
	if sub == "" {
		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "usage: plan-forge hook check-state [--repo PATH] --format text|claude|omp|agy")
			return exitUsage
		}
		sub = fs.Arg(0)
		if fs.NArg() > 1 {
			fmt.Fprintln(os.Stderr, "hook takes exactly one subcommand: check-state")
			return exitUsage
		}
	} else if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "hook takes exactly one subcommand: check-state")
		return exitUsage
	}
	if sub != "check-state" {
		fmt.Fprintln(os.Stderr, "usage: plan-forge hook check-state [--repo PATH] --format text|claude|omp|agy")
		return exitUsage
	}
	switch format {
	case "text", "claude", "omp", "agy":
	default:
		fmt.Fprintf(os.Stderr, "hook: unknown format %q (supported: text, claude, omp, agy)\n", format)
		return exitUsage
	}
	repo := repoFlag
	if repo == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return exitOK // hooks never fail loudly
		}
		repo = cwd
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return exitOK
	}

	// Bounded: on timeout the hook prints nothing and exits 0.
	done := make(chan hookResult, 1)
	go func() {
		st, err := planstate.CheckState(ctx, abs)
		if err != nil {
			done <- hookResult{}
			return
		}
		out, err := planstate.Render(st, format)
		if err != nil || len(out) == 0 {
			done <- hookResult{}
			return
		}
		done <- hookResult{st: st, out: out}
	}()
	select {
	case res := <-done:
		if res.out == nil {
			return exitOK
		}
		if planstate.ShouldNotify(res.st) {
			if err := planstate.RecordNotice(res.st); err != nil {
				// Notice bookkeeping is best-effort; the hook never fails.
				_ = err
			}
		} else {
			return exitOK // unchanged since the last notice: stay quiet
		}
		os.Stdout.Write(res.out)
		return exitOK
	case <-time.After(2 * time.Second):
		return exitOK
	case <-ctx.Done():
		return exitOK
	}
}

type hookResult struct {
	st  planstate.PlanStatus
	out []byte
}

// hookResult carries the bounded plan-state result. Render output of nil or
// zero length means "print nothing".
