//go:build client && !server

package delivery

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/config"
)

// Stage is where a build has got to. The window reads this and nothing else to
// decide what to show, so a stage is never inferred from which fields happen to
// be set — the lesson Page 2 learned when a derived status left the synthesizer
// showing Queued forever (INV-7).
type Stage string

const (
	// StageIdle means the plan is approved and nothing has been built.
	StageIdle Stage = "IDLE"
	// StageCoding means the host agent is writing code in the worktree.
	StageCoding Stage = "CODING"
	// StageCoded means coding finished and the diff is ready to review.
	StageCoded Stage = "CODED"
	// StageBuilding means the worktree is being built into an image.
	StageBuilding Stage = "BUILDING"
	// StagePreviewing means the image is running and reachable.
	StagePreviewing Stage = "PREVIEWING"
	// StageDeploying means the deploy target is being written to.
	StageDeploying Stage = "DEPLOYING"
	// StageDeployed is the end state.
	StageDeployed Stage = "DEPLOYED"
	// StageFailed means the last action failed; Error says which and why.
	StageFailed Stage = "FAILED"
)

// ErrBusy reports a second action started while one was running.
var ErrBusy = errors.New("a build action is already running")

// View is the whole build state, as the window sees it.
type View struct {
	Stage    Stage  `json:"stage"`
	Approval string `json:"approval,omitempty"`
	// Worktree is where coding happens. Shown so the owner can open it.
	Worktree string `json:"worktree,omitempty"`
	Branch   string `json:"branch,omitempty"`
	// Steps is the agent's progress, newest last.
	Steps []CoderStep `json:"steps"`
	// Log is the tail of the current build or deploy output.
	Log []string `json:"log"`
	// Changes summarises the worktree; nil until coding has finished once.
	Changes *Changes `json:"changes,omitempty"`
	Box     *Box     `json:"box,omitempty"`
	// BoxRunning is checked live: a container can exit on its own.
	BoxRunning bool        `json:"box_running"`
	Deployment *Deployment `json:"deployment,omitempty"`
	// Target is the configured deploy target; empty means the owner has not
	// been asked yet, which is what makes the window ask.
	Target string `json:"target"`
	// TargetReady is empty when the target can run here, else the reason it
	// cannot, so the window can say so before the owner commits to it.
	TargetReady string `json:"target_ready,omitempty"`
	Error       string `json:"error,omitempty"`
	// Finished is the owner's explicit "I am done": the window stops holding
	// the session open once it is true.
	Finished bool `json:"finished"`
	// Busy is true while an action is running, so the window disables its
	// buttons rather than queueing a second run behind the first.
	Busy      bool      `json:"busy"`
	UpdatedAt time.Time `json:"updated_at"`
}

// maxLogLines bounds the streamed log the window holds.
const maxLogLines = 1000

// Build owns one approval's path from spec to deployed. One Build exists per
// session; its methods are safe to call from the HTTP handlers and the
// background goroutines at once.
type Build struct {
	repo string

	mu         sync.Mutex
	cfg        config.Config
	stage      Stage
	approval   string
	spec       string
	wt         Worktree
	steps      []CoderStep
	log        []string
	changes    *Changes
	box        *Box
	deployment *Deployment
	err        string
	busy       bool
	finished   bool
	updatedAt  time.Time
	// cancel stops the running action.
	cancel context.CancelFunc
}

// NewBuild returns the build controller for a repository.
func NewBuild(repo string, cfg config.Config) *Build {
	return &Build{repo: repo, cfg: cfg, stage: StageIdle, updatedAt: time.Now()}
}

// SetConfig replaces the configuration, so a target chosen in the window takes
// effect without restarting the session.
func (b *Build) SetConfig(cfg config.Config) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cfg = cfg
	b.updatedAt = time.Now()
}

// Config returns the configuration the build is using.
func (b *Build) Config() config.Config {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cfg
}

