//go:build client && !server

package host

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// writeCredentials points the given providers at a test server through the
// credentials file's base_url override, the same path a self-hosted relay
// uses. It also stores the API key, so no environment is touched.
func writeCredentials(t *testing.T, provider, baseURL string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PLAN_FORGE_HOME", dir)
	creds := map[string]any{
		"providers": map[string]any{
			provider: map[string]any{
				"api_key":  "test-key",
				"base_url": baseURL,
			},
		},
	}
	raw, _ := json.Marshal(creds)
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSSEReaderParsesDataPayloadsAndEndsAtEOF(t *testing.T) {
	body := ": keep-alive\n\n" +
		"data: {\"n\":1}\n\n" +
		"data: [DONE]\n\n" +
		"data: {\"n\":2}\n\n"
	sse := newSSEReader(strings.NewReader(body))
	first, err := sse.next()
	if err != nil {
		t.Fatalf("first event: %v", err)
	}
	if string(first) != `{"n":1}` {
		t.Fatalf("first payload = %q", first)
	}
	second, err := sse.next()
	if err != nil {
		t.Fatalf("second event: %v", err)
	}
	if string(second) != `{"n":2}` {
		t.Fatalf("second payload = %q, [DONE] is data like any other", second)
	}
	if _, err := sse.next(); err == nil {
		t.Fatal("stream must end, not hang or repeat")
	}
}

func TestGeminiRequestBodyCarriesSchemaAndEffort(t *testing.T) {
	g := &Gemini{}
	req := contracts.GenerateRequest{
		SystemPrompt: "sys",
		UserPayload:  json.RawMessage(`{"k":1}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
		Effort:       contracts.EffortMedium,
	}
	body := g.requestBody(req, geminiWire{Schema: true, Thinking: true})
	cfg := body["generationConfig"].(map[string]any)
	if cfg["responseJsonSchema"] == nil {
		t.Error("a supplied OutputSchema must reach responseJsonSchema")
	}
	if cfg["thinkingConfig"] == nil {
		t.Error("medium effort must set thinkingConfig")
	}
	plain := g.requestBody(req, geminiWire{})
	cfg = plain["generationConfig"].(map[string]any)
	if cfg["responseJsonSchema"] != nil {
		t.Error("the retry must drop the constraint: a 400 on the schema must not repeat it")
	}
	if cfg["thinkingConfig"] != nil {
		t.Error("the terse 400 names no field, so the retry must drop thinkingConfig too")
	}
}

// The isolation gate: without the marker file the managed runtime refuses
// every entry point with the same capability error, and never reaches HTTP.
// A key is present, so the failure is the gate, not the credential check.
func TestAntigravityGateBlocksEveryEntryPoint(t *testing.T) {
	writeCredentials(t, config.ProviderGoogle, "https://stub.invalid")
	ForgetCredentials()
	a := &AntigravityRuntime{}
	want := "the managed Antigravity runtime is"
	if _, err := a.Detect(context.Background()); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Detect = %v, want gate error", err)
	}
	if _, err := a.Models(context.Background()); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Models = %v, want gate error", err)
	}
	if err := a.Preflight(context.Background()); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Preflight = %v, want gate error", err)
	}
	if _, err := a.Generate(context.Background(), contracts.GenerateRequest{}, nil); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Generate = %v, want gate error", err)
	}
}

// With the marker file present, the wire request must carry an explicit
// empty tools list; a marshalled map without the key would let the runtime
// pick its own tools, which is exactly what the gate exists to prevent.
func TestAntigravityGenerateSendsAnExplicitlyEmptyToolsList(t *testing.T) {
	ForgetCredentials()
	var gotBody map[string]any
	var gotTools bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Generate resolves the model first, which hits GET /models with
		// no body; only the interactions POST carries the wire contract.
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"models":[{"name":"models/gemini-3-flash"}]}`))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("request body: %v", err)
		}
		_, gotTools = gotBody["tools"]
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"delta\":\"ok\"}\n\n"))
	}))
	defer server.Close()

	writeCredentials(t, config.ProviderGoogle, server.URL)
	ForgetCredentials()
	home, _ := os.LookupEnv("PLAN_FORGE_HOME")
	if err := os.WriteFile(filepath.Join(home, antigravityGateFile), []byte("proven\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := &AntigravityRuntime{}
	req := contracts.GenerateRequest{
		SystemPrompt: "sys",
		UserPayload:  json.RawMessage(`{}`),
	}
	result, err := a.Generate(context.Background(), req, func(contracts.GenerationEvent) error { return nil })
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.Receipt.Host != string(KindAntigravity) {
		t.Errorf("receipt host = %s", result.Receipt.Host)
	}
	if !strings.Contains(string(result.Content), "ok") {
		t.Errorf("content = %s, want the streamed delta", result.Content)
	}
	if !gotTools {
		t.Errorf("tools = %#v, want an explicitly empty list on the wire", gotBody["tools"])
	}
	if gotBody["agent_config"] == nil {
		t.Error("agent_config must name the managed agent and model")
	}
}

// The model comes from the provider's live /models list, never a hardcoded
// id: the first served entry in the preference order wins, and an id the
// provider retired must never be sent.
func TestGeminiModelResolvesFromLiveList(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			hits++
			// The shape the live API served on 2026-10-31: the bare
			// gemini-3-flash id is gone, and the list is alphabetical, so its
			// first entry is a managed agent.
			w.Write([]byte(`{"models":[{"name":"models/antigravity-preview-09-2026"},` +
				`{"name":"models/gemini-3-flash-preview"},` +
				`{"name":"models/gemini-3.1-pro-preview"}]}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()
	writeCredentials(t, config.ProviderGoogle, server.URL)
	ForgetCredentials()

	g := &Gemini{}
	model := g.model(context.Background(), contracts.GenerateRequest{})
	if model != "gemini-3.1-pro-preview" {
		t.Errorf("model = %s, want the first served id in preference order (gemini-3.1-pro-preview)", model)
	}
	if hits != 1 {
		t.Errorf("models endpoint hits = %d", hits)
	}
}

// When none of the preferred ids is served, the choice must still be a Gemini
// text model. Picking the alphabetically first entry shipped a managed-agent
// id (antigravity-preview-05-2026) to a normalize call.
func TestGeminiModelFallbackSkipsNonTextModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"name":"models/antigravity-preview-05-2026"},` +
			`{"name":"models/aqa"},` +
			`{"name":"models/gemini-2.5-flash-image"},` +
			`{"name":"models/gemini-2.5-flash-preview-tts"},` +
			`{"name":"models/gemini-2.0-flash"}]}`))
	}))
	defer server.Close()
	writeCredentials(t, config.ProviderGoogle, server.URL)
	ForgetCredentials()

	g := &Gemini{}
	if got := g.model(context.Background(), contracts.GenerateRequest{}); got != "gemini-2.0-flash" {
		t.Errorf("model = %s, want the first gemini text model served (gemini-2.0-flash)", got)
	}
}

