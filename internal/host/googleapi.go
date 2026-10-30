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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Google generation runtimes. One Google credential (GEMINI_API_KEY) serves
// both, but they are separate kinds because they are different products, not
// modes of one API:
//
//   - gemini: direct model calls against the Gemini API. Thinking effort is
//     an explicit request parameter, so planning controls it directly.
//   - antigravity: the managed Antigravity runtime, reached through the
//     Interactions API. It provisions a managed agent sandbox, so it is only
//     selectable after the isolation gate below passes.
//
// The local google-antigravity Python SDK and the `agy` CLI are NOT supported
// runtimes: they manage a local binary, which is what SDK-backed planning
// removes.
const (
	KindGemini      Kind = "gemini"
	KindAntigravity Kind = "antigravity"

	geminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	// interactionsBaseURL is the Interactions API endpoint the managed
	// Antigravity runtime is reached through.
	interactionsBaseURL = "https://generativelanguage.googleapis.com/v1alpha/interactions"

	geminiWireLabel      = "gemini-api-v1beta"
	antigravityWireLabel = "interactions-v1alpha"

	// antigravityAgent is the managed agent the Interactions API invokes.
	antigravityAgent = "antigravity-preview-09-2026"
)

// geminiModelPreference lists the model ids planning wants, most wanted
// first. As with the other API hosts this is a hint resolved against the
// live model list, never a catalogue baked in at release time.
var geminiModelPreference = []string{
	"gemini-4-argon",
	"gemini-3-pro",
	"gemini-3-flash",
}

// geminiDefaultModel is the last-resort id used only when the model list
// cannot be read at all.
const geminiDefaultModel = "gemini-3-flash"

// antigravityUnavailable is the reason recorded when the isolation gate has
// not passed. The gate lives in the run checkpoint, not here: the adapter
// trusts a capability file the operator (or the preflight harness) wrote
// after proving the wire contract on the real API.
const antigravityGateFile = "antigravity-isolation-ok"

// Gemini generates over the Gemini API with the owner's own API key.
type Gemini struct {
	mu       sync.Mutex
	resolved string
}

// NewGeminiAPI constructs the production Gemini adapter.
func NewGeminiAPI() Adapter { return &Gemini{} }

func (a *Gemini) Kind() Kind { return KindGemini }

func (a *Gemini) key() (string, error) {
	key, _, err := apiKey(KindGemini)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", noKeyError(KindGemini)
	}
	return key, nil
}

func (a *Gemini) Detect(context.Context) (string, error) {
	if _, err := a.key(); err != nil {
		return "", err
	}
	return geminiWireLabel, nil
}

func (a *Gemini) Auth(context.Context) (AuthState, error) {
	key, source, err := apiKey(KindGemini)
	if err != nil {
		return AuthState{}, err
	}
	if key == "" {
		return AuthState{
			Ready:  false,
			Method: "api key",
			Detail: "no key: add one in Setup or export " + config.ProviderKeyEnv(config.ProviderGoogle),
		}, nil
	}
	return AuthState{Ready: true, Method: "api key", Detail: "key from " + source}, nil
}

func (a *Gemini) Models(ctx context.Context) ([]ModelRef, error) {
	ids, err := a.listModels(ctx)
	if err != nil {
		return modelCatalogue(KindGemini), nil
	}
	out := make([]ModelRef, 0, len(ids))
	for _, id := range ids {
		out = append(out, ModelRef{ID: id, Label: shortModelLabel(id)})
	}
	return out, nil
}

// Preflight verifies the key by listing models: an authenticated round trip
// that spends no tokens.
func (a *Gemini) Preflight(ctx context.Context) error {
	if _, err := a.key(); err != nil {
		return err
	}
	if _, err := a.listModels(ctx); err != nil {
		return &contracts.ErrCapability{
			Host:   string(KindGemini),
			Reason: "Google rejected this key: " + err.Error(),
			Remedy: "check the key in Setup, or export a working " + config.ProviderKeyEnv(config.ProviderGoogle),
		}
	}
	return nil
}

// base returns the endpoint root for Gemini calls: the credentials override
// when one is configured (self-hosted relays and proxies), else the public
// endpoint.
func (a *Gemini) base() string {
	if override := baseURLFor(KindGemini); override != "" {
		return strings.TrimRight(override, "/")
	}
	return geminiBaseURL
}