// Adopt records the approval this build implements. It is called once the plan
// is approved and its spec is on disk.
func (b *Build) Adopt(approval, spec string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.approval, b.spec = approval, spec
	b.updatedAt = time.Now()
}

// View reports the current state. The container check runs outside the lock:
// it shells out, and holding the lock across a process would block the window.
func (b *Build) View(ctx context.Context) View {
	b.mu.Lock()
	v := View{
		Stage: b.stage, Approval: b.approval, Worktree: b.wt.Path, Branch: b.wt.Branch,
		Steps: append([]CoderStep(nil), b.steps...), Log: append([]string(nil), b.log...),
		Changes: b.changes, Box: b.box, Deployment: b.deployment,
		Target: b.cfg.DeployTarget, Error: b.err, Busy: b.busy,
		Finished: b.finished, UpdatedAt: b.updatedAt,
	}
	box := b.box
	b.mu.Unlock()
	if v.Steps == nil {
		v.Steps = []CoderStep{}
	}
	if v.Log == nil {
		v.Log = []string{}
	}
	if err := TargetAvailable(v.Target); err != nil {
		v.TargetReady = err.Error()
	}
	if box != nil {
		v.BoxRunning = BoxRunning(ctx, *box)
	}
	return v
}

// Cancel stops whatever action is running. It is not an undo: files the agent
// already wrote stay written, which is why they are written in a worktree.
func (b *Build) Cancel() {
	b.mu.Lock()
	cancel := b.cancel
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// begin claims the controller for one action.
func (b *Build) begin(ctx context.Context, stage Stage) (context.Context, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.busy {
		return nil, ErrBusy
	}
	if b.approval == "" {
		return nil, errors.New("no approved plan to build")
	}
	runCtx, cancel := context.WithCancel(ctx)
	b.busy, b.stage, b.err, b.cancel = true, stage, "", cancel
	b.log = nil
	b.updatedAt = time.Now()
	return runCtx, nil
}

// end releases the controller and records where the action left the build.
func (b *Build) end(stage Stage, err error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.busy, b.cancel = false, nil
	if err != nil {
		b.stage, b.err = StageFailed, err.Error()
	} else {
		b.stage, b.err = stage, ""
	}
	b.updatedAt = time.Now()
	return err
}

func (b *Build) appendLog(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.log) >= maxLogLines {
		b.log = b.log[1:]
	}
	b.log = append(b.log, line)
	b.updatedAt = time.Now()
}

func (b *Build) appendStep(step CoderStep) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.steps) >= MaxTranscriptLines {
		b.steps = b.steps[1:]
	}
	b.steps = append(b.steps, step)
	b.updatedAt = time.Now()
}

// StartCoding opens the worktree and drives the host agent through the plan.
// It returns once the run finishes; callers run it in a goroutine and watch
// View for progress.
func (b *Build) StartCoding(ctx context.Context, host, model string) error {
	runCtx, err := b.begin(ctx, StageCoding)
	if err != nil {
		return err
	}
	b.mu.Lock()
	approval, spec := b.approval, b.spec
	b.steps = nil
	b.mu.Unlock()

	wt, err := OpenWorktree(runCtx, b.repo, approval)
	if err != nil {
		return b.end(StageFailed, err)
	}
	b.mu.Lock()
	b.wt = wt
	b.mu.Unlock()

	_, codeErr := Code(runCtx, wt, CodeRequest{Host: host, Model: model, Spec: spec}, b.appendStep)
	// The diff is read whatever the agent's exit code was: a run that failed
	// halfway still wrote files, and hiding them helps nobody.
	changes, changeErr := ReadChanges(context.WithoutCancel(runCtx), wt)
	b.mu.Lock()
	if changeErr == nil {
		b.changes = &changes
	}
	b.mu.Unlock()
	if codeErr != nil {
		return b.end(StageFailed, codeErr)
	}
	if changeErr != nil {
		return b.end(StageFailed, changeErr)
	}
	return b.end(StageCoded, nil)
}

