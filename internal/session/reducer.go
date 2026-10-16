// Package session implements the authoritative session reducer: phases,
// roles, command validation, deduplication, counters, the executor lease and
// the approval ordering. Solo mode runs this reducer in-process; the relay
// wraps the same reducer with network transport. There is exactly one
// authority implementation; only transport differs.
//
// This package imports only internal/contracts (and stdlib). The wire envelope
// lives in package relay; if this package imported relay the relay hub could
// not import the reducer without an import cycle under the server build tag.
// The hub therefore converts between relay.WSEnvelope and the event structs
// defined here, and hub tests assert that conversion is faithful.
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Mirror of relay.Phase. The hub converts; see the package comment.
type Phase string

const (
	PhaseIngestion        Phase = "INGESTION"
	PhaseDeliberation     Phase = "DELIBERATION"
	PhaseSynthesis        Phase = "SYNTHESIS"
	PhaseApproving        Phase = "APPROVING"
	PhaseApprovingUnknown Phase = "APPROVING_UNKNOWN"
	PhaseApproved         Phase = "APPROVED"
	PhaseCancelled        Phase = "CANCELLED"
)

// Mirror of relay.Role.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// CanExecute reports whether role may claim the executor lease. Only the owner
// machine holds the repository and the AST graph, so cross-machine handoff is
// unsupported by design.
func (r Role) CanExecute() bool { return r == RoleOwner }

// Stage IDs, mirroring relay constants.
const (
	StagePM          = "pm"
	StageArchitect   = "architect"
	StageCritic      = "critic"
	StageSynthesizer = "synthesizer"
	// StageSpecialistPrefix prefixes specialist stage IDs.
	StageSpecialistPrefix = "specialist:"
)

// MaxCycles is the hard cap on automatic deliberation cycles. A user-requested
// rerun after the cap gets a new run_id; it is never a concealed fourth
// automatic cycle.
const MaxCycles = 3

// DedupWindow is the number of recent event IDs remembered per member. Retried
// commands with the same event_id return the original result.
const DedupWindow = 256

// Lease timings, mirroring relay defaults.
const (
	LeaseTTL   = 15 * time.Second
	LeaseRenew = 5 * time.Second
)

// Error is a coded reducer failure. Code mirrors contracts.ErrorCode so the
// transport can answer with ERROR{code} without importing this package's
// internals.
type Error struct {
	Code contracts.ErrorCode
	Msg  string
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Msg }

func coded(code contracts.ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// Update is one uncomposed document update, mirrored from collab/relay. The
// reducer does not interpret changes; the document authority validates them.
type Update struct {
	ClientID string          `json:"client_id"`
	Changes  json.RawMessage `json:"changes"`
}

// --- commands ---------------------------------------------------------------

// Command is one member- or system-submitted authority input. ActorID is
// authority-assigned (the member identity behind the transport); EventID is
// the client-supplied UUID used for deduplication.
type Command struct {
	EventID string
	ActorID string // member ID; "system" for authority-internal actions
	Type    string // event type name, e.g. "TEXT_DELTA", "FREEZE_REQUEST"
	Payload json.RawMessage
}

// Result is the reducer's full reply to one command: the effects the
// transport must emit, the new counters, and whether state changed.
type Result struct {
	// Ack is the direct reply to the submitter, if any (may be zero).
	Ack Event
	// Broadcast holds events every member must receive, in order.
	Broadcast []Event
	// Revision is the session-state revision after the command.
	Revision uint64
	// DocVersion is the document version after the command, when it moved.
	DocVersion uint64
	// Epoch is the current lease epoch.
	Epoch uint64
	// StateChanged reports whether the revision counter advanced.
	StateChanged bool
	// Rejected carries the coded error when the command was refused.
	Rejected *Error
	// Deduplicated reports an event_id retry served from memory.
	Deduplicated bool
	// InputRevision and SynthesisRev are the per-run counters after the
	// command, so the transport can stamp them without a second snapshot.
	InputRevision uint64
	SynthesisRev  uint64
	// IngestSeq is the per-session ingest_seq assigned to an accepted
	// INGEST_APPEND (zero for every other command). IngestClientID is the
	// document client ID "sys:ingest:<ingest_seq>" the collab authority uses
	// when it appends the text.
	IngestSeq      uint64
	IngestClientID string

	// rememberedPayload is unexported deduplication state: the payload the
	// event_id first arrived with, for content comparison on retry.
	rememberedPayload json.RawMessage
}

// Event is one authority-emitted effect before envelope wrapping. The
// transport assigns sender identity, sequence and timestamp.
type Event struct {
	Type    string
	Payload any
	// UnicastTo, when set, restricts delivery to that member.
	UnicastTo string
	// Ephemeral marks a latest-value slot (AVATAR_THOUGHT) that the transport
	// coalesces and must not keep in the durable replay ring.
	Ephemeral bool
}

// --- state ------------------------------------------------------------------

// Member is one joined identity.
type Member struct {
	ID       string
	Role     Role
	JoinedAt time.Time
}

// Decision is one human decision card.
type Decision struct {
	ID                  string
	InputRevision       uint64
	Question            string
	Options             []contracts.DecisionOption
	RecommendedOptionID string
	ResolvedOptionID    string
	ResolvedBy          string
	ResolvedAt          *time.Time
}

// Lease is the executor lease state.
type Lease struct {
	Holder  string
	Epoch   uint64
	Expires time.Time
}

// Snapshot is the authoritative state for STATE_SNAPSHOT and resume.
type Snapshot struct {
	Phase            Phase
	BaselineRev      uint64
	BaselineHash     string
	RunID            string
	Cycle            int
	InputRevision    uint64
	SynthesisRev     uint64
	ApprovalID       string
	LeaseEpoch       uint64
	LeaseHolder      string
	Revision         uint64
	PendingDecisions []string
	// SynthesisHash is the content_hash recorded from the latest accepted
	// complete synthesis.v1 draft of the current run.
	SynthesisHash string
	// CycleCapped reports a run paused at the max-cycles cap.
	CycleCapped bool
}

// Reducer is the single authority. All methods are safe for concurrent use.
type Reducer struct {
	mu  sync.Mutex
	now func() time.Time

	phase Phase
	// revision is the session-state revision: +1 per accepted state-changing
	// command or phase change.
	revision uint64
	// baselineRev counts freezes: +1 per FREEZE_REQUEST.
	baselineRev  uint64
	baselineHash string
	// runID is the authority-assigned UUIDv4 for the current run.
	runID string
	// cycle is the current deliberation cycle within the run (1..MaxCycles).
	cycle int
	// cycleCapped reports the run paused at the max-cycles cap.
	cycleCapped bool
	// inputRevision counts accepted AVATAR_DRAFT/AVATAR_CRITIQUE/DECISION_RESOLVE.
	inputRevision uint64
	// synthesisRev counts accepted complete synthesis.v1 drafts.
	synthesisRev  uint64
	synthesisHash string
	// openBlockers counts open CRITICAL/HIGH critiques in the latest
	// accepted critique payload of the current run.
	openBlockers int
	// anyDraftComplete reports an accepted complete draft this run.
	draftsAccepted bool
	// stageFailed records failed stage IDs this run.
	stageFailed map[string]bool

	approvalID       string
	approvedAt       time.Time
	approvedRevision uint64

	// ingestSeq is the per-session INGEST_APPEND counter; it never resets.
	ingestSeq uint64
	// approvedSpecSHA and approvedManifestSHA are the hashes copied from the
	// executor's ARTIFACT_SAVED ok, kept to answer an idempotent replay.
	approvedSpecSHA     string
	approvedManifestSHA string

	lease Lease
	// leaseEpochAlloc is the highest epoch ever granted; epochs never reset.
	leaseEpochAlloc uint64

	decisions map[string]*Decision
	// dedup is the per-member ring of recent event IDs.
	dedup map[string][]string
	// dedupResults replays the prior result for a retried event_id.
	dedupResults map[string]map[string]Result

	members map[string]*Member
}

// New creates a reducer in phase INGESTION with revision 0. now defaults to
// time.Now; tests inject a fake clock.
func New(now func() time.Time) *Reducer {
	if now == nil {
		now = time.Now
	}
	return &Reducer{
		now:          now,
		phase:        PhaseIngestion,
		stageFailed:  map[string]bool{},
		decisions:    map[string]*Decision{},
		dedup:        map[string][]string{},
		dedupResults: map[string]map[string]Result{},
		members:      map[string]*Member{},
	}
}

// Snapshot returns the authoritative state.
func (r *Reducer) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.internalSnapshot()
}

