//go:build client && !server

package app

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// A frame that does not land means the window is unreachable. The room must
// notice: it used to discard the error, keep believing the window was live and
// write every later frame into a destroyed socket, which is how a run ended at
// exit 130 with nothing logged.
func TestSendLockedStopsPushingAtAnUnreachableWindow(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"backpressure", relay.ErrUIBackpressure},
		{"not connected", relay.ErrUINotConnected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			r := newRoom("session", nil, func([]byte) error {
				calls++
				return tc.err
			}, quietLogger())
			defer r.close()

			r.live = true
			r.sendLocked([]byte(`{"event_type":"AVATAR_DRAFT"}`))
			if calls != 1 {
				t.Fatalf("deliver called %d times, want 1", calls)
			}
			if r.live {
				t.Fatal("the room stayed live after the frame failed to land")
			}
		})
	}
}

// A window that is keeping up stays live.
func TestSendLockedKeepsALiveWindowLive(t *testing.T) {
	r := newRoom("session", nil, func([]byte) error { return nil }, quietLogger())
	defer r.close()

	r.live = true
	r.sendLocked([]byte(`{"event_type":"AVATAR_DRAFT"}`))
	if !r.live {
		t.Fatal("the room dropped a window that accepted the frame")
	}
}

// A reconnecting window is caught up from the replay log. That catch-up is a
// burst into the bridge's fixed-depth write queue, so it must never be longer
// than the queue can absorb: replaying 2000 frames into a 256-deep queue
// destroys the very socket it is catching up, and the window never recovers.
func TestMissedNeverBurstsPastTheWindowQueue(t *testing.T) {
	l := newFrameLog()
	total := uint64(relay.UIQueueDepth * 4)
	for seq := uint64(1); seq <= total; seq++ {
		payload, err := json.Marshal(map[string]string{
			"avatar_id":    "architect",
			"schema_id":    "architect.v1",
			"content_hash": fmt.Sprintf("hash-%d", seq),
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		env := relay.WSEnvelope{EventType: relay.EventAvatarDraft, Seq: seq, Payload: payload}
		l.note(env, []byte(fmt.Sprintf(`{"seq":%d}`, seq)))
	}

	got := l.missed(1)
	if len(got) == 0 {
		t.Fatal("a window behind by the whole run was told it had missed nothing")
	}
	if len(got) > maxReplayBurst {
		t.Fatalf("resync replays %d frames into a %d-deep queue; want at most %d",
			len(got), relay.UIQueueDepth, maxReplayBurst)
	}
}

// A short tail is still replayed exactly, so an ordinary blink loses nothing.
func TestMissedReplaysAShortTailExactly(t *testing.T) {
	l := newFrameLog()
	for seq := uint64(1); seq <= 5; seq++ {
		env := relay.WSEnvelope{EventType: relay.EventCycleStart, Seq: seq, Payload: json.RawMessage(`{}`)}
		l.note(env, []byte(fmt.Sprintf(`{"seq":%d}`, seq)))
	}
	if got := len(l.missed(2)); got != 3 {
		t.Fatalf("missed(2) returned %d frames, want the exact tail of 3", got)
	}
}
