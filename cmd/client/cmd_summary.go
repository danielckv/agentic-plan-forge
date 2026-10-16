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
	register("summary", "print the terminal executive summary of the approved plan", runSummary)
}

// runSummary implements `plan-forge summary --repo PATH`: the terminal
// executive summary. planstate.Summary enforces the untrusted-manifest rules:
// it starts with `UNAPPROVED PLAN (not approved on this machine)` when the
// manifest hash is not in the local approval ledger.
func runSummary(_ context.Context, args []string) int {
	var repoFlag string
	fs, err := parseFlags("summary", args, func(fs *flag.FlagSet) {
		fs.StringVar(&repoFlag, "repo", "", "repository path (default: cwd's repository root)")
	})
	if err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "summary takes no arguments")
		return exitUsage
	}
	repo, code := resolveRepoArg(repoFlag)
	if code != exitOK {
		return code
	}
	text, err := planstate.Summary(context.Background(), repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "summary: %v\n", err)
		return exitRuntimeError
	}
	if text == "" {
		fmt.Fprintln(os.Stderr, "summary: no plan manifest present in this repository")
		return exitRuntimeError
	}
	fmt.Print(text)
	return exitOK
}
