//go:build client && !server

package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// fakeBridge is a minimal loopback WebSocket that speaks the real handshake:
// subprotocol plan-forge.v1, an auth frame first, then envelopes. It exists to
// prove the terminal client authenticates the way a browser does, without
// standing up the whole app in a test.
type fakeBridge struct {
	t      *testing.T
	token  string
	frames chan relay.WSEnvelope

	mu       sync.Mutex
	commands []relay.WSEnvelope
	gotAuth  chan string
}

func newFakeBridge(t *testing.T, token string) (*fakeBridge, *httptest.Server) {
	fb := &fakeBridge{t: t, token: token, frames: make(chan relay.WSEnvelope, 8), gotAuth: make(chan string, 1)}
	srv := httptest.NewServer(http.HandlerFunc(fb.handle))
	t.Cleanup(func() {
		srv.Close()
		close(fb.frames)
	})
	return fb, srv
}

func (f *fakeBridge) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ws" {
		http.NotFound(w, r)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // httptest binds loopback; the client is the browser's own
		Subprotocols:       []string{relay.WSSubprotocol},
	})
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	ctx := context.Background()

	// First frame authenticates, exactly as the bridge requires.
	_, data, err := c.Read(ctx)
	if err != nil {
		return
	}
	var auth struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	if json.Unmarshal(data, &auth) != nil || auth.Type != "auth" {
		return
	}
	f.gotAuth <- auth.Token
	if auth.Token != f.token {
		_ = c.Close(websocket.StatusPolicyViolation, "authentication failed")
		return
	}
	// The bridge's real first answer is not an envelope; the client must skip it.
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"auth.ok"}`))

	// coder/websocket allows one writer at a time but several concurrent
	// readers are fine. The writer loop below is the only writer, and it does
	// not start until the auth.ok frame above is written, so frames can never
	// interleave mid-message.
	go func() {
		for env := range f.frames {
			raw, _ := json.Marshal(env)
			if err := c.Write(ctx, websocket.MessageText, raw); err != nil {
				return
			}
		}
	}()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		env, err := relay.DecodeEnvelope(data)
		if err != nil {
			continue
		}
		f.mu.Lock()
		f.commands = append(f.commands, env)
		f.mu.Unlock()
	}
}

func (f *fakeBridge) sent() []relay.WSEnvelope {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]relay.WSEnvelope(nil), f.commands...)
}

// TestTerminalSessionEndToEnd drives a real socket: it authenticates, receives a
// snapshot, renders it, and sends a decision answer. This is the proof that the
// terminal is a frontend over the same protocol rather than a parallel one.
func TestTerminalSessionEndToEnd(t *testing.T) {
	const token = "0123456789abcdef"
	fb, srv := newFakeBridge(t, token)

	origin := "http://" + strings.TrimPrefix(srv.URL, "http://")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sock, err := dial(ctx, Options{Origin: origin, Token: token})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer sock.close()

	select {
	case got := <-fb.gotAuth:
		if got != token {
			t.Fatalf("auth token = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the bridge never received an auth frame")
	}

	// A snapshot arrives and renders.
	payload, _ := json.Marshal(map[string]any{
		"phase": "DELIBERATION", "role": "owner", "run_id": "run-1", "baseline_rev": "3",
	})
	fb.frames <- relay.WSEnvelope{
		Version: contracts.ProtocolVersion, SessionID: "sess-1", EventID: "ev-1",
		EventType: relay.EventStateSnapshot, Payload: payload,
	}

	select {
	case env := <-sock.frames:
		if env.EventType != relay.EventStateSnapshot {
			t.Fatalf("event = %s, want STATE_SNAPSHOT", env.EventType)
		}
		snap := Snapshot{}.Apply(env)
		if snap.RunID != "run-1" || snap.Phase != relay.PhaseDeliberation || snap.SessionID != "sess-1" {
			t.Fatalf("snapshot state = %+v", snap)
		}
		// Agents arrive as their own frames, not inside the snapshot.
		snap = snap.Apply(relay.WSEnvelope{
			Version: contracts.ProtocolVersion, SessionID: "sess-1", EventID: "ev-2",
			EventType: relay.EventAvatarThought,
			Payload:   json.RawMessage(`{"avatar_id":"pm","status":"Thinking","public_summary":"reading the brief"}`),
		})
		out := Render(View{Snapshot: snap, Tab: TabAgents}, 100)
		if !strings.Contains(out, "DELIBERATION") || !strings.Contains(out, "Product Manager") {
			t.Fatalf("rendered frame lacks the session:\n%s", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no frame arrived")
	}

	// A decision answer goes out in the protocol's shape.
	answer, cerr := command("sess-1", relay.EventDecisionResolve, map[string]string{"decision_id": "d1", "option_id": "stage"})
	if cerr != nil {
		t.Fatalf("build command: %v", cerr)
	}
	if err := sock.send(answer); err != nil {
		t.Fatalf("send: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range fb.sent() {
			if c.EventType == relay.EventDecisionResolve {
				var p struct {
					DecisionID string `json:"decision_id"`
					OptionID   string `json:"option_id"`
				}
				if err := json.Unmarshal(c.Payload, &p); err != nil {
					t.Fatal(err)
				}
				if p.DecisionID != "d1" || p.OptionID != "stage" {
					t.Fatalf("payload = %+v", p)
				}
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the bridge never received DECISION_RESOLVE")
}

// A wrong token is refused by the bridge, and the terminal reports the close
// rather than looping on a dead socket.
func TestTerminalRefusesABadToken(t *testing.T) {
	fb, srv := newFakeBridge(t, "right-token")
	_ = fb
	origin := "http://" + strings.TrimPrefix(srv.URL, "http://")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sock, err := dial(ctx, Options{Origin: origin, Token: "wrong-token"})
	if err != nil {
		return // refused at dial time is also a correct outcome
	}
	defer sock.close()
	select {
	case _, ok := <-sock.frames:
		if ok {
			t.Fatal("a frame arrived on a rejected socket")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the socket stayed open with a bad token")
	}
}
