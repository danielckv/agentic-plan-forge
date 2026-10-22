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

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/gather"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
)

func init() {
	register("context", "print what the repository's notes, specs and decisions say about a topic", runContext)
}

// runContext implements `plan-forge context --repo PATH --query TEXT [--json]`:
// the same project context the technical avatars receive, for the agent to read
// before it interviews the user. It is read-only and needs no model.
func runContext(ctx context.Context, args []string) int {
	var repoFlag, query string
	var asJSON bool
	fs, err := parseFlags("context", args, func(fs *flag.FlagSet) {
		fs.StringVar(&repoFlag, "repo", "", "repository path (default: cwd's repository root)")
		fs.StringVar(&query, "query", "", "the topic or requirements text to match")
		fs.BoolVar(&asJSON, "json", false, "print JSON instead of Markdown")
	})
	if err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "context takes no arguments; pass the topic with --query")
		return exitUsage
	}
	abs, code := resolveRepoArg(repoFlag)
	if code != exitOK {
		return code
	}
	repo := abs
	if root, err := ast.RepoRoot(ctx, abs); err == nil {
		repo = root
	}
	pc, err := gather.Collect(ctx, gather.Options{Repo: repo, Text: query})
	if err != nil {
		fmt.Fprintf(os.Stderr, "context: %v\n", err)
		return exitRuntimeError
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(pc); err != nil {
			return exitRuntimeError
		}
		return exitOK
	}
	writeContextMarkdown(os.Stdout, pc)
	return exitOK
}

// writeContextMarkdown renders the context for an agent transcript. Every
// string from the repository passes through sanitize.Terminal, because notes
// are untrusted text.
func writeContextMarkdown(w io.Writer, pc contracts.ProjectContext) {
	clean := func(s string) string { return sanitize.Terminal(s) }
	fmt.Fprintln(w, "# Project context")
	fmt.Fprintln(w)
	if pc.Branch != "" {
		fmt.Fprintf(w, "Branch `%s` at `%s`.\n\n", clean(pc.Branch), clean(pc.Head))
	}
	if len(pc.Recent) > 0 {
		fmt.Fprintln(w, "## Recent commits")
		fmt.Fprintln(w)
		for _, s := range pc.Recent {
			fmt.Fprintf(w, "- %s\n", clean(s))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "## Related notes (source: %s)\n\n", pc.Source)
	if len(pc.Hits) == 0 {
		fmt.Fprintln(w, "None found.")
	}
	for _, h := range pc.Hits {
		title := h.Path
		if h.Heading != "" {
			title += " — " + h.Heading
		}
		fmt.Fprintf(w, "### %s\n\n", clean(title))
		for _, line := range strings.Split(clean(h.Text), "\n") {
			fmt.Fprintf(w, "> %s\n", line)
		}
		fmt.Fprintln(w)
	}
	for _, n := range pc.Notes {
		fmt.Fprintf(w, "Note: %s\n", clean(n))
	}
	if pc.Truncated {
		fmt.Fprintln(w, "Note: the output was cut to stay within its size bound.")
	}
}
