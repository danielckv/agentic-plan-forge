//go:build client && !server

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/host"
)

func init() {
	register("install", "install agent skills and hooks for detected hosts (exit 3: no host, 4: refused, 5: diverged)", runInstall)
}

// runInstall implements `plan-forge install` and its exact output block.
func runInstall(ctx context.Context, args []string) int {
	var hostFlag, scopeFlag, repoFlag string
	var gitHook, dryRun, yes, force bool

	fs, err := parseFlags("install", args, func(fs *flag.FlagSet) {
		fs.StringVar(&hostFlag, "host", "auto", "auto|claude|omp|agy|all (auto = every host whose binary is found)")
		fs.StringVar(&scopeFlag, "scope", "user", "user|project")
		fs.StringVar(&repoFlag, "repo", "", "repository path (project scope / git hook)")
		fs.BoolVar(&gitHook, "git-hook", false, "also install the post-merge git hook")
		fs.BoolVar(&dryRun, "dry-run", false, "print the plan without writing")
		fs.BoolVar(&yes, "yes", false, "never prompt; required on a non-TTY")
		fs.BoolVar(&force, "force", false, "overwrite content that diverged from the manifest")
	})
	if err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "install takes no arguments")
		return exitUsage
	}

	// Non-TTY without --yes never prompts; prompts only when stdin and
	// stdout are both TTYs.
	if !yes && !bothTTYs() {
		fmt.Fprintln(os.Stderr, "install: non-interactive run requires --yes")
		return exitUsage
	}
	if scopeFlag == "project" && repoFlag == "" {
		fmt.Fprintln(os.Stderr, "install: --scope project requires --repo (or run from inside the repository)")
		return exitUsage
	}

	installer, err := host.NewInstaller(version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "install: %v\n", err)
		return exitRuntimeError
	}
	opts := host.InstallOptions{
		Host: hostFlag, Scope: scopeFlag, Repo: repoFlag,
		GitHook: gitHook, DryRun: dryRun, Yes: yes, Force: force,
	}

	reports, arts, err := installer.Plan(ctx, opts)
	if err != nil {
		return installError(err)
	}
	if !dryRun {
		if err := installer.Apply(arts, opts); err != nil {
			return installError(err)
		}
	}

	printInstallBlock(reports, scopeFlag)
	if dryRun {
		return exitOK
	}
	// The final line depends on whether any generation host is signed in:
	// an API key counts as much as a CLI sign-in.
	anyReady := false
	for _, r := range reports {
		if (r.Host == "claude" || r.Host == "omp" || config.IsAPIProvider(r.Host)) && r.Auth == "ready" {
			anyReady = true
		}
	}
	if anyReady {
		fmt.Println("Done. Restart your agent, then type /plan-forge")
	} else {
		fmt.Println("Warning: no generation host is signed in; /plan-forge exits 3 until one is. Run: plan-forge doctor")
	}
	return exitOK
}

// printInstallBlock prints the exact installer output rows.
func printInstallBlock(reports []host.HostReport, scope string) {
	fmt.Printf("Plan-Forge %s — installing agent skills (%s scope)\n", versionDisplay(), scope)
	for _, r := range reports {
		name := padRight(r.Host, 8)
		if !r.Found {
			fmt.Printf("  %s not found    (skipped)\n", name)
			continue
		}
		auth := "auth: " + padRight(r.Auth, 8)
		fmt.Printf("  %s found %-8s %s -> %s\n", name, r.Version, auth, strings.Join(r.Targets, ", "))
		if r.Auth == "missing" && r.AuthCmd != "" {
			fmt.Printf("    sign in: %s\n", r.AuthCmd)
		}
		if r.Note != "" {
			fmt.Printf("    %s\n", r.Note)
		}
	}
}

// installError maps installer errors onto the documented exit codes.
func installError(err error) int {
	var pathErr *host.PathError
	var modErr *host.ErrInstallModified
	switch {
	case errors.Is(err, host.ErrNoHostFound):
		fmt.Fprintf(os.Stderr, "plan-forge install: %v\nSupported hosts: claude, omp, agy. Pass --host to force one.\n", err)
		return exitPrerequisite
	case errors.As(err, &pathErr):
		fmt.Fprintf(os.Stderr, "plan-forge install: %v\n", err)
		return exitInstallRefused
	case errors.As(err, &modErr):
		fmt.Fprintf(os.Stderr, "plan-forge install: %v (re-run with --force to overwrite)\n", err)
		return exitInstallModified
	default:
		fmt.Fprintf(os.Stderr, "plan-forge install: %v\n", err)
		return exitRuntimeError
	}
}

// padRight pads s to at least width spaces.
func padRight(s string, width int) string {
	for len(s) < width {
		s += " "
	}
	return s
}

// bothTTYs reports whether stdin and stdout are both terminals.
func bothTTYs() bool {
	return isTTY(os.Stdin) && isTTY(os.Stdout)
}

func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
