//go:build client && !server

package host

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// API generation hosts. These reach a provider over HTTP with the owner's own
// developer API key, alongside the local agent CLIs.
//
// They are separate kinds rather than a mode of the CLI adapters because the
// two are not interchangeable: a CLI host spends a consumer subscription and
// is the only sanctioned way to do so, while an API host spends a Console key
// and bills the key's owner. Keeping them apart is what lets an installation
// hold both and route per role.
const (
	KindAnthropic Kind = "anthropic"
	KindOpenAI    Kind = "openai"
	KindFireworks Kind = "fireworks"
)

// providerOf maps a host kind to its credential name in the key file.
func providerOf(k Kind) string {
	switch k {
	case KindAnthropic:
		return config.ProviderAnthropic
	case KindOpenAI:
		return config.ProviderOpenAI
	case KindFireworks:
		return config.ProviderFireworks
	case KindGemini, KindAntigravity:
		return config.ProviderGoogle
	}
	return ""
}

// credentialCacheTTL bounds how long a loaded key file is reused. The window
// writes keys through the setup modal while a session is open, so a long cache
// would report a just-added provider as unconfigured; a short one keeps the
// per-call cost at one stat instead of one read.
const credentialCacheTTL = 2 * time.Second

var credentialCache struct {
	sync.Mutex
	creds  config.Credentials
	loaded time.Time
	err    error
}

// loadCredentials returns the key file, re-reading it at most every
// credentialCacheTTL.
func loadCredentials() (config.Credentials, error) {
	credentialCache.Lock()
	defer credentialCache.Unlock()
	if !credentialCache.loaded.IsZero() && time.Since(credentialCache.loaded) < credentialCacheTTL {
		return credentialCache.creds, credentialCache.err
	}
	creds, err := config.LoadCredentials()
	credentialCache.creds, credentialCache.err, credentialCache.loaded = creds, err, time.Now()
	return creds, err
}

// ForgetCredentials drops the cache so the next call re-reads the key file.
// The window calls it after the owner saves a key, so a provider connected a
// moment ago serves the next call instead of reporting itself unconfigured.
func ForgetCredentials() {
	credentialCache.Lock()
	credentialCache.loaded = time.Time{}
	credentialCache.Unlock()
}

// apiKey returns the usable key for a kind and where it came from.
func apiKey(k Kind) (key, source string, err error) {
	creds, err := loadCredentials()
	if err != nil {
		return "", "", err
	}
	key, source = creds.Key(providerOf(k))
	return key, source, nil
}

// baseURLFor returns the configured endpoint override for a kind, or "".
func baseURLFor(k Kind) string {
	creds, err := loadCredentials()
	if err != nil {
		return ""
	}
	return creds.BaseURL(providerOf(k))
}

// noKeyError is the capability failure for a provider with no key. It names
// the environment variable and the console page, because those are the two
// things the owner needs and neither is guessable.
func noKeyError(k Kind) error {
	provider := providerOf(k)
	return &contracts.ErrCapability{
		Host:   string(k),
		Reason: config.ProviderLabel(provider) + " has no API key",
		Remedy: "add one in Setup, or export " + config.ProviderKeyEnv(provider) + " (create a key at " + config.ProviderConsoleURL(provider) + ")",
	}
}

// cacheThresholdBytes is the payload size above which a request marks the user
// payload as cacheable. Below it the provider's minimum cacheable prefix is not
// reached and the breakpoint is wasted; above it a repeated stage or a revision
// cycle reads the prefix instead of paying for it again.
const cacheThresholdBytes = 8 << 10

// apiMaxTokens bounds one API reply. Streaming is always on for these
// adapters, so a large ceiling costs nothing when the model stops early and
// avoids a truncated document when it does not.
const apiMaxTokens = 32000

