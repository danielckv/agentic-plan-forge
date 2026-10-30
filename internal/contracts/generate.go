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
	// RoleTechLead integrates the code review and judgment findings into the
	// candidate, running the one bounded repair pass over affected sections.
	RoleTechLead RoleID = "tech_lead"
	// RoleCodeReviewer reviews the assembled candidate's code claims: current
	// code plus proposed changes, never a claim that unimplemented code
	// passed tests.
	RoleCodeReviewer RoleID = "code_reviewer"
	RoleIngestion    RoleID = "ingestion"
	// RoleImpact maps the interpreted requirements onto the repository. It is
	// its own role rather than a second ingestion call so a receipt names the
	// prompt that actually ran.
	RoleImpact RoleID = "impact"
	// RoleNarrator turns the step records the other avatars emit into one
	// short, live statement of what the run is doing. It never produces a
	// draft and never blocks a stage: a narrator failure is dropped.
	RoleNarrator RoleID = "narrator"
	// RoleResearch answers one open question from evidence a detached batch
	// gathered. It runs outside a deliberation run, on its own budget, and its
	// output is a claim with a quoted source rather than a draft.
	RoleResearch RoleID = "research"
	// RoleGapAgent is a run-scoped temporary specialist. The architect proposes
	// one when a brief needs expertise no workstream owner covers; the server
	// admits at most MaxGapAgents per run. A gap agent is a descriptor in run
	// history, never a persisted persona and never a source of new agents.
	RoleGapAgent RoleID = "gap_agent"
)

// GapAgent statuses. The lifecycle is proposed → admitted → queued → running
// → completed → fused → retired; rejected, failed and cancelled are terminal
// alternatives. A retired agent stays inspectable in run history and never
// becomes a settings entry.
const (
	GapProposed  = "proposed"
	GapAdmitted  = "admitted"
	GapQueued    = "queued"
	GapRunning   = "running"
	GapCompleted = "completed"
	GapFused     = "fused"
	GapRetired   = "retired"
	GapRejected  = "rejected"
	GapFailed    = "failed"
	GapCancelled = "cancelled"
)

// GapAgentDescriptor describes one temporary agent. The architect proposes it
// alongside the normal design; the server verifies and admits it. Placement
// names where its deliverable lands: a parent workstream lane it joins, or the
// synthesis.
type GapAgentDescriptor struct {
	ID             string   `json:"id"`
	Specialty      string   `json:"specialty"`
	Purpose        string   `json:"purpose"`
	RequirementIDs []string `json:"requirement_ids"`
	EvidenceRefs   []string `json:"evidence_refs"`
	Deliverable    string   `json:"deliverable"`
	ParentID       string   `json:"parent_id"`
	Placement      string   `json:"placement"`
	Status         string   `json:"status"`
	// Reason records why the server rejected the proposal, retired the agent,
	// or ended its run. It stays inspectable with the descriptor.
	Reason string `json:"reason,omitempty"`
}

// Placement names the host and model that served, or will serve, one role.
// Lane distinguishes two avatars of the same role, such as the Domain
// Specialists of two workstreams, which are placed independently.
type Placement struct {
	Role    RoleID `json:"role"`
	Lane    string `json:"lane,omitempty"`
	Host    string `json:"host"`
	Version string `json:"version,omitempty"`
	Model   string `json:"model,omitempty"`
}

// ModelSlot selects which configured model an invocation uses. The critic runs
// on JUDGE so it cannot simply agree with the drafts it reviews.
type ModelSlot string

// Model slots.
const (
	SlotMain  ModelSlot = "MAIN"
	SlotJudge ModelSlot = "JUDGE"
)

// Effort hints for runtimes that take thinking effort as a request
// parameter. Empty means the host default.
const (
	EffortMedium string = "medium"
	EffortHigh   string = "high"
)

// DefaultGenerateTimeout is the budget one provider call gets when its caller
// asks for none. Ingestion and impact size their inputs against it (ADR 0004):
// a section or an evidence batch is built to finish inside it.
const DefaultGenerateTimeout = 180 * time.Second

// MaxGenerateTimeout is the ceiling a caller may raise its own budget to. The
// deliberation stages need more than the default and say so explicitly: the
// architect design part reads the whole repository graph and writes the
// workstream DAG and every interface contract in one call. Measured on
// `claude -p --model opus` with a 206 KiB payload: 110 seconds, 94,184 input
// tokens, 12,075 output tokens. That is the floor, before project context,
// resolved decisions or a revision's previous draft. See ADR 0009.
const MaxGenerateTimeout = 30 * time.Minute

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
	Role RoleID `json:"role"`
	// Lane names which avatar of the role is calling, when a role runs more
	// than one at a time: the workstream id for a Domain Specialist, empty for
	// a role that has a single avatar. Lanes are placed on hosts
	// independently, so one role's avatars are not all stuck behind one host's
	// rate limit.
	Lane         string          `json:"lane,omitempty"`
	ModelSlot    ModelSlot       `json:"model_slot"`
	Model        string          `json:"model,omitempty"`
	SystemPrompt string          `json:"system_prompt"`
	UserPayload  json.RawMessage `json:"user_payload"`
	SchemaID     string          `json:"schema_id"`
	// Effort is the caller's thinking-effort hint for runtimes that expose
	// one as a request parameter. Empty lets the host apply its own default.
	Effort string `json:"effort,omitempty"`
	// OutputSchema constrains the reply to a JSON Schema at the provider,
	// where the provider supports it (Anthropic structured outputs, the
	// OpenAI-compatible json_schema response format). It is independent of
	// SchemaID: SchemaID names an embedded schema the host validates the
	// terminal result against, and the five avatar stages have none because
	// their documents are strict-decoded in the avatars layer instead. A
	// provider that cannot constrain output ignores this field, so the reply
	// is still only as good as the prompt.
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	RunID        string          `json:"run_id,omitempty"`
	Timeout      time.Duration   `json:"timeout"`
	MaxOutput    int             `json:"max_output_bytes"`
}

// ClampTimeout returns the request timeout bounded by MaxGenerateTimeout, and
// DefaultGenerateTimeout when the caller left it unset.
func (r GenerateRequest) ClampTimeout() time.Duration {
	switch {
	case r.Timeout <= 0:
		return DefaultGenerateTimeout
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
	// EventPlacement reports which host and model a generator selected, before
	// any output arrives. A generator that does not route may never send it.
	EventPlacement GenerationEventKind = "placement"
)

// GenerationEvent is one streamed item from a provider invocation.
type GenerationEvent struct {
	Kind GenerationEventKind `json:"kind"`
	Text string              `json:"text,omitempty"`
	// PublicSummary carries the short user-facing rationale summary. It is an
	// explicit model output field, never a transcript of private reasoning.
	PublicSummary string     `json:"public_summary,omitempty"`
	Usage         *Usage     `json:"usage,omitempty"`
	Placement     *Placement `json:"placement,omitempty"`
	Err           string     `json:"error,omitempty"`
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
