//go:build client && !server

package app

import (
	"net/http"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/host"
)

// handleProviders reports which API providers hold a key.
//
// It is separate from the capabilities probe because it is cheap: a single
// file read, no host process and no network call. The window asks it on every
// load to decide whether to open setup, which it could not afford to do
// against the full probe.
func (s *appSession) handleProviders(w http.ResponseWriter, _ *http.Request) {
	if s.exec == nil {
		writeJSON(w, http.StatusOK, []ProviderStatus{})
		return
	}
	writeJSON(w, http.StatusOK, providerStatuses())
}

// setProviderRequest is the setup window's save. An empty api_key disconnects
// the provider, which is how the window offers "Remove".
type setProviderRequest struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
	BaseURL  string `json:"base_url"`
}

// handleSetProvider stores one provider's API key.
//
// The key arrives from the owner's own browser over the loopback bridge, which
// already enforces a strict Origin check and a bearer token on every POST, and
// it is written to a 0600 file outside config.json. It is never echoed back:
// the reply is the same masked status the window renders everywhere else, so a
// key cannot be read out of this endpoint by replaying it.
//
// A follower session has no local filesystem to write to and gets a 404, the
// same gate the capabilities probe uses.
func (s *appSession) handleSetProvider(w http.ResponseWriter, r *http.Request) {
	if s.exec == nil {
		writeAPIError(w, http.StatusNotFound, "this session cannot change local settings")
		return
	}
	var req setProviderRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if !config.IsAPIProvider(req.Provider) {
		writeAPIError(w, http.StatusBadRequest, "unknown provider")
		return
	}

	// Re-read from disk rather than writing back a snapshot: another window or
	// a `plan-forge config` run may have changed a different provider since
	// this session started, and saving a stale copy would silently drop it.
	creds, err := config.LoadCredentials()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "could not read the stored credentials")
		return
	}
	if err := creds.Set(req.Provider, req.APIKey, req.BaseURL); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := config.SaveCredentials(creds); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "could not save the credentials")
		return
	}
	// The adapters cache the key file for a couple of seconds. Drop it now so
	// a provider the owner just connected serves the next call instead of
	// reporting itself unconfigured.
	host.ForgetCredentials()

	writeJSON(w, http.StatusOK, providerStatuses())
}