// StartPreview builds the worktree into an image and runs it.
func (b *Build) StartPreview(ctx context.Context, port int) error {
	runCtx, err := b.begin(ctx, StageBuilding)
	if err != nil {
		return err
	}
	b.mu.Lock()
	wt, approval, previous := b.wt, b.approval, b.box
	b.mu.Unlock()
	if wt.Path == "" {
		return b.end(StageFailed, errors.New("nothing has been coded yet"))
	}
	if previous != nil {
		_ = StopBox(runCtx, *previous)
	}
	box, err := BuildBox(runCtx, wt, approval, b.appendLog)
	if err != nil {
		return b.end(StageFailed, err)
	}
	started, err := StartBox(runCtx, box, approval, port)
	if err != nil {
		return b.end(StageFailed, err)
	}
	b.mu.Lock()
	b.box = &started
	b.mu.Unlock()
	return b.end(StagePreviewing, nil)
}

// StopPreview removes the preview container.
func (b *Build) StopPreview(ctx context.Context) error {
	b.mu.Lock()
	box := b.box
	b.box = nil
	if b.stage == StagePreviewing {
		b.stage = StageCoded
	}
	b.updatedAt = time.Now()
	b.mu.Unlock()
	if box == nil {
		return nil
	}
	return StopBox(ctx, *box)
}

// Deploy publishes the built plan to the configured target.
func (b *Build) Deploy(ctx context.Context) error {
	runCtx, err := b.begin(ctx, StageDeploying)
	if err != nil {
		return err
	}
	b.mu.Lock()
	cfg, wt, approval := b.cfg, b.wt, b.approval
	box := Box{}
	if b.box != nil {
		box = *b.box
	}
	b.mu.Unlock()

	if cfg.DeployTarget == config.DeployTargetDocker && box.Image == "" {
		// A docker deploy needs an image; building it here saves the owner a
		// round trip through Preview they may not want.
		built, buildErr := BuildBox(runCtx, wt, approval, b.appendLog)
		if buildErr != nil {
			return b.end(StageFailed, buildErr)
		}
		box = built
		b.mu.Lock()
		b.box = &built
		b.mu.Unlock()
	}
	deployment, err := Deploy(runCtx, cfg, wt, box, approval, b.appendLog)
	if err != nil {
		return b.end(StageFailed, err)
	}
	b.mu.Lock()
	b.deployment = &deployment
	b.mu.Unlock()
	return b.end(StageDeployed, nil)
}

// HoldsWindow reports whether the window should stay open after an approval so
// the owner can build, preview and deploy.
//
// Before this package, an approval closed the window after five seconds: the
// plan was the product and the host agent took it from there. A build happens
// in that window, so the countdown would close it mid-run. The owner's own
// choice decides: "Don't deploy" keeps the old behaviour exactly, and any other
// answer — including not having been asked yet — holds the window.
func (b *Build) HoldsWindow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cfg.DeployTarget != config.DeployTargetNone
}

// Finished reports whether the owner is done with the build, so the session can
// close. It is set by the window's explicit Finish, never inferred.
func (b *Build) Finished() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.finished
}

// Finish marks the build done and releases the window.
func (b *Build) Finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.finished = true
	b.updatedAt = time.Now()
}

// Worktree reports the worktree, so a handler can read a file out of it.
func (b *Build) Worktree() Worktree {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.wt
}

// Discard removes the preview container and the worktree. It is the owner's
// explicit "throw this away": uncommitted code in the worktree goes with it.
func (b *Build) Discard(ctx context.Context) error {
	b.mu.Lock()
	if b.busy {
		b.mu.Unlock()
		return ErrBusy
	}
	box, wt := b.box, b.wt
	b.box, b.wt, b.changes, b.deployment = nil, Worktree{}, nil, nil
	b.steps, b.log, b.err, b.stage = nil, nil, "", StageIdle
	b.updatedAt = time.Now()
	b.mu.Unlock()

	if box != nil {
		_ = StopBox(ctx, *box)
	}
	if wt.Path == "" {
		return nil
	}
	return RemoveWorktree(ctx, wt)
}
