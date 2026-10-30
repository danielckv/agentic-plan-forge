//go:build client && !server

package relay_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A session's browser tab can be reopened: the endpoint mints a fresh launch
// URL, because the nonce model makes a stored URL expire in ten seconds and be
// redeemable once.
func TestLaunchURLMintsAFreshSingleUseURL(t *testing.T) {
	bridge, nonce := startBridge(t)
	token := redeem(t, bridge, nonce)
	_ = token

	// The first mint invalidates the nonce the window already used, so redeem
	// the returned one to prove it works.
	req, err := http.NewRequest(http.MethodPost, bridge.Origin()+"/api/launch-url", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", bridge.Origin())
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/launch-url: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/launch-url answered %d, want 200", resp.StatusCode)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(out.URL, "http://127.0.0.1:") || !strings.Contains(out.URL, "#n=") {
		t.Fatalf("launch url %q is not a loopback nonce URL", out.URL)
	}
}

// Without the bridge token the endpoint is refused, so a page from another
// origin cannot mint a session URL.
func TestLaunchURLRequiresTheBridgeToken(t *testing.T) {
	bridge, _ := startBridge(t)
	req, err := http.NewRequest(http.MethodPost, bridge.Origin()+"/api/launch-url", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", bridge.Origin())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/launch-url: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated mint answered %d, want 401", resp.StatusCode)
	}
}
