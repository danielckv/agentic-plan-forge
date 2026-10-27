//go:build client && !server

package app

import (
	"net/http"
)

// sandboxRoutes registers the prepared-VM endpoints. They are owner-only: a
// launch spends the owner's money and a preview shows the owner's application.
func (s *appSession) sandboxRoutes() {
	s.bridge.Handle("GET /api/sandbox", s.handleSandbox)
	s.bridge.Handle("POST /api/sandbox/launch", s.handleSandboxLaunch)
	s.bridge.Handle("POST /api/sandbox/stop", s.handleSandboxStop)
}

// sandbox returns the manager, creating it on first use so a session that never
// opens the Build page pays nothing for the feature.
func (s *appSession) sandbox() *sandboxManager {
	s.sandboxOnce.Do(func() {
		settings, configured, hint := s.SessionSandboxSettings()
		s.sandboxMgr = newSandboxManager(s.repo, settings, configured, hint)
	})
	return s.sandboxMgr
}

// GET /api/sandbox — the current launch state.
func (s *appSession) handleSandbox(w http.ResponseWriter, _ *http.Request) {
	if !s.owns() {
		writeAPIError(w, http.StatusNotFound, "this session does not build")
		return
	}
	writeJSON(w, http.StatusOK, s.sandbox().Report())
}

// POST /api/sandbox/launch — create the sandbox the owner confirmed.
func (s *appSession) handleSandboxLaunch(w http.ResponseWriter, r *http.Request) {
	if !s.owns() {
		writeAPIError(w, http.StatusNotFound, "this session does not build")
		return
	}
	var req LaunchRequest
	if !decodeBody(w, r, &req) {
		return
	}
	writeJSON(w, http.StatusOK, s.sandbox().Launch(s.buildContext(), req))
}

// POST /api/sandbox/stop — delete it, and report an unconfirmed deletion.
func (s *appSession) handleSandboxStop(w http.ResponseWriter, r *http.Request) {
	if !s.owns() {
		writeAPIError(w, http.StatusNotFound, "this session does not build")
		return
	}
	writeJSON(w, http.StatusOK, s.sandbox().Stop(s.buildContext()))
}
