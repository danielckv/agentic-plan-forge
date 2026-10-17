//go:build client && !server

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danielckv/agentic-plan-forge/internal/planstate"
)

func init() {
	register("open", "open the read-only Page 3 view of the approved plan", runOpen)
}

// runOpen implements `plan-forge open --repo PATH`: the read-only Page 3
// view of the repository's approved plan. `.plan/plan_manifest.json` and
// `.plan/synthesized_spec.md` are untrusted input; the manifest is opened
// through planstate.Inspect, and the unapproved case is reported instead of
// rendered as trusted.
func runOpen(_ context.Context, args []string) int {
	var repoFlag string
	fs, err := parseFlags("open", args, func(fs *flag.FlagSet) {
		fs.StringVar(&repoFlag, "repo", "", "repository path (default: cwd's repository root)")
	})
	if err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "open takes no arguments")
		return exitUsage
	}
	repo, code := resolveRepoArg(repoFlag)
	if code != exitOK {
		return code
	}
	snap, err := planstate.Inspect(repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		return exitRuntimeError
	}
	if snap.ManifestSHA256 == "" {
		fmt.Fprintln(os.Stderr, "open: no plan manifest present in this repository")
		return exitRuntimeError
	}
	if !snap.Approved {
		fmt.Fprintln(os.Stderr, "open: UNAPPROVED PLAN (not approved on this machine)")
	}
	fmt.Printf("plan at %s\n", filepath.Join(repo, filepath.FromSlash(planstate.SpecRel)))
	spec := string(snap.SpecBytes)
	if spec == "" {
		fmt.Fprintln(os.Stderr, "open: synthesized spec is empty")
		return exitRuntimeError
	}
	fmt.Print(spec)
	return exitOK
}

// resolveRepoArg resolves --repo or the cwd's repository root, mapped onto
// exit codes. A directory outside any repository is an error for open/summary.
func resolveRepoArg(repoFlag string) (string, int) {
	repo := repoFlag
	if repo == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "resolve repository: %v\n", err)
			return "", exitRuntimeError
		}
		repo = cwd
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve repository: %v\n", err)
		return "", exitRuntimeError
	}
	return abs, exitOK
}
