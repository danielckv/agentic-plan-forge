// Package relay carries the Plan Forge wire protocol: the envelope every peer
// exchanges plus the typed payloads for each event. The file is deliberately
// untagged so the client, the in-process Solo authority and the relay server all
// speak exactly the same protocol.
//
// This package imports internal/contracts only. It must never import host, AST,
// ingestion or UI code: the relay's dependency closure is asserted in CI.
package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// MaxFrameBytes bounds one decoded frame on every connection. It is larger than
// the largest legitimate payload (a 1 MiB compiled spec plus JSON escaping)
// while still refusing unbounded input.
const MaxFrameBytes = 4 << 20

// UIQueueDepth is how many frames the loopback bridge queues for the window
// before it gives up on a page that has stopped draining. Producers that emit
// a burst must stay under it: overrunning the queue destroys the socket.
const UIQueueDepth = 256

// SessionMode selects how a session is hosted.
type SessionMode string

// Session modes.
const (
	ModeSolo   SessionMode = "SOLO"
	ModeRemote SessionMode = "REMOTE"
)

// WSEventType names an event on the wire.
type WSEventType string

// The nine business events defined by the specification.
const (
	EventTextDelta       WSEventType = "TEXT_DELTA"
	EventPageTransition  WSEventType = "PAGE_TRANSITION"
	EventCycleStart      WSEventType = "CYCLE_START"
	EventAvatarThought   WSEventType = "AVATAR_THOUGHT"
	EventAvatarDraft     WSEventType = "AVATAR_DRAFT"
	EventAvatarCritique  WSEventType = "AVATAR_CRITIQUE"
	EventDecisionPrompt  WSEventType = "DECISION_PROMPT"
	EventDecisionResolve WSEventType = "DECISION_RESOLVE"
	EventPlanApproved    WSEventType = "PLAN_APPROVED"
)

// Control events. They are separate from the business events above.
const (
	EventHello             WSEventType = "HELLO"
	EventStateSnapshot     WSEventType = "STATE_SNAPSHOT"
	EventReplayRequest     WSEventType = "REPLAY_REQUEST"
	EventPresence          WSEventType = "PRESENCE"
	EventFreezeRequest     WSEventType = "FREEZE_REQUEST"
	EventLeaseClaim        WSEventType = "LEASE_CLAIM"
	EventLeaseGranted      WSEventType = "LEASE_GRANTED"
	EventLeaseRenew        WSEventType = "LEASE_RENEW"
	EventLeaseRelease      WSEventType = "LEASE_RELEASE"
	EventLeaseLost         WSEventType = "LEASE_LOST"
	EventFenced            WSEventType = "FENCED"
	EventCancel            WSEventType = "CANCEL"
	EventError             WSEventType = "ERROR"
	EventApprove           WSEventType = "APPROVE"
	EventApprovalPrepared  WSEventType = "APPROVAL_PREPARED"
	EventArtifactSaved     WSEventType = "ARTIFACT_SAVED"
	EventArtifactSynced    WSEventType = "ARTIFACT_SYNCED"
	EventSynthesizeRequest WSEventType = "SYNTHESIZE_REQUEST"
	EventRetryStage        WSEventType = "RETRY_STAGE"
	EventEditBaseline      WSEventType = "EDIT_BASELINE"
	EventIngestAppend      WSEventType = "INGEST_APPEND"
	EventRunAssigned       WSEventType = "RUN_ASSIGNED"
	EventAmbiguityResolve  WSEventType = "AMBIGUITY_RESOLVE"
	EventInviteRevoke      WSEventType = "INVITE_REVOKE"
	EventMemberRemove      WSEventType = "MEMBER_REMOVE"
	EventExecutorLeft      WSEventType = "EXECUTOR_LEFT"
	EventExecutorLost      WSEventType = "EXECUTOR_LOST"
	EventRunSuperseded     WSEventType = "RUN_SUPERSEDED"
	EventAlreadyResolved   WSEventType = "ALREADY_RESOLVED"
)

