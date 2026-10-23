//go:build client && !server

package avatars

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Hard limits from the plan.
const (
	// HardMaxCycles is the number of automatic design/review cycles. A user
	// requested rerun gets a new run ID instead of a fourth automatic cycle.
	HardMaxCycles = 3
	// HardMaxParallel is the ceiling on concurrent provider calls per stage.
	HardMaxParallel = 4
	// DefaultRetryBackoff is the base of the exponential backoff before the one
	// retry of a transient provider failure.
	DefaultRetryBackoff = time.Second
	// maxSummaryRunes bounds a public summary relayed to the window.
	maxSummaryRunes = 400
)

// ModelSlots names the model each slot resolves to. Empty means the host's
// default for that slot.
type ModelSlots struct {
	Main  string
	Judge string
}

// RunInput is everything one deliberation run is allowed to see. It holds a
// frozen PRD revision and a bounded graph snapshot: no live paths and no tool
// handles. The fields after StartCycle are optional and exist for tests and
// resume checks.
type RunInput struct {
	// RunID is assigned by the session authority.
	RunID string
	// PRD is the immutable PRD revision frozen at the baseline.
	PRD contracts.NormalizedPRD
	// Graph is the bounded snapshot handed to the technical avatars.
	Graph contracts.RepoContextGraph
	// GraphHash identifies Graph; resume refuses a checkpoint with another hash.
	GraphHash string
	// DocDrift is the documentation-drift evidence the architect must cover.
	DocDrift []contracts.DocDriftItem
	// EvidenceNotes name evidence the caller cut to fit the stage bounds, in
	// the words the avatars read. A stage repeats them in its payload and adds
	// its own when it has to cut further.
	EvidenceNotes []string
	// Context is what the repository already says about itself: excerpts from
	// its notes, specs, plans and decision records, and its Git state. The
	// architect, specialists and critic receive it; the Product Manager never
	// does.
	Context contracts.ProjectContext
	// ContextHash identifies Context; resume refuses a checkpoint with another.
	ContextHash string
	Models      ModelSlots
	// MaxCycles is clamped to 1..HardMaxCycles; zero selects HardMaxCycles.
	MaxCycles int
	// MaxParallel bounds concurrent provider calls per stage; the effective
	// value is min(MaxParallel, HardMaxParallel). Zero selects HardMaxParallel.
	MaxParallel int
	// StartCycle is the cycle to enter (1-based). Zero or one starts at cycle
	// one; a retry of a failed stage re-enters its cycle and resumes from the
	// checkpoint.
	StartCycle int

	// HoursPerDay is the productive human workday frozen into the synthesis
	// lock. Zero selects the compiler default (9).
	HoursPerDay float64
	// CallTimeout bounds one provider call. Zero or more than
	// contracts.MaxGenerateTimeout selects contracts.MaxGenerateTimeout.
	CallTimeout time.Duration
	// RetryBackoff is the base backoff before the single transient retry. Zero
	// selects DefaultRetryBackoff.
	RetryBackoff time.Duration
	// Sleep waits for d or until ctx ends. Nil selects a timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now supplies the clock for decision timestamps. Nil selects time.Now.
	Now func() time.Time
	// Checkpoints stores resumable run state. Nil selects the process-wide
	// store.
	Checkpoints *CheckpointStore
}

func (in RunInput) maxCycles() int {
	switch {
	case in.MaxCycles <= 0 || in.MaxCycles > HardMaxCycles:
		return HardMaxCycles
	default:
		return in.MaxCycles
	}
}

func (in RunInput) parallel() int {
	n := in.MaxParallel
	if n <= 0 {
		n = HardMaxParallel
	}
	return min(n, HardMaxParallel)
}

func (in RunInput) callTimeout() time.Duration {
	if in.CallTimeout <= 0 || in.CallTimeout > contracts.MaxGenerateTimeout {
		return contracts.MaxGenerateTimeout
	}
	return in.CallTimeout
}

func (in RunInput) backoff() time.Duration {
	if in.RetryBackoff <= 0 {
		return DefaultRetryBackoff
	}
	return in.RetryBackoff
}

func (in RunInput) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

func (in RunInput) sleep(ctx context.Context, d time.Duration) error {
	if in.Sleep != nil {
		return in.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StageError reports that one stage could not produce a valid draft. The
// drafts of the stages that did finish stay in the checkpoint, so an explicit
// retry re-runs only the failed stage.
type StageError struct {
	// Stage is the stage identifier: pm, architect, specialist:<workstream>,
	// critic or synthesizer.
	Stage string
	// Partial reports that the provider streamed output before failing. A
	// partial failure is never retried automatically: that would bill the same
	// work twice.
	Partial bool
	Err     error
}

func (e *StageError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s stage failed", e.Stage)
	if e.Partial {
		b.WriteString(" after partial output (explicit retry required)")
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	return b.String()
}

func (e *StageError) Unwrap() error { return e.Err }

// PausedError reports that the run reached its cycle cap with blocking issues.
// The run is paused on Page 2: synthesis stays disabled until the user retries
// (which assigns a new run) or revises the baseline.
type PausedError struct {
	// Cycle is the last automatic cycle that ran.
	Cycle int
	// Blockers are the issues still open.
	Blockers []contracts.Critique
}

func (e *PausedError) Error() string {
	return fmt.Sprintf("deliberation paused after cycle %d with %d blocking issues open", e.Cycle, len(e.Blockers))
}

// emitError wraps a failure of the event sink: the authority refused an event
// (fenced, stale run, illegal phase). It is never a stage failure; the run just
// stops.
type emitError struct{ err error }

func (e *emitError) Error() string { return "event refused: " + e.err.Error() }
func (e *emitError) Unwrap() error { return e.err }
