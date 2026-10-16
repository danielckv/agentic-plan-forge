package session_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/session"
)

var synthHash = strings.Repeat("a", 64)

type harness struct {
	t *testing.T
	r *session.Reducer
	n int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	r := session.New(func() time.Time { return time.Unix(1_700_000_000, 0) })
	r.Join("owner", session.RoleOwner)
	r.Join("ed", session.RoleEditor)
	r.Join("vw", session.RoleViewer)
	return &harness{t: t, r: r}
}

func dec(n uint64) string { return strconv.FormatUint(n, 10) }

func (h *harness) send(actor, typ string, payload any) session.Result {
	h.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		h.t.Fatal(err)
	}
	h.n++
	return h.r.Apply(session.Command{
		EventID: fmt.Sprintf("evt-%d", h.n),
		ActorID: actor,
		Type:    typ,
		Payload: raw,
	})
}

func (h *harness) ok(actor, typ string, payload any) session.Result {
	h.t.Helper()
	res := h.send(actor, typ, payload)
	if res.Rejected != nil {
		h.t.Fatalf("%s by %s rejected: %v", typ, actor, res.Rejected)
	}
	return res
}

func wantCode(t *testing.T, res session.Result, code contracts.ErrorCode) {
	t.Helper()
	if res.Rejected == nil {
		t.Fatalf("want rejection %s, command was accepted", code)
	}
	if res.Rejected.Code != code {
		t.Fatalf("want rejection %s, got %s (%s)", code, res.Rejected.Code, res.Rejected.Msg)
	}
}

func broadcastTypes(res session.Result) []string {
	var out []string
	for _, ev := range res.Broadcast {
		out = append(out, ev.Type)
	}
	return out
}

func findEvent(t *testing.T, res session.Result, typ string) map[string]any {
	t.Helper()
	for _, ev := range res.Broadcast {
		if ev.Type == typ {
			m, ok := ev.Payload.(map[string]any)
			if !ok {
				t.Fatalf("%s payload is %T, want map", typ, ev.Payload)
			}
			return m
		}
	}
	t.Fatalf("no %s in broadcast %v", typ, broadcastTypes(res))
	return nil
}

// toDeliberation claims the lease and freezes the baseline; it returns run_id.
func (h *harness) toDeliberation() string {
	h.t.Helper()
	h.ok("owner", "LEASE_CLAIM", map[string]any{})
	h.ok("owner", "FREEZE_REQUEST", map[string]any{})
	return h.r.Snapshot().RunID
}

func (h *harness) draft(runID, avatar, schema, hash string, complete bool) session.Result {
	h.t.Helper()
	return h.send("owner", "AVATAR_DRAFT", map[string]any{
		"run_id": runID, "avatar_id": avatar, "cycle": 1, "draft_id": avatar + "-1",
		"draft_revision": "1", "schema_id": schema, "content": map[string]any{},
		"content_hash": hash, "complete": complete,
	})
}

func (h *harness) critique(runID string, severity, status string) session.Result {
	h.t.Helper()
	return h.send("owner", "AVATAR_CRITIQUE", map[string]any{
		"run_id": runID, "cycle": 1,
		"issues": []map[string]any{{"id": "c1", "severity": severity, "status": status}},
	})
}

func (h *harness) prompt(runID, id string) session.Result {
	h.t.Helper()
	return h.send("owner", "DECISION_PROMPT", map[string]any{
		"decision_id": id, "run_id": runID, "question": "which?",
		"options": []map[string]any{
			{"id": "a", "label": "A", "tradeoff": "fast"},
			{"id": "b", "label": "B", "tradeoff": "safe"},
		},
		"recommended_option_id": "a",
	})
}

// toSynthesis drives a run to phase SYNTHESIS and returns run_id.
func (h *harness) toSynthesis() string {
	h.t.Helper()
	runID := h.toDeliberation()
	h.ok("owner", "CYCLE_START", map[string]any{"run_id": runID, "graph_hash": "g", "cycle": 1})
	h.ok("owner", "AVATAR_DRAFT", map[string]any{
		"run_id": runID, "avatar_id": "pm", "schema_id": "pm.v1", "complete": true,
	})
	h.ok("owner", "SYNTHESIZE_REQUEST", map[string]any{
		"run_id": runID, "input_revision": dec(h.r.Snapshot().InputRevision),
	})
	if got := h.r.Phase(); got != session.PhaseSynthesis {
		h.t.Fatalf("phase = %s, want SYNTHESIS", got)
	}
	return runID
}

