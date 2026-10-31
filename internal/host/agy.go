//go:build client && !server

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

var requiredAGYDenyRules = []string{
	"read_file(*)",
	"write_file(*)",
	"read_url(*)",
	"execute_url(*)",
	"command(*)",
	"unsandboxed(*)",
	"mcp(*)",
}

type agyIsolation struct {
	homeDir           string
	settingsPath      string
	selectionVerified bool
}

// AGY is capability-gated: generation runs only against the isolated profile
// Plan Forge provisions at $PLAN_FORGE_HOME/profiles/agy (see agy_profile.go),
// whose Plan-Forge-owned settings deny every tool family.
type AGY struct {
	binary    string
	extraEnv  []string
	isolation *agyIsolation
}

// NewAGY constructs the production Antigravity adapter. The isolated profile
// is provisioned lazily at Preflight and at the auth/models probes, so mere
// registration and detection create nothing.
func NewAGY() Adapter { return &AGY{binary: "agy"} }

func (a *AGY) Kind() Kind { return KindAGY }

// EnvAllow lists Antigravity credential and configuration families inherited by
// a child after the common nesting and repository markers are removed.
func (a *AGY) EnvAllow() []string {
	allowed := []string{"GEMINI_*", "GOOGLE_*"}
	return append(allowed, a.extraEnv...)
}

func (a *AGY) withEnvAllow(names []string) Adapter {
	clone := *a
	clone.extraEnv = append([]string(nil), names...)
	return &clone
}

func (a *AGY) Detect(ctx context.Context) (string, error) {
	stdout, _, err := runProbe(ctx, a, a.binary, "--version")
	if err != nil {
		return "", err
	}
	version := extractSemanticVersion(string(stdout))
	if version == "" {
		return "", fmt.Errorf("agy --version returned no semantic version")
	}
	return version, nil
}

// Auth probes the isolated profile, never the host's agy sign-in: a host
// sign-in says nothing about what a generation run can use.
func (a *AGY) Auth(ctx context.Context) (AuthState, error) {
	if err := a.ensureIsolation(); err != nil {
		return AuthState{}, err
	}
	_, _, err := a.runIsolatedProbe(ctx, "models")
	if err != nil {
		return AuthState{Ready: false, Method: "agy models", Detail: agySignInHint(a.isolation.homeDir)}, nil
	}
	return AuthState{Ready: true, Method: "agy models", Detail: "ready"}, nil
}

func (a *AGY) Models(ctx context.Context) ([]ModelRef, error) {
	stdout, _, err := a.runIsolatedProbe(ctx, "models")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var models []ModelRef
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "-*• "))
		if line == "" || strings.HasSuffix(line, ":") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		id := fields[0]
		if strings.ContainsAny(id, "{}[]") || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, ModelRef{ID: id, Label: line})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func (a *AGY) Preflight(ctx context.Context) error {
	if err := a.ensureIsolation(); err != nil {
		return err
	}
	if _, err := validateAGYIsolation(a.isolation); err != nil {
		return err
	}
	_, err := a.Detect(ctx)
	return err
}

func (a *AGY) Generate(ctx context.Context, req contracts.GenerateRequest, emit func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	result := contracts.GenerationResult{Receipt: contracts.ToolFreeReceipt{
		Host: string(KindAGY),
		Argv: agyReceiptArgs(req.Model),
	}}
	if err := a.Preflight(ctx); err != nil {
		return result, err
	}
	version, err := a.Detect(ctx)
	result.Receipt.Version = version
	if err != nil {
		return result, err
	}
	settingsHash, err := validateAGYIsolation(a.isolation)
	if err != nil {
		return result, err
	}
	result.Receipt.SettingsSHA = settingsHash

	schemaPath, err := contracts.SchemaPath(req.SchemaID)
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(filepath.Dir(schemaPath))

	dir, err := newRunDir()
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)
	actualArgs := agyArgs(schemaPath, req.Model)
	executable, err := lookPath(a.binary)
	if err != nil {
		return result, err
	}

	// Re-read the policy immediately before spawn. A replacement between
	// Preflight and execution fences this generation.
	currentHash, err := validateAGYIsolation(a.isolation)
	if err != nil {
		return result, err
	}
	if currentHash != settingsHash {
		return result, &contracts.ErrCapability{Host: string(KindAGY), Reason: "isolated settings changed before spawn", Remedy: "retry after inspecting Plan-Forge settings"}
	}

	environment, err := a.childEnv(dir)
	if err != nil {
		return result, err
	}
	callCtx, cancel := context.WithTimeout(ctx, req.ClampTimeout())
	defer cancel()

	var streamed bytes.Buffer
	var terminal string
	var usage contracts.Usage
	seenInit := false
	seenTerminal := false
	_, err = runNDJSON(callCtx, executable, actualArgs, dir, environment, agyPromptBytes(req), func(event map[string]any) error {
		if err := inspectToolFree(KindAGY, event, &result.Receipt); err != nil {
			return err
		}
		typeName := eventType(event)
		if typeName == "init" || (typeName == "system" && stringField(event, "subtype") == "init") {
			if err := requireEmptyArray(event, "tools"); err != nil {
				return &contracts.ErrCapability{Host: string(KindAGY), Reason: err.Error(), Remedy: "use Claude Code or OMP"}
			}
			seenInit = true
			return nil
		}

		switch typeName {
		case "stream_event":
			if !seenInit {
				return &contracts.ErrCapability{Host: string(KindAGY), Reason: "output arrived before the empty-tools init event", Remedy: "use Claude Code or OMP"}
			}
			providerEvent := nestedMap(event, "event")
			delta := nestedMap(providerEvent, "delta")
			if eventType(delta) != "text_delta" {
				return nil
			}
			text := stringField(delta, "text")
			if err := appendBounded(&streamed, text, req.ClampMaxOutput()); err != nil {
				return err
			}
			return emitEvent(emit, contracts.GenerationEvent{Kind: contracts.EventTextDelta, Text: text})
		case "message_update":
			if !seenInit {
				return &contracts.ErrCapability{Host: string(KindAGY), Reason: "output arrived before the empty-tools init event", Remedy: "use Claude Code or OMP"}
			}
			update := nestedMap(event, "assistantMessageEvent")
			if eventType(update) != "text_delta" {
				return nil
			}
			text := stringField(update, "delta")
			if err := appendBounded(&streamed, text, req.ClampMaxOutput()); err != nil {
				return err
			}
			return emitEvent(emit, contracts.GenerationEvent{Kind: contracts.EventTextDelta, Text: text})
		case "result":
			if !seenInit {
				return &contracts.ErrCapability{Host: string(KindAGY), Reason: "result arrived without the empty-tools init event", Remedy: "use Claude Code or OMP"}
			}
			terminal = terminalValue(event["result"])
			if terminal == "" {
				terminal = streamed.String()
			}
			mergeUsage(&usage, usageFromEvent(event))
			seenTerminal = true
		case "error":
			return errors.New("AGY stream reported an error")
		}
		return nil
	})
	if err != nil {
		result.Usage = usage
		return result, err
	}
	if !seenInit {
		return result, &contracts.ErrCapability{Host: string(KindAGY), Reason: "stream omitted the empty-tools init event", Remedy: "use Claude Code or OMP"}
	}
	if !seenTerminal {
		return result, errors.New("AGY exited without a terminal result event")
	}
	return finalizeGeneration(req, terminal, result.Receipt, usage, emit)
}

