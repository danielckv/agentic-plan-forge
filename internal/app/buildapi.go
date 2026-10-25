//go:build client && !server

package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/delivery"
)

// The build API is owner-only. Every route writes to the owner's machine — it
// opens a worktree, runs an agent with file permissions, starts containers and
// deploys — so a follower, who is a guest on someone else's repository, gets
// 404 rather than a filtered view.

// buildStartGrace is how long a start waits for an immediate rejection before
// answering 202. Long enough to catch "busy" or "no approval", short enough
// that the window is never left waiting on a real run.
const buildStartGrace = 150 * time.Millisecond

// buildRoutes registers the post-approval build and deploy API.
func (s *appSession) buildRoutes() {
	s.bridge.Handle("GET /api/build", s.handleBuild)
	s.bridge.Handle("POST /api/build/code", s.handleBuildCode)
	s.bridge.Handle("POST /api/build/preview", s.handleBuildPreview)
	s.bridge.Handle("POST /api/build/preview/stop", s.handleBuildPreviewStop)
	s.bridge.Handle("POST /api/build/cancel", s.handleBuildCancel)
	s.bridge.Handle("POST /api/build/discard", s.handleBuildDiscard)
	s.bridge.Handle("POST /api/build/finish", s.handleBuildFinish)
	s.bridge.Handle("GET /api/build/file", s.handleBuildFile)
	s.bridge.Handle("POST /api/deploy", s.handleDeploy)
	s.bridge.Handle("GET /api/deploy/target", s.handleDeployTarget)
	s.bridge.Handle("POST /api/deploy/target", s.handleSetDeployTarget)
}

// owns reports whether this session may drive a build. It also answers the
// question the window asks on load: a follower never shows the Build page.
func (s *appSession) owns() bool { return s.build != nil && s.exec != nil }

func (s *appSession) requireBuild(w http.ResponseWriter) bool {
	if !s.owns() {
		writeAPIError(w, http.StatusNotFound, "this session does not own a build")
		return false
	}
	return true
}

// GET /api/build — the whole Build page state.
func (s *appSession) handleBuild(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuild(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.build.View(r.Context()))
}

// buildCodeRequest names the host that writes the code. It defaults to the host
// that invoked Plan Forge, so the owner codes with the agent they are already
// signed in to and already trust with their files.
type buildCodeRequest struct {
	Host  string `json:"host"`
	Model string `json:"model"`
}

// POST /api/build/code — open the worktree and drive the agent through the plan.
// It answers 202 and runs in the background: a coding run takes hours, and an
// HTTP request that long is a request that dies to a proxy timeout.
func (s *appSession) handleBuildCode(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuild(w) {
		return
	}
	var req buildCodeRequest
	if r.ContentLength > 0 && !decodeBody(w, r, &req) {
		return
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		host = s.opts.Host
	}
	if host == "" {
		host = "claude"
	}
	s.runBuildAction(w, func(ctx context.Context) error {
		return s.build.StartCoding(ctx, host, strings.TrimSpace(req.Model))
	})
}

// buildPreviewRequest carries the container port to publish.
type buildPreviewRequest struct {
	Port int `json:"port"`
}

// POST /api/build/preview — build the worktree into an image and run it.
func (s *appSession) handleBuildPreview(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuild(w) {
		return
	}
	var req buildPreviewRequest
	if r.ContentLength > 0 && !decodeBody(w, r, &req) {
		return
	}
	if req.Port < 0 || req.Port > 65535 {
		writeAPIError(w, http.StatusBadRequest, "port is out of range")
		return
	}
	s.runBuildAction(w, func(ctx context.Context) error {
		return s.build.StartPreview(ctx, req.Port)
	})
}

// POST /api/build/preview/stop — remove the preview container.
func (s *appSession) handleBuildPreviewStop(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuild(w) {
		return
	}
	if err := s.build.StopPreview(r.Context()); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.build.View(r.Context()))
}

// POST /api/build/cancel — stop the running action.
func (s *appSession) handleBuildCancel(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuild(w) {
		return
	}
	s.build.Cancel()
	writeJSON(w, http.StatusOK, s.build.View(r.Context()))
}

// POST /api/build/discard — remove the preview container and the worktree.
func (s *appSession) handleBuildDiscard(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuild(w) {
		return
	}
	if err := s.build.Discard(r.Context()); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, delivery.ErrBusy) {
			code = http.StatusConflict
		}
		writeAPIError(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.build.View(r.Context()))
}

// POST /api/build/finish — the owner is done; release the window so the binary
// can close. The worktree and any deployed container are left in place: they
// are the output, not scratch space.
func (s *appSession) handleBuildFinish(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuild(w) {
		return
	}
	s.build.Finish()
	writeJSON(w, http.StatusOK, s.build.View(r.Context()))
}