func (h *harness) approvePayload(runID, hash string) map[string]any {
	snap := h.r.Snapshot()
	return map[string]any{
		"baseline_rev":   dec(snap.BaselineRev),
		"run_id":         runID,
		"synthesis_rev":  dec(snap.SynthesisRev),
		"content_sha256": hash,
	}
}

func TestSynthesisDraftIncrementsRevisionAndRecordsHash(t *testing.T) {
	h := newHarness(t)
	runID := h.toSynthesis()
	if snap := h.r.Snapshot(); snap.SynthesisRev != 0 || snap.SynthesisHash != "" {
		t.Fatalf("before any draft: rev=%d hash=%q", snap.SynthesisRev, snap.SynthesisHash)
	}

	// A partial draft is accepted but neither counts nor records a hash.
	h.ok("owner", "AVATAR_DRAFT", map[string]any{
		"run_id": runID, "avatar_id": "synthesizer", "schema_id": "synthesis.v1", "complete": false,
	})
	if snap := h.r.Snapshot(); snap.SynthesisRev != 0 || snap.SynthesisHash != "" {
		t.Fatalf("partial draft: rev=%d hash=%q", snap.SynthesisRev, snap.SynthesisHash)
	}

	before := h.r.Snapshot().InputRevision
	res := h.draft(runID, "synthesizer", "synthesis.v1", synthHash, true)
	if res.Rejected != nil {
		t.Fatal(res.Rejected)
	}
	snap := h.r.Snapshot()
	if snap.SynthesisRev != 1 || snap.SynthesisHash != synthHash || h.r.SynthesisHash() != synthHash {
		t.Fatalf("after complete draft: rev=%d hash=%q", snap.SynthesisRev, snap.SynthesisHash)
	}
	if res.SynthesisRev != 1 {
		t.Fatalf("Result.SynthesisRev = %d, want 1", res.SynthesisRev)
	}
	if snap.InputRevision != before+1 {
		t.Fatalf("input_revision = %d, want %d", snap.InputRevision, before+1)
	}

	other := strings.Repeat("b", 64)
	h.ok("owner", "AVATAR_DRAFT", map[string]any{
		"run_id": runID, "avatar_id": "synthesizer", "schema_id": "synthesis.v1",
		"content_hash": other, "complete": true,
	})
	if snap := h.r.Snapshot(); snap.SynthesisRev != 2 || snap.SynthesisHash != other {
		t.Fatalf("second draft: rev=%d hash=%q", snap.SynthesisRev, snap.SynthesisHash)
	}
}

func TestCompleteSynthesisDraftRejectsBadHashAndPhase(t *testing.T) {
	h := newHarness(t)
	runID := h.toDeliberation()
	// synthesis.v1 is only valid in SYNTHESIS.
	wantCode(t, h.draft(runID, "synthesizer", "synthesis.v1", synthHash, true), contracts.CodeIllegalPhase)

	h2 := newHarness(t)
	runID = h2.toSynthesis()
	wantCode(t, h2.draft(runID, "synthesizer", "synthesis.v1", "nothex", true), contracts.CodeInvalidPayload)
	if h2.r.Snapshot().SynthesisRev != 0 {
		t.Fatal("a rejected draft must not move synthesis_rev")
	}
}

