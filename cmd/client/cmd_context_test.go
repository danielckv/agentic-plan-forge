//go:build client && !server

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

func TestContextRefusesPositionalArguments(t *testing.T) {
	if code := runContext(context.Background(), []string{"topic"}); code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
}

// Notes are untrusted repository text: control sequences must not reach the
// agent's terminal, and the Markdown must stay readable.
func TestContextMarkdownIsReadableAndSanitized(t *testing.T) {
	var out bytes.Buffer
	writeContextMarkdown(&out, contracts.ProjectContext{
		Source: contracts.ContextSourceFiles, Branch: "main", Head: "0123456789ab",
		Recent: []string{"Add invoice export"},
		Hits: []contracts.ContextHit{{
			Path: "docs/adrs/0002-csv.md", Heading: "Decision", Text: "Invoices export as CSV.\n\x1b[31mred\x1b[0m", Score: 2,
		}},
		Truncated: true,
	})
	got := out.String()
	for _, want := range []string{
		"Branch `main` at `0123456789ab`.", "- Add invoice export", "## Related notes (source: files)",
		"### docs/adrs/0002-csv.md — Decision", "> Invoices export as CSV.", "cut to stay within its size bound",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("an escape sequence reached the output: %q", got)
	}
}