// A 400 without a field name must be retried once with both optional request
// parts dropped, and that retry must reach the wire without them.
func TestGeminiRetriesATerseRefusalWithoutOptionalFields(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Write([]byte(`{"models":[{"name":"models/gemini-3-flash-preview"}]}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("request body: %v", err)
		}
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"{\"a\":1}"}]}}]}` + "\n\n"))
	}))
	defer server.Close()
	writeCredentials(t, config.ProviderGoogle, server.URL)
	ForgetCredentials()

	g := &Gemini{}
	req := contracts.GenerateRequest{
		SystemPrompt: "sys",
		UserPayload:  json.RawMessage(`{}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"integer"}}}`),
		Effort:       contracts.EffortMedium,
	}
	if _, err := g.Generate(context.Background(), req, func(contracts.GenerationEvent) error { return nil }); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want the call and one retry", len(bodies))
	}
	first := bodies[0]["generationConfig"].(map[string]any)
	if first["responseJsonSchema"] == nil || first["thinkingConfig"] == nil {
		t.Errorf("the first request must carry both optional fields, got %v", first)
	}
	retry := bodies[1]["generationConfig"].(map[string]any)
	if retry["responseJsonSchema"] != nil || retry["thinkingConfig"] != nil {
		t.Errorf("the retry must drop both optional fields, got %v", retry)
	}
}

