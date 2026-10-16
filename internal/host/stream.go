//go:build client && !server

package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

var toolEventPattern = regexp.MustCompile(`tool(use|call|execution|result)`)

func inspectToolFree(host Kind, event map[string]any, receipt *contracts.ToolFreeReceipt) error {
	observed := findToolEvent(event, 0)
	if observed == "" {
		return nil
	}
	if receipt != nil {
		receipt.Observed = append(receipt.Observed, observed)
	}
	return &contracts.ErrToolUseObserved{Host: string(host), Event: observed}
}

func findToolEvent(value any, depth int) string {
	if depth > 8 {
		return ""
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if strings.EqualFold(key, "type") || strings.EqualFold(key, "subtype") {
				if text, ok := child.(string); ok {
					normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(text))
					if toolEventPattern.MatchString(normalized) {
						return text
					}
				}
			}
			if found := findToolEvent(child, depth+1); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findToolEvent(child, depth+1); found != "" {
				return found
			}
		}
	}
	return ""
}

func requireEmptyArray(event map[string]any, field string) error {
	value, ok := event[field]
	if !ok {
		return fmt.Errorf("required %s field is missing", field)
	}
	items, ok := value.([]any)
	if !ok || len(items) != 0 {
		return fmt.Errorf("%s must be an empty array", field)
	}
	return nil
}

func rejectAdvertisedTools(value any, depth int) error {
	if depth > 8 {
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if strings.EqualFold(key, "tools") {
				items, ok := child.([]any)
				if !ok || len(items) != 0 {
					return errors.New("host advertised a non-empty or invalid tools list")
				}
			}
			if err := rejectAdvertisedTools(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := rejectAdvertisedTools(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func eventType(event map[string]any) string {
	value, _ := event["type"].(string)
	return value
}

func nestedMap(parent map[string]any, key string) map[string]any {
	value, _ := parent[key].(map[string]any)
	return value
}

func stringField(parent map[string]any, key string) string {
	value, _ := parent[key].(string)
	return value
}

func appendBounded(buffer *bytes.Buffer, text string, limit int) error {
	if text == "" {
		return nil
	}
	if buffer.Len()+len(text) > limit {
		return &contracts.ErrBound{Name: "generation output", Limit: int64(limit), Observed: int64(buffer.Len() + len(text))}
	}
	_, _ = buffer.WriteString(text)
	return nil
}

func emitEvent(emit func(contracts.GenerationEvent) error, event contracts.GenerationEvent) error {
	if emit == nil {
		return nil
	}
	return emit(event)
}

func contentText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var result strings.Builder
		for _, item := range typed {
			result.WriteString(contentText(item))
		}
		return result.String()
	case map[string]any:
		kind := strings.ToLower(stringField(typed, "type"))
		if strings.Contains(kind, "thinking") || strings.Contains(kind, "reasoning") {
			return ""
		}
		if text := stringField(typed, "text"); text != "" {
			return text
		}
		if delta := stringField(typed, "delta"); delta != "" {
			return delta
		}
		if content, ok := typed["content"]; ok {
			return contentText(content)
		}
	}
	return ""
}

func usageFromEvent(event map[string]any) contracts.Usage {
	usage := nestedMap(event, "usage")
	if usage == nil {
		if message := nestedMap(event, "message"); message != nil {
			usage = nestedMap(message, "usage")
		}
	}
	return contracts.Usage{
		InputTokens:  integerField(usage, "input_tokens", "inputTokens", "input"),
		OutputTokens: integerField(usage, "output_tokens", "outputTokens", "output"),
	}
}

func integerField(parent map[string]any, keys ...string) int {
	for _, key := range keys {
		value, ok := parent[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case json.Number:
			parsed, err := typed.Int64()
			if err == nil && parsed >= 0 {
				return int(parsed)
			}
		case float64:
			if typed >= 0 {
				return int(typed)
			}
		}
	}
	return 0
}

func mergeUsage(current *contracts.Usage, next contracts.Usage) {
	if next.InputTokens != 0 {
		current.InputTokens = next.InputTokens
	}
	if next.OutputTokens != 0 {
		current.OutputTokens = next.OutputTokens
	}
}

func finalizeGeneration(
	req contracts.GenerateRequest,
	content string,
	receipt contracts.ToolFreeReceipt,
	usage contracts.Usage,
	emit func(contracts.GenerationEvent) error,
) (contracts.GenerationResult, error) {
	result := contracts.GenerationResult{Receipt: receipt, Usage: usage}
	raw := bytes.TrimSpace([]byte(content))
	if len(raw) == 0 {
		return result, errors.New("host exited without a terminal result")
	}
	if len(raw) > req.ClampMaxOutput() {
		return result, &contracts.ErrBound{Name: "generation output", Limit: int64(req.ClampMaxOutput()), Observed: int64(len(raw))}
	}
	if req.SchemaID != "" {
		if err := contracts.Validate(req.SchemaID, raw); err != nil {
			return result, fmt.Errorf("validate %s host output: %w", req.SchemaID, err)
		}
	}
	result.Content = append(json.RawMessage(nil), raw...)
	if usage.InputTokens != 0 || usage.OutputTokens != 0 {
		if err := emitEvent(emit, contracts.GenerationEvent{Kind: contracts.EventUsage, Usage: &usage}); err != nil {
			return result, err
		}
	}
	if err := emitEvent(emit, contracts.GenerationEvent{Kind: contracts.EventFinal, Text: string(raw)}); err != nil {
		return result, err
	}
	return result, nil
}

func promptBytes(req contracts.GenerateRequest) []byte {
	if len(req.UserPayload) == 0 {
		return []byte("{}")
	}
	return append([]byte(nil), req.UserPayload...)
}
