//go:build client && !server

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/planstate"
)

func init() {
	register("validate", "check the approved plan against the repository (exit 6 when invalid)", runValidate)
}

// runValidate implements `plan-forge validate [--repo PATH] [--json] [--timeout 30s]`.
// Exit 0 valid, 6 invalid, 1 runtime error, 2 usage error.
func runValidate(ctx context.Context, args []string) int {
	var (
		repoFlag   string
		jsonOut    bool
		timeoutRaw string
	)
	fs, err := parseFlags("validate", args, func(fs *flag.FlagSet) {
		fs.StringVar(&repoFlag, "repo", "", "repository path (default: cwd's repository root)")
		fs.BoolVar(&jsonOut, "json", false, "print one JSON object")
		fs.StringVar(&timeoutRaw, "timeout", "30s", "bound for the repository scan (for example: 10s, 1m)")
	})
	if err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "validate takes no arguments")
		return exitUsage
	}
	timeout, err := time.ParseDuration(timeoutRaw)
	if err != nil || timeout <= 0 {
		fmt.Fprintf(os.Stderr, "validate: invalid --timeout %q\n", timeoutRaw)
		return exitUsage
	}
	repo, code := resolveRepoArg(repoFlag)
	if code != exitOK {
		return code
	}
	v, err := planstate.Validate(ctx, repo, planstate.CheckOptions{ScanTimeout: timeout})
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate: %v\n", err)
		return exitRuntimeError
	}
	if jsonOut {
		raw, err := json.Marshal(v)
		if err != nil {
			fmt.Fprintf(os.Stderr, "validate: %v\n", err)
			return exitRuntimeError
		}
		fmt.Println(string(raw))
	} else {
		writeValidationText(os.Stdout, v)
	}
	if !v.Valid {
		return exitValidationFailed
	}
	return exitOK
}

const maxPendingShown = 10

func writeValidationText(w io.Writer, v planstate.Validation) {
	verdict := "INVALID"
	if v.Valid {
		verdict = "VALID"
	}
	fmt.Fprintf(w, "Plan validation: %s — %s · %d/%d AST targets present · %d tasks\n", verdict, v.Status, v.Verified, v.Total, v.Tasks)
	fmt.Fprintf(w, "Spec: %s\n", v.SpecPath)
	if len(v.Drift) == 0 {
		fmt.Fprintln(w, "Drift: (none)")
	} else {
		parts := make([]string, 0, len(v.Drift))
		for _, d := range v.Drift {
			parts = append(parts, d.Code+": "+d.Detail)
		}
		fmt.Fprintf(w, "Drift: %s\n", strings.Join(parts, "; "))
	}
	if len(v.Pending) == 0 {
		fmt.Fprintln(w, "Pending verification: (none)")
	} else {
		shown := v.Pending
		extra := ""
		if len(shown) > maxPendingShown {
			extra = fmt.Sprintf(" (+%d more)", len(shown)-maxPendingShown)
			shown = shown[:maxPendingShown]
		}
		fmt.Fprintf(w, "Pending verification: %s%s\n", strings.Join(shown, "; "), extra)
	}
	for _, n := range v.Notes {
		fmt.Fprintf(w, "Note: %s\n", n)
	}
}