// GET /api/build/file?path=… — one file of the worktree for the Code tab.
func (s *appSession) handleBuildFile(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuild(w) {
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeAPIError(w, http.StatusBadRequest, "path is required")
		return
	}
	if len(rel) > 1024 {
		writeAPIError(w, http.StatusBadRequest, "path is too long")
		return
	}
	file, err := delivery.ReadFile(s.build.Worktree(), rel)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, file)
}

// POST /api/deploy — publish the built plan to the configured target.
func (s *appSession) handleDeploy(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuild(w) {
		return
	}
	if target := s.build.Config().DeployTarget; target == "" || target == config.DeployTargetNone {
		writeAPIError(w, http.StatusPreconditionFailed, "choose a deploy target first")
		return
	}
	s.runBuildAction(w, s.build.Deploy)
}

// deployTargetView is what the toggle renders: the choice, the endpoint behind
// it, and whether each option can actually run on this machine.
type deployTargetView struct {
	Target  string              `json:"target"`
	BoxURL  string              `json:"box_url"`
	Options []deployTargetOptin `json:"options"`
	// Asked is false until the owner has chosen once. The window shows the
	// first-time question on false and the toggle on true.
	Asked bool `json:"asked"`
}

type deployTargetOptin struct {
	ID   string `json:"id"`
	Why  string `json:"why,omitempty"`
	OK   bool   `json:"ok"`
	Name string `json:"name"`
}

var deployTargetNames = map[string]string{
	config.DeployTargetAgentBox: "Agent Box",
	config.DeployTargetDocker:   "Local Docker",
	config.DeployTargetNone:     "Don't deploy",
}

// GET /api/deploy/target — the current target and what this machine supports.
func (s *appSession) handleDeployTarget(w http.ResponseWriter, _ *http.Request) {
	if !s.requireBuild(w) {
		return
	}
	cfg := s.build.Config()
	view := deployTargetView{
		Target: cfg.DeployTarget,
		BoxURL: cfg.AgentBoxEndpoint(),
		Asked:  cfg.DeployTarget != "",
	}
	for _, id := range config.DeployTargetNames() {
		option := deployTargetOptin{ID: id, Name: deployTargetNames[id], OK: true}
		if err := delivery.TargetAvailable(id); err != nil {
			option.OK, option.Why = false, err.Error()
		}
		view.Options = append(view.Options, option)
	}
	writeJSON(w, http.StatusOK, view)
}

// setDeployTargetRequest is the toggle's write.
type setDeployTargetRequest struct {
	Target string `json:"target"`
	BoxURL string `json:"box_url"`
}

// POST /api/deploy/target — record the owner's choice. It is written to the
// config file, so the question is asked once per installation and the toggle
// shows the same answer in the next session.
func (s *appSession) handleSetDeployTarget(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuild(w) {
		return
	}
	var req setDeployTargetRequest
	if !decodeBody(w, r, &req) {
		return
	}
	target := strings.TrimSpace(req.Target)
	if !config.IsValidDeployTarget(target) || target == "" {
		writeAPIError(w, http.StatusBadRequest, "target must be one of "+strings.Join(config.DeployTargetNames(), ", "))
		return
	}
	boxURL := strings.TrimSpace(req.BoxURL)
	if boxURL != "" {
		if err := config.ValidateAgentBoxURL(boxURL); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	// Re-read from disk rather than writing back the snapshot this session
	// started with: another session may have changed an unrelated setting, and
	// a whole-file write would silently revert it.
	cfg, err := config.Load("")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg.DeployTarget = target
	if boxURL != "" {
		cfg.AgentBoxURL = boxURL
	}
	if err := config.Validate(cfg); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := config.Save("", cfg); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.cfg = cfg
	s.build.SetConfig(cfg)
	s.handleDeployTarget(w, r)
}

// runBuildAction starts a long action in the background and answers 202 with
// the state the window should render while it runs. A second action while one
// is running is 409, never a queue: the owner pressed a button twice.
func (s *appSession) runBuildAction(w http.ResponseWriter, action func(context.Context) error) {
	// The action outlives the request, so it takes the session's context, not
	// the request's: the window polls GET /api/build and may well navigate
	// away while a two-hour coding run is going.
	ctx := s.buildContext()
	started := make(chan error, 1)
	go func() {
		err := action(ctx)
		started <- err
		if err != nil {
			s.log.Warn("build action failed", "err", err)
		}
	}()
	// A rejected start (busy, no approval) fails immediately; waiting for it
	// lets the window show the real reason instead of an optimistic 202.
	select {
	case err := <-started:
		if err != nil {
			code := http.StatusInternalServerError
			if errors.Is(err, delivery.ErrBusy) {
				code = http.StatusConflict
			}
			writeAPIError(w, code, err.Error())
			return
		}
	case <-time.After(buildStartGrace):
	}
	writeJSON(w, http.StatusAccepted, s.build.View(ctx))
}
