//go:build client && !server

package avatars

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// The narrator is the avatar that watches the others.
//
// Every other avatar reports a step: a status, which part of its stage is
// running, which host and model serve it, and how far the call has got. Those
// steps are true but thin — "Running for 40s, 7.2 KB written, now on
// contracts" says nothing about what the run is deciding. The narrator reads
// the steps of every avatar together and says, in one or two sentences, what
// is going on. That line is what the avatar room shows above the faces while
// a cycle runs.
//
// Three rules hold it in place:
//
//  1. It never blocks a stage. It runs in its own goroutine and its failures
//     are dropped, never returned. A run that loses its narrator loses a
//     status line and nothing else.
//  2. It reads steps, never drafts. The steps are already public summaries, so
//     the narrator adds no new boundary to audit.
//  3. It is rate limited and bounded. One call every narrateEvery at most, and
//     only when a step arrived since the last one.
const (
	// narrateEvery is the floor between two narrator calls.
	narrateEvery = 25 * time.Second
	// narrateTimeout bounds one narrator call. It is far below a stage budget:
	// a narration that is not quick is not worth having.
	narrateTimeout = 60 * time.Second
	// narrateMaxSteps bounds the steps one narration reads, newest last.
	narrateMaxSteps = 40
	// narrateMaxOutput bounds a narration's bytes.
	narrateMaxOutput = 4 << 10
)

// stepRecord is one avatar's report of what it is doing now.
type stepRecord struct {
	Avatar  string `json:"avatar"`
	Cycle   int    `json:"cycle"`
	Status  string `json:"status"`
	Step    string `json:"step,omitempty"`
	Host    string `json:"host,omitempty"`
	Model   string `json:"model,omitempty"`
	Context string `json:"evidence,omitempty"`
	Summary string `json:"said,omitempty"`
	Seconds int    `json:"seconds,omitempty"`
}

// narratorPayload is everything one narration reads.
type narratorPayload struct {
	Cycle    int          `json:"cycle"`
	MaxCycle int          `json:"max_cycles"`
	Steps    []stepRecord `json:"steps"`
	Previous string       `json:"previous_status,omitempty"`
}

// narrator collects step records and turns them into a live status line.
type narrator struct {
	o *orchestrator

	mu       sync.Mutex
	steps    []stepRecord
	fresh    bool
	previous string

	stop chan struct{}
	done chan struct{}
}

func newNarrator(o *orchestrator) *narrator {
	return &narrator{o: o, stop: make(chan struct{}), done: make(chan struct{})}
}

// record adds one avatar's step. It never blocks and never fails.
func (n *narrator) record(rec stepRecord) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.steps = append(n.steps, rec)
	if len(n.steps) > narrateMaxSteps {
		n.steps = append(n.steps[:0], n.steps[len(n.steps)-narrateMaxSteps:]...)
	}
	n.fresh = true
}

// take returns the collected steps when at least one is new since the last
// narration, and reports whether there was anything to say.
func (n *narrator) take() ([]stepRecord, string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.fresh || len(n.steps) == 0 {
		return nil, "", false
	}
	n.fresh = false
	return append([]stepRecord(nil), n.steps...), n.previous, true
}

func (n *narrator) remember(status string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.previous = status
}

// start runs the narration loop until ctx ends or close is called.
func (n *narrator) start(ctx context.Context) {
	if n == nil {
		return
	}
	go func() {
		defer close(n.done)
		t := time.NewTicker(n.o.in.narrateEvery())
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-n.stop:
				return
			case <-t.C:
				n.narrate(ctx)
			}
		}
	}()
}

// close ends the loop and waits for the call in flight.
func (n *narrator) close() {
	if n == nil {
		return
	}
	close(n.stop)
	<-n.done
}

// narrate runs one narration. Every failure is dropped: the narrator is a
// status line, and a status line must never end a run.
func (n *narrator) narrate(ctx context.Context) {
	steps, previous, ok := n.take()
	if !ok {
		return
	}
	cycle := steps[len(steps)-1].Cycle
	payload, err := json.Marshal(narratorPayload{
		Cycle: cycle, MaxCycle: n.o.maxC, Steps: steps, Previous: previous,
	})
	if err != nil {
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, narrateTimeout)
	defer cancel()
	res, err := n.o.gen.GenerateStream(callCtx, contracts.GenerateRequest{
		Role: contracts.RoleNarrator, ModelSlot: contracts.SlotMain,
		SystemPrompt: NarratorSystemPrompt(), UserPayload: payload, RunID: n.o.in.RunID,
		Timeout: narrateTimeout, MaxOutput: narrateMaxOutput,
	}, func(ev contracts.GenerationEvent) error {
		if ev.Kind == contracts.EventPlacement && ev.Placement != nil {
			d := n.o.detail(relay.StageNarrator)
			n.o.liveMu.Lock()
			d.host, d.model = ev.Placement.Host, ev.Placement.Model
			n.o.liveMu.Unlock()
		}
		return callCtx.Err()
	})
	if err != nil || ctx.Err() != nil {
		return
	}
	status := narrationOf(res.Content)
	if status == "" {
		return
	}
	n.remember(status)
	// The narrator is not an avatar of the cycle: it holds no draft and gates
	// nothing. It is published outside o.thought so it never enters o.seen,
	// where it would be counted as an avatar still working.
	d := n.o.snapshotDetail(relay.StageNarrator)
	_ = n.o.emitEvent(ctx, relay.EventAvatarThought, relay.AvatarThoughtPayload{
		RunID: n.o.in.RunID, AvatarID: relay.StageNarrator, Cycle: cycle,
		Status: statusNarrating, PublicSummary: cleanSummary(status),
		Host: d.host, Model: d.model, Step: fmt.Sprintf("cycle %d of %d", cycle, n.o.maxC),
	})
}

// narrationOf reads the status sentence out of a narrator reply. A reply that
// is not the agreed object is used as plain text when it is short enough: a
// narration is prose, and rejecting it would cost the line for nothing.
func narrationOf(raw json.RawMessage) string {
	var doc struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &doc); err == nil {
		return strings.TrimSpace(doc.Status)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" || strings.HasPrefix(text, "{") || len(text) > narrateMaxOutput {
		return ""
	}
	return text
}
