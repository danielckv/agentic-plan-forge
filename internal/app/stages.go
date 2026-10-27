//go:build client && !server

package app

import (
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// Stage states. They are the window's whole vocabulary for "where is the run",
// so a stage never has to be inferred from whichever field happens to be set.
const (
	StageQueued           = "queued"
	StageRunning          = "running"
	StageWaitingForAnswer = "waiting_for_answer"
	StageRetrying         = "retrying"
	StageFailed           = "failed"
	StageCancelled        = "cancelled"
	StageCompleted        = "completed"
)

// StageView is one stage of the current run as the window shows it. Times are
// authoritative: they come from the executor that owns the call, never from a
// browser timer.
type StageView struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	// StartedAt is when the call started; nil while the stage is queued.
	StartedAt *time.Time `json:"startedAt,omitempty"`
	// LastActivity moves on a heartbeat. It says the call is alive, not that it
	// is progressing, so it never changes State.
	LastActivity *time.Time `json:"lastActivity,omitempty"`
	// DeadlineAt is when the call gives up. It is the configured stage budget,
	// so a stuck stage is visible before it fails.
	DeadlineAt *time.Time `json:"deadlineAt,omitempty"`
	// Recovery names the action that moves the stage forward, when one exists.
	Recovery string `json:"recovery,omitempty"`
}

// stageBook records the stages of one run in the order they are attempted.
// The zero value is usable: it reports no stages, which is what a session that
// has not deliberated yet must show.
type stageBook struct {
	order []string
	byID  map[string]*stageRecord
}

type stageRecord struct {
	view     StageView
	attempts int
}

// reset clears the book for a new run. A new run does not inherit the previous
// run's failures.
func (b *stageBook) reset() {
	b.order = nil
	b.byID = nil
}

// begin records that a stage is about to run, with the budget it was given.
// A second call for the same stage is a retry, not a fresh start: the earlier
// attempt and its deadline stay visible.
func (b *stageBook) begin(id string, budget time.Duration, at time.Time) {
	if b.byID == nil {
		b.byID = map[string]*stageRecord{}
	}
	rec, ok := b.byID[id]
	if !ok {
		rec = &stageRecord{}
		b.byID[id] = rec
		b.order = append(b.order, id)
	}
	rec.attempts++
	rec.view.ID = id
	rec.view.State = StageRunning
	if rec.attempts > 1 {
		rec.view.State = StageRetrying
	}
	started := at
	rec.view.StartedAt = &started
	rec.view.LastActivity = &started
	if budget > 0 {
		deadline := at.Add(budget)
		rec.view.DeadlineAt = &deadline
	} else {
		rec.view.DeadlineAt = nil
	}
	rec.view.Recovery = "cancel"
}

// touch records a heartbeat. It moves LastActivity only: a heartbeat from a
// stage that already failed or completed cannot revive it.
func (b *stageBook) touch(id string, at time.Time) {
	rec, ok := b.byID[id]
	if !ok {
		return
	}
	switch rec.view.State {
	case StageFailed, StageCancelled, StageCompleted:
		return
	}
	rec.view.LastActivity = &at
}

// wait records that the stage is blocked on a human answer.
func (b *stageBook) wait(id, detail string) {
	rec, ok := b.byID[id]
	if !ok {
		return
	}
	rec.view.State = StageWaitingForAnswer
	rec.view.Detail = detail
	rec.view.Recovery = "answer the question"
}

// finish records a terminal outcome for one stage.
func (b *stageBook) finish(id, state, detail, recovery string, at time.Time) {
	rec, ok := b.byID[id]
	if !ok {
		rec = &stageRecord{}
		b.byID[id] = rec
		b.order = append(b.order, id)
		rec.view.ID = id
	}
	rec.view.State = state
	rec.view.Detail = detail
	rec.view.Recovery = recovery
	rec.view.LastActivity = &at
	if state == StageCompleted || state == StageCancelled {
		// A finished stage has no deadline to wait for.
		rec.view.DeadlineAt = nil
	}
}

// fail records a stage failure with the reason the owner needs to act on.
func (b *stageBook) fail(id, detail string, at time.Time) {
	b.finish(id, StageFailed, detail, "retry", at)
}

// cancelAll marks every unfinished stage cancelled, so a cancelled run leaves
// no stage looking like it is still working.
func (b *stageBook) cancelAll(at time.Time) {
	for _, id := range b.order {
		rec := b.byID[id]
		switch rec.view.State {
		case StageCompleted, StageFailed, StageCancelled:
			continue
		}
		rec.view.State = StageCancelled
		rec.view.Recovery = "start the run again"
		rec.view.LastActivity = &at
		rec.view.DeadlineAt = nil
	}
}

// views returns the stages in the order they were attempted. The slice is a
// copy: the caller may hand it to a JSON encoder while the run continues.
func (b *stageBook) views() []StageView {
	if len(b.order) == 0 {
		return []StageView{}
	}
	out := make([]StageView, 0, len(b.order))
	for _, id := range b.order {
		rec := b.byID[id]
		if rec == nil {
			continue
		}
		out = append(out, cloneStage(rec.view))
	}
	return out
}

// recoveryLine names the first action that moves the run forward, or empty when
// nothing is waiting on the owner. It is derived from the stages, so the two can
// never disagree.
func (b *stageBook) recoveryLine() string {
	for _, id := range b.order {
		rec := b.byID[id]
		if rec == nil {
			continue
		}
		if rec.view.State == StageFailed || rec.view.State == StageWaitingForAnswer {
			return rec.view.ID + ": " + rec.view.Recovery
		}
	}
	return ""
}

// cloneStage copies a stage so the book and the wire never share pointers.
func cloneStage(v StageView) StageView {
	out := v
	out.StartedAt = cloneTime(v.StartedAt)
	out.LastActivity = cloneTime(v.LastActivity)
	out.DeadlineAt = cloneTime(v.DeadlineAt)
	return out
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

// stageID names the bookkeeping entry for one avatar stage. Specialists share a
// prefix and carry their workstream, so two specialists never share a record.
func stageID(avatarID string) string {
	if avatarID == "" {
		return relay.StageArchitect
	}
	return avatarID
}
