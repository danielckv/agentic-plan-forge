//go:build client && !server

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/app"
	"github.com/danielckv/agentic-plan-forge/internal/config"
)

func init() {
	register("center", "command center: serve the live dashboard, or ensure it is running", runCenter)
	register("__center-child", "hidden: detached command center daemon", runCenterChild)
}

// The subcommand is `center`, not `ui`: `serve-ui` is already the hidden
// release smoke test, and `open` already means "print the approved spec".
const centerUsage = "center takes one subcommand: serve, ensure, status or report"

func runCenter(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, centerUsage)
		return exitUsage
	}
	switch args[0] {
	case "serve":
		return runCenterServe(ctx, args[1:])
	case "ensure":
		return runCenterEnsure(ctx, args[1:])
	case "status":
		return runCenterStatus(ctx, args[1:])
	case "report":
		return runCenterReport(ctx, args[1:])
	}
	fmt.Fprintln(os.Stderr, centerUsage)
	return exitUsage
}

// centerPort resolves the port: the flag wins, then PLAN_FORGE_CENTER_PORT,
// then the config, then the default. A value that is not a usable port is an
// error rather than a silent fallback, because a command center that moves is
// a bookmark that breaks.
func centerPort(flagPort int) (int, error) {
	if flagPort != 0 {
		return flagPort, nil
	}
	if raw := os.Getenv("PLAN_FORGE_CENTER_PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1024 || port > 65535 {
			return 0, fmt.Errorf("PLAN_FORGE_CENTER_PORT must be a port between 1024 and 65535, got %q", raw)
		}
		return port, nil
	}
	cfg, err := loadConfig()
	if err != nil {
		return 0, err
	}
	return cfg.CenterPortOrDefault(), nil
}

func runCenterServe(ctx context.Context, args []string) int {
	var portFlag int
	if _, err := parseFlags("center serve", args, func(fs *flag.FlagSet) {
		fs.IntVar(&portFlag, "port", 0, "loopback port to bind (default: center_port, else "+strconv.Itoa(config.DefaultCenterPort)+")")
	}); err != nil {
		return exitUsage
	}
	port, err := centerPort(portFlag)
	if err != nil {
		return fail(err)
	}
	res := app.RunCenter(ctx, app.CenterOptions{Port: port})
	if res.AlreadyUp {
		fmt.Fprintf(os.Stderr, "center: already running on %s (pid %d)\n", res.URL, res.PID)
		return exitOK
	}
	if res.ExitCode != exitOK {
		fmt.Fprintln(os.Stderr, "center: "+res.Message)
	}
	return res.ExitCode
}

// runCenterChild is the detached daemon. It is hidden because only `center
// ensure` starts it, and it differs from `center serve` only in being quiet.
func runCenterChild(ctx context.Context, args []string) int {
	var portFlag int
	if _, err := parseFlags("__center-child", args, func(fs *flag.FlagSet) {
		fs.IntVar(&portFlag, "port", 0, "loopback port to bind")
	}); err != nil {
		return exitUsage
	}
	port, err := centerPort(portFlag)
	if err != nil {
		return exitRuntimeError
	}
	return app.RunCenter(ctx, app.CenterOptions{Port: port}).ExitCode
}

// runCenterEnsure starts the daemon if it is down and opens one tab if none is
// already on screen. It is called from session-start hooks, so it always exits
// 0: a dashboard that failed to open must never be the reason an agent stops.
func runCenterEnsure(ctx context.Context, args []string) int {
	var (
		repoFlag string
		portFlag int
		open     bool
		asJSON   bool
	)
	if _, err := parseFlags("center ensure", args, func(fs *flag.FlagSet) {
		fs.StringVar(&repoFlag, "repo", "", "repository path to focus (default: none)")
		fs.IntVar(&portFlag, "port", 0, "loopback port to bind")
		fs.BoolVar(&open, "open", false, "open a browser tab when none is connected")
		fs.BoolVar(&asJSON, "json", false, "print the outcome as JSON")
	}); err != nil {
		return exitOK
	}
	port, err := centerPort(portFlag)
	if err != nil {
		return exitOK
	}
	cfg, cfgErr := loadConfig()
	if cfgErr == nil && !cfg.CenterAutoOpenEnabled() {
		open = false
	}
	res := app.EnsureCenter(ctx, app.CenterOptions{Port: port, Repo: repoFlag, Open: open})
	if asJSON {
		state := "started"
		switch {
		case res.Opened:
			state = "opened"
		case res.ActiveClients > 0:
			state = "already_open"
		case res.AlreadyUp:
			state = "already_running"
		}
		if res.PID == 0 {
			state = "unavailable"
		}
		out, _ := json.Marshal(map[string]any{
			"center":         state,
			"pid":            res.PID,
			"port":           res.Port,
			"url":            res.URL,
			"active_clients": res.ActiveClients,
		})
		fmt.Println(string(out))
	}
	return exitOK
}

func runCenterStatus(ctx context.Context, args []string) int {
	var asJSON bool
	if _, err := parseFlags("center status", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&asJSON, "json", false, "print the status as JSON")
	}); err != nil {
		return exitUsage
	}
	rec, up := app.CenterHolder()
	if asJSON {
		out, _ := json.Marshal(map[string]any{
			"running": up, "pid": rec.PID, "port": rec.Port,
		})
		fmt.Println(string(out))
		return exitOK
	}
	if !up {
		fmt.Println("command center: not running")
		return exitOK
	}
	fmt.Printf("command center: running on http://127.0.0.1:%d (pid %d)\n", rec.Port, rec.PID)
	return exitOK
}

// centerReportBound keeps a report well inside the budget a host hook allows.
// It is deliberately tighter than the hook's own bound: telemetry is the least
// important thing happening in that process.
const centerReportBound = 400 * time.Millisecond

// runCenterReport posts one telemetry record. It always exits 0 and prints
// nothing: it runs inside host hooks, where a non-zero exit or stray output
// would surface to the user as a failure of whatever they were actually doing.
func runCenterReport(ctx context.Context, args []string) int {
	var (
		rec   app.CenterRecord
		dirty string
	)
	if _, err := parseFlags("center report", args, func(fs *flag.FlagSet) {
		fs.StringVar(&rec.Repo, "repo", "", "repository path")
		fs.StringVar(&rec.SessionID, "session", "", "harness-qualified session id, e.g. claude:abc")
		fs.StringVar(&rec.Harness, "harness", "", "harness name, e.g. claude-code or omp")
		fs.StringVar(&rec.Developer, "developer", "", "developer handle")
		fs.StringVar(&rec.Branch, "branch", "", "git branch")
		fs.StringVar(&rec.Mode, "mode", "", "PLANNED or UNPLANNED")
		fs.StringVar(&rec.TaskID, "task", "", "planned task id")
		fs.StringVar(&rec.Tool, "tool", "", "tool name")
		fs.StringVar(&rec.Command, "command", "", "command line")
		fs.StringVar(&rec.Kind, "kind", "", "event kind: session_start, tool, command, untracked, idle or session_end")
		fs.StringVar(&rec.Detail, "detail", "", "short description")
		fs.StringVar(&dirty, "dirty", "", "comma-separated repository-relative paths")
	}); err != nil {
		return exitOK
	}
	for _, p := range strings.Split(dirty, ",") {
		if p = strings.TrimSpace(p); p != "" {
			rec.DirtyFiles = append(rec.DirtyFiles, p)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, centerReportBound)
	defer cancel()
	_ = app.CenterReport(ctx, rec)
	return exitOK
}
