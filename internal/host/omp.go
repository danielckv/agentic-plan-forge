//go:build client && !server

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// OMP runs Oh My Pi with every tool, extension, skill, rule, and session source
// disabled for the generation process.
type OMP struct {
	binary   string
	extraEnv []string
}

// NewOMP constructs the production OMP adapter.
func NewOMP() Adapter { return &OMP{binary: "omp"} }

func (a *OMP) Kind() Kind { return KindOMP }

// EnvAllow lists OMP credential and configuration families inherited by a child.
func (a *OMP) EnvAllow() []string {
	allowed := []string{"PI_CODING_AGENT_DIR", "OMP_*"}
	return append(allowed, a.extraEnv...)
}

func (a *OMP) withEnvAllow(names []string) Adapter {
	clone := *a
	clone.extraEnv = append([]string(nil), names...)
	return &clone
}

func (a *OMP) Detect(ctx context.Context) (string, error) {
	stdout, _, err := runProbe(ctx, a, a.binary, "--version")
	if err != nil {
		return "", err
	}
	version := extractSemanticVersion(string(stdout))
	if version == "" {
		return "", fmt.Errorf("omp --version returned no semantic version")
	}
	return version, nil
}

func (a *OMP) Auth(ctx context.Context) (AuthState, error) {
	stdout, _, err := runProbe(ctx, a, a.binary, "usage", "accounts", "--json")
	if err != nil {
		return AuthState{Ready: false, Method: "omp usage accounts --json", Detail: "run: omp login"}, nil
	}
	ready, parseErr := jsonContainsAccount(stdout)
	if parseErr != nil {
		return AuthState{}, fmt.Errorf("decode omp account status: %w", parseErr)
	}
	if !ready {
		return AuthState{Ready: false, Method: "omp usage accounts --json", Detail: "run: omp login"}, nil
	}
	return AuthState{Ready: true, Method: "omp usage accounts --json", Detail: "ready"}, nil
}

func (a *OMP) Models(ctx context.Context) ([]ModelRef, error) {
	help, _, err := runProbe(ctx, a, a.binary, "models", "--help")
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(help), "--json") {
		return nil, nil
	}
	stdout, _, err := runProbe(ctx, a, a.binary, "models", "--json")
	if err != nil {
		return nil, err
	}
	return parseModelRefs(stdout)
}

func (a *OMP) Preflight(ctx context.Context) error {
	if _, err := a.Detect(ctx); err != nil {
		return err
	}
	help, _, err := runProbe(ctx, a, a.binary, "--help")
	if err != nil {
		return err
	}
	text := string(help)
	for _, flag := range []string{
		"--mode", "--no-tools", "--no-lsp", "--no-pty", "--no-skills",
		"--no-rules", "--no-extensions", "--no-session", "--no-title", "--config", "--system-prompt",
	} {
		if !strings.Contains(text, flag) {
			return &contracts.ErrCapability{
				Host:   string(KindOMP),
				Reason: "installed OMP lacks required flag " + flag,
				Remedy: "update OMP",
			}
		}
	}
	return nil
}