func (r *Reducer) internalSnapshot() Snapshot {
	var pending []string
	for id, d := range r.decisions {
		if d.ResolvedOptionID == "" {
			pending = append(pending, id)
		}
	}
	holder := r.lease.Holder
	if r.leaseExpiredLocked() {
		holder = ""
	}
	return Snapshot{
		Phase:            r.phase,
		BaselineRev:      r.baselineRev,
		BaselineHash:     r.baselineHash,
		RunID:            r.runID,
		Cycle:            r.cycle,
		InputRevision:    r.inputRevision,
		SynthesisRev:     r.synthesisRev,
		ApprovalID:       r.approvalID,
		LeaseEpoch:       r.leaseEpochAlloc,
		LeaseHolder:      holder,
		Revision:         r.revision,
		PendingDecisions: pending,
		SynthesisHash:    r.synthesisHash,
		CycleCapped:      r.cycleCapped,
	}
}

// Phase returns the current phase.
func (r *Reducer) Phase() Phase {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.phase
}

// SynthesisHash returns the content_hash recorded from the latest accepted
// complete synthesis.v1 draft of the current run, or "" when none exists.
func (r *Reducer) SynthesisHash() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.synthesisHash
}

// Join registers a member. It is transport-facing bookkeeping; the relay hub
// owns credential checks, this reducer only records identity and role.
func (r *Reducer) Join(id string, role Role) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.members[id] = &Member{ID: id, Role: role, JoinedAt: r.now()}
}

// Leave removes a member.
func (r *Reducer) Leave(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.members, id)
	if r.lease.Holder == id {
		// The lease expires by TTL; the authority never auto-elects.
		r.lease.Holder = ""
	}
}

// Role reports a member's role, or "" when unknown.
func (r *Reducer) Role(id string) Role {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.members[id]; ok {
		return m.Role
	}
	return ""
}

// --- dispatch ---------------------------------------------------------------

// Apply validates and applies one command. The returned Result is complete:
// effects, counters and any coded rejection. A retried event_id with
// identical content returns the original result; changed content with the
// same event_id is EVENT_ID_CONFLICT.
func (r *Reducer) Apply(cmd Command) Result {
	r.mu.Lock()
	defer r.mu.Unlock()

	if cmd.EventID == "" {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "event_id is required")}
	}
	if cmd.Type == "" || !validCommandType(cmd.Type) {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "unknown event type %q", cmd.Type)}
	}

	if prior, payload, ok := r.dedupLookup(cmd.ActorID, cmd.EventID); ok {
		if !jsonEqual(payload, cmd.Payload) {
			return Result{Rejected: coded(contracts.CodeEventIDConflict,
				"event_id %s was already used with different content", cmd.EventID)}
		}
		prior.Deduplicated = true
		return prior
	}

	res := r.dispatch(cmd)
	res.Revision = r.revision
	res.Epoch = r.leaseEpochAlloc
	if res.Rejected == nil && res.StateChanged {
		r.revision++
		res.Revision = r.revision
	}
	res.InputRevision = r.inputRevision
	res.SynthesisRev = r.synthesisRev
	if cmd.ActorID != "system" {
		r.dedupRemember(cmd.ActorID, cmd.EventID, cmd.Payload, res)
	}
	return res
}

func (r *Reducer) dispatch(cmd Command) Result {
	actor := r.members[cmd.ActorID]
	if cmd.ActorID == "system" {
		actor = &Member{ID: "system", Role: RoleOwner}
	}
	if actor == nil {
		return Result{Rejected: coded(contracts.CodeRoleForbidden, "unknown member %s", cmd.ActorID)}
	}

	switch cmd.Type {
	case "TEXT_DELTA", "TEXT_CHECK", "TEXT_SNAPSHOT", "PRESENCE":
		// Doc-channel events are validated and applied by the collab
		// authority; the reducer only gates phase and role.
		return r.docChannel(actor, cmd)
	case "INGEST_APPEND":
		return r.ingestAppend(actor, cmd)
	case "AMBIGUITY_RESOLVE":
		return r.ambiguityResolve(actor, cmd)
	case "FREEZE_REQUEST":
		return r.freezeRequest(actor)
	case "LEASE_CLAIM":
		return r.leaseClaim(actor)
	case "LEASE_RENEW":
		return r.leaseRenew(actor)
	case "LEASE_RELEASE":
		return r.leaseRelease(actor)
	case "ARTIFACT_SYNCED":
		return r.artifactSynced(cmd)
	case "CYCLE_START", "AVATAR_THOUGHT", "AVATAR_DRAFT", "AVATAR_CRITIQUE",
		"SYNTHESIZE_REQUEST", "RETRY_STAGE", "ARTIFACT_SAVED",
		"DECISION_PROMPT":
		return r.executorEvent(actor, cmd)
	case "DECISION_RESOLVE":
		return r.decisionResolve(actor, cmd)
	case "APPROVE":
		return r.approve(actor, cmd)
	case "CANCEL":
		return r.cancel(actor)
	case "EDIT_BASELINE":
		return r.editBaseline(actor, cmd)
	default:
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "unhandled event type %q", cmd.Type)}
	}
}