func TestApproveWithMatchingContentHashSucceeds(t *testing.T) {
	h := newHarness(t)
	runID := h.toSynthesis()
	h.ok("owner", "AVATAR_DRAFT", map[string]any{
		"run_id": runID, "avatar_id": "synthesizer", "schema_id": "synthesis.v1",
		"content_hash": synthHash, "complete": true,
	})

	res := h.ok("owner", "APPROVE", h.approvePayload(runID, synthHash))
	if got := h.r.Phase(); got != session.PhaseApproving {
		t.Fatalf("phase = %s, want APPROVING", got)
	}
	prepared := findEvent(t, res, "APPROVAL_PREPARED")
	approvalID, _ := prepared["approval_id"].(string)
	if approvalID == "" {
		t.Fatal("APPROVAL_PREPARED has no approval_id")
	}

	spec := sha256.Sum256([]byte("spec"))
	manifest := sha256.Sum256([]byte("manifest"))
	saved := h.ok("owner", "ARTIFACT_SAVED", map[string]any{
		"ok": true, "approval_id": approvalID,
		"spec_sha256": hex.EncodeToString(spec[:]), "manifest_sha256": hex.EncodeToString(manifest[:]),
	})
	if got := h.r.Phase(); got != session.PhaseApproved {
		t.Fatalf("phase = %s, want APPROVED", got)
	}
	approved := findEvent(t, saved, "PLAN_APPROVED")
	bundle := sha256.Sum256(append(append([]byte{}, spec[:]...), manifest[:]...))
	if approved["bundle_id"] != hex.EncodeToString(bundle[:]) {
		t.Fatalf("bundle_id = %v, want SHA-256 over spec digest || manifest digest", approved["bundle_id"])
	}
	if approved["spec_sha256"] != hex.EncodeToString(spec[:]) || approved["manifest_sha256"] != hex.EncodeToString(manifest[:]) {
		t.Fatalf("PLAN_APPROVED hashes were not copied from ARTIFACT_SAVED: %v", approved)
	}
	if _, isString := approved["approved_revision"].(string); !isString {
		t.Fatalf("approved_revision must be a decimal string, got %T", approved["approved_revision"])
	}
}

func TestApproveWithMismatchedContentHashIsStaleSynthesis(t *testing.T) {
	h := newHarness(t)
	runID := h.toSynthesis()
	h.ok("owner", "AVATAR_DRAFT", map[string]any{
		"run_id": runID, "avatar_id": "synthesizer", "schema_id": "synthesis.v1",
		"content_hash": synthHash, "complete": true,
	})
	wrong := strings.Repeat("c", 64)
	wantCode(t, h.send("owner", "APPROVE", h.approvePayload(runID, wrong)), contracts.CodeStaleSynthesis)
	wantCode(t, h.send("owner", "APPROVE", h.approvePayload(runID, "")), contracts.CodeStaleSynthesis)
	if got := h.r.Phase(); got != session.PhaseSynthesis {
		t.Fatalf("a rejected approval changed the phase to %s", got)
	}
}

func TestApproveWithoutSynthesisDraftIsStaleSynthesis(t *testing.T) {
	h := newHarness(t)
	runID := h.toSynthesis()
	wantCode(t, h.send("owner", "APPROVE", h.approvePayload(runID, synthHash)), contracts.CodeStaleSynthesis)
}

func TestApproveRejectionPrecedence(t *testing.T) {
	h := newHarness(t)
	runID := h.toSynthesis()
	h.ok("owner", "AVATAR_DRAFT", map[string]any{
		"run_id": runID, "avatar_id": "synthesizer", "schema_id": "synthesis.v1",
		"content_hash": synthHash, "complete": true,
	})
	snap := h.r.Snapshot()
	good := func() map[string]any {
		return map[string]any{
			"baseline_rev": dec(snap.BaselineRev), "run_id": runID,
			"synthesis_rev": dec(snap.SynthesisRev), "content_sha256": synthHash,
		}
	}

	// Authorization first: a non-owner sees NOT_APPROVER even with a stale payload.
	wantCode(t, h.send("ed", "APPROVE", map[string]any{"baseline_rev": "99"}), contracts.CodeNotApprover)

	// Everything wrong: the first documented rejection wins.
	p := good()
	p["baseline_rev"] = "99"
	p["run_id"] = "other-run"
	p["synthesis_rev"] = "99"
	p["content_sha256"] = strings.Repeat("d", 64)
	wantCode(t, h.send("owner", "APPROVE", p), contracts.CodeStaleBaseline)
	p["baseline_rev"] = dec(snap.BaselineRev)
	wantCode(t, h.send("owner", "APPROVE", p), contracts.CodeStaleRun)
	p["run_id"] = runID
	wantCode(t, h.send("owner", "APPROVE", p), contracts.CodeStaleSynthesis)
	p["synthesis_rev"] = dec(snap.SynthesisRev)
	wantCode(t, h.send("owner", "APPROVE", p), contracts.CodeStaleSynthesis)

	// A released lease fences the owner before any staleness check.
	h.ok("owner", "LEASE_RELEASE", map[string]any{})
	wantCode(t, h.send("owner", "APPROVE", map[string]any{"baseline_rev": "99"}), contracts.CodeFenced)
	h.ok("owner", "LEASE_CLAIM", map[string]any{})
	snap = h.r.Snapshot()
	h.ok("owner", "APPROVE", good())

	// A second APPROVE while APPROVING is an illegal phase, not a new approval.
	wantCode(t, h.send("owner", "APPROVE", good()), contracts.CodeIllegalPhase)
}

