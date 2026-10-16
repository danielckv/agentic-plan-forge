//go:build client && !server

package host

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

func TestExactProviderArgv(t *testing.T) {
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{
			name: "claude",
			got:  claudeReceiptArgs("claude-sonnet-4-6"),
			want: []string{
				"claude", "-p", "--safe-mode", "--tools", "", "--disallowedTools", "*",
				"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
				"--no-session-persistence", "--permission-prompts", "none",
				"--system-prompt-file", "<role.txt>", "--output-format", "stream-json",
				"--verbose", "--include-partial-messages", "--model", "claude-sonnet-4-6",
			},
		},
		{
			name: "omp",
			got:  ompReceiptArgs("anthropic/claude-sonnet-4-6"),
			want: []string{
				"omp", "-p", "--mode", "json", "--no-tools", "--no-lsp", "--no-pty",
				"--no-skills", "--no-rules", "--no-extensions", "--no-session", "--no-title",
				"--config", "<overlay.yml>", "--system-prompt", "<role.txt>",
				"--model", "anthropic/claude-sonnet-4-6",
			},
		},
		{
			name: "agy",
			got:  agyReceiptArgs("gemini-3-pro"),
			want: []string{
				"agy", "-p", "--output-format", "stream-json", "--model", "gemini-3-pro",
				"--json-schema", "<stage.schema.json>",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !reflect.DeepEqual(test.got, test.want) {
				t.Fatalf("argv mismatch\n got: %#v\nwant: %#v", test.got, test.want)
			}
			joined := strings.Join(test.got, " ")
			for _, forbidden := range []string{"--bare", "--plan-yolo", "--profile"} {
				if strings.Contains(joined, forbidden) {
					t.Fatalf("argv contains forbidden flag %s", forbidden)
				}
			}
		})
	}
}

func TestToolUseTripwireRejectsStreamSpellings(t *testing.T) {
	streams := []string{
		`{"type":"tool_use"}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"server_tool_use"}}}`,
		`{"type":"stream_event","event":{"content_block":{"type":"mcp_tool_use"}}}`,
		`{"assistantMessageEvent":{"type":"toolcall_start"}}`,
		`{"subtype":"tool_execution_end"}`,
		`{"message":{"content":[{"type":"tool-result"}]}}`,
	}
	for _, stream := range streams {
		event, err := decodeJSONObject([]byte(stream))
		if err != nil {
			t.Fatal(err)
		}
		receipt := contracts.ToolFreeReceipt{Host: string(KindClaude)}
		err = inspectToolFree(KindClaude, event, &receipt)
		var observed *contracts.ErrToolUseObserved
		if !errors.As(err, &observed) {
			t.Fatalf("stream %s was not rejected: %v", stream, err)
		}
		if len(receipt.Observed) != 1 {
			t.Fatalf("receipt did not record observed event for %s", stream)
		}
	}
}

func TestToolUseTripwireAllowsTextEvents(t *testing.T) {
	event, err := decodeJSONObject([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := inspectToolFree(KindClaude, event, &contracts.ToolFreeReceipt{}); err != nil {
		t.Fatalf("text event rejected: %v", err)
	}
}

func TestAGYPreflightFailsWithoutVerifiedIsolation(t *testing.T) {
	adapter := &AGY{binary: "agy"}
	err := adapter.Preflight(context.Background())
	var capability *contracts.ErrCapability
	if !errors.As(err, &capability) {
		t.Fatalf("expected ErrCapability, got %v", err)
	}
	if !strings.Contains(capability.Reason, "isolated settings") {
		t.Fatalf("unexpected capability reason: %s", capability.Reason)
	}
}

func TestRouterSelectionOrderAndJudgeDefault(t *testing.T) {
	configured := &routingAdapter{kind: KindClaude, detectErr: errors.New("missing")}
	invoked := &routingAdapter{kind: "fake", version: "1.0.0", ready: true}
	fallback := &routingAdapter{kind: KindOMP, version: "2.0.0", ready: true}
	router := &Router{
		cfg:         config.Config{MainHost: string(KindClaude), DefaultMainModel: "main-model"},
		invokedHost: "fake",
		adapters: map[Kind]Adapter{
			KindClaude: configured,
			"fake":     invoked,
			KindOMP:     fallback,
		},
		ready: make(map[Kind]resolvedAdapter),
	}
	main, err := router.Resolve(context.Background(), contracts.SlotMain)
	if err != nil {
		t.Fatal(err)
	}
	if main.Kind != "fake" || main.Model != "main-model" {
		t.Fatalf("unexpected main resolution: %+v", main)
	}
	judge, err := router.Resolve(context.Background(), contracts.SlotJudge)
	if err != nil {
		t.Fatal(err)
	}
	if judge.Kind != "fake" || !judge.SharesMain || judge.Model != "main-model" {
		t.Fatalf("unexpected judge resolution: %+v", judge)
	}
	if configured.detectCalls != 1 || invoked.preflightCalls != 1 || fallback.detectCalls != 0 {
		t.Fatalf("unexpected probe counts: configured=%d invoked=%d/%d fallback=%d", configured.detectCalls, invoked.detectCalls, invoked.preflightCalls, fallback.detectCalls)
	}
}

type routingAdapter struct {
	kind           Kind
	version        string
	ready          bool
	detectErr      error
	detectCalls    int
	preflightCalls int
}

func (a *routingAdapter) Kind() Kind { return a.kind }
func (a *routingAdapter) Detect(context.Context) (string, error) {
	a.detectCalls++
	return a.version, a.detectErr
}
func (a *routingAdapter) Auth(context.Context) (AuthState, error) {
	return AuthState{Ready: a.ready}, nil
}
func (a *routingAdapter) Models(context.Context) ([]ModelRef, error) { return nil, nil }
func (a *routingAdapter) Preflight(context.Context) error {
	a.preflightCalls++
	return nil
}
func (a *routingAdapter) Generate(context.Context, contracts.GenerateRequest, func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	return contracts.GenerationResult{Receipt: contracts.ToolFreeReceipt{Host: string(a.kind), Version: a.version}}, nil
}
