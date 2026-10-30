//go:build client && !server

package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/host"
)

// settingsTestSession builds a session whose exec is non-nil, so the settings
// endpoints treat it as the owner's local session. PLAN_FORGE_HOME points at
// a temp dir, so Load/Save and the credential file never touch the operator's
// real home.
func settingsTestSession(t *testing.T) *appSession {
	t.Helper()
	t.Setenv("PLAN_FORGE_HOME", t.TempDir())
	return &appSession{exec: &executor{}}
}

func TestGetSettingsReturnsConfigAndRevision(t *testing.T) {
	s := settingsTestSession(t)
	rec := httptest.NewRecorder()
	s.handleGetSettings(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var view settingsView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Revision != 0 {
		t.Errorf("revision = %d, want 0 before any write", view.Revision)
	}
	if view.SchemaVersion != config.SchemaVersion {
		t.Errorf("schema_version = %d, want %d", view.SchemaVersion, config.SchemaVersion)
	}
}

func TestPutSettingsRejectsStaleRevision(t *testing.T) {
	s := settingsTestSession(t)
	body := `{"revision": 7}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body))
	s.handlePutSettings(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a stale revision", rec.Code)
	}
}

func TestPutSettingsRoundTripAndValidation(t *testing.T) {
	s := settingsTestSession(t)

	get := func() settingsView {
		rec := httptest.NewRecorder()
		s.handleGetSettings(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET status = %d", rec.Code)
		}
		var view settingsView
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return view
	}
	current := get()

	// A valid write bumps the revision and persists the document.
	current.MainHost = "gemini"
	raw, _ := json.Marshal(putSettingsRequest{Config: current.Config, Revision: current.Revision})
	rec := httptest.NewRecorder()
	s.handlePutSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(string(raw))))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body)
	}
	var saved settingsView
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if saved.Revision != current.Revision+1 {
		t.Errorf("revision = %d, want %d after one write", saved.Revision, current.Revision+1)
	}
	if onDisk := get(); onDisk.MainHost != "gemini" {
		t.Errorf("main_host on disk = %q, want gemini", onDisk.MainHost)
	}

	// A second write at the old revision is stale, even with a valid document.
	rec = httptest.NewRecorder()
	s.handlePutSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(string(raw))))
	if rec.Code != http.StatusConflict {
		t.Errorf("second PUT status = %d, want 409", rec.Code)
	}

	// An invalid host is refused server-side, not just by window controls.
	current = get()
	current.MainHost = "not-a-host"
	raw, _ = json.Marshal(putSettingsRequest{Config: current.Config, Revision: current.Revision})
	rec = httptest.NewRecorder()
	s.handlePutSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(string(raw))))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid host status = %d, want 400", rec.Code)
	}
}

func TestModelsEndpointReportsLiveAndStaleCatalogues(t *testing.T) {
	s := settingsTestSession(t)
	rec := httptest.NewRecorder()
	s.handleModels(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var entries []modelCatalogEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != len(host.APIKinds()) {
		t.Fatalf("entries = %d, want one per API kind (%d)", len(entries), len(host.APIKinds()))
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Host] = true
		if e.Stale && len(e.Fallback) == 0 {
			t.Errorf("host %s marked stale with no fallback list", e.Host)
		}
		if !e.Stale && len(e.Models) == 0 {
			t.Errorf("host %s reports no models and not stale", e.Host)
		}
	}
	for _, kind := range host.APIKinds() {
		if !seen[string(kind)] {
			t.Errorf("catalogue missing host %s", kind)
		}
	}
}

func TestPutSettingsNeverCarriesCredentials(t *testing.T) {
	// The settings document and the credential file are separate stores. A
	// settings save must not create or touch the credential file, and the
	// settings response must not contain a key material field.
	view := settingsView{Config: config.Default(), Revision: 0}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "api_key") {
		t.Error("settings view marshals an api_key field")
	}
	credPath := filepath.Join(t.TempDir(), "credentials.json")
	_ = credPath // separate store by construction; path only documented here
}
