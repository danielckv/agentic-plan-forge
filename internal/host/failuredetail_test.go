//go:build client && !server

package host

import (
	"strings"
	"testing"
)

func TestFailureDetailCarriesTheProviderMessage(t *testing.T) {
	// The shape Claude Code emits when the configured model does not exist on
	// the host: the owner needs the model name, not "generation failed".
	event := map[string]any{
		"type":             "result",
		"subtype":          "success",
		"is_error":         true,
		"result":           "There's an issue with the selected model (claude-opus-5-5-high). It may not exist or you may not have access to it.",
		"api_error_status": 404,
	}
	detail := failureDetail(event)
	if !strings.Contains(detail, "claude-opus-5-5-high") {
		t.Fatalf("detail = %q, want the model name in it", detail)
	}
}

func TestFailureDetailFallsBackAndStaysBounded(t *testing.T) {
	if detail := failureDetail(map[string]any{"error": "upstream refused"}); detail != "upstream refused" {
		t.Errorf("detail = %q, want the error field", detail)
	}
	if detail := failureDetail(map[string]any{}); detail != "the host reported no detail" {
		t.Errorf("detail = %q, want the no-detail notice", detail)
	}
	long := failureDetail(map[string]any{"result": strings.Repeat("x", maxFailureDetail*2)})
	if len([]rune(long)) > maxFailureDetail {
		t.Errorf("detail is %d runes, over the %d bound", len([]rune(long)), maxFailureDetail)
	}
	if detail := failureDetail(map[string]any{"result": "line\x1b[31mred"}); strings.Contains(detail, "\x1b") {
		t.Errorf("detail = %q, want terminal escapes stripped", detail)
	}
}