// interactionsURL returns the managed-runtime endpoint, override first.
func interactionsURL() string {
	if override := baseURLFor(KindAntigravity); override != "" {
		return strings.TrimRight(override, "/") + "/interactions"
	}
	return interactionsBaseURL
}

// listModels reads the provider's served model ids.
func (a *Gemini) listModels(ctx context.Context) ([]string, error) {
	key, err := a.key()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base()+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", key)
	resp, err := apiHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models list returned %s: %s", resp.Status, readBoundedError(resp.Body))
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIResponseBytes)).Decode(&payload); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(payload.Models))
	for _, entry := range payload.Models {
		// The API returns "models/<id>"; callers use the bare id.
		ids = append(ids, strings.TrimPrefix(entry.Name, "models/"))
	}
	if len(ids) == 0 {
		return nil, errors.New("the provider listed no models")
	}
	sort.Strings(ids)
	return ids, nil
}

// model resolves which model this call uses. An explicit choice wins;
// otherwise the first served preference is taken and remembered for the
// process, so placement stays stable within a run.
func (a *Gemini) model(ctx context.Context, req contracts.GenerateRequest) string {
	if explicit := strings.TrimSpace(req.Model); explicit != "" {
		return explicit
	}
	a.mu.Lock()
	cached := a.resolved
	a.mu.Unlock()
	if cached != "" {
		return cached
	}
	chosen := geminiDefaultModel
	if ids, err := a.listModels(ctx); err == nil {
		served := make(map[string]bool, len(ids))
		for _, id := range ids {
			served[id] = true
		}
		chosen = ids[0]
		for _, want := range geminiModelPreference {
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

// Generate runs one bounded model call. Thinking effort is carried in
// req.Effort when the caller set it; direct Gemini calls accept it.
func (a *Gemini) Generate(
	ctx context.Context,
	req contracts.GenerateRequest,
	emit func(contracts.GenerationEvent) error,
) (contracts.GenerationResult, error) {
	key, err := a.key()
	result := contracts.GenerationResult{Receipt: contracts.ToolFreeReceipt{
		Host:    string(KindGemini),
		Version: geminiWireLabel,
	}}
	if err != nil {
		return result, err
	}
	callCtx, cancel := context.WithTimeout(ctx, req.ClampTimeout())
	defer cancel()

	model := a.model(callCtx, req)
	endpoint := a.base() + "/models/" + model + ":streamGenerateContent?alt=sse"
	result.Receipt.Argv = apiReceiptArgs(endpoint, model)

	body := a.requestBody(req, true)
	text, usage, err := a.stream(callCtx, endpoint, key, body, req, emit)
	if err != nil {
		// A refused schema costs one retry, not the stage.
		if schemaRejectionText(err) {
			text, usage, err = a.stream(callCtx, endpoint, key, a.requestBody(req, false), req, emit)
		}
		if err != nil {
			result.Usage = usage
			return result, apiFailure(KindGemini, "generate", err)
		}
	}
	return finalizeGeneration(req, text, result.Receipt, usage, emit)
}

// requestBody builds one Gemini request. withSchema is false on the retry
// that follows a schema rejection.
//
// Thinking: an explicit effort parameter is the reason direct calls are the
// default PRD path. The API takes thinkingConfig.thinkingBudget; medium maps
// to a bounded budget rather than "unbounded", so deliberation stays
// predictable. A runtime that rejects the field costs one retry without it.
func (a *Gemini) requestBody(req contracts.GenerateRequest, withSchema bool) map[string]any {
	body := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []map[string]any{{"text": req.SystemPrompt}},
		},
		"contents": []map[string]any{{
			"role":  "user",
			"parts": []map[string]any{{"text": string(promptBytes(req))}},
		}},
		"generationConfig": map[string]any{
			"maxOutputTokens": apiMaxTokens,
		},
	}
	if req.Effort == contracts.EffortMedium {
		body["generationConfig"].(map[string]any)["thinkingConfig"] = map[string]any{
			"thinkingBudget": geminiMediumThinkingBudget,
		}
	}
	if !withSchema {
		return body
	}
	if schema := outputSchemaMap(req); schema != nil {
		body["generationConfig"].(map[string]any)["responseMimeType"] = "application/json"
		body["generationConfig"].(map[string]any)["responseJsonSchema"] = schema
	}
	return body
}

// geminiMediumThinkingBudget bounds thinking for a medium-effort call. The
// API takes tokens, not levels; this is the bounded, predictable form.
const geminiMediumThinkingBudget = 12288

// stream issues the request and assembles the SSE reply.
func (a *Gemini) stream(
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
	httpReq.Header.Set("x-goog-api-key", key)
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
	sse := newSSEReader(io.LimitReader(resp.Body, maxAPIResponseBytes))
	for {
		line, err := sse.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", usage, err
		}
		var chunk struct {
			UsageMetadata struct {
				InputTokens  int `json:"promptTokenCount"`
				OutputTokens int `json:"candidatesTokenCount"`
			} `json:"usageMetadata"`
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		if err := json.Unmarshal(line, &chunk); err != nil {
			return "", usage, fmt.Errorf("malformed stream chunk: %w", err)
		}
		for _, cand := range chunk.Candidates {
			for _, part := range cand.Content.Parts {
				if part.Text == "" {
					continue
				}
				if err := appendBounded(&assembled, part.Text, req.ClampMaxOutput()); err != nil {
					return "", usage, err
				}
				if err := emitEvent(emit, contracts.GenerationEvent{Kind: contracts.EventTextDelta, Text: part.Text}); err != nil {
					return "", usage, err
				}
			}
		}
		if chunk.UsageMetadata.InputTokens != 0 {
			usage.InputTokens = chunk.UsageMetadata.InputTokens
		}
		if chunk.UsageMetadata.OutputTokens != 0 {
			usage.OutputTokens = chunk.UsageMetadata.OutputTokens
		}
	}
	if assembled.Len() == 0 {
		return "", usage, errors.New("the Gemini API returned no content")
	}
	return assembled.String(), usage, nil
}

// AntigravityRuntime generates through the managed Antigravity runtime over
// the Interactions API. It is selectable only when the isolation gate has
// passed; before that every call returns the gate failure instead of
// weakening the sandbox contract.
type AntigravityRuntime struct{}

// NewAntigravityAPI constructs the managed Antigravity adapter.
func NewAntigravityAPI() Adapter { return &AntigravityRuntime{} }

func (a *AntigravityRuntime) Kind() Kind { return KindAntigravity }

func (a *AntigravityRuntime) key() (string, error) { return (&Gemini{}).key() }

// gateDir returns the directory the isolation gate file lives in. It is the
// same home the credentials file uses, so one install has one gate.
func gateDir() (string, error) { return config.EnsureHome() }

// isolationProven reports whether the operator proved the wire contract:
// that the Interactions API preserves an explicit empty tools list and
// omits the environment, so the managed sandbox cannot execute tools.
func isolationProven() bool {
	dir, err := gateDir()
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, antigravityGateFile))
	return err == nil && info.Mode().IsRegular()
}

