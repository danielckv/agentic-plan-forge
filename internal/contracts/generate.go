package contracts

import (
	"context"
	"encoding/json"
	"time"
)

// RoleID identifies an avatar role. The set is closed: prompts, schemas and the
// UI all key off these values.
type RoleID string

// Avatar roles. Stage 3 of the pipeline runs one RoleID per invocation.
const (
	RoleProductManager RoleID = "pm"
	RoleArchitect      RoleID = "architect"
	RoleSpecialist     RoleID = "specialist"
	RoleCritic         RoleID = "critic"
	RoleSynthesizer    RoleID = "synthesizer"
	RoleIngestion      RoleID = "ingestion"
	// RoleImpact maps the interpreted requirements onto the repository. It is
	// its own role rather than a second ingestion call so a receipt names the
	// prompt that actually ran.
	RoleImpact RoleID = "impact"
)

// ModelSlot selects which configured model an invocation uses. The critic runs
// on JUDGE so it cannot simply agree with the drafts it reviews.
type ModelSlot string

// Model slots.
const (
	SlotMain  ModelSlot = "MAIN"
	SlotJudge ModelSlot = "JUDGE"
)

// MaxGenerateTimeout bounds a single provider call regardless of what a caller
// requests.
const MaxGenerateTimeout = 180 * time.Second

// MaxStageOutputBytes bounds one stage's raw output before schema validation.
const MaxStageOutputBytes = 2 << 20

// MaxStagePayloadBytes bounds one stage's user payload. Output has always been
// bounded; an unbounded payload is how a stage silently grew past the model's
// context window and the host was killed mid-call with no diagnosable error.
// The bound is deliberately generous: the repository graph it carries is
// already cut to ast.SnapshotBudgetBytes, so only a pathological payload trips
// it, and tripping it reports the limit instead of failing inside the provider.
const MaxStagePayloadBytes = 2 << 20

// GenerateRequest is one tool-free provider invocation.
type GenerateRequest struct {
	Role         RoleID          `json:"role"`
	ModelSlot    ModelSlot       `json:"model_slot"`
	Model        string          `json:"model,omitempty"`
	SystemPrompt string          `json:"system_prompt"`
	UserPayload  json.RawMessage `json:"user_payload"`
	SchemaID     string          `json:"schema_id"`
	RunID        string          `json:"run_id,omitempty"`
	Timeout      time.Duration   `json:"timeout"`
	MaxOutput    int             `json:"max_output_bytes"`
}

// ClampTimeout returns the request timeout bounded by MaxGenerateTimeout, and
// the default when the caller left it unset.
func (r GenerateRequest) ClampTimeout() time.Duration {
	switch {
	case r.Timeout <= 0:
		return MaxGenerateTimeout
	case r.Timeout > MaxGenerateTimeout:
		return MaxGenerateTimeout
	default:
		return r.Timeout
	}
}

// ClampMaxOutput returns the output bound, defaulting to MaxStageOutputBytes.
func (r GenerateRequest) ClampMaxOutput() int {
	if r.MaxOutput <= 0 || r.MaxOutput > MaxStageOutputBytes {
		return MaxStageOutputBytes
	}
	return r.MaxOutput
}

// GenerationEventKind enumerates streamed event kinds. Only public material is
// ever emitted: provider private thinking blocks are discarded by adapters.
type GenerationEventKind string

// Generation event kinds.
const (
	EventStatus        GenerationEventKind = "status"
	EventTextDelta     GenerationEventKind = "text_delta"
	EventPublicSummary GenerationEventKind = "public_summary"
	EventUsage         GenerationEventKind = "usage"
	EventFinal         GenerationEventKind = "final"
	EventError         GenerationEventKind = "error"
)

// GenerationEvent is one streamed item from a provider invocation.
type GenerationEvent struct {
	Kind GenerationEventKind `json:"kind"`
	Text string              `json:"text,omitempty"`
	// PublicSummary carries the short user-facing rationale summary. It is an
	// explicit model output field, never a transcript of private reasoning.
	PublicSummary string `json:"public_summary,omitempty"`
	Usage         *Usage `json:"usage,omitempty"`
	Err           string `json:"error,omitempty"`
}

// Usage reports provider token accounting when the adapter can observe it.
type Usage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

// UsageRow accounts for the calls one host, model and role served in this
// session. The token counts are what the host reported; a host that reports
// none leaves them at zero.
type UsageRow struct {
	Host         string `json:"host"`
	Model        string `json:"model"`
	Role         string `json:"role"`
	Requests     int    `json:"requests"`
	Failures     int    `json:"failures"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	Milliseconds int64  `json:"milliseconds"`
}

// UsageReport is the session's provider accounting: one row per host, model and
// role, busiest first, and the totals across them.
type UsageReport struct {
	Rows   []UsageRow `json:"rows"`
	Totals UsageRow   `json:"totals"`
}

// GenerationResult is a completed provider invocation.
type GenerationResult struct {
	// Content is the schema-validated payload produced by the stage.
	Content json.RawMessage `json:"content"`
	// Receipt records how the invocation was launched, for audit.
	Receipt ToolFreeReceipt `json:"receipt"`
	Usage   Usage           `json:"usage"`
}

// ToolFreeReceipt is the evidence that a generation ran without tools.
type ToolFreeReceipt struct {
	Host        string   `json:"host"`
	Version     string   `json:"version"`
	Argv        []string `json:"argv"`
	OverlaySHA  string   `json:"overlay_sha256,omitempty"`
	SettingsSHA string   `json:"settings_sha256,omitempty"`
	Observed    []string `json:"observed_tools,omitempty"`
}

// Generator is the provider boundary consumed by avatars and ingestion. The
// host router implements it; tests inject stubs. Neither implementation is
// visible to callers, which is what keeps avatar packages free of host code.
type Generator interface {
	GenerateStream(ctx context.Context, req GenerateRequest, emit func(GenerationEvent) error) (GenerationResult, error)
}

// PublicEvent is what an avatar stage emits for the session authority. The
// authority owns envelope construction: it assigns sender identity, sequence
// numbers and timestamps, so stages never build envelopes themselves.
type PublicEvent struct {
	Type    string
	Payload any
}
