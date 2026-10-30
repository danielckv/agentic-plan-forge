//go:build client && !server

package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// env builds one envelope with a payload, the way the authority sends it.
func env(t *testing.T, typ relay.WSEventType, payload any) relay.WSEnvelope {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return relay.WSEnvelope{EventType: typ, Payload: raw}
}

// A snapshot replaces the whole view, because merging a stale pane with a new
// one would show a state the authority never had. It carries the real payload:
// identity, counters and draft hashes, never document text or agent rows.
func TestSnapshotReplacesRatherThanMerges(t *testing.T) {
	var s Snapshot
	s = s.Apply(env(t, relay.EventStateSnapshot, map[string]any{
		"member_id": "owner", "role": "owner", "phase": "DELIBERATION",
		"run_id": "run-1", "baseline_rev": "7", "synthesis_rev": "2",
		"draft_hashes": map[string]string{"synthesizer": "hash-1"},
	}))
	if s.Phase != relay.PhaseDeliberation || s.RunID != "run-1" || s.BaselineRev != 7 || s.SynthesisRev != 2 {
		t.Fatalf("snapshot not applied: %+v", s)
	}
	if s.SynthesisHash != "hash-1" || s.MemberID != "owner" {
		t.Fatalf("draft hash or member not decoded: %+v", s)
	}

	// A second snapshot with different counters replaces them entirely.
	s = s.Apply(env(t, relay.EventStateSnapshot, map[string]any{
		"phase": "SYNTHESIS", "baseline_rev": "9", "run_id": "run-2",
	}))
	if s.Phase != relay.PhaseSynthesis || s.BaselineRev != 9 || s.RunID != "run-2" {
		t.Fatalf("second snapshot not applied: %+v", s)
	}
	if s.SynthesisHash != "" {
		t.Fatalf("stale draft hash survived a snapshot: %q", s.SynthesisHash)
	}
}

// A thought updates one agent in place and keeps the list ordered, so the screen
// does not reshuffle between frames.
func TestThoughtUpdatesOneAgentInOrder(t *testing.T) {
	var s Snapshot
	s = s.Apply(env(t, relay.EventAvatarThought, map[string]any{
		"avatar_id": "architect", "status": "Drafting", "cycle": 1, "host": "claude", "model": "opus",
	}))
	s = s.Apply(env(t, relay.EventAvatarThought, map[string]any{
		"avatar_id": "pm", "status": "Thinking", "cycle": 1,
	}))
	if len(s.Agents) != 2 || s.Agents[0].ID != "architect" || s.Agents[1].ID != "pm" {
		t.Fatalf("agents not sorted: %+v", s.Agents)
	}
	// A later frame that omits host/model must not erase them.
	s = s.Apply(env(t, relay.EventAvatarThought, map[string]any{
		"avatar_id": "architect", "status": "Synced", "cycle": 1,
	}))
	if a := s.Agents[0]; a.Status != "Synced" || a.Host != "claude" || a.Model != "opus" {
		t.Fatalf("host/model lost on update: %+v", a)
	}
}

// A failed stage raises the banner, which is the one line a reader must see
// without opening a pane.
func TestFailureRaisesTheBanner(t *testing.T) {
	var s Snapshot
	s = s.Apply(env(t, relay.EventAvatarThought, map[string]any{
		"avatar_id": "critic", "status": "Failed", "public_summary": "the provider timed out",
	}))
	if !strings.Contains(s.Banner, "Red-Team Critic") || !strings.Contains(s.Banner, "timed out") {
		t.Fatalf("banner = %q", s.Banner)
	}
}

// Temporary agents are marked as temporary: a run-scoped gap agent must never be
// mistaken for the fixed roster.
func TestTemporaryAgentsAreLabelled(t *testing.T) {
	var s Snapshot
	s = s.Apply(env(t, relay.EventAvatarThought, map[string]any{"avatar_id": "gap:security", "status": "Thinking"}))
	if s.Agents[0].Kind != "temporary" {
		t.Fatalf("kind = %q, want temporary", s.Agents[0].Kind)
	}
	if !strings.Contains(s.Agents[0].Name, "security") {
		t.Fatalf("name = %q", s.Agents[0].Name)
	}
	s = s.Apply(env(t, relay.EventAvatarThought, map[string]any{"avatar_id": "specialist:billing", "status": "Drafting"}))
	if s.Agents[1].Kind != "specialist" {
		t.Fatalf("kind = %q, want specialist", s.Agents[1].Kind)
	}
}

