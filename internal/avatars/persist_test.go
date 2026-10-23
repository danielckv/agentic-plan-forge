//go:build client && !server

package avatars_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/avatars"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer/synthtest"
)

// memSink is a CheckpointSink that keeps one run, like the file on disk.
type memSink struct {
	mu    sync.Mutex
	runID string
	state json.RawMessage
	puts  int
	drops int
}

func (m *memSink) Put(runID string, state json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runID, m.state, m.puts = runID, append(json.RawMessage(nil), state...), m.puts+1
	return nil
}

func (m *memSink) Get() (string, json.RawMessage, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.state) == 0 {
		return "", nil, false
	}
	return m.runID, m.state, true
}

func (m *memSink) Drop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runID, m.state, m.drops = "", nil, m.drops+1
	return nil
}

func (m *memSink) held() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.state) > 0
}

// A run that stops pays for the drafts it finished. They must survive the
// process, so the next run over the same baseline adopts them instead of
// calling the provider again, even though it has a new run id.
func TestAnUnfinishedRunIsAdoptedByTheNextRun(t *testing.T) {
	sink := &memSink{}
	first := newHarness(t, synthtest.Sample())
	first.in.Checkpoints = avatars.NewCheckpointStore()
	first.in.Checkpoints.Persist(sink, nil)
	first.stub.on("architect", func(c *call, in input) (reply, bool) {
		return reply{Err: errors.New("provider overloaded")}, true
	})
	if _, err := first.run(context.Background(), nil); err == nil {
		t.Fatal("the first run was expected to fail at the architect")
	}
	if !sink.held() {
		t.Fatal("the unfinished run was not persisted")
	}

	second := newHarness(t, synthtest.Sample())
	second.in.RunID = "run-after-restart"
	second.in.Checkpoints = avatars.NewCheckpointStore()
	second.in.Checkpoints.Persist(sink, nil)

	if _, err := second.run(context.Background(), nil); err != nil {
		t.Fatalf("the resumed run failed: %v", err)
	}
	if n := second.stub.count("pm"); n != 0 {
		t.Errorf("the Product Manager ran %d times in the resumed run; its draft was already paid for", n)
	}
	// The adopted drafts reach the window, or the board would show nothing for
	// the stages that did not re-run.
	if got := second.sink.drafts("pm"); len(got) == 0 {
		t.Error("the adopted Product Manager draft was never published")
	}
}

// A checkpoint for another baseline is discarded, not replayed, and the file
// that held it is cleared.
func TestAnIncompatibleCheckpointIsDroppedFromTheSink(t *testing.T) {
	sink := &memSink{}
	first := newHarness(t, synthtest.Sample())
	first.in.Checkpoints = avatars.NewCheckpointStore()
	first.in.Checkpoints.Persist(sink, nil)
	first.stub.on("architect", func(c *call, in input) (reply, bool) {
		return reply{Err: errors.New("provider overloaded")}, true
	})
	if _, err := first.run(context.Background(), nil); err == nil {
		t.Fatal("the first run was expected to fail at the architect")
	}

	second := newHarness(t, synthtest.Sample())
	second.in.RunID = "run-new-baseline"
	second.in.GraphHash = "another-graph"
	second.in.Checkpoints = avatars.NewCheckpointStore()
	second.in.Checkpoints.Persist(sink, nil)

	if _, err := second.run(context.Background(), nil); err != nil {
		t.Fatalf("the re-run failed: %v", err)
	}
	if n := second.stub.count("pm"); n != 1 {
		t.Errorf("the Product Manager ran %d times; a stale checkpoint must be re-run, not replayed", n)
	}
	if got := second.sink.thoughts(relay.StagePM); len(got) == 0 || got[0].Status != "Stale" {
		t.Error("the window was not told that the saved drafts no longer match the baseline")
	}
}

// Forget clears the persisted run: an approved plan leaves no work in
// progress behind.
func TestForgetClearsThePersistedRun(t *testing.T) {
	sink := &memSink{}
	store := avatars.NewCheckpointStore()
	store.Persist(sink, nil)
	h := newHarness(t, synthtest.Sample())
	h.in.Checkpoints = store
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !sink.held() {
		t.Fatal("the run was not persisted")
	}

	store.Forget(h.in.RunID)

	if sink.held() {
		t.Error("Forget left the unfinished run on disk")
	}
	if sink.drops == 0 {
		t.Error("Forget never asked the sink to drop the run")
	}
}
