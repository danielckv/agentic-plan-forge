//go:build client && !server

package host

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// TestLiveGeminiNormalize is the manual reproduction of the reported failure:
// a normalize-shaped call with the embedded normalized_prd.v1 schema. It runs
// only with PLAN_FORGE_LIVE_GEMINI=1, because it spends tokens on the owner's
// key and reaches the real API.
func TestLiveGeminiNormalize(t *testing.T) {
	if os.Getenv("PLAN_FORGE_LIVE_GEMINI") != "1" {
		t.Skip("set PLAN_FORGE_LIVE_GEMINI=1 to run against the live Gemini API")
	}
	ForgetCredentials()
	g := &Gemini{}
	payload, err := json.Marshal(map[string]any{
		"task":              "Classify the document into the normalized_prd.v1 object.",
		"document_markdown": "# Build a todo app\nUsers add tasks, mark them done, and see a list. Must work offline.\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := contracts.GenerateRequest{
		Role:         contracts.RoleIngestion,
		ModelSlot:    contracts.SlotMain,
		SystemPrompt: "Return exactly one JSON object satisfying the schema.",
		UserPayload:  payload,
		SchemaID:     "normalized_prd.v1",
		MaxOutput:    1 << 20,
	}
	res, err := g.Generate(context.Background(), req, func(contracts.GenerationEvent) error { return nil })
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(res.Content, &obj); err != nil {
		t.Fatalf("reply is not JSON: %v\n%s", err, res.Content)
	}
	if err := contracts.Validate("normalized_prd.v1", res.Content); err != nil {
		t.Fatalf("reply does not satisfy normalized_prd.v1: %v", err)
	}
	t.Logf("host=%s model-in-receipt=%v keys=%d", res.Receipt.Host, res.Receipt.Argv, len(obj))
}
