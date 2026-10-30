//go:build client && !server

package host

import (
	"context"
	"encoding/json"
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
	body := g.requestBody(req, true)
	cfg := body["generationConfig"].(map[string]any)
	if cfg["responseJsonSchema"] == nil {
		t.Error("a supplied OutputSchema must reach responseJsonSchema")
	}
	if cfg["thinkingConfig"] == nil {
		t.Error("medium effort must set thinkingConfig")
	}
	plain := g.requestBody(req, false)
	cfg = plain["generationConfig"].(map[string]any)
	if cfg["responseJsonSchema"] != nil {
		t.Error("the schema retry must drop the constraint: a 400 on the schema must not repeat it")
	}
	if cfg["thinkingConfig"] == nil {
		t.Error("thinking config survives a schema retry; effort is independent of the constraint")
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
			w.Write([]byte(`{"models":[{"name":"models/gemini-3-flash"},{"name":"models/gemini-3-pro"}]}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()
	writeCredentials(t, config.ProviderGoogle, server.URL)
	ForgetCredentials()

	g := &Gemini{}
	model := g.model(context.Background(), contracts.GenerateRequest{})
	if model != "gemini-3-pro" {
		t.Errorf("model = %s, want the first served id in preference order (gemini-3-pro)", model)
	}
	if hits != 1 {
		t.Errorf("models endpoint hits = %d", hits)
	}
}