func TestApproveDecisionsPendingPrecedesBlockersOpen(t *testing.T) {
	h := newHarness(t)
	runID := h.toSynthesis()
	h.ok("owner", "AVATAR_DRAFT", map[string]any{
		"run_id": runID, "avatar_id": "synthesizer", "schema_id": "synthesis.v1",
		"content_hash": synthHash, "complete": true,
	})

	promptRev := h.r.Snapshot().InputRevision
	if res := h.prompt(runID, "d1"); res.Rejected != nil {
		t.Fatal(res.Rejected)
	}
	if res := h.critique(runID, "HIGH", "open"); res.Rejected != nil {
		t.Fatal(res.Rejected)
	}

	// Both a pending decision and an open blocker: DECISIONS_PENDING wins.
	wantCode(t, h.send("owner", "APPROVE", h.approvePayload(runID, synthHash)), contracts.CodeDecisionsPending)

	h.ok("ed", "DECISION_RESOLVE", map[string]any{
		"decision_id": "d1", "input_revision": dec(promptRev), "option_id": "a",
	})
	wantCode(t, h.send("owner", "APPROVE", h.approvePayload(runID, synthHash)), contracts.CodeBlockersOpen)

	h.ok("owner", "AVATAR_CRITIQUE", map[string]any{
		"run_id": runID, "cycle": 1,
		"issues": []map[string]any{{"id": "c1", "severity": "HIGH", "status": "resolved"}},
	})
	h.ok("owner", "APPROVE", h.approvePayload(runID, synthHash))
}

func TestIngestAppendRoles(t *testing.T) {
	h := newHarness(t)
	composer := map[string]any{"source": "composer", "markdown": "an idea"}
	upload := map[string]any{"source": "upload", "source_name": "spec.docx", "markdown": "# parsed"}

	// Editors may append composer text.
	res := h.ok("ed", "INGEST_APPEND", composer)
	if res.IngestSeq != 1 || res.IngestClientID != "sys:ingest:1" {
		t.Fatalf("first append: seq=%d client=%q", res.IngestSeq, res.IngestClientID)
	}
	// Viewers may not append anything.
	wantCode(t, h.send("vw", "INGEST_APPEND", composer), contracts.CodeRoleForbidden)
	wantCode(t, h.send("vw", "INGEST_APPEND", upload), contracts.CodeRoleForbidden)
	// Uploads are owner-only: a non-owner cannot upload.
	wantCode(t, h.send("ed", "INGEST_APPEND", upload), contracts.CodeRoleForbidden)
	// The owner may do both, and ingest_seq keeps counting.
	if res := h.ok("owner", "INGEST_APPEND", upload); res.IngestSeq != 2 || res.IngestClientID != "sys:ingest:2" {
		t.Fatalf("owner upload: seq=%d client=%q", res.IngestSeq, res.IngestClientID)
	}
	if res := h.ok("owner", "INGEST_APPEND", composer); res.IngestSeq != 3 {
		t.Fatalf("owner composer: seq=%d", res.IngestSeq)
	}
	// A rejected append never consumes an ingest_seq.
	wantCode(t, h.send("ed", "INGEST_APPEND", upload), contracts.CodeRoleForbidden)
	if res := h.ok("ed", "INGEST_APPEND", composer); res.IngestSeq != 4 {
		t.Fatalf("seq after rejections = %d, want 4", res.IngestSeq)
	}
}

