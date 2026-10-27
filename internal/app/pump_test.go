//go:build client && !server

package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

func thought(t *testing.T, status string, host string) relay.WSEnvelope {
	t.Helper()
	raw, err := json.Marshal(relay.AvatarThoughtPayload{
		RunID: "run-1", AvatarID: "architect", Status: status, Host: host, PublicSummary: status,
	})
	if err != nil {
		t.Fatal(err)
	}
	return relay.WSEnvelope{EventType: relay.EventAvatarThought, Payload: raw}
}

func decision(t *testing.T, id string) relay.WSEnvelope {
	t.Helper()
	raw, err := json.Marshal(relay.DecisionPromptPayload{DecisionID: id})
	if err != nil {
		t.Fatal(err)
	}
	return relay.WSEnvelope{EventType: relay.EventDecisionPrompt, Payload: raw}
}

// A full queue drops progress chatter, never a decision. Flapping between
// "thinking" and "drafting" on many avatars must not silently lose the one
// event a human has to answer.
func TestPumpDropsHeartbeatsBeforeDecisions(t *testing.T) {
	p := newPump()
	for range maxPumpEvents + 50 {
		p.push(thought(t, "Thinking", ""))
	}
	if !p.resyncNeeded() {
		t.Fatal("an unbounded flood of heartbeats did not set resync")
	}

	// With a fresh pump, the decision survives a flood of chatter.
	p = newPump()
	for range maxPumpEvents - 1 {
		p.push(thought(t, "Thinking", ""))
	}
	if !p.push(decision(t, "d-1")) {
		t.Fatal("a decision was refused while chatter could have been dropped")
	}
	if p.resyncNeeded() {
		t.Error("resync was raised while a disposable event could still be dropped")
	}

	// Close first: a live pump blocks in next until the next push, so draining
	// without closing would hang the test rather than fail it.
	p.close()
	found := false
	for {
		env, ok := p.next(context.Background())
		if !ok {
			break
		}
		if env.EventType == relay.EventDecisionPrompt {
			found = true
		}
	}
	if !found {
		t.Fatal("the decision was dropped from the queue")
	}
}

// A grounded thought (host, model, step) is what the window uses to say where
// work is running, so it is not disposable chatter.
func TestPumpKeepsGroundedThoughts(t *testing.T) {
	p := newPump()
	for range maxPumpEvents {
		p.push(thought(t, "Drafting", "claude"))
	}
	// The queue is full of grounded thoughts; there is nothing disposable, so
	// the next event must be refused rather than replacing one.
	if p.push(thought(t, "Thinking", "")) {
		t.Fatal("a full queue of grounded thoughts accepted another event")
	}
	if !p.resyncNeeded() {
		t.Error("a refused event did not raise resync")
	}
}

// A terminal status is evidence, not chatter: it is never disposable.
func TestPumpNeverDropsATerminalStatus(t *testing.T) {
	for _, status := range []string{"Synced", "Failed", "Cancelled", "Stale"} {
		p := newPump()
		for range maxPumpEvents {
			p.push(thought(t, "Thinking", ""))
		}
		if dropCoalescible(thought(t, status, "")) {
			t.Errorf("status %q was treated as disposable", status)
		}
	}
}

// Order is preserved for everything that survives.
func TestPumpPreservesOrderOfWhatItKeeps(t *testing.T) {
	p := newPump()
	first := decision(t, "d-1")
	second := decision(t, "d-2")
	p.push(first)
	p.push(second)
	p.close()

	got1, ok := p.next(context.Background())
	if !ok {
		t.Fatal("no first event")
	}
	got2, ok := p.next(context.Background())
	if !ok {
		t.Fatal("no second event")
	}
	if got1.EventID != first.EventID || got2.EventID != second.EventID {
		t.Fatal("the queue reordered two decisions")
	}
}

// A closed pump reports nothing and accepts nothing, so shutdown is quiet.
func TestPumpClosedIsQuiet(t *testing.T) {
	p := newPump()
	p.close()
	if p.push(decision(t, "d-1")) {
		t.Error("a closed pump accepted an event")
	}
	if _, ok := p.next(context.Background()); ok {
		t.Error("a closed pump returned an event")
	}
}
