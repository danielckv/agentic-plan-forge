//go:build client && !server

package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// blockingGen holds every call until release is closed, so a pipeline stage can
// be observed while it is in flight.
type blockingGen struct {
	mu      sync.Mutex
	calls   int
	release chan struct{}
}

func (g *blockingGen) GenerateStream(ctx context.Context, _ contracts.GenerateRequest,
	_ func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	select {
	case <-g.release:
		return contracts.GenerationResult{}, context.Canceled
	case <-ctx.Done():
		return contracts.GenerationResult{}, ctx.Err()
	}
}

func (g *blockingGen) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// A double click on Retry must not cancel the call that is already running and
// pay for it again. The second request is refused, and the window is told the
// stage is still running so the card re-synchronises.
func TestASecondRetryDoesNotRestartARunningStage(t *testing.T) {
	rm := newRoom("session", nil, func([]byte) error { return nil }, quietLogger(), "# Brief\n\nExport invoices.\n")
	defer rm.close()
	gen := &blockingGen{release: make(chan struct{})}
	defer close(gen.release)
	exec := newExecutor(executorConfig{
		Auth: soloAuthority{rm}, Logger: quietLogger(), Repo: t.TempDir(), Gen: gen,
	})
	wireSolo(rm, exec)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec.ctx = ctx
	exec.mu.Lock()
	exec.phase, exec.runID, exec.cycle = relay.PhaseDeliberation, "run-1", 1
	exec.mu.Unlock()

	exec.retryStage(relay.RetryStagePayload{RunID: "run-1", StageID: relay.StageArchitect})
	waitFor(t, func() bool { return gen.count() == 1 })

	exec.retryStage(relay.RetryStagePayload{RunID: "run-1", StageID: relay.StageArchitect})
	time.Sleep(50 * time.Millisecond)
	if got := gen.count(); got != 1 {
		t.Errorf("provider calls = %d, want 1: the second retry must not start a second run", got)
	}
	exec.mu.Lock()
	busy := exec.busy
	exec.mu.Unlock()
	if busy != busyDeliberation {
		t.Errorf("busy = %q, want the deliberation claim held", busy)
	}

	// Stopping the run frees the claim, so a later retry is accepted again.
	exec.stopRun()
	exec.mu.Lock()
	busy = exec.busy
	exec.mu.Unlock()
	if busy != "" {
		t.Errorf("busy = %q after stopRun, want it cleared", busy)
	}
}

// recordingAuthority keeps the executor's events so a test can read what the
// window was told.
type recordingAuthority struct {
	authority
	mu      sync.Mutex
	thought []relay.AvatarThoughtPayload
}

func (a *recordingAuthority) Emit(t relay.WSEventType, payload any) error {
	if t == relay.EventAvatarThought {
		if p, ok := payload.(relay.AvatarThoughtPayload); ok {
			a.mu.Lock()
			a.thought = append(a.thought, p)
			a.mu.Unlock()
		}
	}
	return a.authority.Emit(t, payload)
}

// The refusal is reported, not swallowed: the window hears that the stage is
// still running, so a card cannot sit on a button that does nothing.
func TestARefusedRetryTellsTheWindow(t *testing.T) {
	rm := newRoom("session", nil, func([]byte) error { return nil }, quietLogger(), "")
	defer rm.close()
	auth := &recordingAuthority{authority: soloAuthority{rm}}
	exec := newExecutor(executorConfig{Auth: auth, Logger: quietLogger(), Repo: t.TempDir()})

	exec.stageAlreadyRunning("run-1", relay.StageArchitect, 2)

	auth.mu.Lock()
	defer auth.mu.Unlock()
	if len(auth.thought) != 1 {
		t.Fatalf("thoughts = %d, want one", len(auth.thought))
	}
	got := auth.thought[0]
	if got.AvatarID != relay.StageArchitect || got.Cycle != 2 || !strings.Contains(got.PublicSummary, "already running") {
		t.Fatalf("thought = %+v, want the architect card told the stage is already running", got)
	}
}