func (a *AntigravityRuntime) Detect(context.Context) (string, error) {
	if _, err := a.key(); err != nil {
		return "", err
	}
	if !isolationProven() {
		return "", &contracts.ErrCapability{
			Host:   string(KindAntigravity),
			Reason: "the managed Antigravity runtime is not available for planning",
			Remedy: "the Interactions API must be proven to preserve an empty tools list and omit the environment before this runtime is selectable; direct Gemini calls are available now",
		}
	}
	return antigravityWireLabel, nil
}

func (a *AntigravityRuntime) Auth(ctx context.Context) (AuthState, error) {
	return (&Gemini{}).Auth(ctx)
}

func (a *AntigravityRuntime) Models(ctx context.Context) ([]ModelRef, error) {
	if !isolationProven() {
		return nil, &contracts.ErrCapability{
			Host:   string(KindAntigravity),
			Reason: "the managed Antigravity runtime is gated; direct Gemini calls are available",
			Remedy: "prove the isolation contract, then re-run doctor",
		}
	}
	// The managed runtime serves the models its agent config accepts; until
	// the gate passes there is no honest list to show.
	return (&Gemini{}).Models(ctx)
}

func (a *AntigravityRuntime) Preflight(ctx context.Context) error {
	if _, err := a.key(); err != nil {
		return err
	}
	if !isolationProven() {
		return &contracts.ErrCapability{
			Host:   string(KindAntigravity),
			Reason: "the managed Antigravity runtime is gated; direct Gemini calls are available",
			Remedy: "prove the isolation contract, then re-run doctor",
		}
	}
	return nil
}

