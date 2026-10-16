//go:build client && !server

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

const minimumClaudeVersion = "2.1.259"

var semanticVersionPattern = regexp.MustCompile(`(?:^|[^0-9])v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:[^0-9]|$)`)

// Claude runs Claude Code in safe, tool-free print mode.
type Claude struct {
	binary   string
	extraEnv []string
}

// NewClaude constructs the production Claude Code adapter.
func NewClaude() Adapter { return &Claude{binary: "claude"} }

func (a *Claude) Kind() Kind { return KindClaude }

// EnvAllow lists Claude credential and configuration families inherited by a
// child. Values remain in the child environment and never enter receipts.
func (a *Claude) EnvAllow() []string {
	allowed := []string{"ANTHROPIC_*", "CLAUDE_CONFIG_DIR", "CLAUDE_CODE_USE_*"}
	return append(allowed, a.extraEnv...)
}

func (a *Claude) withEnvAllow(names []string) Adapter {
	clone := *a
	clone.extraEnv = append([]string(nil), names...)
	return &clone
}

func (a *Claude) Detect(ctx context.Context) (string, error) {
	stdout, _, err := runProbe(ctx, a, a.binary, "--version")
	if err != nil {
		return "", err
	}
	version := extractSemanticVersion(string(stdout))
	if version == "" {
		return "", fmt.Errorf("claude --version returned no semantic version")
	}
	return version, nil
}

func (a *Claude) Auth(ctx context.Context) (AuthState, error) {
	_, _, err := runProbe(ctx, a, a.binary, "auth", "status")
	if err != nil {
		return AuthState{Ready: false, Method: "claude auth status", Detail: "run: claude auth login"}, nil
	}
	return AuthState{Ready: true, Method: "claude auth status", Detail: "ready"}, nil
}

func (a *Claude) Models(context.Context) ([]ModelRef, error) {
	return []ModelRef{
		{ID: "haiku", Label: "Claude Haiku"},
		{ID: "opus", Label: "Claude Opus"},
		{ID: "sonnet", Label: "Claude Sonnet"},
	}, nil
}

func (a *Claude) Preflight(ctx context.Context) error {
	version, err := a.Detect(ctx)
	if err != nil {
		return err
	}
	if compareSemanticVersions(version, minimumClaudeVersion) < 0 {
		return &contracts.ErrCapability{
			Host:   string(KindClaude),
			Reason: "Claude Code " + version + " is older than " + minimumClaudeVersion,
			Remedy: "run: claude update",
		}
	}
	probe := contracts.GenerateRequest{
		Role:         contracts.RoleProductManager,
		ModelSlot:    contracts.SlotMain,
		SystemPrompt: "This is a capability probe. Reply exactly OK.",
		UserPayload:  json.RawMessage(`"Reply OK"`),
		Timeout:      30 * time.Second,
		MaxOutput:    1024,
	}
	_, stderr, err := a.generate(ctx, probe, nil, false)
	if err == nil {
		return nil
	}
	message := strings.ToLower(stderr + " " + err.Error())
	if strings.Contains(message, "unknown option") || strings.Contains(message, "unknown flag") || strings.Contains(message, "unrecognized option") {
		return &contracts.ErrCapability{
			Host:   string(KindClaude),
			Reason: "installed Claude Code lacks required safe-mode or permission flags",
			Remedy: "run: claude update",
		}
	}
	return err
}

func (a *Claude) Generate(ctx context.Context, req contracts.GenerateRequest, emit func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	result, _, err := a.generate(ctx, req, emit, true)
	return result, err
}