func TestIngestAppendValidation(t *testing.T) {
	h := newHarness(t)
	wantCode(t, h.send("owner", "INGEST_APPEND", map[string]any{"source": "paste", "markdown": "x"}), contracts.CodeInvalidPayload)
	wantCode(t, h.send("owner", "INGEST_APPEND", map[string]any{"markdown": "x"}), contracts.CodeInvalidPayload)
	wantCode(t, h.send("owner", "INGEST_APPEND", map[string]any{"source": "composer", "markdown": ""}), contracts.CodeInvalidPayload)

	atCap := strings.Repeat("a", session.MaxIngestBytes)
	h.ok("owner", "INGEST_APPEND", map[string]any{"source": "composer", "markdown": atCap})
	wantCode(t, h.send("owner", "INGEST_APPEND", map[string]any{"source": "composer", "markdown": atCap + "a"}), contracts.CodeInvalidPayload)

	h.toDeliberation()
	wantCode(t, h.send("owner", "INGEST_APPEND", map[string]any{"source": "composer", "markdown": "late"}), contracts.CodeIllegalPhase)
	wantCode(t, h.send("ed", "INGEST_APPEND", map[string]any{"source": "composer", "markdown": "late"}), contracts.CodeIllegalPhase)
}

func TestRunAssignedPayloadAndOrdering(t *testing.T) {
	h := newHarness(t)
	h.ok("owner", "LEASE_CLAIM", map[string]any{})
	res := h.ok("owner", "FREEZE_REQUEST", map[string]any{})

	types := broadcastTypes(res)
	if len(types) != 2 || types[0] != "PAGE_TRANSITION" || types[1] != "RUN_ASSIGNED" {
		t.Fatalf("freeze broadcast = %v, want PAGE_TRANSITION then RUN_ASSIGNED", types)
	}
	snap := h.r.Snapshot()
	assigned := findEvent(t, res, "RUN_ASSIGNED")
	if assigned["run_id"] != snap.RunID || snap.RunID == "" {
		t.Fatalf("RUN_ASSIGNED run_id = %v, snapshot %q", assigned["run_id"], snap.RunID)
	}
	if assigned["baseline_rev"] != "1" || assigned["lease_epoch"] != "1" {
		t.Fatalf("RUN_ASSIGNED counters = %v / %v, want decimal strings 1 / 1",
			assigned["baseline_rev"], assigned["lease_epoch"])
	}
	if snap.Phase != session.PhaseDeliberation || snap.BaselineRev != 1 || snap.Cycle != 1 {
		t.Fatalf("snapshot after freeze: %+v", snap)
	}

	// CYCLE_START repeats the run_id and carries authority-supplied fields.
	cycle := h.ok("owner", "CYCLE_START", map[string]any{"run_id": snap.RunID, "graph_hash": "g", "cycle": 1})
	started := findEvent(t, cycle, "CYCLE_START")
	if started["run_id"] != snap.RunID || started["executor_id"] != "owner" || started["lease_epoch"] != "1" {
		t.Fatalf("CYCLE_START payload = %v", started)
	}
	wantCode(t, h.send("owner", "CYCLE_START", map[string]any{"run_id": "stale", "graph_hash": "g", "cycle": 1}), contracts.CodeRunSuperseded)
}

func TestInputRevisionCountsDraftsCritiquesAndResolutions(t *testing.T) {
	h := newHarness(t)
	runID := h.toDeliberation()
	rev := func() uint64 { return h.r.Snapshot().InputRevision }

	h.ok("owner", "AVATAR_THOUGHT", map[string]any{"run_id": runID, "avatar_id": "pm", "status": "Thinking", "public_summary": "x"})
	if rev() != 0 {
		t.Fatalf("AVATAR_THOUGHT moved input_revision to %d", rev())
	}
	if res := h.draft(runID, "pm", "pm.v1", "", true); res.Rejected != nil {
		t.Fatal(res.Rejected)
	}
	if rev() != 1 {
		t.Fatalf("after draft: %d, want 1", rev())
	}
	if res := h.critique(runID, "MEDIUM", "open"); res.Rejected != nil {
		t.Fatal(res.Rejected)
	}
	if rev() != 2 {
		t.Fatalf("after critique: %d, want 2", rev())
	}
	if res := h.prompt(runID, "d1"); res.Rejected != nil {
		t.Fatal(res.Rejected)
	}
	if rev() != 2 {
		t.Fatalf("DECISION_PROMPT moved input_revision to %d", rev())
	}
	h.ok("owner", "DECISION_RESOLVE", map[string]any{"decision_id": "d1", "input_revision": "2", "option_id": "b"})
	if rev() != 3 {
		t.Fatalf("after resolve: %d, want 3", rev())
	}
	// A rejected draft (stale run) moves nothing.
	wantCode(t, h.draft("old-run", "pm", "pm.v1", "", true), contracts.CodeRunSuperseded)
	if rev() != 3 {
		t.Fatalf("a rejected draft moved input_revision to %d", rev())
	}
}