// docChannel gates TEXT_* events by phase and role. The document authority
// performs the actual validation; the reducer only marks that state did not
// change (doc version changes are counted in the doc channel, not in the
// session revision).
func (r *Reducer) docChannel(actor *Member, cmd Command) Result {
	if actor.Role != RoleOwner && actor.Role != RoleEditor {
		return Result{Rejected: coded(contracts.CodeRoleForbidden,
			"role %s may not edit the document", actor.Role)}
	}
	if r.phase != PhaseIngestion {
		return Result{Rejected: coded(contracts.CodeIllegalPhase,
			"document edits are closed in phase %s", r.phase)}
	}
	return Result{Epoch: r.leaseEpochAlloc}
}

// MaxIngestBytes caps the markdown of one INGEST_APPEND (256 KiB).
const MaxIngestBytes = 256 << 10

// Ingest sources accepted by INGEST_APPEND. Only the owner uploads; the owner
// and editors may append composer text.
const (
	IngestSourceUpload   = "upload"
	IngestSourceComposer = "composer"
)

// ingestAppend gates INGEST_APPEND by role, source, phase and size, and
// assigns the per-session ingest_seq. The collab authority appends the text
// under client ID sys:ingest:<ingest_seq>; the reducer never touches the text.
func (r *Reducer) ingestAppend(actor *Member, cmd Command) Result {
	if actor.Role != RoleOwner && actor.Role != RoleEditor {
		return Result{Rejected: coded(contracts.CodeRoleForbidden, "role %s may not append ingested text", actor.Role)}
	}
	var p struct {
		Source     string `json:"source"`
		SourceName string `json:"source_name"`
		Markdown   string `json:"markdown"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "ingest payload: %v", err)}
	}
	switch p.Source {
	case IngestSourceUpload:
		if actor.Role != RoleOwner {
			return Result{Rejected: coded(contracts.CodeRoleForbidden, "only the owner may upload documents")}
		}
	case IngestSourceComposer:
	default:
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "unknown ingest source %q", p.Source)}
	}
	if r.phase != PhaseIngestion {
		return Result{Rejected: coded(contracts.CodeIllegalPhase,
			"INGEST_APPEND is closed in phase %s", r.phase)}
	}
	if p.Markdown == "" {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "markdown is required")}
	}
	if len(p.Markdown) > MaxIngestBytes {
		return Result{Rejected: coded(contracts.CodeInvalidPayload,
			"markdown is %d bytes, over the %d byte cap", len(p.Markdown), MaxIngestBytes)}
	}
	r.ingestSeq++
	return Result{
		IngestSeq:      r.ingestSeq,
		IngestClientID: fmt.Sprintf("sys:ingest:%d", r.ingestSeq),
		Epoch:          r.leaseEpochAlloc,
	}
}

func (r *Reducer) ambiguityResolve(actor *Member, cmd Command) Result {
	if actor.Role != RoleOwner {
		return Result{Rejected: coded(contracts.CodeRoleForbidden, "only the owner resolves ambiguities")}
	}
	if r.phase != PhaseIngestion {
		return Result{Rejected: coded(contracts.CodeIllegalPhase,
			"AMBIGUITY_RESOLVE is closed in phase %s", r.phase)}
	}
	return Result{StateChanged: true, Epoch: r.leaseEpochAlloc}
}

// freezeRequest moves INGESTION -> DELIBERATION, assigns run_id and baseline
// hash, and emits PAGE_TRANSITION plus RUN_ASSIGNED before the first
// CYCLE_START.
func (r *Reducer) freezeRequest(actor *Member) Result {
	if actor.Role != RoleOwner {
		return Result{Rejected: coded(contracts.CodeNotApprover, "only the owner confirms the baseline")}
	}
	if r.phase != PhaseIngestion {
		return Result{Rejected: coded(contracts.CodeIllegalPhase,
			"FREEZE_REQUEST is closed in phase %s", r.phase)}
	}
	from := r.phase
	r.baselineRev++
	r.baselineHash = randomHex(32)
	r.resetRunLocked(newUUIDv4(), 1)
	r.setPhase(PhaseDeliberation)
	return Result{
		Broadcast: []Event{
			{Type: "PAGE_TRANSITION", Payload: map[string]any{
				"from": string(from), "to": string(PhaseDeliberation),
				"expected_revision": dec(r.revision), "baseline_hash": r.baselineHash,
			}},
			r.runAssignedEvent(),
		},
		StateChanged: true,
		Epoch:        r.leaseEpochAlloc,
	}
}

// executorEvent gates every executor-emitted run event by phase, lease
// holder and epoch, and advances the input/synthesis counters.
func (r *Reducer) executorEvent(actor *Member, cmd Command) Result {
	if actor.Role != RoleOwner {
		return Result{Rejected: coded(contracts.CodeRoleForbidden, "only the executor reports run events")}
	}
	if r.lease.Holder == "" || r.leaseExpiredLocked() {
		return Result{Rejected: coded(contracts.CodeFenced, "no executor lease is held")}
	}
	if r.lease.Holder != cmd.ActorID {
		return Result{
			Rejected: coded(contracts.CodeFenced, "executor events are fenced (holder %s)", r.lease.Holder),
			Ack: Event{Type: "FENCED", Payload: map[string]any{
				"current_epoch": dec(r.lease.Epoch),
			}, UnicastTo: actor.ID},
		}
	}
	if r.lease.Epoch != r.leaseEpochAlloc {
		return Result{
			Rejected: coded(contracts.CodeFenced, "epoch %d is obsolete (current %d)", r.lease.Epoch, r.leaseEpochAlloc),
			Ack: Event{Type: "FENCED", Payload: map[string]any{
				"current_epoch": dec(r.leaseEpochAlloc),
			}, UnicastTo: actor.ID},
		}
	}

	switch cmd.Type {
	case "CYCLE_START":
		var p struct {
			RunID     string `json:"run_id"`
			GraphHash string `json:"graph_hash"`
			Cycle     int    `json:"cycle"`
		}
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return Result{Rejected: coded(contracts.CodeInvalidPayload, "cycle payload: %v", err)}
		}
		if r.phase != PhaseDeliberation {
			return Result{Rejected: coded(contracts.CodeIllegalPhase, "CYCLE_START requires DELIBERATION, have %s", r.phase)}
		}
		if e := r.checkRunID(p.RunID); e != nil {
			return Result{Rejected: e}
		}
		if r.cycleCapped {
			return Result{Rejected: coded(contracts.CodeRunSuperseded, "run is paused at the cycle cap")}
		}
		if p.Cycle < 1 || p.Cycle > MaxCycles {
			return Result{Rejected: coded(contracts.CodeInvalidPayload, "cycle %d is outside 1..%d", p.Cycle, MaxCycles)}
		}
		if p.Cycle != r.cycle && p.Cycle != r.cycle+1 {
			return Result{Rejected: coded(contracts.CodeInvalidPayload,
				"cycle %d must be %d or %d", p.Cycle, r.cycle, r.cycle+1)}
		}
		r.cycle = p.Cycle
		return Result{
			Broadcast: []Event{{Type: "CYCLE_START", Payload: map[string]any{
				"run_id": r.runID, "baseline_hash": r.baselineHash, "graph_hash": p.GraphHash,
				"cycle": r.cycle, "executor_id": cmd.ActorID, "lease_epoch": dec(r.leaseEpochAlloc),
			}}},
			StateChanged: true,
			Epoch:        r.leaseEpochAlloc,
		}
	case "AVATAR_THOUGHT":
		var p struct {
			RunID    string `json:"run_id"`
			AvatarID string `json:"avatar_id"`
			Status   string `json:"status"`
		}
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return Result{Rejected: coded(contracts.CodeInvalidPayload, "thought payload: %v", err)}
		}
		if r.phase != PhaseDeliberation && r.phase != PhaseSynthesis {
			return Result{Rejected: coded(contracts.CodeIllegalPhase, "AVATAR_THOUGHT requires DELIBERATION or SYNTHESIS, have %s", r.phase)}
		}
		if e := r.checkRunID(p.RunID); e != nil {
			return Result{Rejected: e}
		}
		if p.AvatarID != "" && strings.EqualFold(p.Status, "failed") {
			r.stageFailed[p.AvatarID] = true
		}
		// Ephemeral: no revision change, never durable.
		return Result{
			Broadcast: []Event{{Type: "AVATAR_THOUGHT", Payload: json.RawMessage(cmd.Payload), Ephemeral: true}},
			Epoch:     r.leaseEpochAlloc,
		}
	case "AVATAR_DRAFT":
		return r.avatarDraft(cmd)
	case "AVATAR_CRITIQUE":
		return r.avatarCritique(cmd)
	case "SYNTHESIZE_REQUEST":
		return r.synthesizeRequest(cmd)
	case "RETRY_STAGE":
		return r.retryStage(cmd)
	case "DECISION_PROMPT":
		return r.decisionPrompt(cmd)
	case "ARTIFACT_SAVED":
		return r.artifactSaved(cmd)
	}
	return Result{Rejected: coded(contracts.CodeInternal, "unreachable executor event %q", cmd.Type)}
}

// artifactSynced records one member's artifact sync result. Followers, not
// only the executor, report it; a follower failure never reverts the approval.
func (r *Reducer) artifactSynced(cmd Command) Result {
	if r.phase != PhaseApproved {
		return Result{Rejected: coded(contracts.CodeIllegalPhase, "ARTIFACT_SYNCED requires APPROVED, have %s", r.phase)}
	}
	if !json.Valid(cmd.Payload) {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "artifact-synced payload is not valid JSON")}
	}
	return Result{
		Broadcast:    []Event{{Type: "ARTIFACT_SYNCED", Payload: json.RawMessage(cmd.Payload)}},
		StateChanged: true,
		Epoch:        r.leaseEpochAlloc,
	}
}

// avatarDraft accepts one draft of the current run and counts it in
// input_revision. A complete synthesis.v1 draft (only valid in SYNTHESIS)
// also increments synthesis_rev and records its content_hash; the reducer does
// not recompute the hash, the approval path compares against it.
func (r *Reducer) avatarDraft(cmd Command) Result {
	if r.phase != PhaseDeliberation && r.phase != PhaseSynthesis {
		return Result{Rejected: coded(contracts.CodeIllegalPhase, "AVATAR_DRAFT requires DELIBERATION or SYNTHESIS, have %s", r.phase)}
	}
	var p struct {
		RunID       string `json:"run_id"`
		SchemaID    string `json:"schema_id"`
		ContentHash string `json:"content_hash"`
		Complete    bool   `json:"complete"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "draft payload: %v", err)}
	}
	if e := r.checkRunID(p.RunID); e != nil {
		return Result{Rejected: e}
	}
	isSynthesis := p.SchemaID == "synthesis.v1"
	if isSynthesis {
		if r.phase != PhaseSynthesis {
			return Result{Rejected: coded(contracts.CodeIllegalPhase, "synthesis.v1 drafts require SYNTHESIS, have %s", r.phase)}
		}
		if p.Complete && !isHex64(p.ContentHash) {
			return Result{Rejected: coded(contracts.CodeInvalidPayload, "a complete synthesis.v1 draft needs a 64-hex content_hash")}
		}
	}
	r.inputRevision++
	if p.Complete {
		r.draftsAccepted = true
	}
	if isSynthesis && p.Complete {
		r.synthesisRev++
		r.synthesisHash = p.ContentHash
	}
	delete(r.stageFailed, stageOfDraft(cmd.Payload))
	return Result{
		Broadcast:    []Event{{Type: "AVATAR_DRAFT", Payload: json.RawMessage(cmd.Payload)}},
		StateChanged: true,
		Epoch:        r.leaseEpochAlloc,
	}
}