func (a *OMP) Generate(ctx context.Context, req contracts.GenerateRequest, emit func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	version, err := a.Detect(ctx)
	result := contracts.GenerationResult{Receipt: contracts.ToolFreeReceipt{
		Host:    string(KindOMP),
		Version: version,
		Argv:    ompReceiptArgs(req.Model),
	}}
	if err != nil {
		return result, err
	}
	parentCWD, err := os.Getwd()
	if err != nil {
		return result, err
	}
	overlay, err := buildOMPOverlay(parentCWD)
	if err != nil {
		return result, fmt.Errorf("build OMP isolation overlay: %w", err)
	}
	overlayDigest := sha256.Sum256(overlay)
	result.Receipt.OverlaySHA = hex.EncodeToString(overlayDigest[:])

	dir, err := newRunDir()
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)
	rolePath := filepath.Join(dir, "role.txt")
	if err := writePrivate(rolePath, []byte(req.SystemPrompt)); err != nil {
		return result, fmt.Errorf("write OMP role prompt: %w", err)
	}
	overlayPath := filepath.Join(dir, "overlay.yml")
	if err := writePrivate(overlayPath, overlay); err != nil {
		return result, fmt.Errorf("write OMP isolation overlay: %w", err)
	}

	actualArgs := ompArgs(overlayPath, rolePath, req.Model)
	executable, err := lookPath(a.binary)
	if err != nil {
		return result, err
	}

	callCtx, cancel := context.WithTimeout(ctx, req.ClampTimeout())
	defer cancel()
	var streamed bytes.Buffer
	var terminal string
	var usage contracts.Usage
	seenTerminal := false

	_, err = runNDJSON(callCtx, executable, actualArgs, dir, childEnvironment(dir, a, a.extraEnv), promptBytes(req), func(event map[string]any) error {
		if err := inspectToolFree(KindOMP, event, &result.Receipt); err != nil {
			return err
		}
		if err := rejectAdvertisedTools(event, 0); err != nil {
			return &contracts.ErrCapability{Host: string(KindOMP), Reason: err.Error(), Remedy: "update OMP"}
		}
		mergeUsage(&usage, usageFromEvent(event))

		switch eventType(event) {
		case "message_update":
			update := nestedMap(event, "assistantMessageEvent")
			if eventType(update) != "text_delta" {
				return nil
			}
			text := stringField(update, "delta")
			if err := appendBounded(&streamed, text, req.ClampMaxOutput()); err != nil {
				return err
			}
			return emitEvent(emit, contracts.GenerationEvent{Kind: contracts.EventTextDelta, Text: text})
		case "message_end":
			message := nestedMap(event, "message")
			if role := stringField(message, "role"); role != "" && role != "assistant" {
				return nil
			}
			terminal = contentText(message["content"])
			if terminal == "" {
				terminal = streamed.String()
			}
			seenTerminal = true
		case "extension_error", "error":
			return fmt.Errorf("OMP stream reported an error")
		}
		return nil
	})
	if err != nil {
		result.Usage = usage
		return result, err
	}
	if !seenTerminal {
		return result, fmt.Errorf("OMP exited without a terminal message_end event")
	}
	return finalizeGeneration(req, terminal, result.Receipt, usage, emit)
}

func ompArgs(overlayPath, rolePath, model string) []string {
	args := []string{
		"-p",
		"--mode", "json",
		"--no-tools",
		"--no-lsp",
		"--no-pty",
		"--no-skills",
		"--no-rules",
		"--no-extensions",
		"--no-session",
		"--no-title",
		"--config", overlayPath,
		"--system-prompt", rolePath,
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args
}

func ompReceiptArgs(model string) []string {
	args := ompArgs("<overlay.yml>", "<role.txt>", model)
	return append([]string{"omp"}, args...)
}

func jsonContainsAccount(raw []byte) (bool, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, err
	}
	var contains func(any) bool
	contains = func(current any) bool {
		switch typed := current.(type) {
		case []any:
			return len(typed) > 0
		case map[string]any:
			for _, child := range typed {
				if contains(child) {
					return true
				}
			}
		}
		return false
	}
	return contains(value), nil
}

func parseModelRefs(raw []byte) ([]ModelRef, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}
	models := make(map[string]ModelRef)
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case map[string]any:
			id := firstString(typed, "id", "model_id", "modelId", "value")
			if id != "" {
				label := firstString(typed, "label", "name", "display_name", "displayName")
				if label == "" {
					label = id
				}
				models[id] = ModelRef{ID: id, Label: label}
			}
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]ModelRef, 0, len(ids))
	for _, id := range ids {
		result = append(result, models[id])
	}
	return result, nil
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}