func TestDecisionResolveFirstWriteWins(t *testing.T) {
	h := newHarness(t)
	runID := h.toDeliberation()
	h.prompt(runID, "d1")

	h.ok("ed", "DECISION_RESOLVE", map[string]any{"decision_id": "d1", "input_revision": "0", "option_id": "a"})
	late := h.send("owner", "DECISION_RESOLVE", map[string]any{"decision_id": "d1", "input_revision": "0", "option_id": "b"})
	wantCode(t, late, contracts.CodeAlreadyResolved)
	if late.Ack.Type != "ALREADY_RESOLVED" {
		t.Fatalf("ack = %s, want ALREADY_RESOLVED", late.Ack.Type)
	}
	if payload, _ := late.Ack.Payload.(map[string]any); payload["option_id"] != "a" {
		t.Fatalf("ALREADY_RESOLVED must carry the winning option, got %v", late.Ack.Payload)
	}
	wantCode(t, h.send("vw", "DECISION_RESOLVE", map[string]any{"decision_id": "d1", "input_revision": "0", "option_id": "a"}), contracts.CodeRoleForbidden)
}

func TestRetryStageWithStaleRunIsStaleRun(t *testing.T) {
	h := newHarness(t)
	runID := h.toDeliberation()
	wantCode(t, h.send("owner", "RETRY_STAGE", map[string]any{"run_id": "not-the-run", "stage_id": "architect"}), contracts.CodeStaleRun)
	wantCode(t, h.send("owner", "RETRY_STAGE", map[string]any{"run_id": runID, "stage_id": "bogus"}), contracts.CodeInvalidPayload)
	if h.r.Snapshot().RunID != runID {
		t.Fatal("a rejected retry changed the run")
	}
}

func TestRetryStageBelowCapKeepsRunAndInputRevision(t *testing.T) {
	h := newHarness(t)
	runID := h.toDeliberation()
	h.draft(runID, "pm", "pm.v1", "", true)
	before := h.r.Snapshot()

	res := h.ok("owner", "RETRY_STAGE", map[string]any{"run_id": runID, "stage_id": "specialist:backend"})
	if len(res.Broadcast) != 0 {
		t.Fatalf("same-run retry broadcast %v, want no RUN_ASSIGNED", broadcastTypes(res))
	}
	after := h.r.Snapshot()
	if after.RunID != runID || after.InputRevision != before.InputRevision || after.Cycle != before.Cycle {
		t.Fatalf("same-run retry changed counters: before %+v after %+v", before, after)
	}
}