// avatarCritique accepts the critic's findings for the current run, replaces
// the open-blocker count and counts the critique in input_revision. Open
// CRITICAL/HIGH issues on the last allowed cycle pause the run at the cap.
func (r *Reducer) avatarCritique(cmd Command) Result {
	if r.phase != PhaseDeliberation && r.phase != PhaseSynthesis {
		return Result{Rejected: coded(contracts.CodeIllegalPhase, "AVATAR_CRITIQUE requires DELIBERATION or SYNTHESIS, have %s", r.phase)}
	}
	var p struct {
		RunID  string               `json:"run_id"`
		Issues []contracts.Critique `json:"issues"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "critique payload: %v", err)}
	}
	if e := r.checkRunID(p.RunID); e != nil {
		return Result{Rejected: e}
	}
	open := 0
	for _, c := range p.Issues {
		if c.Status != "resolved" && (c.Severity == "CRITICAL" || c.Severity == "HIGH") {
			open++
		}
	}
	r.openBlockers = open
	r.inputRevision++
	if open > 0 && r.phase == PhaseDeliberation && r.cycle >= MaxCycles {
		r.cycleCapped = true
	}
	return Result{
		Broadcast:    []Event{{Type: "AVATAR_CRITIQUE", Payload: json.RawMessage(cmd.Payload)}},
		StateChanged: true,
		Epoch:        r.leaseEpochAlloc,
	}
}

func (r *Reducer) synthesizeRequest(cmd Command) Result {
	if r.phase != PhaseDeliberation && r.phase != PhaseSynthesis {
		return Result{Rejected: coded(contracts.CodeIllegalPhase, "SYNTHESIZE_REQUEST requires DELIBERATION, have %s", r.phase)}
	}
	var p struct {
		RunID         string `json:"run_id"`
		InputRevision uint64 `json:"input_revision,string"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "synthesize payload: %v", err)}
	}
	if p.RunID != r.runID {
		return Result{Rejected: coded(contracts.CodeStaleRun, "run %s is not the current run %s", p.RunID, r.runID)}
	}
	if p.InputRevision != r.inputRevision {
		return Result{Rejected: coded(contracts.CodeStaleRun,
			"input_revision %d is stale (current %d)", p.InputRevision, r.inputRevision)}
	}
	if r.openBlockers > 0 {
		return Result{Rejected: coded(contracts.CodeBlockersOpen, "%d CRITICAL/HIGH critiques are open", r.openBlockers)}
	}
	if !r.draftsAccepted {
		return Result{Rejected: coded(contracts.CodeBlockersOpen, "no validated draft exists for this run")}
	}
	if pending := len(r.pendingDecisions()); pending > 0 {
		return Result{Rejected: coded(contracts.CodeDecisionsPending, "%d decisions are pending", pending)}
	}
	from := r.phase
	r.setPhase(PhaseSynthesis)
	return Result{
		Broadcast: []Event{{Type: "PAGE_TRANSITION", Payload: map[string]any{
			"from": string(from), "to": string(PhaseSynthesis), "expected_revision": dec(r.revision),
		}}},
		StateChanged: true,
		Epoch:        r.leaseEpochAlloc,
	}
}

