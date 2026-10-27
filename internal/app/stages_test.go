//go:build client && !server

package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// A stage is never inferred in the browser: the executor owns the state, the
// timestamps and the deadline, and a heartbeat can only say the call is alive.
func TestStageBookTracksStateAndDeadline(t *testing.T) {
	var b stageBook
	at := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	b.begin("architect", 600*time.Second, at)

	stage := b.views()[0]
	if stage.State != StageRunning {
		t.Errorf("state = %q, want running", stage.State)
	}
	if stage.StartedAt == nil || !stage.StartedAt.Equal(at) {
		t.Errorf("started at = %v, want %v", stage.StartedAt, at)
	}
	if stage.DeadlineAt == nil || !stage.DeadlineAt.Equal(at.Add(600*time.Second)) {
		t.Errorf("deadline = %v, want start + the configured budget", stage.DeadlineAt)
	}

	// A heartbeat moves last-activity and nothing else.
	later := at.Add(30 * time.Second)
	b.touch("architect", later)
	stage = b.views()[0]
	if stage.LastActivity == nil || !stage.LastActivity.Equal(later) {
		t.Errorf("last activity = %v, want %v", stage.LastActivity, later)
	}
	if stage.State != StageRunning {
		t.Errorf("a heartbeat changed the state to %q", stage.State)
	}
}

// A retry is a second attempt on the same stage: the deadline restarts and the
// state says retrying, so a card does not look like a first run.
func TestStageBookMarksARetry(t *testing.T) {
	var b stageBook
	at := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	b.begin("specialist:billing", 600*time.Second, at)
	b.begin("specialist:billing", 600*time.Second, at.Add(2*time.Minute))

	stage := b.views()[0]
	if stage.State != StageRetrying {
		t.Errorf("state = %q, want retrying", stage.State)
	}
	if stage.StartedAt == nil || !stage.StartedAt.Equal(at.Add(2*time.Minute)) {
		t.Errorf("started at = %v, want the retry's start", stage.StartedAt)
	}
}

// A heartbeat from a failed stage cannot revive it: the call is over.
func TestStageBookHeartbeatCannotReviveAFailedStage(t *testing.T) {
	var b stageBook
	at := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	b.begin("architect", 600*time.Second, at)
	b.fail("architect", "host timed out", at.Add(time.Minute))
	b.touch("architect", at.Add(2*time.Minute))

	stage := b.views()[0]
	if stage.State != StageFailed {
		t.Errorf("state = %q after a late heartbeat, want failed", stage.State)
	}
	if stage.Recovery != "retry" {
		t.Errorf("recovery = %q, want retry", stage.Recovery)
	}
	if b.recoveryLine() == "" {
		t.Error("a failed stage produced no recovery line")
	}
}

// Cancelling a run marks every unfinished stage cancelled, so nothing keeps
// spinning for work that will never finish.
func TestStageBookCancelsUnfinishedStages(t *testing.T) {
	var b stageBook
	at := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	b.begin("pm", 600*time.Second, at)
	b.finish("pm", StageCompleted, "published", "", at.Add(time.Minute))
	b.begin("architect", 600*time.Second, at.Add(time.Minute))
	b.wait("architect", "waiting for an answer to d-1")

	b.cancelAll(at.Add(2 * time.Minute))
	for _, stage := range b.views() {
		switch stage.ID {
		case "pm":
			if stage.State != StageCompleted {
				t.Errorf("a completed stage became %q on cancel", stage.State)
			}
		case "architect":
			if stage.State != StageCancelled {
				t.Errorf("an unfinished stage stayed %q on cancel", stage.State)
			}
			if stage.DeadlineAt != nil {
				t.Error("a cancelled stage still advertises a deadline")
			}
		}
	}
}

