//go:build client && !server

package relay_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// startBridge runs a bridge and returns it with the launch nonce the window
// would read out of its URL fragment.
func startBridge(t *testing.T) (*relay.Bridge, string) {
	t.Helper()
	bridge, err := relay.NewBridge(relay.BridgeConfig{})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- bridge.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		bridge.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return")
		}
	})

	launchURL, err := bridge.LaunchURL()
	if err != nil {
		t.Fatalf("LaunchURL: %v", err)
	}
	_, nonce, ok := strings.Cut(launchURL, "/#n=")
	if !ok {
		t.Fatalf("launch URL %q carries no nonce fragment", launchURL)
	}
	return bridge, nonce
}

// redeem performs the page's POST /api/session and returns the session token.
func redeem(t *testing.T, bridge *relay.Bridge, nonce string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"nonce": nonce})
	if err != nil {
		t.Fatalf("marshal nonce: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, bridge.Origin()+"/api/session", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", bridge.Origin())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/session answered %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if out.Token == "" {
		t.Fatal("POST /api/session returned an empty token")
	}
	return out.Token
}

// The window opens its socket with the plan-forge.v1 subprotocol. A bridge
// that does not offer it answers the handshake without
// Sec-WebSocket-Protocol, and the browser fails the connection before the
// auth frame is ever written.
func TestBridgeWebSocketNegotiatesSubprotocol(t *testing.T) {
	bridge, nonce := startBridge(t)
	token := redeem(t, bridge, nonce)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws://"+bridge.Addr()+"/ws", &websocket.DialOptions{
		HTTPHeader:   http.Header{"Origin": {bridge.Origin()}},
		Subprotocols: []string{relay.WSSubprotocol},
	})
	if err != nil {
		t.Fatalf("dial /ws: %v", err)
	}
	defer conn.CloseNow()

	if got := conn.Subprotocol(); got != relay.WSSubprotocol {
		t.Fatalf("negotiated subprotocol = %q, want %q", got, relay.WSSubprotocol)
	}

	auth, err := json.Marshal(map[string]string{"type": "auth", "token": token})
	if err != nil {
		t.Fatalf("marshal auth frame: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, auth); err != nil {
		t.Fatalf("write auth frame: %v", err)
	}

	select {
	case up := <-bridge.UIEvents():
		if !up {
			t.Fatal("UIEvents reported a disconnect for an authenticated socket")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the authenticated socket never reached UIEvents")
	}
}

// A client that negotiates no subprotocol is refused rather than served.
func TestBridgeWebSocketRefusesMissingSubprotocol(t *testing.T) {
	bridge, nonce := startBridge(t)
	token := redeem(t, bridge, nonce)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws://"+bridge.Addr()+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {bridge.Origin()}},
	})
	if err != nil {
		return // refused at the handshake is also a refusal
	}
	defer conn.CloseNow()

	auth, err := json.Marshal(map[string]string{"type": "auth", "token": token})
	if err != nil {
		t.Fatalf("marshal auth frame: %v", err)
	}
	_ = conn.Write(ctx, websocket.MessageText, auth)

	readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
	defer readCancel()
	if _, _, err := conn.Read(readCtx); err == nil {
		t.Fatal("a socket without the subprotocol stayed open")
	}

	select {
	case up := <-bridge.UIEvents():
		if up {
			t.Fatal("a socket without the subprotocol was accepted as the UI")
		}
	case <-time.After(time.Second):
	}
}

// The e2e suite boots the real bundle behind this exact policy
// (web/e2e/bridge-harness.mjs). A harness that serves a weaker one would pass
// a window that the bridge still renders blank, which is how the Ajv
// runtime-compile regression reached a release.
func TestBridgeCSPMatchesE2EHarness(t *testing.T) {
	path := filepath.Join("..", "..", "web", "e2e", "bridge-csp.txt")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got := strings.TrimSpace(string(raw)); got != relay.BridgeCSP {
		t.Errorf("%s pins\n  %s\nbut the bridge sends\n  %s", path, got, relay.BridgeCSP)
	}
}

// The board window launches at /board; any path outside "/" plus [a-z0-9-/]
// falls back to "/".
func TestBridgeLaunchPath(t *testing.T) {
	cases := []struct{ path, want string }{
		{"", "/"},
		{"/", "/"},
		{"/board", "/board"},
		{"/a-b/c9", "/a-b/c9"},
		{"board", "/"},
		{"/Board", "/"},
		{"/a b", "/"},
		{"/a?x=1", "/"},
		{"/a#frag", "/"},
		{"//evil", "/"},
		{"/../x", "/"},
	}
	for _, tc := range cases {
		bridge, err := relay.NewBridge(relay.BridgeConfig{LaunchPath: tc.path})
		if err != nil {
			t.Fatalf("NewBridge: %v", err)
		}
		url, err := bridge.LaunchURL()
		bridge.Close()
		if err != nil {
			t.Fatalf("LaunchURL(%q): %v", tc.path, err)
		}
		prefix := bridge.Origin() + tc.want + "#n="
		if !strings.HasPrefix(url, prefix) || len(url) == len(prefix) {
			t.Errorf("LaunchPath %q: got %q, want prefix %q followed by a nonce", tc.path, url, prefix)
		}
	}
}