// retryStage re-runs one stage. run_id must equal the current run (STALE_RUN).
// Below the cap the stage re-runs under the same run_id: no RUN_ASSIGNED and
// input_revision continues. At the cap the authority assigns a new run_id,
// emits RUN_SUPERSEDED and RUN_ASSIGNED, and resets the cycle counter to 1;
// it is never a concealed fourth automatic cycle.
func (r *Reducer) retryStage(cmd Command) Result {
	var p struct {
		RunID   string `json:"run_id"`
		StageID string `json:"stage_id"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "retry payload: %v", err)}
	}
	if p.RunID == "" || p.StageID == "" {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "run_id and stage_id are required")}
	}
	if !validStageID(p.StageID) {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "unknown stage_id %q", p.StageID)}
	}
	if r.phase != PhaseDeliberation && r.phase != PhaseSynthesis {
		return Result{Rejected: coded(contracts.CodeIllegalPhase, "RETRY_STAGE requires DELIBERATION or SYNTHESIS, have %s", r.phase)}
	}
	if p.RunID != r.runID {
		return Result{Rejected: coded(contracts.CodeStaleRun, "run %s is not the current run %s", p.RunID, r.runID)}
	}
	if r.cycleCapped {
		old := r.runID
		r.resetRunLocked(newUUIDv4(), 1)
		return Result{
			Broadcast: []Event{
				{Type: "RUN_SUPERSEDED", Payload: map[string]any{"old_run_id": old}},
				r.runAssignedEvent(),
			},
			StateChanged: true,
			Epoch:        r.leaseEpochAlloc,
		}
	}
	delete(r.stageFailed, p.StageID)
	return Result{StateChanged: true, Epoch: r.leaseEpochAlloc}
}

// decisionPrompt opens a human decision card. The authority records the
// current input_revision as the card's expected revision and rebroadcasts the
// prompt with that authoritative value.
func (r *Reducer) decisionPrompt(cmd Command) Result {
	if r.phase != PhaseDeliberation && r.phase != PhaseSynthesis {
		return Result{Rejected: coded(contracts.CodeIllegalPhase, "DECISION_PROMPT requires DELIBERATION or SYNTHESIS, have %s", r.phase)}
	}
	var p struct {
		DecisionID    string                     `json:"decision_id"`
		RunID         string                     `json:"run_id"`
		Question      string                     `json:"question"`
		Options       []contracts.DecisionOption `json:"options"`
		RecommendedID string                     `json:"recommended_option_id"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "decision payload: %v", err)}
	}
	if e := r.checkRunID(p.RunID); e != nil {
		return Result{Rejected: e}
	}
	if p.DecisionID == "" {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "decision_id is required")}
	}
	if len(p.Options) < 2 || len(p.Options) > 4 {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "a decision needs 2-4 options, got %d", len(p.Options))}
	}
	seen := map[string]bool{}
	for _, o := range p.Options {
		if o.ID == "" || seen[o.ID] {
			return Result{Rejected: coded(contracts.CodeInvalidPayload, "decision options need distinct non-empty IDs")}
		}
		seen[o.ID] = true
	}
	if p.RecommendedID != "" && !seen[p.RecommendedID] {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "recommended option %s is not offered", p.RecommendedID)}
	}
	if _, exists := r.decisions[p.DecisionID]; exists {
		return Result{Rejected: coded(contracts.CodeAlreadyResolved, "decision %s already exists", p.DecisionID)}
	}
	r.decisions[p.DecisionID] = &Decision{
		ID:                  p.DecisionID,
		InputRevision:       r.inputRevision,
		Question:            p.Question,
		Options:             p.Options,
		RecommendedOptionID: p.RecommendedID,
	}
	return Result{
		Broadcast: []Event{{Type: "DECISION_PROMPT", Payload: map[string]any{
			"decision_id": p.DecisionID, "run_id": r.runID, "input_revision": dec(r.inputRevision),
			"question": p.Question, "options": p.Options, "recommended_option_id": p.RecommendedID,
		}}},
		StateChanged: true,
		Epoch:        r.leaseEpochAlloc,
	}
}