// Generate runs one managed interaction. The isolation contract is enforced
// in the wire request, not the prompt: an explicit empty tools list and no
// environment block are serialized into every call.
func (a *AntigravityRuntime) Generate(
	ctx context.Context,
	req contracts.GenerateRequest,
	emit func(contracts.GenerationEvent) error,
) (contracts.GenerationResult, error) {
	key, err := a.key()
	result := contracts.GenerationResult{Receipt: contracts.ToolFreeReceipt{
		Host:    string(KindAntigravity),
		Version: antigravityWireLabel,
	}}
	if err != nil {
		return result, err
	}
	if !isolationProven() {
		return result, &contracts.ErrCapability{
			Host:   string(KindAntigravity),
			Reason: "the managed Antigravity runtime is gated; direct Gemini calls are available",
			Remedy: "prove the isolation contract, then re-run doctor",
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, req.ClampTimeout())
	defer cancel()

	model := (&Gemini{}).model(callCtx, req)
	result.Receipt.Argv = apiReceiptArgs(interactionsURL(), model)

	body := map[string]any{
		"agent":        antigravityAgent,
		"agent_config": map[string]any{"type": "antigravity", "model": model},
		// The isolation contract: an explicit empty list, not an omitted
		// field. Serialization MUST preserve it; the gate file exists because
		// a future SDK change could silently drop the empty list.
		"tools":  []any{},
		"input":  string(promptBytes(req)),
		"stream": true,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return result, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, interactionsURL(), bytes.NewReader(encoded))
	if err != nil {
		return result, err
	}
	httpReq.Header.Set("x-goog-api-key", key)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := apiHTTPClient.Do(httpReq)
	if err != nil {
		return result, apiFailure(KindAntigravity, "generate", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result, apiFailure(KindAntigravity, "generate", fmt.Errorf("%s: %s", resp.Status, readBoundedError(resp.Body)))
	}
	var assembled bytes.Buffer
	var antUsage contracts.Usage
	sse := newSSEReader(io.LimitReader(resp.Body, maxAPIResponseBytes))
	for {
		line, err := sse.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return result, apiFailure(KindAntigravity, "generate", err)
		}
		var chunk struct {
			Delta string `json:"delta"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(line, &chunk); err != nil {
			return result, apiFailure(KindAntigravity, "generate", fmt.Errorf("malformed stream chunk: %w", err))
		}
		if chunk.Delta != "" {
			if err := appendBounded(&assembled, chunk.Delta, req.ClampMaxOutput()); err != nil {
				return result, err
			}
			if err := emitEvent(emit, contracts.GenerationEvent{Kind: contracts.EventTextDelta, Text: chunk.Delta}); err != nil {
				return result, err
			}
		}
		if chunk.Usage.OutputTokens != 0 {
			antUsage.OutputTokens = chunk.Usage.OutputTokens
		}
	}
	if assembled.Len() == 0 {
		return result, apiFailure(KindAntigravity, "generate", errors.New("the managed runtime returned no content"))
	}
	return finalizeGeneration(req, assembled.String(), result.Receipt, antUsage, emit)
}

// sseReader splits an SSE byte stream into data payloads. Both Google
// endpoints stream "data: {json}" events separated by blank lines; events
// without a data line carry only comments or retry hints and are skipped.
type sseReader struct {
	scanner *bufio.Scanner
}

func newSSEReader(r io.Reader) *sseReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxAPIResponseBytes)
	return &sseReader{scanner: sc}
}

// next returns the next data payload, or io.EOF at stream end.
func (s *sseReader) next() ([]byte, error) {
	for s.scanner.Scan() {
		line := s.scanner.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			return []byte(payload), nil
		default:
			// comment, event id, or the blank line between events
		}
	}
	if err := s.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}
