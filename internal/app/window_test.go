//go:build client && !server

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// redeemNonce trades a launch nonce for a bridge token, as the page does.
func redeemNonce(t *testing.T, origin, nonce string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"nonce": nonce})
	req, _ := http.NewRequest(http.MethodPost, origin+"/api/session", bytes.NewReader(body))
	req.Header.Set("Origin", origin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Token string `json:"token"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || out.Token == "" {
		t.Fatalf("redeem nonce: status %d", resp.StatusCode)
	}
	return out.Token
}

type fakeWindow struct{ done chan error }

func (f *fakeWindow) Done() <-chan error      { return f.done }
func (f *fakeWindow) Terminate(time.Duration) {}

// uiClient plays the page: it redeems the launch nonce, opens the bridge
// socket, authenticates and forwards every text frame it receives.
type uiClient struct {
	frames chan string
	closeC func()
}

func launchUI(t *testing.T, launchURL string) *uiClient {
	t.Helper()
	u, err := neturl.Parse(launchURL)
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + u.Host
	token := redeemNonce(t, origin, strings.TrimPrefix(u.Fragment, "n="))
	ctx, cancel := context.WithCancel(context.Background())
	conn, _, err := websocket.Dial(ctx, "ws://"+u.Host+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {origin}}, Subprotocols: []string{relay.WSSubprotocol},
	})
	if err != nil {
		cancel()
		t.Fatalf("dial: %v", err)
	}
	auth, _ := json.Marshal(map[string]string{"type": "auth", "token": token})
	if err := conn.Write(ctx, websocket.MessageText, auth); err != nil {
		cancel()
		t.Fatalf("auth: %v", err)
	}
	c := &uiClient{frames: make(chan string, 64), closeC: func() { _ = conn.Close(websocket.StatusNormalClosure, "bye"); cancel() }}
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			c.frames <- string(data)
		}
	}()
	t.Cleanup(c.closeC)
	return c
}

func TestStartWindowOnConnectDoesNotBlockSupervisor(t *testing.T) {
	bridge, err := relay.NewBridge(relay.BridgeConfig{Assets: UIAssets()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = bridge.Serve(ctx) }()
	t.Cleanup(func() { cancel(); bridge.Close() })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	connected := make(chan struct{}, 1)
	clients := make(chan *uiClient, 1)
	win := &fakeWindow{done: make(chan error)}
	_, outcome, err := startWindow(ctx, windowParams{
		Bridge: bridge, Grace: 100 * time.Millisecond, StartupWait: 20 * time.Second,
		Launch: func(url string) (windowHandle, error) {
			go func() { clients <- launchUI(t, url) }()
			return win, nil
		},
		OnConnect: func() {
			select {
			case connected <- struct{}{}:
			default:
			}
			<-release
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("OnConnect was never called")
	}
	ui := <-clients
	ui.closeC()
	select {
	case r := <-outcome:
		if r.Phase != PhaseCancelled {
			t.Errorf("outcome = %+v, want the window-closed result", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a blocked OnConnect stalled the window supervisor")
	}
}