// decisionResolve applies the first-wins ordering. The first authorized
// resolution at the expected input_revision wins; later conflicting ones
// receive ALREADY_RESOLVED with the winning option.
func (r *Reducer) decisionResolve(actor *Member, cmd Command) Result {
	if actor.Role != RoleOwner && actor.Role != RoleEditor {
		return Result{Rejected: coded(contracts.CodeRoleForbidden, "viewers cannot resolve decisions")}
	}
	if r.phase != PhaseDeliberation && r.phase != PhaseSynthesis {
		return Result{Rejected: coded(contracts.CodeIllegalPhase, "DECISION_RESOLVE is closed in phase %s", r.phase)}
	}
	var p struct {
		DecisionID    string `json:"decision_id"`
		InputRevision uint64 `json:"input_revision,string"`
		OptionID      string `json:"option_id"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "resolve payload: %v", err)}
	}
	d, ok := r.decisions[p.DecisionID]
	if !ok {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "unknown decision %s", p.DecisionID)}
	}
	// A resolved card answers ALREADY_RESOLVED before any revision check: the
	// winning write bumped input_revision, so later writers always look stale.
	if d.ResolvedOptionID != "" {
		return Result{
			Rejected: coded(contracts.CodeAlreadyResolved,
				"decision %s is already resolved as %s", p.DecisionID, d.ResolvedOptionID),
			Ack: Event{Type: "ALREADY_RESOLVED", Payload: map[string]any{
				"decision_id": p.DecisionID, "option_id": d.ResolvedOptionID, "actor_id": d.ResolvedBy,
			}},
		}
	}
	if p.InputRevision != d.InputRevision {
		return Result{Rejected: coded(contracts.CodeStaleRun,
			"input_revision %d is stale (decision opened at %d)", p.InputRevision, d.InputRevision)}
	}
	if !d.hasOption(p.OptionID) {
		return Result{Rejected: coded(contracts.CodeInvalidPayload,
			"option %s is not offered by decision %s", p.OptionID, p.DecisionID)}
	}
	now := r.now()
	d.ResolvedOptionID = p.OptionID
	d.ResolvedBy = cmd.ActorID
	d.ResolvedAt = &now
	r.inputRevision++
	return Result{
		Ack: Event{Type: "DECISION_RESOLVE", Payload: json.RawMessage(cmd.Payload)},
		Broadcast: []Event{{Type: "DECISION_RESOLVE", Payload: map[string]any{
			"decision_id": p.DecisionID, "input_revision": dec(r.inputRevision),
			"option_id": p.OptionID, "actor_id": cmd.ActorID,
		}}},
		StateChanged: true,
		Epoch:        r.leaseEpochAlloc,
	}
}

func (d *Decision) hasOption(id string) bool {
	for _, o := range d.Options {
		if o.ID == id {
			return true
		}
	}
	return false
}

// approve validates the approval request. A rejection is exactly one code.
// Authorization is checked first (NOT_APPROVER, then FENCED), then the phase
// gate (ILLEGAL_PHASE: only SYNTHESIS may approve; APPROVING never re-approves),
// then the staleness and blocker chain in this fixed order: STALE_BASELINE,
// STALE_RUN, STALE_SYNTHESIS, DECISIONS_PENDING, BLOCKERS_OPEN.
// STALE_SYNTHESIS covers synthesis_rev, an absent synthesis draft and a
// content_sha256 that differs from the recorded synthesis.v1 content_hash.
func (r *Reducer) approve(actor *Member, cmd Command) Result {
	if actor.Role != RoleOwner {
		return Result{Rejected: coded(contracts.CodeNotApprover, "only the owner approves")}
	}
	if r.lease.Holder == "" || r.leaseExpiredLocked() || r.lease.Holder != cmd.ActorID ||
		r.lease.Epoch != r.leaseEpochAlloc {
		return Result{Rejected: coded(contracts.CodeFenced, "approval requires the current executor lease")}
	}
	if r.phase != PhaseSynthesis {
		return Result{Rejected: coded(contracts.CodeIllegalPhase, "approval is closed in phase %s", r.phase)}
	}
	var p struct {
		BaselineRev   uint64 `json:"baseline_rev,string"`
		RunID         string `json:"run_id"`
		SynthesisRev  uint64 `json:"synthesis_rev,string"`
		ContentSHA256 string `json:"content_sha256"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "approve payload: %v", err)}
	}
	if p.BaselineRev != r.baselineRev {
		return Result{Rejected: coded(contracts.CodeStaleBaseline,
			"baseline_rev %d is stale (current %d)", p.BaselineRev, r.baselineRev)}
	}
	if p.RunID != r.runID {
		return Result{Rejected: coded(contracts.CodeStaleRun, "run %s is not the current run %s", p.RunID, r.runID)}
	}
	if r.synthesisRev == 0 {
		return Result{Rejected: coded(contracts.CodeStaleSynthesis, "no complete synthesis draft has been accepted")}
	}
	if p.SynthesisRev != r.synthesisRev {
		return Result{Rejected: coded(contracts.CodeStaleSynthesis,
			"synthesis_rev %d is stale (current %d)", p.SynthesisRev, r.synthesisRev)}
	}
	if p.ContentSHA256 != r.synthesisHash {
		return Result{Rejected: coded(contracts.CodeStaleSynthesis,
			"content_sha256 does not match the recorded synthesis content_hash")}
	}
	if pending := len(r.pendingDecisions()); pending > 0 {
		return Result{Rejected: coded(contracts.CodeDecisionsPending, "%d decisions are pending", pending)}
	}
	if r.openBlockers > 0 {
		return Result{Rejected: coded(contracts.CodeBlockersOpen, "%d CRITICAL/HIGH critiques are open", r.openBlockers)}
	}
	r.approvalID = newUUIDv4()
	r.approvedAt = r.now()
	r.approvedRevision = r.revision
	r.approvedSpecSHA = ""
	r.approvedManifestSHA = ""
	r.setPhase(PhaseApproving)
	return Result{
		Ack: Event{Type: "APPROVAL_PREPARED", Payload: map[string]any{
			"approval_id": r.approvalID, "created_at": r.approvedAt,
		}},
		Broadcast: []Event{{Type: "APPROVAL_PREPARED", Payload: map[string]any{
			"approval_id": r.approvalID, "created_at": r.approvedAt,
		}}},
		StateChanged: true,
		Epoch:        r.leaseEpochAlloc,
	}
}