// The rule the live API enforces: maxItems anywhere in the schema is refused
// with a bare "invalid argument", and the identical schema without it is
// accepted. Measured 2026-10-31 on gemini-3-flash-preview.
func TestGeminiSchemaDropsMaxItems(t *testing.T) {
	schema := map[string]any{
		"$schema":  "https://json-schema.org/draft/2020-12/schema",
		"$id":      "normalized_prd.v1.json",
		"type":     "object",
		"required": []any{"a"},
		"properties": map[string]any{
			"a": map[string]any{
				"type": "array", "maxItems": float64(500),
				"items": map[string]any{"type": "string", "maxLength": float64(4000)},
			},
			"b": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"c": map[string]any{"type": "array", "maxItems": float64(200), "items": map[string]any{"type": "string"}},
				},
			},
		},
	}
	got := geminiSchema(schema)
	if _, ok := got["$schema"]; ok {
		t.Error("$schema is a draft marker the API does not read; it must be dropped")
	}
	found := false
	dropKey(got, "__probe__")
	var walk func(node any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if _, ok := v["maxItems"]; ok {
				found = true
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(got)
	if found {
		t.Error("maxItems must be removed everywhere: the API refuses it and names no field")
	}
	// The constraints the contract does validate on must survive.
	if got["required"] == nil || got["$id"] == nil {
		t.Error("required and $id are kept: only maxItems is unusable")
	}
	item := got["properties"].(map[string]any)["a"].(map[string]any)["items"].(map[string]any)
	if item["maxLength"] == nil {
		t.Error("maxLength is accepted by the API and must survive")
	}
	if got["properties"].(map[string]any)["a"].(map[string]any)["maxItems"] != nil {
		t.Error("maxItems must be gone from the array that carried it")
	}
}

// `const` is not in the keyword list the API honours: it is silently dropped,
// and schema_version came back as a different value. The single-value enum is
// enforced instead.
func TestGeminiSchemaConvertsConstToEnum(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"schema_version": map[string]any{"const": float64(1)}},
	}
	got := geminiSchema(schema)
	field := got["properties"].(map[string]any)["schema_version"].(map[string]any)
	if _, ok := field["const"]; ok {
		t.Error("const must not be sent: the API ignores it")
	}
	enum, ok := field["enum"].([]any)
	if !ok || len(enum) != 1 || fmt.Sprint(enum[0]) != "1" {
		t.Errorf("const must become enum:[1], got %#v", field["enum"])
	}
}

// The embedded normalized_prd.v1 schema is the one that failed in production
// ("normalize section 1 of 7"). Its prepared form must be exactly the
// accepted shape: no maxItems, every declared property and required field
// intact.
func TestGeminiSchemaPreparesTheNormalizedPRD(t *testing.T) {
	raw, err := contracts.SchemaBytes("normalized_prd.v1")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	got := geminiSchema(decoded)

	var walk func(node any) bool
	walk = func(node any) bool {
		switch v := node.(type) {
		case map[string]any:
			if _, ok := v["maxItems"]; ok {
				return true
			}
			for _, child := range v {
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range v {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	if walk(got) {
		t.Error("normalized_prd.v1 still carries maxItems after preparation")
	}
	props := got["properties"].(map[string]any)
	for _, want := range []string{"schema_version", "product", "technical_appendix", "epics", "ambiguities", "warnings"} {
		if props[want] == nil {
			t.Errorf("preparation dropped the declared property %q", want)
		}
	}
	if got["required"] == nil {
		t.Error("required must survive: the reply is validated against it")
	}
}
