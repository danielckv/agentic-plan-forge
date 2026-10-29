//go:build client && !server

package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Public endpoints. Fireworks serves inference under /inference/v1; the bare
// /v1 host is its account-management API, and sending a completion there fails
// in a way that reads like a bad key.
const (
	openAIBaseURL    = "https://api.openai.com/v1"
	fireworksBaseURL = "https://api.fireworks.ai/inference/v1"

	openAICompatLabel = "chat-completions-v1"
)

// Model preferences, most wanted first.
//
// These are hints, not a catalogue. Both providers rotate their served models
// — Fireworks retired nine serverless models in one day in October 2026, and
// OpenAI retires ids on a deprecation schedule — so a constant baked in at
// release time goes stale and fails with "model not found". The adapter asks
// the provider what it serves and picks the first preference that is actually
// there, falling back to the provider's own first listed model.
var (
	openAIModelPreference = []string{"gpt-5.6-sol", "gpt-5.6", "gpt-5.5-pro", "gpt-5.5"}
	// Fireworks ids are fully qualified: accounts/fireworks/models/<name>.
	fireworksModelPreference = []string{
		"accounts/fireworks/models/gpt-oss-120b",
		"accounts/fireworks/models/qwen3-235b-a22b",
	}
)

// openAIDefaultModel and fireworksDefaultModel are the last-resort ids used
// only when the provider's model list cannot be read at all.
const (
	openAIDefaultModel    = "gpt-5.6-sol"
	fireworksDefaultModel = "accounts/fireworks/models/gpt-oss-120b"
)

// OpenAICompat generates over the OpenAI-compatible chat completions API. One
// adapter serves both OpenAI and Fireworks: the wire format is the same, so
// the difference is a base URL, a model preference and a label.
type OpenAICompat struct {
	kind        Kind
	defaultBase string
	preference  []string
	fallback    string

	mu       sync.Mutex
	resolved string
}

// NewOpenAIAPI constructs the OpenAI adapter.
func NewOpenAIAPI() Adapter {
	return &OpenAICompat{
		kind: KindOpenAI, defaultBase: openAIBaseURL,
		preference: openAIModelPreference, fallback: openAIDefaultModel,
	}
}

// NewFireworksAPI constructs the Fireworks adapter.
func NewFireworksAPI() Adapter {
	return &OpenAICompat{
		kind: KindFireworks, defaultBase: fireworksBaseURL,
		preference: fireworksModelPreference, fallback: fireworksDefaultModel,
	}
}

func (a *OpenAICompat) Kind() Kind { return a.kind }

func (a *OpenAICompat) base() string {
	if override := baseURLFor(a.kind); override != "" {
		return strings.TrimRight(override, "/")
	}
	return a.defaultBase
}

func (a *OpenAICompat) keyEnv() string { return config.ProviderKeyEnv(providerOf(a.kind)) }

func (a *OpenAICompat) Detect(context.Context) (string, error) {
	key, _, err := apiKey(a.kind)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", noKeyError(a.kind)
	}
	return openAICompatLabel, nil
}

func (a *OpenAICompat) Auth(context.Context) (AuthState, error) {
	key, source, err := apiKey(a.kind)
	if err != nil {
		return AuthState{}, err
	}
	if key == "" {
		return AuthState{
			Ready:  false,
			Method: "api key",
			Detail: "no key: add one in Setup or export " + a.keyEnv(),
		}, nil
	}
	return AuthState{Ready: true, Method: "api key", Detail: "key from " + source}, nil
}

func (a *OpenAICompat) Models(ctx context.Context) ([]ModelRef, error) {
	ids, err := a.listModels(ctx)
	if err != nil {
		return modelCatalogue(a.kind), nil
	}
	out := make([]ModelRef, 0, len(ids))
	for _, id := range ids {
		out = append(out, ModelRef{ID: id, Label: shortModelLabel(id)})
	}
	return out, nil
}

// Preflight verifies the key by listing models: an authenticated round trip
// that spends no tokens.
func (a *OpenAICompat) Preflight(ctx context.Context) error {
	if _, _, err := a.credential(); err != nil {
		return err
	}
	if _, err := a.listModels(ctx); err != nil {
		return &contracts.ErrCapability{
			Host:   string(a.kind),
			Reason: config.ProviderLabel(providerOf(a.kind)) + " rejected this key: " + err.Error(),
			Remedy: "check the key in Setup, or export a working " + a.keyEnv(),
		}
	}
	return nil
}

func (a *OpenAICompat) credential() (key, source string, err error) {
	key, source, err = apiKey(a.kind)
	if err != nil {
		return "", "", err
	}
	if key == "" {
		return "", "", noKeyError(a.kind)
	}
	return key, source, nil
}

// listModels reads the provider's served model ids.
func (a *OpenAICompat) listModels(ctx context.Context) ([]string, error) {
	key, _, err := a.credential()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base()+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := apiHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models list returned %s: %s", resp.Status, readBoundedError(resp.Body))
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIResponseBytes)).Decode(&payload); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(payload.Data))
	for _, entry := range payload.Data {
		if entry.ID != "" {
			ids = append(ids, entry.ID)
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("the provider listed no models")
	}
	sort.Strings(ids)
	return ids, nil
}

