//go:build client && !server

package avatars

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

// A stage must not die because the repository has a lot of documentation
// drift: the payload gives the drift up, keeps the stories, and says so.
func TestArchitectPayloadTrimsDriftToFitTheBound(t *testing.T) {
	drift := make([]contracts.DocDriftItem, 0, 4000)
	for i := range 4000 {
		drift = append(drift, contracts.DocDriftItem{
			ID: "D" + strconv.Itoa(i), DocFilePath: "docs/x.md", DocLine: i,
			DriftType: contracts.DriftMissingDoc, Expected: strings.Repeat("x", 500),
			Observed: strings.Repeat("y", 500), EvidenceKind: "ast_symbol_absent_from_doc", Confidence: "MEDIUM",
		})
	}
	p := &architectPayload{
		Cycle: 1, DocDrift: drift,
		PM: synthesizer.PMDoc{Stories: []contracts.Story{{ID: "S-1", Need: "bill the customer"}}},
	}

	raw, err := fitPayload("architect", p)
	if err != nil {
		t.Fatalf("fitPayload refused a trimmable payload: %v", err)
	}
	if len(raw) > contracts.MaxStagePayloadBytes {
		t.Fatalf("payload is %d bytes, over the %d byte bound", len(raw), contracts.MaxStagePayloadBytes)
	}
	if len(p.DocDrift) == len(drift) {
		t.Error("the drift list was not cut")
	}
	if len(p.EvidenceNotes) == 0 {
		t.Fatal("the payload cut evidence without saying so")
	}
	if !strings.Contains(p.EvidenceNotes[0], "documentation drift") {
		t.Errorf("evidence note = %q, want it to name the drift list", p.EvidenceNotes[0])
	}
	var got architectPayload
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if len(got.PM.Stories) != 1 {
		t.Error("the stories were cut; they are the stage's subject")
	}
}

// A payload that fits is handed over untouched, with no notes about cuts that
// did not happen.
func TestFitPayloadLeavesASmallPayloadAlone(t *testing.T) {
	p := &architectPayload{Cycle: 1, DocDrift: []contracts.DocDriftItem{{ID: "D1"}}}

	raw, err := fitPayload("architect", p)
	if err != nil {
		t.Fatalf("fitPayload refused a small payload: %v", err)
	}
	if len(p.DocDrift) != 1 || len(p.EvidenceNotes) != 0 {
		t.Errorf("fitPayload changed a payload that fits: %d drift items, %d notes", len(p.DocDrift), len(p.EvidenceNotes))
	}
	if !strings.Contains(string(raw), `"cycle":1`) {
		t.Errorf("payload = %s, want the stage input", raw)
	}
}

// When the subject itself is too big there is nothing left to trim. The error
// must name the parts that filled the payload, so the next step is obvious.
func TestFitPayloadNamesTheLargestPartsWhenNothingCanBeCut(t *testing.T) {
	stories := make([]contracts.Story, 0, 3000)
	for i := range 3000 {
		stories = append(stories, contracts.Story{
			ID: "S-" + strconv.Itoa(i), Need: strings.Repeat("n", 700),
			AcceptanceCriteria: []string{strings.Repeat("a", 300)},
		})
	}
	p := &architectPayload{Cycle: 1, PM: synthesizer.PMDoc{Stories: stories}}

	_, err := fitPayload("architect", p)
	var bound *contracts.ErrBound
	if !errors.As(err, &bound) {
		t.Fatalf("fitPayload returned %v, want an ErrBound", err)
	}
	if !strings.Contains(bound.Detail, "pm") {
		t.Errorf("ErrBound.Detail = %q, want it to name the part that filled the payload", bound.Detail)
	}
	if !strings.Contains(bound.Error(), bound.Detail) {
		t.Errorf("ErrBound.Error() = %q, want it to carry the detail", bound.Error())
	}
}

// The critic re-lists every prior critique, so a critic payload gives up its
// evidence but never the findings it must carry forward.
func TestCriticPayloadKeepsPriorCritiquesWhenItTrims(t *testing.T) {
	hits := make([]contracts.ContextHit, 0, 400)
	for i := range 400 {
		hits = append(hits, contracts.ContextHit{Path: "docs/n.md", Text: strings.Repeat("c", 6000), Score: float64(400 - i)})
	}
	p := &criticPayload{
		Cycle: 1, Prior: []contracts.Critique{{ID: "C-1", Severity: "HIGH"}},
		Context: &contracts.ProjectContext{Source: "files", Hits: hits},
	}

	raw, err := fitPayload("critic", p)
	if err != nil {
		t.Fatalf("fitPayload refused a trimmable critic payload: %v", err)
	}
	if len(raw) > contracts.MaxStagePayloadBytes {
		t.Fatalf("payload is %d bytes, over the bound", len(raw))
	}
	if len(p.Prior) != 1 {
		t.Error("the prior critiques were cut; the critic must re-list every one")
	}
	if len(p.EvidenceNotes) == 0 || !strings.Contains(p.EvidenceNotes[0], "project context") {
		t.Errorf("evidence notes = %v, want the context cut named", p.EvidenceNotes)
	}
}