// artifactSaved finalizes or fails the approval. ok requires both hashes as
// 64-hex digests; PLAN_APPROVED copies them and adds bundle_id (hex SHA-256
// over the 32-byte spec digest followed by the 32-byte manifest digest). A
// failure returns the session to SYNTHESIS with APPROVAL_FAILED. An executor
// lost mid-write is resolved idempotently on return, keyed by approval_id.
func (r *Reducer) artifactSaved(cmd Command) Result {
	var p struct {
		OK             bool   `json:"ok"`
		ApprovalID     string `json:"approval_id"`
		SpecSHA256     string `json:"spec_sha256"`
		ManifestSHA256 string `json:"manifest_sha256"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "artifact payload: %v", err)}
	}
	switch r.phase {
	case PhaseApproving, PhaseApprovingUnknown:
	case PhaseApproved:
		if !p.OK {
			return Result{Rejected: coded(contracts.CodeApprovalFailed, "approval already finalized")}
		}
		if (p.ApprovalID != "" && p.ApprovalID != r.approvalID) ||
			p.SpecSHA256 != r.approvedSpecSHA || p.ManifestSHA256 != r.approvedManifestSHA {
			return Result{Rejected: coded(contracts.CodeApprovalFailed, "approval already finalized with different artifacts")}
		}
		return Result{Epoch: r.leaseEpochAlloc} // idempotent finalize
	default:
		return Result{Rejected: coded(contracts.CodeIllegalPhase,
			"ARTIFACT_SAVED requires APPROVING or APPROVING_UNKNOWN, have %s", r.phase)}
	}
	if p.ApprovalID != "" && p.ApprovalID != r.approvalID {
		return Result{Rejected: coded(contracts.CodeApprovalFailed,
			"approval %s is not the pending approval %s", p.ApprovalID, r.approvalID)}
	}
	if !p.OK {
		r.setPhase(PhaseSynthesis)
		return Result{
			Broadcast:    []Event{{Type: "ERROR", Payload: map[string]any{"code": string(contracts.CodeApprovalFailed)}}},
			StateChanged: true,
			Epoch:        r.leaseEpochAlloc,
		}
	}
	if !isHex64(p.SpecSHA256) || !isHex64(p.ManifestSHA256) {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "ok requires both hashes as 64-hex SHA-256 digests")}
	}
	specRaw, _ := hex.DecodeString(p.SpecSHA256)
	manifestRaw, _ := hex.DecodeString(p.ManifestSHA256)
	bundle := sha256.Sum256(append(specRaw, manifestRaw...))
	r.approvedSpecSHA = p.SpecSHA256
	r.approvedManifestSHA = p.ManifestSHA256
	r.setPhase(PhaseApproved)
	return Result{
		Broadcast: []Event{{Type: "PLAN_APPROVED", Payload: map[string]any{
			"approval_id": r.approvalID, "approved_revision": dec(r.approvedRevision),
			"spec_sha256": p.SpecSHA256, "manifest_sha256": p.ManifestSHA256,
			"bundle_id":    hex.EncodeToString(bundle[:]),
			"persisted_by": cmd.ActorID,
		}}},
		StateChanged: true,
		Epoch:        r.leaseEpochAlloc,
	}
}

// ExecutorLost moves APPROVING -> APPROVING_UNKNOWN when the executor
// disappears mid-write. On return the executor reports on-disk hashes and
// ARTIFACT_SAVED finalizes idempotently by approval_id.
func (r *Reducer) ExecutorLost(executorID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase == PhaseApproving && r.lease.Holder == executorID {
		r.setPhase(PhaseApprovingUnknown)
		// A phase change is a state change: +1 on the session revision.
		r.revision++
	}
}

// cancel is session-terminal: phase CANCELLED, the executor lease is released
// and every later command is refused by its phase gate. Stopping only the run
// is EDIT_BASELINE.
func (r *Reducer) cancel(actor *Member) Result {
	if actor.Role != RoleOwner {
		return Result{Rejected: coded(contracts.CodeRoleForbidden, "only the owner cancels the session")}
	}
	if r.phase == PhaseApproved || r.phase == PhaseCancelled {
		return Result{Rejected: coded(contracts.CodeIllegalPhase, "session is already %s", r.phase)}
	}
	from := r.phase
	r.setPhase(PhaseCancelled)
	r.lease.Holder = ""
	return Result{
		Broadcast: []Event{{Type: "PAGE_TRANSITION", Payload: map[string]any{
			"from": string(from), "to": string(PhaseCancelled), "expected_revision": dec(r.revision),
		}}},
		StateChanged: true,
		Epoch:        r.leaseEpochAlloc,
	}
}

// editBaseline rewinds to INGESTION for a new baseline. EDIT_BASELINE carries
// baseline_rev, which must equal the current baseline (STALE_BASELINE). The
// current run is superseded; the next freeze assigns a new run_id.
func (r *Reducer) editBaseline(actor *Member, cmd Command) Result {
	if actor.Role != RoleOwner {
		return Result{Rejected: coded(contracts.CodeRoleForbidden, "only the owner rewinds the baseline")}
	}
	switch r.phase {
	case PhaseCancelled, PhaseApproved:
		return Result{Rejected: coded(contracts.CodeIllegalPhase, "session is %s; a rewind is closed", r.phase)}
	case PhaseIngestion:
		return Result{Rejected: coded(contracts.CodeIllegalPhase, "session is already in INGESTION")}
	case PhaseApproving, PhaseApprovingUnknown:
		return Result{Rejected: coded(contracts.CodeIllegalPhase, "an approval is in flight; a rewind is closed")}
	}
	var p struct {
		BaselineRev json.RawMessage `json:"baseline_rev"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "edit-baseline payload: %v", err)}
	}
	rev, ok := parseCounter(p.BaselineRev)
	if !ok {
		return Result{Rejected: coded(contracts.CodeInvalidPayload, "baseline_rev is required")}
	}
	if rev != r.baselineRev {
		return Result{Rejected: coded(contracts.CodeStaleBaseline,
			"baseline_rev %d is stale (current %d)", rev, r.baselineRev)}
	}
	from := r.phase
	old := r.runID
	r.resetRunLocked("", 0)
	r.approvalID = ""
	r.setPhase(PhaseIngestion)
	var out []Event
	if old != "" {
		out = append(out, Event{Type: "RUN_SUPERSEDED", Payload: map[string]any{"old_run_id": old}})
	}
	out = append(out, Event{Type: "PAGE_TRANSITION", Payload: map[string]any{
		"from": string(from), "to": string(PhaseIngestion), "expected_revision": dec(r.revision),
	}})
	return Result{
		Broadcast:    out,
		StateChanged: true,
		Epoch:        r.leaseEpochAlloc,
	}
}

// --- lease ------------------------------------------------------------------

func (r *Reducer) leaseClaim(actor *Member) Result {
	if !actor.Role.CanExecute() {
		return Result{Rejected: coded(contracts.CodeRoleForbidden, "role %s cannot hold the executor lease", actor.Role)}
	}
	if r.phase != PhaseDeliberation && r.phase != PhaseIngestion && r.phase != PhaseSynthesis &&
		r.phase != PhaseApproving && r.phase != PhaseApprovingUnknown {
		return Result{Rejected: coded(contracts.CodeIllegalPhase, "lease claim is closed in phase %s", r.phase)}
	}
	if r.lease.Holder != "" && !r.leaseExpiredLocked() {
		return Result{
			Rejected: coded(contracts.CodeFenced, "executor lease is held by %s", r.lease.Holder),
			Ack: Event{Type: "FENCED", Payload: map[string]any{
				"current_epoch": dec(r.lease.Epoch),
			}, UnicastTo: actor.ID},
		}
	}
	// Epochs are monotonic per room: incremented on every grant, never reset.
	r.leaseEpochAlloc++
	r.lease = Lease{Holder: actor.ID, Epoch: r.leaseEpochAlloc, Expires: r.now().Add(LeaseTTL)}
	return Result{
		Ack: Event{Type: "LEASE_GRANTED", Payload: map[string]any{
			"epoch": dec(r.lease.Epoch), "ttl_ms": LeaseTTL.Milliseconds(), "holder": r.lease.Holder,
		}},
		Broadcast: []Event{{Type: "LEASE_GRANTED", Payload: map[string]any{
			"epoch": dec(r.lease.Epoch), "ttl_ms": LeaseTTL.Milliseconds(), "holder": r.lease.Holder,
		}}},
		StateChanged: true,
		Epoch:        r.leaseEpochAlloc,
	}
}