func (a *Claude) generate(
	ctx context.Context,
	req contracts.GenerateRequest,
	emit func(contracts.GenerationEvent) error,
	validate bool,
) (contracts.GenerationResult, string, error) {
	version, err := a.Detect(ctx)
	result := contracts.GenerationResult{Receipt: contracts.ToolFreeReceipt{
		Host:    string(KindClaude),
		Version: version,
		Argv:    claudeReceiptArgs(req.Model),
	}}
	if err != nil {
		return result, "", err
	}
	if compareSemanticVersions(version, minimumClaudeVersion) < 0 {
		return result, "", &contracts.ErrCapability{Host: string(KindClaude), Reason: "Claude Code is too old", Remedy: "run: claude update"}
	}

	dir, err := newRunDir()
	if err != nil {
		return result, "", err
	}
	defer os.RemoveAll(dir)
	rolePath := filepath.Join(dir, "role.txt")
	if err := writePrivate(rolePath, []byte(req.SystemPrompt)); err != nil {
		return result, "", fmt.Errorf("write Claude role prompt: %w", err)
	}

	actualArgs := claudeArgs(rolePath, req.Model)
	executable, err := lookPath(a.binary)
	if err != nil {
		return result, "", err
	}

	callCtx, cancel := context.WithTimeout(ctx, req.ClampTimeout())
	defer cancel()
	var streamed bytes.Buffer
	var terminal string
	var usage contracts.Usage
	seenInit := false
	seenTerminal := false

	stderr, err := runNDJSON(callCtx, executable, actualArgs, dir, childEnvironment(dir, a, a.extraEnv), promptBytes(req), func(event map[string]any) error {
		if err := inspectToolFree(KindClaude, event, &result.Receipt); err != nil {
			return err
		}
		typeName := eventType(event)
		if typeName == "system" && stringField(event, "subtype") == "init" {
			if err := requireEmptyArray(event, "tools"); err != nil {
				return &contracts.ErrCapability{Host: string(KindClaude), Reason: err.Error(), Remedy: "run: claude update"}
			}
			if err := requireEmptyArray(event, "mcp_servers"); err != nil {
				return &contracts.ErrCapability{Host: string(KindClaude), Reason: err.Error(), Remedy: "run: claude update"}
			}
			seenInit = true
			return nil
		}

		switch typeName {
		case "stream_event":
			if !seenInit {
				return &contracts.ErrCapability{Host: string(KindClaude), Reason: "output arrived before the tool-free init event", Remedy: "run: claude update"}
			}
			providerEvent := nestedMap(event, "event")
			if eventType(providerEvent) != "content_block_delta" {
				return nil
			}
			delta := nestedMap(providerEvent, "delta")
			if eventType(delta) != "text_delta" {
				return nil
			}
			text := stringField(delta, "text")
			if err := appendBounded(&streamed, text, req.ClampMaxOutput()); err != nil {
				return err
			}
			return emitEvent(emit, contracts.GenerationEvent{Kind: contracts.EventTextDelta, Text: text})
		case "result":
			if !seenInit {
				return &contracts.ErrCapability{Host: string(KindClaude), Reason: "terminal result arrived without a tool-free init event", Remedy: "run: claude update"}
			}
			if failed, _ := event["is_error"].(bool); failed || stringField(event, "subtype") == "error" {
				return fmt.Errorf("Claude generation failed")
			}
			terminal = terminalValue(event["result"])
			if terminal == "" {
				terminal = streamed.String()
			}
			mergeUsage(&usage, usageFromEvent(event))
			seenTerminal = true
		case "error":
			return fmt.Errorf("Claude stream reported an error")
		}
		return nil
	})
	if err != nil {
		result.Usage = usage
		return result, stderr, err
	}
	if !seenInit {
		return result, stderr, &contracts.ErrCapability{Host: string(KindClaude), Reason: "stream omitted the tool-free init event", Remedy: "run: claude update"}
	}
	if !seenTerminal {
		return result, stderr, fmt.Errorf("Claude exited without a terminal result event")
	}
	if !validate {
		req.SchemaID = ""
	}
	result, err = finalizeGeneration(req, terminal, result.Receipt, usage, emit)
	return result, stderr, err
}

func claudeArgs(rolePath, model string) []string {
	args := []string{
		"-p",
		"--safe-mode",
		"--tools", "",
		"--disallowedTools", "*",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
		"--no-session-persistence",
		"--permission-prompts", "none",
		"--system-prompt-file", rolePath,
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args
}

func claudeReceiptArgs(model string) []string {
	args := claudeArgs("<role.txt>", model)
	return append([]string{"claude"}, args...)
}

func terminalValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if value == nil {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func extractSemanticVersion(output string) string {
	match := semanticVersionPattern.FindStringSubmatch(output)
	if len(match) != 4 {
		return ""
	}
	return match[1] + "." + match[2] + "." + match[3]
}

func compareSemanticVersions(left, right string) int {
	parse := func(value string) [3]int {
		match := semanticVersionPattern.FindStringSubmatch(value)
		var parts [3]int
		if len(match) == 4 {
			for i := range parts {
				parts[i], _ = strconv.Atoi(match[i+1])
			}
		}
		return parts
	}
	l, r := parse(left), parse(right)
	for i := range l {
		if l[i] < r[i] {
			return -1
		}
		if l[i] > r[i] {
			return 1
		}
	}
	return 0
}