// outputSchemaMap decodes a request's JSON Schema for a provider that
// constrains output. A request carrying none, or carrying one that is not a
// JSON object, returns nil and the call falls back to the prompt's own reply
// shape.
func outputSchemaMap(req contracts.GenerateRequest) map[string]any {
	raw := schemaForRequest(req)
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// schemaForRequest returns the schema to constrain output with: the explicit
// OutputSchema when the caller supplied one, otherwise the embedded schema
// named by SchemaID. The five avatar stages carry OutputSchema because their
// documents are strict-decoded in the avatars layer and have no embedded
// schema; ingestion and impact carry SchemaID.
func schemaForRequest(req contracts.GenerateRequest) json.RawMessage {
	if len(req.OutputSchema) != 0 {
		return req.OutputSchema
	}
	if strings.TrimSpace(req.SchemaID) == "" {
		return nil
	}
	raw, err := contracts.SchemaBytes(req.SchemaID)
	if err != nil {
		return nil
	}
	return raw
}

// apiReceiptArgs records how an API call was made. It names the method, the
// endpoint and the model, and never the key: a receipt is written to disk and
// quoted in reports.
func apiReceiptArgs(endpoint, model string) []string {
	args := []string{"POST", endpoint}
	if model != "" {
		args = append(args, "model="+model)
	}
	return args
}

// modelCatalogue is a provider's fallback model list, used when the live list
// cannot be fetched. It is deliberately short: the point is that a run can
// still be placed, not that the window shows every model a provider sells.
func modelCatalogue(k Kind) []ModelRef {
	switch k {
	case KindAnthropic:
		return []ModelRef{
			{ID: anthropicMainModel, Label: "Claude Opus 5.5"},
			{ID: anthropicJudgeModel, Label: "Claude Sonnet 5.5"},
			{ID: anthropicSmallModel, Label: "Claude Haiku 5.5"},
		}
	case KindOpenAI:
		return []ModelRef{{ID: openAIDefaultModel, Label: openAIDefaultModel}}
	case KindFireworks:
		return []ModelRef{{ID: fireworksDefaultModel, Label: "Fireworks " + fireworksDefaultModel}}
	case KindGemini, KindAntigravity:
		return []ModelRef{
			{ID: geminiModelPreference[0], Label: "Gemini " + geminiModelPreference[0]},
			{ID: geminiDefaultModel, Label: "Gemini " + geminiDefaultModel},
		}
	}
	return nil
}

// ModelCatalogue is the exported fallback catalogue for the settings window:
// the same short list, served when a host's live list cannot be fetched.
func ModelCatalogue(k Kind) []ModelRef { return modelCatalogue(k) }

// apiKindLabels are the display names the settings window shows per
// generation host.
var apiKindLabels = map[Kind]string{
	KindAnthropic:   "Anthropic API",
	KindOpenAI:      "OpenAI-compatible API",
	KindFireworks:   "Fireworks API",
	KindGemini:      "Google Gemini API",
	KindAntigravity: "Google Antigravity (managed)",
}

// APIKinds lists the SDK generation hosts in display order. CLI
// integrations (claude, agy) are not generation candidates, and OMP is
// removed this release.
func APIKinds() []Kind {
	return []Kind{KindAnthropic, KindGemini, KindAntigravity, KindOpenAI, KindFireworks}
}

// KindLabel is the display name for one generation host, or the kind itself
// when the host has no friendlier name.
func KindLabel(k Kind) string {
	if label, ok := apiKindLabels[k]; ok {
		return label
	}
	return string(k)
}

// anthropicSlotModel picks the Anthropic model for one request: the caller's
// explicit choice first, then the slot default. The OpenAI-compatible hosts
// resolve their own model against the provider's live list instead, because
// those providers rotate which models they serve.
func anthropicSlotModel(req contracts.GenerateRequest) string {
	if model := strings.TrimSpace(req.Model); model != "" {
		return model
	}
	if req.Role == contracts.RoleNarrator {
		return anthropicSmallModel
	}
	if req.ModelSlot == contracts.SlotJudge {
		return anthropicJudgeModel
	}
	return anthropicMainModel
}

// apiFailure wraps a provider error with the host that produced it, so a
// failed run names which provider refused rather than reporting a bare HTTP
// status with no owner.
func apiFailure(k Kind, stage string, err error) error {
	return fmt.Errorf("%s %s: %w", k, stage, err)
}