func agyArgs(schemaPath, model string) []string {
	args := []string{"-p", "--output-format", "stream-json"}
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, "--json-schema", schemaPath)
}

func agyReceiptArgs(model string) []string {
	return append([]string{"agy"}, agyArgs("<stage.schema.json>", model)...)
}

func agyPromptBytes(req contracts.GenerateRequest) []byte {
	payload := struct {
		SystemPrompt string          `json:"system_prompt"`
		UserPayload  json.RawMessage `json:"user_payload"`
	}{SystemPrompt: req.SystemPrompt, UserPayload: req.UserPayload}
	if len(payload.UserPayload) == 0 {
		payload.UserPayload = json.RawMessage(`{}`)
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func validateAGYIsolation(isolation *agyIsolation) (string, error) {
	if isolation == nil || !isolation.selectionVerified {
		return "", &contracts.ErrCapability{Host: string(KindAGY), Reason: "isolated settings selection is not verified", Remedy: "use Claude Code or OMP"}
	}
	home, err := config.Home()
	if err != nil {
		return "", err
	}
	ownedRoot, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}
	settingsPath, err := filepath.Abs(isolation.settingsPath)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(ownedRoot, settingsPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", &contracts.ErrCapability{Host: string(KindAGY), Reason: "isolated settings are outside PlanForgeHome", Remedy: "use Claude Code or OMP"}
	}
	expected := filepath.Join(isolation.homeDir, ".gemini", "antigravity-cli", "settings.json")
	if filepath.Clean(settingsPath) != filepath.Clean(expected) {
		return "", &contracts.ErrCapability{Host: string(KindAGY), Reason: "isolated settings path does not match the selected isolated HOME", Remedy: "use Claude Code or OMP"}
	}
	if err := rejectSymlinkComponents(ownedRoot, settingsPath); err != nil {
		return "", &contracts.ErrCapability{Host: string(KindAGY), Reason: err.Error(), Remedy: "use Claude Code or OMP"}
	}
	info, err := os.Stat(settingsPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", &contracts.ErrCapability{Host: string(KindAGY), Reason: "isolated settings must be a private regular file", Remedy: "set mode 0600 or use Claude Code or OMP"}
	}
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return "", err
	}
	var settings struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return "", &contracts.ErrCapability{Host: string(KindAGY), Reason: "isolated settings are invalid JSON", Remedy: "use Claude Code or OMP"}
	}
	denied := make(map[string]bool, len(settings.Permissions.Deny))
	for _, rule := range settings.Permissions.Deny {
		denied[rule] = true
	}
	for _, required := range requiredAGYDenyRules {
		if !denied[required] {
			return "", &contracts.ErrCapability{Host: string(KindAGY), Reason: "isolated settings omit deny rule " + required, Remedy: "use Claude Code or OMP"}
		}
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func rejectSymlinkComponents(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("isolated settings path contains a symlink")
		}
	}
	return nil
}

func replaceEnvironment(environment []string, name, value string) []string {
	for i, entry := range environment {
		existing, _, ok := strings.Cut(entry, "=")
		if ok && environmentKey(existing) == environmentKey(name) {
			environment[i] = name + "=" + value
			return environment
		}
	}
	return append(environment, name+"="+value)
}
