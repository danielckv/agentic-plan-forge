//go:build client && !server

package avatars

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// payloadOfSize returns a valid JSON object whose encoding is at least n bytes.
func payloadOfSize(n int) json.RawMessage {
	return json.RawMessage(`{"filler":"` + strings.Repeat("x", n) + `"}`)
}

// Output has always been bounded; the payload was not, so a stage could hand
// the provider more than the model's context window and die inside the host
// with no diagnosable error. The bound must be reported as ErrBound, naming the
// limit and what was observed.
func TestGenerateRefusesAPayloadOverTheBound(t *testing.T) {
	o := &orchestrator{}
	sp := stageSpec{avatar: "architect", role: contracts.RoleArchitect}
	over := payloadOfSize(contracts.MaxStagePayloadBytes + 1)

	_, err := o.generate(context.Background(), sp, fixedInput(over))
	if err == nil {
		t.Fatal("generate accepted a payload past the bound")
	}
	var bound *contracts.ErrBound
	if !errors.As(err, &bound) {
		t.Fatalf("generate returned %v, want an ErrBound", err)
	}
	if bound.Limit != contracts.MaxStagePayloadBytes {
		t.Errorf("ErrBound.Limit = %d, want %d", bound.Limit, contracts.MaxStagePayloadBytes)
	}
	if bound.Observed <= bound.Limit {
		t.Errorf("ErrBound.Observed = %d, want more than the limit %d", bound.Observed, bound.Limit)
	}
	if !strings.Contains(bound.Name, "architect") {
		t.Errorf("ErrBound.Name = %q, want the stage named", bound.Name)
	}
}

// echoGen is a provider that returns one fixed object without touching the
// network, so the in-bound path can be exercised.
type echoGen struct{ reply string }

func (g echoGen) GenerateStream(_ context.Context, _ contracts.GenerateRequest,
	emit func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	if err := emit(contracts.GenerationEvent{Kind: contracts.EventFinal, Text: g.reply}); err != nil {
		return contracts.GenerationResult{}, err
	}
	return contracts.GenerationResult{}, nil
}

// A payload that fits reaches the provider untouched.
func TestGenerateAcceptsAPayloadAtTheBound(t *testing.T) {
	o := &orchestrator{gen: echoGen{reply: `{"ok":true}`}}
	sp := stageSpec{avatar: "architect", role: contracts.RoleArchitect}

	raw, err := o.generate(context.Background(), sp, fixedInput(payloadOfSize(1<<10)))
	if err != nil {
		t.Fatalf("a %d byte payload was refused: %v", 1<<10, err)
	}
	if !strings.Contains(string(raw), "ok") {
		t.Fatalf("generate returned %s, want the provider reply", raw)
	}
}

// Repair re-sends the stage input with the rejected output attached, which
// doubles a large payload. The rejected output is droppable: the problem list
// already names every change, so repair keeps working while staying in bounds.
func TestRepairPayloadDropsTheRejectedOutputToStayInBounds(t *testing.T) {
	input := payloadOfSize(contracts.MaxStagePayloadBytes - (16 << 10))
	previous := payloadOfSize(contracts.MaxStagePayloadBytes - (16 << 10))

	got := repairPayload(input, previous, errors.New("schema violation"))
	if len(got) > contracts.MaxStagePayloadBytes {
		t.Fatalf("repair payload is %d bytes, over the %d byte bound", len(got), contracts.MaxStagePayloadBytes)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("repair payload is not a JSON object: %v", err)
	}
	if _, ok := out["input"]; !ok {
		t.Error("repair payload dropped the stage input")
	}
	if !strings.Contains(string(got), "schema violation") {
		t.Error("repair payload dropped the problem list")
	}
}

// A normal repair keeps the rejected output, which is the signal that makes
// repair work; trimming must only happen when the payload would not fit.
func TestRepairPayloadKeepsTheRejectedOutputWhenItFits(t *testing.T) {
	got := repairPayload(json.RawMessage(`{"cycle":1}`), json.RawMessage(`{"bad":"draft"}`), errors.New("boom"))
	if len(got) > contracts.MaxStagePayloadBytes {
		t.Fatalf("a small repair payload came out at %d bytes", len(got))
	}
	if !strings.Contains(string(got), `"bad"`) {
		t.Errorf("repair payload dropped the rejected output: %s", got)
	}
}