func TestRetryStageAtCycleCapAssignsNewRun(t *testing.T) {
	h := newHarness(t)
	runID := h.toDeliberation()
	for cycle := 1; cycle <= session.MaxCycles; cycle++ {
		h.ok("owner", "CYCLE_START", map[string]any{"run_id": runID, "graph_hash": "g", "cycle": cycle})
	}
	h.draft(runID, "architect", "architect.v1", "", true)
	// Open HIGH critique on the last allowed cycle pauses the run.
	h.ok("owner", "AVATAR_CRITIQUE", map[string]any{
		"run_id": runID, "cycle": session.MaxCycles,
		"issues": []map[string]any{{"id": "c1", "severity": "HIGH", "status": "open"}},
	})
	snap := h.r.Snapshot()
	if !snap.CycleCapped || snap.Cycle != session.MaxCycles {
		t.Fatalf("expected a paused run at cycle %d, got %+v", session.MaxCycles, snap)
	}
	// No fourth automatic cycle.
	wantCode(t, h.send("owner", "CYCLE_START", map[string]any{"run_id": runID, "graph_hash": "g", "cycle": session.MaxCycles}), contracts.CodeRunSuperseded)
	wantCode(t, h.send("owner", "CYCLE_START", map[string]any{"run_id": runID, "graph_hash": "g", "cycle": session.MaxCycles + 1}), contracts.CodeRunSuperseded)
	// Synthesis stays disabled while blockers are open.
	wantCode(t, h.send("owner", "SYNTHESIZE_REQUEST", map[string]any{"run_id": runID, "input_revision": dec(snap.InputRevision)}), contracts.CodeBlockersOpen)

	// A stale run_id is rejected even at the cap.
	wantCode(t, h.send("owner", "RETRY_STAGE", map[string]any{"run_id": "old", "stage_id": "critic"}), contracts.CodeStaleRun)

	res := h.ok("owner", "RETRY_STAGE", map[string]any{"run_id": runID, "stage_id": "critic"})
	types := broadcastTypes(res)
	if len(types) != 2 || types[0] != "RUN_SUPERSEDED" || types[1] != "RUN_ASSIGNED" {
		t.Fatalf("cap retry broadcast = %v, want RUN_SUPERSEDED then RUN_ASSIGNED", types)
	}
	after := h.r.Snapshot()
	if after.RunID == "" || after.RunID == runID {
		t.Fatalf("run_id = %q, want a new run (old %q)", after.RunID, runID)
	}
	if after.Cycle != 1 || after.CycleCapped || after.InputRevision != 0 || after.SynthesisRev != 0 {
		t.Fatalf("new run counters not reset: %+v", after)
	}
	assigned := findEvent(t, res, "RUN_ASSIGNED")
	if assigned["run_id"] != after.RunID || assigned["baseline_rev"] != dec(after.BaselineRev) {
		t.Fatalf("RUN_ASSIGNED payload = %v", assigned)
	}
	if superseded := findEvent(t, res, "RUN_SUPERSEDED"); superseded["old_run_id"] != runID {
		t.Fatalf("RUN_SUPERSEDED payload = %v", superseded)
	}
	// The old run is gone: its late events are refused.
	wantCode(t, h.draft(runID, "architect", "architect.v1", "", true), contracts.CodeRunSuperseded)
	// The new run starts again at cycle 1.
	h.ok("owner", "CYCLE_START", map[string]any{"run_id": after.RunID, "graph_hash": "g", "cycle": 1})
}

func TestCancelIsSessionTerminal(t *testing.T) {
	h := newHarness(t)
	h.toDeliberation()
	wantCode(t, h.send("ed", "CANCEL", map[string]any{}), contracts.CodeRoleForbidden)

	res := h.ok("owner", "CANCEL", map[string]any{})
	if got := h.r.Phase(); got != session.PhaseCancelled {
		t.Fatalf("phase = %s, want CANCELLED", got)
	}
	if transition := findEvent(t, res, "PAGE_TRANSITION"); transition["to"] != string(session.PhaseCancelled) {
		t.Fatalf("PAGE_TRANSITION = %v", transition)
	}
	if holder, _, _ := h.r.LeaseSnapshot(); holder != "" {
		t.Fatalf("cancel left the lease with %q", holder)
	}
	for _, typ := range []string{"CANCEL", "FREEZE_REQUEST", "LEASE_CLAIM"} {
		wantCode(t, h.send("owner", typ, map[string]any{}), contracts.CodeIllegalPhase)
	}
	wantCode(t, h.send("owner", "EDIT_BASELINE", map[string]any{"baseline_rev": "1"}), contracts.CodeIllegalPhase)
	wantCode(t, h.send("owner", "INGEST_APPEND", map[string]any{"source": "composer", "markdown": "x"}), contracts.CodeIllegalPhase)
	if got := h.r.Phase(); got != session.PhaseCancelled {
		t.Fatalf("phase left CANCELLED: %s", got)
	}
}

