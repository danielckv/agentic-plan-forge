//go:build client && !server

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/host"
)

func init() {
	register("uninstall", "remove installed skills and hooks (manifest-owned only; --purge also deletes Plan Forge state)", runUninstall)
}

// runUninstall implements `plan-forge uninstall`. Removal touches only
// manifest-owned files with matching hashes and only our hook entries and
// managed blocks. `--purge` additionally deletes the enumerated children of
// PlanForgeHome() and PlanForgeCache(), guarded by the .plan-forge-home
// marker; repository .plan/ folders are never touched.
func runUninstall(ctx context.Context, args []string) int {
	var hostFlag, scopeFlag, repoFlag string
	var gitHook, dryRun, yes, force, purge bool

	fs, err := parseFlags("uninstall", args, func(fs *flag.FlagSet) {
		fs.StringVar(&hostFlag, "host", "all", "claude|omp|agy|all (which host's artifacts to remove)")
		fs.StringVar(&scopeFlag, "scope", "user", "user|project")
		fs.StringVar(&repoFlag, "repo", "", "repository path (project scope)")
		fs.BoolVar(&gitHook, "git-hook", false, "also remove the post-merge managed block")
		fs.BoolVar(&dryRun, "dry-run", false, "print what would be removed")
		fs.BoolVar(&yes, "yes", false, "never prompt; required on a non-TTY")
		fs.BoolVar(&force, "force", false, "kept for symmetry with install")
		fs.BoolVar(&purge, "purge", false, "additionally delete Plan Forge home and cache state")
	})
	if err != nil {
		return exitUsage
	}
	_ = ctx
	_ = gitHook
	_ = force
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "uninstall takes no arguments")
		return exitUsage
	}
	if !yes && !bothTTYs() {
		fmt.Fprintln(os.Stderr, "uninstall: non-interactive run requires --yes")
		return exitUsage
	}
	if scopeFlag == "project" && repoFlag == "" {
		fmt.Fprintln(os.Stderr, "uninstall: --scope project requires --repo (or run from inside the repository)")
		return exitUsage
	}

	installer, err := host.NewInstaller(version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "uninstall: %v\n", err)
		return exitRuntimeError
	}
	opts := host.InstallOptions{Host: hostFlag, Scope: scopeFlag, Repo: repoFlag, DryRun: dryRun}
	removed, err := installer.Uninstall(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "uninstall: %v\n", err)
		return exitRuntimeError
	}
	for _, line := range removed {
		if dryRun {
			fmt.Printf("would remove: %s\n", line)
		} else {
			fmt.Printf("removed: %s\n", line)
		}
	}
	if dryRun {
		return exitOK
	}

	if purge {
		code := runPurge()
		if code != exitOK {
			return code
		}
	}
	fmt.Println("Done. Delete the binary (~/.local/bin/plan-forge or %LOCALAPPDATA%\\Programs\\plan-forge\\plan-forge.exe) to finish.")
	return exitOK
}

// runPurge deletes the enumerated children of PlanForgeHome() and
// PlanForgeCache(). It refuses with exit 2 unless the .plan-forge-home marker
// exists, and repository .plan/ folders are never touched.
func runPurge() int {
	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "uninstall --purge: %v\n", err)
		return exitRuntimeError
	}
	if err := config.VerifyOwned(home); err != nil {
		fmt.Fprintf(os.Stderr, "uninstall --purge: refusing to delete %s: %v\n", home, err)
		return exitUsage
	}
	targets, err := config.PurgeTargets()
	if err != nil {
		fmt.Fprintf(os.Stderr, "uninstall --purge: %v\n", err)
		return exitRuntimeError
	}
	for _, target := range targets {
		if err := os.RemoveAll(target); err != nil {
			fmt.Fprintf(os.Stderr, "uninstall --purge: %v\n", err)
			return exitRuntimeError
		}
	}
	return exitOK
}