// model resolves which model this call uses. An explicit choice always wins;
// otherwise the first served preference is taken and remembered for the
// process, so placement stays stable within a run and a retired id cannot
// fail every stage.
func (a *OpenAICompat) model(ctx context.Context, req contracts.GenerateRequest) string {
	if explicit := strings.TrimSpace(req.Model); explicit != "" {
		return explicit
	}
	a.mu.Lock()
	cached := a.resolved
	a.mu.Unlock()
	if cached != "" {
		return cached
	}

	chosen := a.fallback
	if ids, err := a.listModels(ctx); err == nil {
		served := make(map[string]bool, len(ids))
		for _, id := range ids {
			served[id] = true
		}
		chosen = ids[0]
		for _, want := range a.preference {
			if served[want] {
				chosen = want
				break
			}
		}
	}
	a.mu.Lock()
	a.resolved = chosen
	a.mu.Unlock()
	return chosen
}

func (a *OpenAICompat) Generate(
	ctx context.Context,
	req contracts.GenerateRequest,
	emit func(contracts.GenerationEvent) error,
) (contracts.GenerationResult, error) {
	key, _, err := a.credential()
	result := contracts.GenerationResult{Receipt: contracts.ToolFreeReceipt{
		Host:    string(a.kind),
		Version: openAICompatLabel,
	}}
	if err != nil {
		return result, err
	}

	callCtx, cancel := context.WithTimeout(ctx, req.ClampTimeout())
	defer cancel()

	model := a.model(callCtx, req)
	endpoint := a.base() + "/chat/completions"
	result.Receipt.Argv = apiReceiptArgs(endpoint, model)

	body := a.requestBody(req, model, true)
	text, usage, err := a.stream(callCtx, endpoint, key, body, req, emit)
	if err != nil {
		// As with Anthropic, a refused schema costs one retry, not the stage.
		if schemaRejectionText(err) {
			text, usage, err = a.stream(callCtx, endpoint, key, a.requestBody(req, model, false), req, emit)
		}
		if err != nil {
			result.Usage = usage
			return result, apiFailure(a.kind, "generate", err)
		}
	}
	return finalizeGeneration(req, text, result.Receipt, usage, emit)
}

// requestBody builds one chat completions request. withSchema is false on the
// retry that follows a schema rejection.
func (a *OpenAICompat) requestBody(req contracts.GenerateRequest, model string, withSchema bool) map[string]any {
	body := map[string]any{
		"model": model,
		"messages": []map[string]any{
			{"role": "system", "content": req.SystemPrompt},
			{"role": "user", "content": string(promptBytes(req))},
		},
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"max_tokens":     apiMaxTokens,
	}
	if !withSchema {
		return body
	}
	if schema := outputSchemaMap(req); schema != nil {
		body["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "plan_forge_reply",
				"strict": true,
				"schema": schema,
			},
		}
	}
	return body
}

// stream issues the request and assembles the streamed reply.
func (a *OpenAICompat) stream(
	ctx context.Context,
	endpoint, key string,
	body map[string]any,
	req contracts.GenerateRequest,
	emit func(contracts.GenerationEvent) error,
) (string, contracts.Usage, error) {
	var usage contracts.Usage
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", usage, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return "", usage, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := apiHTTPClient.Do(httpReq)
	if err != nil {
		return "", usage, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", usage, fmt.Errorf("%s: %s", resp.Status, readBoundedError(resp.Body))
	}

	var assembled bytes.Buffer
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, maxAPIStreamBytes))
	scanner.Buffer(make([]byte, 0, 64<<10), maxAPIStreamLine)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// A provider may interleave keep-alive or comment frames; a frame
			// that is not a chunk is skipped rather than failing the call.
			continue
		}
		if chunk.Usage != nil {
			usage.InputTokens = chunk.Usage.PromptTokens
			usage.OutputTokens = chunk.Usage.CompletionTokens
		}
		for _, choice := range chunk.Choices {
			text := choice.Delta.Content
			if text == "" {
				continue
			}
			if err := appendBounded(&assembled, text, req.ClampMaxOutput()); err != nil {
				return "", usage, err
			}
			if err := emitEvent(emit, contracts.GenerationEvent{Kind: contracts.EventTextDelta, Text: text}); err != nil {
				return "", usage, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", usage, err
	}
	if assembled.Len() == 0 {
		return "", usage, errors.New("the provider returned no content")
	}
	return assembled.String(), usage, nil
}

// Bounds on what a provider may send back, so a misbehaving endpoint cannot
// exhaust memory.
const (
	maxAPIResponseBytes = 4 << 20
	maxAPIStreamBytes   = 64 << 20
	maxAPIStreamLine    = 2 << 20
	maxAPIErrorBytes    = 2 << 10
)

// apiHTTPClient is shared by the HTTP adapters. The per-call deadline comes
// from the request context, so the client itself sets no overall timeout.
var apiHTTPClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	},
}

// readBoundedError returns a provider's own error text, bounded, so a failure
// names its cause instead of reporting a bare status.
func readBoundedError(r io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(r, maxAPIErrorBytes))
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return "no detail"
	}
	return strings.TrimSpace(string(raw))
}

// schemaRejectionText reports whether an error names the output schema. The
// OpenAI-compatible providers return a plain 400 with a message, so unlike the
// Anthropic adapter there is no typed error to match on.
func schemaRejectionText(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "400") {
		return false
	}
	return strings.Contains(message, "schema") || strings.Contains(message, "response_format")
}

// shortModelLabel renders a fully qualified id for a menu.
func shortModelLabel(id string) string {
	if idx := strings.LastIndex(id, "/"); idx >= 0 && idx+1 < len(id) {
		return id[idx+1:]
	}
	return id
}
