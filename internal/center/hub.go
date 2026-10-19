//go:build client && !server

package center

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Hub limits. The queue depth and write timeout match the bridge's, so a slow
// reader is treated the same way on both surfaces.
const (
	MaxUIClients   = 16
	clientQueue    = 256
	heartbeatEvery = 15 * time.Second
	maxStreamAge   = 6 * time.Hour
	// staleFactor decides when a client stops counting as connected. A lid
	// closed mid-stream leaves a socket that looks open until a write fails,
	// so active_clients counts only clients seen within this many heartbeats.
	staleFactor = 2
)

// ErrHubFull is returned when MaxUIClients streams are already connected.
var ErrHubFull = errors.New("center: too many connected clients")

// frame is one server-sent event, already encoded.
type frame []byte

// client is one connected event stream.
type client struct {
	out  chan frame
	done chan struct{}

	mu   sync.Mutex
	seen time.Time
}

func (c *client) touch(t time.Time) {
	c.mu.Lock()
	c.seen = t
	c.mu.Unlock()
}

func (c *client) lastSeen() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen
}

// hub fans one event out to every connected tab.
//
// This is the whole reason the command center does not reuse the window
// bridge: that bridge holds a single UI socket and answers a second connection
// with 409, by construction. A shared tab that several terminals push into
// needs the opposite.
type hub struct {
	mu      sync.Mutex
	clients map[*client]struct{}
	seq     uint64
	now     func() time.Time
}

func newHub() *hub {
	return &hub{clients: map[*client]struct{}{}, now: time.Now}
}

// add registers a stream. It reports ErrHubFull rather than evicting anyone:
// the tabs already open are the ones the user is looking at.
func (h *hub) add() (*client, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) >= MaxUIClients {
		return nil, ErrHubFull
	}
	c := &client{out: make(chan frame, clientQueue), done: make(chan struct{}), seen: h.now()}
	h.clients[c] = struct{}{}
	return c, nil
}

func (h *hub) remove(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; !ok {
		return
	}
	delete(h.clients, c)
	close(c.done)
}

// active counts the streams that have flushed recently. A client whose machine
// slept still occupies a slot, but it must not make `center ensure` believe a
// tab is on screen.
func (h *hub) active() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	cutoff := h.now().Add(-staleFactor * heartbeatEvery)
	n := 0
	for c := range h.clients {
		if c.lastSeen().After(cutoff) {
			n++
		}
	}
	return n
}

// broadcast encodes one event and queues it for every client. A client whose
// queue is full is dropped with an overflow frame: SSE has no close code, so
// the final frame is how the server says "resynchronise", the same contract
// the bridge expresses with close 1013.
func (h *hub) broadcast(event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	h.mu.Lock()
	h.seq++
	f := encodeFrame(h.seq, event, data)
	over := encodeFrame(h.seq, "overflow", []byte(`{"reason":"backpressure"}`))
	var dropped []*client
	for c := range h.clients {
		select {
		case c.out <- f:
		default:
			// The queue is full, so make room for the final frame by dropping
			// the oldest queued one. This client is being dropped anyway, and
			// the overflow frame is the only thing it still needs to read.
			select {
			case <-c.out:
			default:
			}
			select {
			case c.out <- over:
			default:
			}
			dropped = append(dropped, c)
		}
	}
	for _, c := range dropped {
		delete(h.clients, c)
		close(c.done)
	}
	h.mu.Unlock()
}

// encodeFrame renders one event in the server-sent events wire format. Every
// line of the payload is prefixed, because a bare newline inside data would
// otherwise end the event early.
func encodeFrame(seq uint64, event string, data []byte) frame {
	var b strings.Builder
	fmt.Fprintf(&b, "id: %d\n", seq)
	if event != "" {
		fmt.Fprintf(&b, "event: %s\n", event)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fmt.Fprintf(&b, "data: %s\n", line)
	}
	b.WriteString("\n")
	return frame(b.String())
}

// heartbeatFrame is an SSE comment. It keeps intermediaries from closing an
// idle stream and, because writing it fails on a dead peer, is also how a
// vanished tab is noticed.
func heartbeatFrame() frame { return frame(":\n\n") }

// stream writes events to one connected tab until the client goes away, the
// server stops, or the stream reaches maxStreamAge. Returning ends the
// request, which is what closes the connection.
func (h *hub) stream(w http.ResponseWriter, r *http.Request, c *client) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	deadline := time.NewTimer(maxStreamAge)
	defer deadline.Stop()

	write := func(f frame) bool {
		if _, err := w.Write(f); err != nil {
			return false
		}
		flusher.Flush()
		c.touch(h.now())
		return true
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-c.done:
			return
		case <-deadline.C:
			// Bound how long one stream, and the credential behind it, lives.
			// The client reconnects; it is not an error.
			_ = write(encodeFrame(0, "rotate", []byte(`{"reason":"max stream age"}`)))
			return
		case <-ticker.C:
			if !write(heartbeatFrame()) {
				return
			}
		case f := <-c.out:
			if !write(f) {
				return
			}
		}
	}
}
