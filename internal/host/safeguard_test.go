//go:build client && !server

package host

import (
	"encoding/json"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// claudeSafeguardDelta is the message_delta Claude Code 2.1.312 sends at the
// end of a clean tool-free call, captured from a real probe on 2026-10-29.
// The safeguard verdict is named after the check, and its tool_uses map is
// empty: no tool ran.
const claudeSafeguardDelta = `{
	"type": "stream_event",
	"event": {
		"type": "message_delta",
		"delta": {
			"stop_reason": "end_turn",
			"safeguard_results": [
				{"type": "dangerous_tool_use", "status": {"type": "available", "tool_uses": {}}}
			]
		},
		"usage": {"input_tokens": 2, "output_tokens": 4}
	},
	"parent_tool_use_id": null
}`

func decodeEvent(t *testing.T, raw string) map[string]any {
	t.Helper()
	var event map[string]any
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	return event
}

// A safeguard verdict reporting no tool use must not discard the generation.
// Reading the check's name as a tool call made every Claude call fail and left
// `doctor` saying serves: NO for a host that used no tools.
func TestSafeguardVerdictWithNoToolUseIsClean(t *testing.T) {
	var receipt contracts.ToolFreeReceipt
	if err := inspectToolFree(KindClaude, decodeEvent(t, claudeSafeguardDelta), &receipt); err != nil {
		t.Fatalf("clean safeguard verdict rejected: %v", err)
	}
	if len(receipt.Observed) != 0 {
		t.Errorf("receipt observed %v, want nothing", receipt.Observed)
	}
}

// The safeguard verdict is still read: a classifier that names a tool must
// stop the call exactly as a tool_use content block does.
func TestSafeguardVerdictNamingAToolIsRejected(t *testing.T) {
	raw := `{"type":"stream_event","event":{"type":"message_delta","delta":{
		"safeguard_results":[{"type":"dangerous_tool_use",
		"status":{"type":"available","tool_uses":{"Bash":{"count":1}}}}]}}}`

	var receipt contracts.ToolFreeReceipt
	err := inspectToolFree(KindClaude, decodeEvent(t, raw), &receipt)
	if err == nil {
		t.Fatal("a safeguard verdict naming a tool must discard the generation")
	}
	if len(receipt.Observed) != 1 || receipt.Observed[0] != "Bash" {
		t.Errorf("receipt observed %v, want [Bash]", receipt.Observed)
	}
}

// Real tool events must still be caught, including the ones whose type only
// ends in the tool token.
func TestRealToolEventsAreStillRejected(t *testing.T) {
	for _, raw := range []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"server_tool_use","name":"web_search"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"mcp_tool_use","name":"x"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"out"}]}}`,
		`{"type":"toolCall","name":"Bash"}`,
	} {
		if err := inspectToolFree(KindClaude, decodeEvent(t, raw), nil); err == nil {
			t.Errorf("tool event not rejected: %s", raw)
		}
	}
}
