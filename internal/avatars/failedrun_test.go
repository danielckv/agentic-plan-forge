//go:build client && !server

package avatars_test

import (
	"context"
	"errors"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/avatars"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer/synthtest"
)

// A failed stage ends the run, but the avatars that never ran kept their
// "queued" look on Page 2 and the board stayed that way forever. Every avatar
// must reach a terminal status when a run stops.
func TestAFailedStageCancelsTheAvatarsThatNeverRan(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.stub.on("architect", func(c *call, in input) (reply, bool) {
		return reply{Err: errors.New("provider overloaded")}, true
	})

	_, err := h.run(context.Background(), nil)

	var stage *avatars.StageError
	if !errors.As(err, &stage) || stage.Stage != relay.StageArchitect {
		t.Fatalf("Run error = %v, want an architect StageError", err)
	}
	for _, avatar := range []string{relay.StageCritic, relay.StageSynthesizer} {
		got := h.sink.thoughts(avatar)
		if len(got) == 0 {
			t.Errorf("%s was never given a status: the card waits forever", avatar)
			continue
		}
		last := got[len(got)-1]
		if last.Status != "Cancelled" {
			t.Errorf("%s ended at %q, want Cancelled", avatar, last.Status)
		}
		if last.PublicSummary == "" {
			t.Errorf("%s was cancelled without saying why", avatar)
		}
	}
	// The stage that failed keeps its own failure, and the Product Manager
	// keeps the work it finished.
	if last := lastStatus(h, relay.StageArchitect); last != "Failed" {
		t.Errorf("architect ended at %q, want Failed", last)
	}
	if last := lastStatus(h, relay.StagePM); last != "Synced" {
		t.Errorf("product manager ended at %q, want its finished status kept", last)
	}
}

// A run that finishes normally announces nothing extra: the cancel path is
// only for a run that stopped.
func TestASuccessfulRunCancelsNoAvatar(t *testing.T) {
	h := newHarness(t, synthtest.Sample())

	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, ev := range h.sink.ofType(relay.EventAvatarThought) {
		if p := ev.Payload.(relay.AvatarThoughtPayload); p.Status == "Cancelled" {
			t.Errorf("%s was cancelled in a run that converged", p.AvatarID)
		}
	}
}

func lastStatus(h *harness, avatar string) string {
	got := h.sink.thoughts(avatar)
	if len(got) == 0 {
		return ""
	}
	return got[len(got)-1].Status
}
