//go:build client && !server

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/danielckv/agentic-plan-forge/internal/app"
)

func init() {
	// Hidden command: the release smoke test run by scripts/verify-archive.sh.
	register("serve-ui", "hidden: serve the embedded UI and self-check (release smoke test)", runServeUI)
}

// runServeUI implements the hidden `serve-ui --check` subcommand: the
// embedded assets are served on 127.0.0.1:0, "/" and one hashed asset are
// fetched from the server itself, and the command exits 0 only when both
// answer 200. app.CheckUI performs the serve-and-fetch cycle.
func runServeUI(ctx context.Context, args []string) int {
	var check bool
	fs, err := parseFlags("serve-ui", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&check, "check", false, "serve, self-fetch / and one hashed asset, exit 0 on 200s")
	})
	if err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || !check {
		fmt.Fprintln(os.Stderr, "serve-ui is internal; use serve-ui --check")
		return exitUsage
	}
	if err := app.CheckUI(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "serve-ui --check: %v\n", err)
		return exitRuntimeError
	}
	return exitOK
}