// A question is a state, not a stall: the stage says it is waiting and names
// the action that moves it on.
func TestStageBookShowsAQuestionAsWaiting(t *testing.T) {
	var b stageBook
	at := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	b.begin("architect", 600*time.Second, at)
	b.wait("architect", "waiting for an answer to d-7")

	stage := b.views()[0]
	if stage.State != StageWaitingForAnswer {
		t.Errorf("state = %q, want waiting_for_answer", stage.State)
	}
	if b.recoveryLine() != "architect: answer the question" {
		t.Errorf("recovery = %q, want the answer action", b.recoveryLine())
	}
}

// Status copies the stages: a caller must not be able to mutate the book by
// holding the slice, and the encoder must not race the run.
func TestStageViewsAreCopied(t *testing.T) {
	var b stageBook
	at := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	b.begin("pm", 600*time.Second, at)
	first := b.views()
	*first[0].StartedAt = at.Add(time.Hour)

	if got := b.views()[0]; got.StartedAt != nil && got.StartedAt.Equal(at.Add(time.Hour)) {
		t.Fatal("mutating the returned stage changed the book")
	}
}

// An empty book reports an empty list, never nil: the window maps over it.
func TestEmptyStageBookIsAnEmptyList(t *testing.T) {
	var b stageBook
	got := b.views()
	if got == nil || len(got) != 0 {
		t.Fatalf("views = %v, want an empty slice", got)
	}
	if b.recoveryLine() != "" {
		t.Error("an empty run reported a recovery action")
	}
}

// The stage book follows the events the avatars already emit, so Status never
// has to guess where the run is.
func TestExecutorStageBookFollowsAvatarEvents(t *testing.T) {
	exec := newExecutor(executorConfig{Repo: t.TempDir(), Logger: quietLogger()})
	emit := func(status string) contracts.PublicEvent {
		raw, err := json.Marshal(relay.AvatarThoughtPayload{
			RunID: "run-1", AvatarID: "architect", Cycle: 1, Status: status, PublicSummary: status, Host: "claude",
		})
		if err != nil {
			t.Fatal(err)
		}
		return contracts.PublicEvent{Type: string(relay.EventAvatarThought), Payload: raw}
	}

	exec.noteStage(emit("Thinking"))
	stages := exec.Status().Stages
	if len(stages) != 1 || stages[0].State != StageRunning {
		t.Fatalf("stages = %+v, want one running architect", stages)
	}

	exec.noteStage(emit(statusFailedLike))
	stages = exec.Status().Stages
	if stages[0].State != StageFailed {
		t.Errorf("state = %q after a failure event, want failed", stages[0].State)
	}
	if exec.Status().Recovery == "" {
		t.Error("a failed stage produced no recovery hint in Status")
	}
}

// A question event blocks the stage with a visible state and action, so a
// product manager sees "waiting for you" instead of an unexplained pause.
func TestExecutorStageBookShowsADecisionAsWaiting(t *testing.T) {
	exec := newExecutor(executorConfig{Repo: t.TempDir(), Logger: quietLogger()})
	raw, err := json.Marshal(relay.DecisionPromptPayload{DecisionID: "d-1"})
	if err != nil {
		t.Fatal(err)
	}
	exec.noteStage(contracts.PublicEvent{Type: string(relay.EventDecisionPrompt), Payload: raw})

	status := exec.Status()
	if len(status.Stages) == 0 || status.Stages[0].State != StageWaitingForAnswer {
		t.Fatalf("stages = %+v, want a waiting stage", status.Stages)
	}
	if status.Recovery == "" {
		t.Error("a waiting stage produced no recovery hint")
	}
}

// A stopped run cancels its stages: the window must not keep a spinner on work
// that was abandoned.
func TestExecutorStopRunCancelsStages(t *testing.T) {
	exec := newExecutor(executorConfig{Repo: t.TempDir(), Logger: quietLogger()})
	raw, _ := json.Marshal(relay.AvatarThoughtPayload{AvatarID: "pm", Status: "Thinking"})
	exec.noteStage(contracts.PublicEvent{Type: string(relay.EventAvatarThought), Payload: raw})
	exec.stopRun()

	for _, stage := range exec.Status().Stages {
		if stage.State == StageRunning {
			t.Errorf("stage %q is still running after stopRun", stage.ID)
		}
	}
}
