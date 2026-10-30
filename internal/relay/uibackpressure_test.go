//go:build client && !server

package relay_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// connectUI opens an authenticated window socket and waits for the bridge to
// report it up.
func connectUI(t *testing.T, ctx context.Context, bridge *relay.Bridge, token string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws://"+bridge.Addr()+"/ws", &websocket.DialOptions{
		HTTPHeader:   http.Header{"Origin": {bridge.Origin()}},
		Subprotocols: []string{relay.WSSubprotocol},
	})
	if err != nil {
		t.Fatalf("dial /ws: %v", err)
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
			t.Fatal("the authenticated socket was reported down")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the authenticated socket never reached UIEvents")
	}
	return conn
}

// floodUntilBackpressure writes frames at a window that is not draining until
// Send gives up on it, and returns how many frames it took.
func floodUntilBackpressure(t *testing.T, bridge *relay.Bridge) int {
	t.Helper()
	blob := make([]byte, 64<<10)
	for i := range blob {
		blob[i] = 'x'
	}
	payload, err := json.Marshal(map[string]string{"blob": string(blob)})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	for i := 1; i <= 4*relay.UIQueueDepth; i++ {
		err := bridge.Send(payload)
		if err == nil {
			continue
		}
		if !errors.Is(err, relay.ErrUIBackpressure) {
			t.Fatalf("Send returned %v, want ErrUIBackpressure", err)
		}
		return i
	}
	t.Fatalf("Send never reported backpressure in %d frames", 4*relay.UIQueueDepth)
	return 0
}

// A window that stops draining has its socket destroyed once the queue is full,
// and Send says so rather than failing silently.
//
// Only the lower bound is a contract. The queue accepts a full UIQueueDepth
// frames before Send reports backpressure, and the count can run higher when the
// writer goroutine drains a frame between two Send calls — that is scheduling,
// not queue depth, so it is not asserted.
//
// The previous assertion was `sent > UIQueueDepth`. It encoded "the writer
// drains at least one frame", so it passed on a busy machine and failed
// deterministically under `-cpu=1`: the flooder never yields and Send gives up
// at exactly the queue depth. The upper bound is left to floodUntilBackpressure,
// which fails if backpressure never arrives inside 4*UIQueueDepth frames.
func TestSendReportsBackpressureAtQueueDepth(t *testing.T) {
	bridge, nonce := startBridge(t)
	token := redeem(t, bridge, nonce)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn := connectUI(t, ctx, bridge, token)
	defer conn.CloseNow()

	sent := floodUntilBackpressure(t, bridge)
	// A shallow queue would give up early and would break the real contract: the
	// window may miss frames, but not before its buffer is full.
	if sent < relay.UIQueueDepth {
		t.Fatalf("Send gave up after %d frames, want at least the %d-deep queue", sent, relay.UIQueueDepth)
	}
}

// The page reconnects on a 250ms backoff after its socket is destroyed. The
// bridge must let it back in: refusing it with 409 while the old socket runs
// its closing handshake burns the supervisor's grace and ends the session at
// exit 130 with "the window was closed without an approval".
func TestReconnectDisplacesClosingSocket(t *testing.T) {
	bridge, nonce := startBridge(t)
	token := redeem(t, bridge, nonce)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn := connectUI(t, ctx, bridge, token)
	defer conn.CloseNow()
	floodUntilBackpressure(t, bridge)

	// The old socket is mid-teardown and deliberately still not draining.
	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	defer dialCancel()
	next, resp, err := websocket.Dial(dialCtx, "ws://"+bridge.Addr()+"/ws", &websocket.DialOptions{
		HTTPHeader:   http.Header{"Origin": {bridge.Origin()}},
		Subprotocols: []string{relay.WSSubprotocol},
	})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("reconnect refused (http %d): %v", status, err)
	}
	defer next.CloseNow()

	auth, err := json.Marshal(map[string]string{"type": "auth", "token": token})
	if err != nil {
		t.Fatalf("marshal auth frame: %v", err)
	}
	if err := next.Write(ctx, websocket.MessageText, auth); err != nil {
		t.Fatalf("write auth frame on the reconnect: %v", err)
	}
	// Writing the auth frame only queues it. The server goroutine still has to
	// read it, claim the window and start its writer; Send before that reaches
	// the displaced socket's full queue and reports backpressure that has nothing
	// to do with the reconnect. Wait for the window to be reported up, exactly as
	// connectUI does — this failed deterministically under `-cpu=1`.
	select {
	case up := <-bridge.UIEvents():
		if !up {
			t.Fatal("the reconnected socket was reported down")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reconnected socket never reached UIEvents")
	}
	if err := bridge.Send([]byte(`{"type":"probe"}`)); err != nil {
		t.Fatalf("Send to the reconnected window: %v", err)
	}
}

// A live window still refuses a second tab: displacement is only for a socket
// that is already closing.
func TestSecondWindowStillRefused(t *testing.T) {
	bridge, nonce := startBridge(t)
	token := redeem(t, bridge, nonce)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn := connectUI(t, ctx, bridge, token)
	defer conn.CloseNow()

	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	defer dialCancel()
	second, resp, err := websocket.Dial(dialCtx, "ws://"+bridge.Addr()+"/ws", &websocket.DialOptions{
		HTTPHeader:   http.Header{"Origin": {bridge.Origin()}},
		Subprotocols: []string{relay.WSSubprotocol},
	})
	if err == nil {
		second.CloseNow()
		t.Fatal("a second window was accepted while the first was live")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("second window got %v, want http 409", resp)
	}
}
