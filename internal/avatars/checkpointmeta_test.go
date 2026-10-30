//go:build client && !server

package avatars

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
)

// A checkpoint carries the execution metadata the plan requires: what ran,
// against which inputs, what finished, what comes next. A resumed run reads
// that instead of guessing, and a save is the only place it is filled, so it
// cannot drift from the state beside it.
func TestCheckpointMetaRecordsProgress(t *testing.T) {
	o := &orchestrator{in: RunInput{
		RunID: "run-1", GraphHash: "graph-hash", ContextHash: "ctx-hash",
		Models: ModelSlots{Main: "main-model", Judge: "judge-model"},
	}}
	o.st = newRunState()
	o.st.PRDRevision = 7
	o.st.PM = draftRec{Raw: json.RawMessage(`{"epics":[]}`)}
	o.st.Arch = draftRec{Raw: json.RawMessage(`{"summary":"s"}`)}
	o.st.Specs["billing-api"] = draftRec{Raw: json.RawMessage(`{"workstream_id":"billing-api"}`)}

	m := o.metaLocked()
	if m.SchemaVersion != CheckpointMetaSchema {
		t.Errorf("schema version = %d, want %d", m.SchemaVersion, CheckpointMetaSchema)
	}
	if m.RunID != "run-1" || m.InputRevision != 7 {
		t.Errorf("identity = %q rev %d", m.RunID, m.InputRevision)
	}
	if m.RepositoryFingerprint != "graph-hash" || m.ContextHash != "ctx-hash" {
		t.Errorf("fingerprints = %q %q", m.RepositoryFingerprint, m.ContextHash)
	}
	if m.Models.Main != "main-model" || m.Models.Judge != "judge-model" {
		t.Errorf("models = %+v", m.Models)
	}
	got := strings.Join(m.ValidatedOutputs, ",")
	if got != "architect,billing-api,pm" {
		t.Errorf("validated outputs = %q, want architect,billing-api,pm", got)
	}
	if len(m.CompletedWork) == 0 || !strings.Contains(strings.Join(m.CompletedWork, "; "), "billing-api") {
		t.Errorf("completed work = %v", m.CompletedWork)
	}
	if m.NextAction == "" {
		t.Error("next action is empty; a resumed run has nothing to act on")
	}
}

// The next action follows the state rather than a fixed sentence: at the start
// it names the PM, and with every draft in place it names synthesis.
func TestCheckpointMetaNextActionFollowsTheState(t *testing.T) {
	o := &orchestrator{in: RunInput{RunID: "run-1"}}
	o.st = newRunState()
	if got := o.nextActionLocked(); !strings.Contains(got, "product manager") {
		t.Errorf("empty run next action = %q", got)
	}
	o.st.PM = draftRec{Raw: json.RawMessage(`{}`)}
	o.st.ArchDesign = draftRec{Raw: json.RawMessage(`{}`)}
	o.st.Specs["ws"] = draftRec{Raw: json.RawMessage(`{}`)}
	if got := o.nextActionLocked(); !strings.Contains(got, "synthes") {
		t.Errorf("complete-run next action = %q, want synthesis", got)
	}
}

// A pending decision outranks everything else: the run cannot advance without
// the owner, and the checkpoint must say so.
func TestCheckpointMetaNamesTheWaitingDecision(t *testing.T) {
	o := &orchestrator{in: RunInput{RunID: "run-1"}}
	o.st = newRunState()
	o.st.PM = draftRec{Raw: json.RawMessage(`{}`)}
	o.st.ArchDesign = draftRec{Raw: json.RawMessage(`{}`)}
	o.st.Specs["ws"] = draftRec{Raw: json.RawMessage(`{}`)}
	o.st.Pending = []contracts.Decision{{ID: "d1"}}
	m := o.metaLocked()
	if m.Phase != "awaiting_decisions" {
		t.Errorf("phase = %q, want awaiting_decisions", m.Phase)
	}
	if m.UnresolvedDecisions != 1 {
		t.Errorf("unresolved decisions = %d, want 1", m.UnresolvedDecisions)
	}
	if !strings.Contains(m.NextAction, "decision") {
		t.Errorf("next action = %q, want the pending decision", m.NextAction)
	}
}

// A checkpoint round trip preserves the metadata, so a restarted process reads
// the same picture the previous one saved.
func TestCheckpointMetaSurvivesRoundTrip(t *testing.T) {
	o := &orchestrator{in: RunInput{RunID: "run-1", GraphHash: "g", ContextHash: "c"}}
	o.st = newRunState()
	o.st.PM = draftRec{Raw: json.RawMessage(`{}`)}
	meta := o.metaLocked()
	o.st.Meta = meta

	raw, err := json.Marshal(o.st)
	if err != nil {
		t.Fatal(err)
	}
	back := newRunState()
	if err := json.Unmarshal(raw, back); err != nil {
		t.Fatal(err)
	}
	back.afterDecode()
	if back.Meta.RunID != "run-1" || back.Meta.SchemaVersion != CheckpointMetaSchema {
		t.Fatalf("meta lost in round trip: %+v", back.Meta)
	}
	if len(back.Meta.ValidatedOutputs) != 1 || back.Meta.ValidatedOutputs[0] != "pm" {
		t.Fatalf("validated outputs = %v", back.Meta.ValidatedOutputs)
	}
}

// The error recorded in a checkpoint is bounded: a long provider error must not
// bloat every subsequent save.
func TestCheckpointLastErrorIsBounded(t *testing.T) {
	long := strings.Repeat("x", 5000)
	got := sanitize.Truncate(sanitize.Terminal(long), checkpointErrorLimit)
	if len(got) > checkpointErrorLimit {
		t.Fatalf("last error is %d bytes, want at most %d", len(got), checkpointErrorLimit)
	}
}
