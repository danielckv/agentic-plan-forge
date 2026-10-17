//go:build client && !server

package main

import (
	"context"
	"testing"
)

func TestRunHookSubcommandBeforeAndAfterFlags(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PLAN_FORGE_HOME", dir)

	// Subcommand before flags — the exact shape invoked by Claude SessionStart
	// and OMP hooks: `plan-forge hook check-state --repo <dir> --format claude`.
	code := runHook(context.Background(), []string{"check-state", "--repo", dir, "--format", "claude"})
	if code != exitOK {
		t.Fatalf("runHook(check-state before flags) = %d, want %d", code, exitOK)
	}

	// Flags before subcommand.
	code = runHook(context.Background(), []string{"--repo", dir, "--format", "claude", "check-state"})
	if code != exitOK {
		t.Fatalf("runHook(flags before check-state) = %d, want %d", code, exitOK)
	}

	// Extra positional argument must still return exitUsage.
	code = runHook(context.Background(), []string{"check-state", "extra", "--repo", dir})
	if code != exitUsage {
		t.Fatalf("runHook(extra arg) = %d, want %d", code, exitUsage)
	}
}
