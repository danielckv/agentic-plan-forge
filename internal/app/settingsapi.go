//go:build client && !server

package app

import (
	"net/http"
	"sync"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/host"
)

// settingsRevision guards concurrent settings writes. GET returns the current
// value; PUT must carry it back, and a value that no longer matches means
// another window saved first — the stale writer gets a 409 instead of
// silently dropping its changes or someone else's.
var settingsRevision struct {
	sync.Mutex
	value int64
}

func currentSettingsRevision() int64 {
	settingsRevision.Lock()
	defer settingsRevision.Unlock()
	return settingsRevision.value
}

func bumpSettingsRevision() int64 {
	settingsRevision.Lock()
	defer settingsRevision.Unlock()
	settingsRevision.value++
	return settingsRevision.value
}

// modelCatalogEntry is one host's selectable models for the settings window.
// Live is the provider's own list; when that cannot be fetched, Fallback is
// the short built-in catalogue and Stale is true, so the window can label the
// list as unverified instead of certifying availability.
type modelCatalogEntry struct {
	Host     string          `json:"host"`
	Label    string          `json:"label"`
	Models   []host.ModelRef `json:"models"`
	Fallback []host.ModelRef `json:"fallback,omitempty"`
	Stale    bool            `json:"stale,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// GET /api/models — the model catalogue per API host.
//
// The plan requires that a catalog failure preserves the last list marked
// stale rather than certifying models as available: a host whose live list
// cannot be fetched reports its built-in fallback with stale=true, never a
// silently truncated "live" list.
func (s *appSession) handleModels(w http.ResponseWriter, r *http.Request) {
	entries := make([]modelCatalogEntry, 0, len(host.APIKinds()))
	for _, kind := range host.APIKinds() {
		entry := modelCatalogEntry{
			Host:  string(kind),
			Label: host.KindLabel(kind),
		}
		router := host.NewRouter(s.cfg, kind)
		models, err := router.Models(r.Context(), kind)
		if err != nil {
			entry.Fallback = host.ModelCatalogue(kind)
			entry.Stale = true
			entry.Error = err.Error()
		} else {
			entry.Models = models
		}
		entries = append(entries, entry)
	}
	writeJSON(w, http.StatusOK, entries)
}

// settingsView is the GET /api/settings body: the persisted configuration
// plus the revision a saving client must echo. Keys never appear here —
// credentials live in the credential file and surface only through
// GET /api/providers as masked statuses.
type settingsView struct {
	config.Config
	Revision int64 `json:"revision"`
}

// GET /api/settings — the current settings and their revision.
func (s *appSession) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	if s.exec == nil {
		writeAPIError(w, http.StatusNotFound, "this session cannot read local settings")
		return
	}
	cfg, err := config.Load("")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "could not read the settings file")
		return
	}
	writeJSON(w, http.StatusOK, settingsView{Config: cfg, Revision: currentSettingsRevision()})
}

// putSettingsRequest is the PUT /api/settings body. The settings are the full
// document, not a patch: the window edits a whole form and a partial write
// would need per-field merge rules that drift from the CLI.
type putSettingsRequest struct {
	config.Config
	Revision int64 `json:"revision"`
}

// PUT /api/settings — save the settings document.
//
// Validation is server-side on purpose: the controls in the window constrain
// what a user picks, but the endpoint is reachable with curl, so provider and
// host membership are checked here against the same rules `plan-forge config`
// applies. A stale revision returns 409 and changes nothing.
func (s *appSession) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	if s.exec == nil {
		writeAPIError(w, http.StatusNotFound, "this session cannot change local settings")
		return
	}
	var req putSettingsRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Revision != currentSettingsRevision() {
		writeAPIError(w, http.StatusConflict, "settings changed since you loaded them; reload and reapply")
		return
	}
	if err := config.Validate(req.Config); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := config.Save("", req.Config); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "could not save the settings")
		return
	}
	writeJSON(w, http.StatusOK, settingsView{Config: req.Config, Revision: bumpSettingsRevision()})
}
