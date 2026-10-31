//go:build client && !server

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

func TestOMPOverlayPreservesEffectiveDisabledProviders(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	globalDir := filepath.Join(home, ".omp", "agent")
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "config.yml"), []byte("disabledProviders:\n  - anthropic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(repo, ".omp")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "config.yml"), []byte("disabledProviders: [openai, custom-source]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	overlay, err := buildOMPOverlay(repo)
	if err != nil {
		t.Fatal(err)
	}
	text := string(overlay)
	for _, required := range append(append([]string(nil), mandatoryOMPDisabledProviders...), "openai", "custom-source") {
		if !strings.Contains(text, `"`+required+`"`) {
			t.Fatalf("overlay omitted %q:\n%s", required, text)
		}
	}
	if strings.Contains(text, `"anthropic"`) {
		t.Fatalf("project disabledProviders must replace the global array:\n%s", text)
	}
	if !strings.Contains(text, "mcp:\n  enableProjectConfig: false\n") {
		t.Fatalf("overlay omitted MCP project-config gate:\n%s", text)
	}
}

// With the isolated profile provisioned but no agy binary on PATH, generation
// still fails closed and the audit receipt records the attempted invocation.
func TestAGYGenerateReturnsAuditReceiptWhenBinaryIsUnavailable(t *testing.T) {
	t.Setenv("PLAN_FORGE_HOME", t.TempDir())
	dir := t.TempDir()
	stubPath(t, dir)
	adapter := &AGY{binary: "agy"}
	result, err := adapter.Generate(context.Background(), contracts.GenerateRequest{Model: "model-x"}, nil)
	if err == nil {
		t.Fatal("expected AGY generation to fail closed")
	}
	if result.Receipt.Host != string(KindAGY) || result.Receipt.Version != "" {
		t.Fatalf("unexpected receipt identity: %+v", result.Receipt)
	}
	joined := strings.Join(result.Receipt.Argv, " ")
	if !strings.Contains(joined, "--model model-x") || !strings.Contains(joined, "--json-schema <stage.schema.json>") {
		t.Fatalf("receipt omitted attempted invocation: %+v", result.Receipt.Argv)
	}
}