func (r *Reducer) leaseRenew(actor *Member) Result {
	if r.lease.Holder != actor.ID || r.leaseExpiredLocked() {
		return Result{
			Rejected: coded(contracts.CodeFenced, "no current lease to renew"),
			Ack:      Event{Type: "FENCED", Payload: map[string]any{"current_epoch": dec(r.leaseEpochAlloc)}, UnicastTo: actor.ID},
		}
	}
	r.lease.Expires = r.now().Add(LeaseTTL)
	return Result{Epoch: r.leaseEpochAlloc}
}

func (r *Reducer) leaseRelease(actor *Member) Result {
	if r.lease.Holder != actor.ID {
		return Result{Rejected: coded(contracts.CodeFenced, "only the holder releases the lease")}
	}
	if r.phase == PhaseApproving || r.phase == PhaseApprovingUnknown {
		// Approval freezes lease changes.
		return Result{Rejected: coded(contracts.CodeIllegalPhase, "lease changes are frozen in phase %s", r.phase)}
	}
	r.lease.Holder = ""
	return Result{
		Ack:          Event{Type: "LEASE_RELEASE", Payload: map[string]any{"epoch": dec(r.lease.Epoch)}},
		StateChanged: true,
		Epoch:        r.leaseEpochAlloc,
	}
}

func (r *Reducer) leaseExpiredLocked() bool {
	return r.lease.Holder == "" || !r.now().Before(r.lease.Expires)
}

// LeaseSnapshot reports the lease state for diagnostics.
func (r *Reducer) LeaseSnapshot() (holder string, epoch uint64, valid bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lease.Holder, r.lease.Epoch, !r.leaseExpiredLocked()
}

// --- helpers ----------------------------------------------------------------

func (r *Reducer) pendingDecisions() []string {
	var ids []string
	for id, d := range r.decisions {
		if d.ResolvedOptionID == "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func (r *Reducer) setPhase(p Phase) {
	r.phase = p
}

// dec renders a counter as the decimal string used on the wire.
func dec(n uint64) string { return strconv.FormatUint(n, 10) }

// parseCounter reads a uint64 sent either as a decimal string or a bare number.
func parseCounter(raw json.RawMessage) (uint64, bool) {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

// isHex64 reports whether s is a lowercase 64-digit hex SHA-256 digest.
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// validStageID reports whether s is a documented stage_id.
func validStageID(s string) bool {
	switch s {
	case StagePM, StageArchitect, StageCritic, StageSynthesizer:
		return true
	}
	return strings.HasPrefix(s, StageSpecialistPrefix) && len(s) > len(StageSpecialistPrefix)
}

// checkRunID verifies that a run-scoped executor payload names the current
// run. A payload for any other run is RUN_SUPERSEDED; a missing run_id is
// INVALID_PAYLOAD.
func (r *Reducer) checkRunID(runID string) *Error {
	if runID == "" {
		return coded(contracts.CodeInvalidPayload, "run_id is required")
	}
	if r.runID == "" || runID != r.runID {
		return coded(contracts.CodeRunSuperseded, "run %s is not the current run %q", runID, r.runID)
	}
	return nil
}

// resetRunLocked installs a new run identity and clears every per-run
// counter and bookkeeping item: input_revision, synthesis_rev, the recorded
// synthesis hash, blockers, drafts, failed stages and decision cards.
// baseline_rev and the session-wide counters are untouched. Caller holds r.mu.
func (r *Reducer) resetRunLocked(runID string, cycle int) {
	r.runID = runID
	r.cycle = cycle
	r.cycleCapped = false
	r.inputRevision = 0
	r.synthesisRev = 0
	r.synthesisHash = ""
	r.openBlockers = 0
	r.draftsAccepted = false
	r.stageFailed = map[string]bool{}
	r.decisions = map[string]*Decision{}
}

// runAssignedEvent builds RUN_ASSIGNED{run_id, baseline_rev, lease_epoch}.
func (r *Reducer) runAssignedEvent() Event {
	return Event{Type: "RUN_ASSIGNED", Payload: map[string]any{
		"run_id": r.runID, "baseline_rev": dec(r.baselineRev), "lease_epoch": dec(r.leaseEpochAlloc),
	}}
}

func stageOfDraft(payload json.RawMessage) string {
	var p struct {
		AvatarID string `json:"avatar_id"`
		SchemaID string `json:"schema_id"`
	}
	_ = json.Unmarshal(payload, &p)
	if p.AvatarID == "synthesizer" || p.SchemaID == "synthesis.v1" {
		return StageSynthesizer
	}
	return p.AvatarID
}

func (r *Reducer) dedupLookup(memberID, eventID string) (Result, json.RawMessage, bool) {
	seen := r.dedup[memberID]
	for _, id := range seen {
		if id == eventID {
			if m := r.dedupResults[memberID]; m != nil {
				if res, ok := m[eventID]; ok {
					return res, res.rememberedPayload, true
				}
			}
			return Result{Deduplicated: true}, nil, true
		}
	}
	return Result{}, nil, false
}

func (r *Reducer) dedupRemember(memberID, eventID string, payload json.RawMessage, res Result) {
	// The remembered payload lets a retried event_id be compared by content.
	res.rememberedPayload = append(json.RawMessage(nil), payload...)
	seen := append(r.dedup[memberID], eventID)
	if len(seen) > DedupWindow {
		// Drop the oldest ID's remembered result too; the ring stays bounded.
		dropped := seen[0]
		if m := r.dedupResults[memberID]; m != nil {
			delete(m, dropped)
		}
		seen = seen[1:]
	}
	r.dedup[memberID] = seen
	if r.dedupResults[memberID] == nil {
		r.dedupResults[memberID] = map[string]Result{}
	}
	r.dedupResults[memberID][eventID] = res
}

// jsonEqual compares two payloads canonically (sorted keys, trimmed space).
func jsonEqual(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return string(a) == string(b)
	}
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return string(a) == string(b)
	}
	na, erra := json.Marshal(va)
	nb, errb := json.Marshal(vb)
	return erra == nil && errb == nil && string(na) == string(nb)
}

var commandTypes = map[string]bool{
	"TEXT_DELTA": true, "TEXT_CHECK": true, "TEXT_SNAPSHOT": true, "PRESENCE": true,
	"INGEST_APPEND": true, "AMBIGUITY_RESOLVE": true, "FREEZE_REQUEST": true,
	"LEASE_CLAIM": true, "LEASE_RENEW": true, "LEASE_RELEASE": true,
	"CYCLE_START": true, "AVATAR_THOUGHT": true, "AVATAR_DRAFT": true,
	"AVATAR_CRITIQUE": true, "SYNTHESIZE_REQUEST": true, "RETRY_STAGE": true,
	"DECISION_PROMPT": true, "DECISION_RESOLVE": true, "APPROVE": true,
	"CANCEL": true, "EDIT_BASELINE": true, "ARTIFACT_SAVED": true, "ARTIFACT_SYNCED": true,
}

func validCommandType(t string) bool { return commandTypes[t] }

func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; a failure is fatal.
		panic("session: crypto/rand unavailable: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("session: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
