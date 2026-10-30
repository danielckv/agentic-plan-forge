//go:build client && !server

package relay_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// A terminal frontend must send HELLO as its first frame after auth, and the
// real bridge must accept it.
//
// This drives the actual Bridge rather than a stub: the stub accepted any number
// of frames, so a frontend that sent none looked identical to one that sent the
// right thing. HELLO is required — the hub refuses every other event until it
// arrives, and the room answers it with the state snapshot and the document — so
// a frontend that omits it authenticates and then never receives a session.
func TestBridgeAcceptsTerminalHello(t *testing.T) {
	hello := make(chan relay.WSEnvelope, 4)
	bridge, err := relay.NewBridge(relay.BridgeConfig{
		OnMessage: func(raw []byte) {
			env, derr := relay.DecodeEnvelope(raw)
			if derr != nil {
				return
			}
			select {
			case hello <- env:
			default:
			}
		},
	})
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
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("the bridge did not stop")
		}
	})

	launchURL, err := bridge.LaunchURL()
	if err != nil {
		t.Fatalf("LaunchURL: %v", err)
	}
	_, nonce, ok := strings.Cut(launchURL, "/#n=")
	if !ok {
		t.Fatalf("launch URL %q carries no nonce", launchURL)
	}
	token := redeem(t, bridge, nonce)

	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dialCancel()
	conn, _, err := websocket.Dial(ctx, "ws://"+bridge.Addr()+"/ws", &websocket.DialOptions{
		HTTPHeader:   http.Header{"Origin": {bridge.Origin()}},
		Subprotocols: []string{relay.WSSubprotocol},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	auth, _ := json.Marshal(map[string]string{"type": "auth", "token": token})
	if err := conn.Write(dialCtx, websocket.MessageText, auth); err != nil {
		t.Fatalf("auth: %v", err)
	}

	// The frame the terminal sends. The session id is the socket URL, which is
	// what the page fills that field with; the local bridge does not compare it.
	env := relay.WSEnvelope{
		Version: 1, SessionID: "ws://" + bridge.Addr() + "/ws",
		SenderID: "terminal", EventID: "ev-hello",
		EventType: relay.EventHello,
		Timestamp: time.Now().UTC(),
		Payload:   mustJSON(t, relay.HelloPayload{Proto: 1, CanExecute: true}),
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	// It must survive the decoder the bridge itself uses, or the frame is
	// malformed before any behaviour is in question.
	if _, err := relay.DecodeEnvelope(raw); err != nil {
		t.Fatalf("the bridge would refuse this HELLO: %v", err)
	}
	if err := conn.Write(dialCtx, websocket.MessageText, raw); err != nil {
		t.Fatalf("hello: %v", err)
	}

	select {
	case got := <-hello:
		if got.EventType != relay.EventHello {
			t.Fatalf("the bridge received %s, want HELLO", got.EventType)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the bridge never delivered HELLO to the room")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