func TestEditBaselineChecksBaselineRevAndSupersedesRun(t *testing.T) {
	h := newHarness(t)
	runID := h.toDeliberation()
	wantCode(t, h.send("owner", "EDIT_BASELINE", map[string]any{"baseline_rev": "9"}), contracts.CodeStaleBaseline)
	wantCode(t, h.send("owner", "EDIT_BASELINE", map[string]any{}), contracts.CodeInvalidPayload)

	res := h.ok("owner", "EDIT_BASELINE", map[string]any{"baseline_rev": "1"})
	if got := h.r.Phase(); got != session.PhaseIngestion {
		t.Fatalf("phase = %s, want INGESTION", got)
	}
	if superseded := findEvent(t, res, "RUN_SUPERSEDED"); superseded["old_run_id"] != runID {
		t.Fatalf("RUN_SUPERSEDED payload = %v", superseded)
	}
	// The next freeze gets a new run_id and a new baseline_rev.
	h.ok("owner", "FREEZE_REQUEST", map[string]any{})
	snap := h.r.Snapshot()
	if snap.RunID == runID || snap.BaselineRev != 2 {
		t.Fatalf("after re-freeze: run %q (old %q), baseline_rev %d", snap.RunID, runID, snap.BaselineRev)
	}
}

func TestRevisionAdvancesOncePerStateChangingCommand(t *testing.T) {
	h := newHarness(t)
	h.ok("owner", "LEASE_CLAIM", map[string]any{})
	rev := h.r.Snapshot().Revision
	res := h.ok("owner", "FREEZE_REQUEST", map[string]any{})
	if res.Revision != rev+1 || h.r.Snapshot().Revision != rev+1 {
		t.Fatalf("revision = %d, want %d", res.Revision, rev+1)
	}
	// Ephemeral thoughts and the doc channel never advance it.
	runID := h.r.Snapshot().RunID
	h.ok("owner", "AVATAR_THOUGHT", map[string]any{"run_id": runID, "avatar_id": "pm", "status": "Drafting"})
	if h.r.Snapshot().Revision != rev+1 {
		t.Fatal("AVATAR_THOUGHT advanced the revision")
	}
}

func TestFollowerReportsArtifactSyncedAfterApproval(t *testing.T) {
	h := newHarness(t)
	runID := h.toSynthesis()
	h.ok("owner", "AVATAR_DRAFT", map[string]any{
		"run_id": runID, "avatar_id": "synthesizer", "schema_id": "synthesis.v1",
		"content_hash": synthHash, "complete": true,
	})
	// Not yet approved: a follower report is an illegal phase.
	wantCode(t, h.send("vw", "ARTIFACT_SYNCED", map[string]any{"ok": true}), contracts.CodeIllegalPhase)

	approvalID, _ := findEvent(t, h.ok("owner", "APPROVE", h.approvePayload(runID, synthHash)), "APPROVAL_PREPARED")["approval_id"].(string)
	digest := sha256.Sum256([]byte("artifact"))
	hexDigest := hex.EncodeToString(digest[:])
	// ok requires 64-hex digests.
	wantCode(t, h.send("owner", "ARTIFACT_SAVED", map[string]any{"ok": true, "approval_id": approvalID, "spec_sha256": "x", "manifest_sha256": hexDigest}), contracts.CodeInvalidPayload)
	// A different approval_id is not this approval.
	wantCode(t, h.send("owner", "ARTIFACT_SAVED", map[string]any{"ok": true, "approval_id": "other", "spec_sha256": hexDigest, "manifest_sha256": hexDigest}), contracts.CodeApprovalFailed)
	h.ok("owner", "ARTIFACT_SAVED", map[string]any{"ok": true, "approval_id": approvalID, "spec_sha256": hexDigest, "manifest_sha256": hexDigest})

	// A viewer follower reports its sync; the owner's replay is idempotent.
	h.ok("vw", "ARTIFACT_SYNCED", map[string]any{"ok": true})
	h.ok("owner", "ARTIFACT_SAVED", map[string]any{"ok": true, "approval_id": approvalID, "spec_sha256": hexDigest, "manifest_sha256": hexDigest})
	if got := h.r.Phase(); got != session.PhaseApproved {
		t.Fatalf("phase = %s, want APPROVED", got)
	}
}
