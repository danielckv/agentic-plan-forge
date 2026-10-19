//go:build client && !server

// Command plan-forge is the Plan Forge client: a pre-planning gate that reads
// requirements, grounds them in the repository, runs a staged multi-avatar
// review, and writes an approved specification for the calling agent.
//
// Subcommands live in their own files (cmd_<name>.go) and register themselves
// here, so each implementation step adds one file rather than editing a growing
// switch. A subcommand exists only once its real implementation does: nothing
// registers a placeholder that reports success without doing the work.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Exit codes shared by every subcommand. They are part of the documented
// interface: the installed agent skills branch on them.
const (
	exitOK               = 0
	exitRuntimeError     = 1
	exitUsage            = 2
	exitPrerequisite     = 3
	exitInstallRefused   = 4
	exitInstallModified  = 5
	exitValidationFailed = 6
	exitWaitTimeout      = 75
	exitCancelled        = 130
)

// Build metadata, overridden with -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "unknown"
)

// commandFunc runs one subcommand and returns its process exit code.
type commandFunc func(ctx context.Context, args []string) int

type command struct {
	name    string
	summary string
	run     commandFunc
}

var registry = map[string]command{}

// register adds a subcommand. It panics on a duplicate or empty name, which is
// a programming error rather than a user-facing condition.
func register(name, summary string, run commandFunc) {
	if name == "" || run == nil {
		panic("plan-forge: register called with an empty name or nil handler")
	}
	if _, exists := registry[name]; exists {
		panic("plan-forge: duplicate command " + name)
	}
	registry[name] = command{name: name, summary: summary, run: run}
}

func main() {
	ctx := context.Background()
	args := os.Args[1:]

	if len(args) == 0 {
		usage(os.Stderr)
		os.Exit(exitUsage)
	}
	switch args[0] {
	case "-h", "--help", "help":
		usage(os.Stdout)
		os.Exit(exitOK)
	case "-v", "--version":
		args = []string{"version"}
	}

	cmd, ok := registry[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "plan-forge: unknown command %q\n\n", args[0])
		usage(os.Stderr)
		os.Exit(exitUsage)
	}
	os.Exit(cmd.run(ctx, args[1:]))
}

func usage(w *os.File) {
	fmt.Fprintf(w, "plan-forge %s — multi-avatar pre-planning gate\n\n", version)
	fmt.Fprintln(w, "Usage: plan-forge <command> [flags]")
	fmt.Fprintln(w, "\nCommands:")
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "  %-12s %s\n", name, registry[name].summary)
	}
	fmt.Fprintf(w, "\nRun \"plan-forge <command> --help\" for command-specific flags.\n")
}

// parseFlags parses args into a flag set that never exits the process itself, so
// callers can map parse failures onto the documented exit codes.
func parseFlags(name string, args []string, configure func(*flag.FlagSet)) (*flag.FlagSet, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if configure != nil {
		configure(fs)
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return fs, nil
}

// loadConfig reads the configuration, reporting a failure in the shape the user
// needs: the path and what is wrong with it.
func loadConfig() (config.Config, error) {
	cfg, err := config.Load("")
	if err != nil {
		return config.Config{}, err
	}
	if err := config.Validate(cfg); err != nil {
		return config.Config{}, fmt.Errorf("configuration is invalid: %w", err)
	}
	return cfg, nil
}

// fail prints err to stderr and returns the exit code for it.
func fail(err error) int {
	fmt.Fprintf(os.Stderr, "plan-forge: %v\n", err)
	switch contracts.CodeOf(err) {
	case contracts.CodeRoleForbidden, contracts.CodeIllegalPhase:
		return exitUsage
	default:
		return exitRuntimeError
	}
}

// isHelp reports whether args ask for command help.
func isHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}