// A decision prompt becomes a question row with its options and the
// recommendation marked, never applied.
func TestDecisionPromptBecomesAQuestion(t *testing.T) {
	var s Snapshot
	s = s.Apply(env(t, relay.EventDecisionPrompt, map[string]any{
		"decision_id": "d1", "prompt": "Where should the export be written?",
		"options": []map[string]string{
			{"id": "stream", "label": "Stream the download"},
			{"id": "stage", "label": "Stage the file"},
		},
		"recommended_option_id": "stage",
	}))
	if len(s.Questions) != 1 {
		t.Fatalf("questions = %+v", s.Questions)
	}
	q := s.Questions[0]
	if q.Kind != "decision" || len(q.Options) != 2 || q.Recommended != "stage" {
		t.Fatalf("question = %+v", q)
	}

	// Resolving it removes the row.
	s = s.Apply(env(t, relay.EventDecisionResolve, map[string]any{"decision_id": "d1"}))
	if len(s.Questions) != 0 {
		t.Fatalf("answered question still listed: %+v", s.Questions)
	}
}

// A critique replaces the findings list, so a resolved finding stops showing.
func TestCritiqueReplacesFindings(t *testing.T) {
	var s Snapshot
	s = s.Apply(env(t, relay.EventAvatarCritique, map[string]any{
		"issues": []map[string]any{
			{"severity": "HIGH", "target_ids": []string{"billing"}, "required_change": "check org_id"},
			{"severity": "MEDIUM", "target_ids": []string{"web"}, "required_change": "stream"},
		},
	}))
	if len(s.Findings) != 2 {
		t.Fatalf("findings = %v", s.Findings)
	}
	s = s.Apply(env(t, relay.EventAvatarCritique, map[string]any{"issues": []map[string]any{}}))
	if len(s.Findings) != 0 {
		t.Fatalf("stale finding survived: %v", s.Findings)
	}
}

// An error frame is surfaced; a snapshot clears it, so a recovered session does
// not keep showing a failure.
func TestErrorIsSurfacedAndCleared(t *testing.T) {
	var s Snapshot
	s = s.Apply(env(t, relay.EventError, map[string]any{"code": "STALE_RUN", "message": "the run moved on"}))
	if !strings.Contains(s.Err, "STALE_RUN") {
		t.Fatalf("err = %q", s.Err)
	}
	s = s.Apply(env(t, relay.EventStateSnapshot, map[string]any{"phase": "SYNTHESIS"}))
	if s.Err != "" {
		t.Fatalf("error survived a snapshot: %q", s.Err)
	}
}

// An unknown event type is ignored, not fatal: the protocol grows and a terminal
// that crashed on a new frame would be worse than one that skips it.
func TestUnknownEventIsIgnored(t *testing.T) {
	var s Snapshot
	s = s.Apply(relay.WSEnvelope{EventType: "SOMETHING_NEW", Payload: json.RawMessage(`{}`)})
	if s.Phase != "" {
		t.Fatalf("unknown event mutated the snapshot: %+v", s)
	}
}

// Approval and artifacts are recorded, because the handoff is what a reader
// carries away from the session.
func TestApprovalAndArtifactsAreRecorded(t *testing.T) {
	var s Snapshot
	s = s.Apply(env(t, relay.EventPlanApproved, map[string]any{}))
	if s.Phase != relay.PhaseApproved || s.Banner == "" {
		t.Fatalf("approval not recorded: %+v", s)
	}
	s = s.Apply(env(t, relay.EventArtifactSaved, map[string]any{"path": "docs/plans/.plan/plan_manifest.json"}))
	s = s.Apply(env(t, relay.EventArtifactSaved, map[string]any{"path": "docs/plans/.plan/synthesized_spec.md"}))
	if len(s.Artifacts) != 2 || s.Artifacts[0] != "docs/plans/.plan/plan_manifest.json" {
		t.Fatalf("artifacts = %v", s.Artifacts)
	}
	// A repeated save does not duplicate the row.
	s = s.Apply(env(t, relay.EventArtifactSaved, map[string]any{"path": "docs/plans/.plan/synthesized_spec.md"}))
	if len(s.Artifacts) != 2 {
		t.Fatalf("artifact duplicated: %v", s.Artifacts)
	}
}