// Document-channel replies.
const (
	EventTextAck      WSEventType = "TEXT_ACK"
	EventTextReject   WSEventType = "TEXT_REJECT"
	EventTextUpdates  WSEventType = "TEXT_UPDATES"
	EventTextSnapshot WSEventType = "TEXT_SNAPSHOT"
	EventTextCheck    WSEventType = "TEXT_CHECK"
)

// Phase is the session phase. PAGE_TRANSITION is emitted by the authority as a
// consequence of a phase change; clients request changes, never assert them.
type Phase string

// Session phases.
const (
	PhaseIngestion        Phase = "INGESTION"
	PhaseDeliberation     Phase = "DELIBERATION"
	PhaseSynthesis        Phase = "SYNTHESIS"
	PhaseApproving        Phase = "APPROVING"
	PhaseApprovingUnknown Phase = "APPROVING_UNKNOWN"
	PhaseApproved         Phase = "APPROVED"
	PhaseCancelled        Phase = "CANCELLED"
)

// Role is a session role. Capabilities are derived from it.
type Role string

// Session roles.
const (
	RoleOwner  Role = "owner"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// WSEnvelope is the wire format for every peer message. The authority supplies
// SenderID, Seq and Timestamp: peer-submitted values are ignored so a member
// cannot impersonate another. Counters are decimal strings because JavaScript
// cannot represent uint64 exactly.
type WSEnvelope struct {
	Version   uint16          `json:"version"`
	SessionID string          `json:"session_id"`
	SenderID  string          `json:"sender_id"`
	EventID   string          `json:"event_id"`
	EventType WSEventType     `json:"event_type"`
	Seq       uint64          `json:"seq,string"`
	Revision  uint64          `json:"revision,string"`
	Epoch     uint64          `json:"epoch,string"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// DecodeEnvelope strictly decodes one frame. Unknown event types, unsupported
// protocol versions, duplicate JSON keys, trailing JSON and invalid UTF-8 are
// all errors: a peer that sends any of them is not speaking this protocol.
func DecodeEnvelope(raw []byte) (WSEnvelope, error) {
	var env WSEnvelope
	if int64(len(raw)) > MaxFrameBytes {
		return env, &contracts.ErrBound{Name: "frame_bytes", Limit: MaxFrameBytes, Observed: int64(len(raw))}
	}
	if !json.Valid(raw) {
		return env, contracts.Coded(contracts.CodeInvalidPayload, "frame is not valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return env, contracts.Wrap(contracts.CodeInvalidPayload, err, "decode envelope")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return env, contracts.Coded(contracts.CodeInvalidPayload, "trailing data after envelope")
	}
	if env.Version != contracts.ProtocolVersion {
		return env, contracts.Coded(contracts.CodeUnsupportedVer, "envelope version %d", env.Version)
	}
	if !env.EventType.Valid() {
		return env, contracts.Coded(contracts.CodeInvalidPayload, "unknown event type %q", env.EventType)
	}
	if env.SessionID == "" || env.EventID == "" {
		return env, contracts.Coded(contracts.CodeInvalidPayload, "envelope missing session_id or event_id")
	}
	return env, nil
}

// Encode serializes an envelope, refusing to exceed the frame bound.
func Encode(env WSEnvelope) ([]byte, error) {
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, contracts.Wrap(contracts.CodeInternal, err, "encode envelope")
	}
	if int64(len(raw)) > MaxFrameBytes {
		return nil, &contracts.ErrBound{Name: "frame_bytes", Limit: MaxFrameBytes, Observed: int64(len(raw))}
	}
	return raw, nil
}

var eventTypes = map[WSEventType]struct{}{
	EventTextDelta: {}, EventPageTransition: {}, EventCycleStart: {}, EventAvatarThought: {},
	EventAvatarDraft: {}, EventAvatarCritique: {}, EventDecisionPrompt: {}, EventDecisionResolve: {},
	EventPlanApproved: {}, EventHello: {}, EventStateSnapshot: {}, EventReplayRequest: {},
	EventPresence: {}, EventFreezeRequest: {}, EventLeaseClaim: {}, EventLeaseGranted: {},
	EventLeaseRenew: {}, EventLeaseRelease: {}, EventLeaseLost: {}, EventFenced: {},
	EventCancel: {}, EventError: {}, EventApprove: {}, EventApprovalPrepared: {},
	EventArtifactSaved: {}, EventArtifactSynced: {}, EventSynthesizeRequest: {}, EventRetryStage: {},
	EventEditBaseline: {}, EventIngestAppend: {}, EventRunAssigned: {}, EventAmbiguityResolve: {},
	EventInviteRevoke: {}, EventMemberRemove: {}, EventExecutorLeft: {}, EventExecutorLost: {},
	EventRunSuperseded: {}, EventAlreadyResolved: {}, EventTextAck: {}, EventTextReject: {},
	EventTextUpdates: {}, EventTextSnapshot: {}, EventTextCheck: {},
}

// Valid reports whether the event type is part of the protocol.
func (t WSEventType) Valid() bool {
	_, ok := eventTypes[t]
	return ok
}

// --- typed payloads -------------------------------------------------------

// Update is one uncomposed CodeMirror update pushed by a client.
type Update struct {
	ClientID string          `json:"client_id"`
	Changes  json.RawMessage `json:"changes"`
}

// TextDeltaPayload pushes document updates. v1 has exactly one editable
// document; unknown document IDs are rejected.
type TextDeltaPayload struct {
	DocumentID  string   `json:"document_id"`
	BaseVersion uint64   `json:"base_version,string"`
	Updates     []Update `json:"updates"`
}

// TextAckPayload acknowledges an accepted push.
type TextAckPayload struct {
	DocumentID string `json:"document_id"`
	Version    uint64 `json:"version,string"`
}

// TextRejectPayload tells a client to rebase and retry, or to resync.
type TextRejectPayload struct {
	DocumentID  string   `json:"document_id"`
	HeadVersion uint64   `json:"head_version,string"`
	Updates     []Update `json:"updates,omitempty"`
	Resync      bool     `json:"resync,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

// TextUpdatesPayload broadcasts accepted updates to other members.
type TextUpdatesPayload struct {
	DocumentID  string   `json:"document_id"`
	FromVersion uint64   `json:"from_version,string"`
	Updates     []Update `json:"updates"`
}

// TextSnapshotPayload carries the whole document for a resync.
type TextSnapshotPayload struct {
	DocumentID string `json:"document_id"`
	Version    uint64 `json:"version,string"`
	Text       string `json:"text"`
	SHA256     string `json:"sha256"`
}

// TextCheckPayload is an idle divergence check.
type TextCheckPayload struct {
	DocumentID string `json:"document_id"`
	Version    uint64 `json:"version,string"`
	SHA256     string `json:"sha256"`
}

// PageTransitionPayload announces a phase change.
type PageTransitionPayload struct {
	From             Phase  `json:"from"`
	To               Phase  `json:"to"`
	ExpectedRevision uint64 `json:"expected_revision,string"`
	BaselineHash     string `json:"baseline_hash,omitempty"`
}

// CycleStartPayload starts a deliberation cycle.
type CycleStartPayload struct {
	RunID        string `json:"run_id"`
	BaselineHash string `json:"baseline_hash"`
	GraphHash    string `json:"graph_hash"`
	Cycle        int    `json:"cycle"`
	ExecutorID   string `json:"executor_id"`
	LeaseEpoch   uint64 `json:"lease_epoch,string"`
}

// AvatarThoughtPayload is the public progress summary for one avatar. It never
// carries provider private reasoning.
type AvatarThoughtPayload struct {
	RunID         string `json:"run_id"`
	AvatarID      string `json:"avatar_id"`
	Cycle         int    `json:"cycle"`
	Status        string `json:"status"`
	PublicSummary string `json:"public_summary"`
}

// AvatarDraftPayload carries one avatar's structured draft.
type AvatarDraftPayload struct {
	RunID         string          `json:"run_id"`
	AvatarID      string          `json:"avatar_id"`
	Cycle         int             `json:"cycle"`
	DraftID       string          `json:"draft_id"`
	DraftRevision uint64          `json:"draft_revision,string"`
	SchemaID      string          `json:"schema_id"`
	Content       json.RawMessage `json:"content"`
	ContentHash   string          `json:"content_hash"`
	Complete      bool            `json:"complete"`
}

// AvatarCritiquePayload carries severity-ranked critiques.
type AvatarCritiquePayload struct {
	RunID  string               `json:"run_id"`
	Cycle  int                  `json:"cycle"`
	Issues []contracts.Critique `json:"issues"`
}

// DecisionPromptPayload opens a human decision card.
type DecisionPromptPayload struct {
	DecisionID          string                     `json:"decision_id"`
	RunID               string                     `json:"run_id"`
	InputRevision       uint64                     `json:"input_revision,string"`
	Question            string                     `json:"question"`
	Options             []contracts.DecisionOption `json:"options"`
	RecommendedOptionID string                     `json:"recommended_option_id,omitempty"`
}

// DecisionResolvePayload resolves a decision card.
type DecisionResolvePayload struct {
	DecisionID    string `json:"decision_id"`
	InputRevision uint64 `json:"input_revision,string"`
	OptionID      string `json:"option_id"`
	ActorID       string `json:"actor_id"`
}

// PlanApprovedPayload announces a persisted approval.
type PlanApprovedPayload struct {
	ApprovalID       string `json:"approval_id"`
	ApprovedRevision uint64 `json:"approved_revision,string"`
	SpecSHA256       string `json:"spec_sha256"`
	ManifestSHA256   string `json:"manifest_sha256"`
	BundleID         string `json:"bundle_id"`
	PersistedBy      string `json:"persisted_by"`
}

// HelloPayload opens a connection.
type HelloPayload struct {
	Proto      uint16 `json:"proto"`
	LastSeq    uint64 `json:"last_seq,string"`
	DocVersion uint64 `json:"doc_version,string"`
	CanExecute bool   `json:"can_execute"`
}

// StateSnapshotPayload resynchronizes a client without embedding document text
// or draft bodies: clients fetch those separately.
type StateSnapshotPayload struct {
	MemberID         string            `json:"member_id"`
	Role             Role              `json:"role"`
	HeadSeq          uint64            `json:"head_seq,string"`
	HeadDocVersion   uint64            `json:"head_doc_version,string"`
	Phase            Phase             `json:"phase"`
	BaselineRev      uint64            `json:"baseline_rev,string"`
	BaselineHash     string            `json:"baseline_hash,omitempty"`
	RunID            string            `json:"run_id,omitempty"`
	InputRevision    uint64            `json:"input_revision,string"`
	SynthesisRev     uint64            `json:"synthesis_rev,string"`
	LeaseEpoch       uint64            `json:"lease_epoch,string"`
	LeaseHolder      string            `json:"lease_holder,omitempty"`
	PendingDecisions []string          `json:"pending_decisions,omitempty"`
	DraftHashes      map[string]string `json:"draft_hashes,omitempty"`
}

// LeaseGrantedPayload reports a granted executor lease.
type LeaseGrantedPayload struct {
	Epoch  uint64 `json:"epoch,string"`
	TTLMS  int64  `json:"ttl_ms"`
	Holder string `json:"holder"`
}

// FencedPayload tells a stale executor that its epoch is obsolete.
type FencedPayload struct {
	CurrentEpoch uint64 `json:"current_epoch,string"`
	RunID        string `json:"run_id,omitempty"`
}

// ErrorPayload carries a coded failure.
type ErrorPayload struct {
	Code    contracts.ErrorCode `json:"code"`
	Message string              `json:"message"`
	RelPath string              `json:"rel_path,omitempty"`
}

// ArtifactSavedPayload reports the outcome of an artifact commit.
type ArtifactSavedPayload struct {
	OK             bool           `json:"ok"`
	SpecSHA256     string         `json:"spec_sha256,omitempty"`
	ManifestSHA256 string         `json:"manifest_sha256,omitempty"`
	Error          *ArtifactError `json:"error,omitempty"`
}

// ArtifactError describes a failed artifact write without leaking local paths.
type ArtifactError struct {
	Code    string `json:"code"`
	RelPath string `json:"rel_path"`
}

// Artifact error codes.
const (
	ArtifactEACCES   = "EACCES"
	ArtifactENOSPC   = "ENOSPC"
	ArtifactEEXIST   = "EEXIST"
	ArtifactEJOURNAL = "EJOURNAL"
	ArtifactEHASH    = "EHASH"
	ArtifactESYMLINK = "ESYMLINK"
	ArtifactEOTHER   = "EOTHER"
)

// ApprovePayload requests approval of the current synthesis revision.
type ApprovePayload struct {
	BaselineRev   uint64 `json:"baseline_rev,string"`
	RunID         string `json:"run_id"`
	SynthesisRev  uint64 `json:"synthesis_rev,string"`
	ContentSHA256 string `json:"content_sha256"`
}

// ApprovalPreparedPayload reports the assigned approval identity.
type ApprovalPreparedPayload struct {
	ApprovalID string    `json:"approval_id"`
	CreatedAt  time.Time `json:"created_at"`
}

// IngestAppendPayload adds parsed text to the shared document.
type IngestAppendPayload struct {
	Source     string `json:"source"`
	SourceName string `json:"source_name,omitempty"`
	Markdown   string `json:"markdown"`
}

// Ingest sources.
const (
	IngestSourceUpload   = "upload"
	IngestSourceComposer = "composer"
)

// RunAssignedPayload announces the authority-assigned run identity.
type RunAssignedPayload struct {
	RunID       string `json:"run_id"`
	BaselineRev uint64 `json:"baseline_rev,string"`
	LeaseEpoch  uint64 `json:"lease_epoch,string"`
}

// RetryStagePayload re-runs one stage.
type RetryStagePayload struct {
	RunID   string `json:"run_id"`
	StageID string `json:"stage_id"`
}

// Stage identifiers used by RetryStage and stage bookkeeping.
const (
	StagePM          = "pm"
	StageArchitect   = "architect"
	StageCritic      = "critic"
	StageSynthesizer = "synthesizer"
)

// StageSpecialistPrefix prefixes specialist stage IDs: specialist:<workstream>.
const StageSpecialistPrefix = "specialist:"

// SynthesizeRequestPayload asks for the master specification.
type SynthesizeRequestPayload struct {
	RunID         string `json:"run_id"`
	InputRevision uint64 `json:"input_revision,string"`
}

// AmbiguityResolvePayload resolves one normalization ambiguity.
type AmbiguityResolvePayload struct {
	AmbiguityID string `json:"ambiguity_id"`
	Choice      string `json:"choice"`
	Note        string `json:"note,omitempty"`
}

// MaxIngestAppendBytes bounds one appended document fragment.
const MaxIngestAppendBytes = 256 << 10

// MaxDocumentBytes bounds the shared document. It is smaller than MaxFrameBytes
// so a snapshot always fits in one frame after JSON escaping.
const MaxDocumentBytes = 512 << 10

// MaxInsertBytes bounds a single inserted run inside one update.
const MaxInsertBytes = 256 << 10

// DefaultLeaseTTL and DefaultLeaseRenew are the executor lease timings.
const (
	DefaultLeaseTTL   = 15 * time.Second
	DefaultLeaseRenew = 5 * time.Second
)

// String renders the payload for logs. Callers must not log payloads that can
// contain document text.
func (p IngestAppendPayload) String() string {
	return fmt.Sprintf("INGEST_APPEND{source:%s,name:%s,bytes:%d}", p.Source, p.SourceName, len(p.Markdown))
}
