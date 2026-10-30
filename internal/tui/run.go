//go:build client && !server

package tui

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/danielckv/agentic-plan-forge/internal/collab"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// Run drives one terminal session against a live bridge.
//
// It dials the loopback WebSocket the browser uses, negotiates the same
// subprotocol, sends the same auth frame first, and then renders the envelopes
// it receives. Commands go out in the same shape the browser sends, so the
// authority cannot tell a terminal from a tab — which is the point: one
// authority, two frontends.
//
// Run returns when the reader quits or the socket closes for good. Closing the
// terminal does not cancel the session: the caller decides that, because a
// session can outlive the window that watched it.
func Run(ctx context.Context, opts Options) error {
	if opts.Origin == "" || opts.Token == "" {
		return errors.New("tui: an origin and a bridge token are required")
	}
	sock, err := dial(ctx, opts)
	if err != nil {
		return err
	}
	defer sock.close()

	// The screen is the caller's, so the caller restores it: Run only draws.
	term := opts.Terminal
	if term == nil {
		term = stdTerminal()
	}
	state := View{Tab: TabAgents}
	defer term.Restore()

	keys := make(chan keyMsg, 16)
	go readKeys(ctx, term, keys)

	resize := term.Resize()
	width := Width(term.Width())

	ticker := time.NewTicker(opts.Tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case k, ok := <-keys:
			if !ok {
				return nil
			}
			state, err = handleKey(state, k, sock)
			if err != nil {
				return err
			}
			if state.quitting() {
				return nil
			}
			term.Draw(Render(state, width))
		case env, ok := <-sock.frames:
			if !ok {
				if err := sock.readError(); err != nil && !errors.Is(err, context.Canceled) {
					return err
				}
				return nil
			}
			state.Snapshot = state.Snapshot.Apply(env)
			term.Draw(Render(state, width))
		case w, ok := <-resize:
			if !ok {
				break
			}
			width = Width(w)
			term.Draw(Render(state, width))
		case <-ticker.C:
			// The ticker keeps the clock honest for pages with elapsed time and
			// redraws after a resize the terminal reported without an event.
			term.Draw(Render(state, width))
		}
	}
}

// Options configures one terminal run.
type Options struct {
	// Origin is the bridge origin, for example http://127.0.0.1:53211.
	Origin string
	// Token is the bridge token from POST /api/session.
	Token string
	// Terminal renders and reads. Nil uses the real terminal.
	Terminal Terminal
	// Tick is how often the screen refreshes when nothing arrives.
	Tick time.Duration
}

// Terminal is the screen and keyboard seam. The real implementation owns the
// tty; a test provides its own.
type Terminal interface {
	// Draw writes a whole frame and moves the cursor home.
	Draw(frame string)
	// Width is the current column count.
	Width() int
	// Resize reports new widths as the reader changes the window.
	Resize() <-chan int
	// ReadKey returns the next key press.
	ReadKey(ctx context.Context) (keyMsg, error)
	// Restore puts the terminal back the way it was found: cooked mode, cursor
	// visible. It must be safe to call twice.
	Restore()
}

// keyMsg is one key press, already interpreted.
type keyMsg struct {
	// Rune is the character, empty for a special key.
	Rune rune
	// Special names a non-printing key: up, down, enter, tab, backtab, esc.
	Special string
}

// quitting reports whether the reader asked to leave.
func (v View) quitting() bool {
	return v.quit
}

// handleKey applies one key and returns the next view. It also sends the command
// a key asks for, through the same socket the browser uses.
func handleKey(v View, k keyMsg, sock *socket) (View, error) {
	v.Notice = ""
	// The channel mutates the view; keep it simple and explicit.
	next := v
	switch {
	case next.Editing:
		// While the brief is being written, every printable key is text. Only
		// the control keys below act, so a brief containing "q" or "a" is not
		// taken as a quit or an approval.
		switch {
		case k.Special == "esc":
			next.Editing = false
			next.Notice = "Brief editing cancelled; nothing was sent."
		case k.Special == "enter":
			next.Draft += "\n"
		case k.Special == "backspace":
			if r := []rune(next.Draft); len(r) > 0 {
				next.Draft = string(r[:len(r)-1])
			}
		case k.Special == "ctrl-s":
			sent, err := appendBrief(next, sock)
			if err != nil {
				next.Notice = err.Error()
				break
			}
			next = sent
		case k.Rune != 0:
			next.Draft += string(k.Rune)
		}
	case k.Special == "esc":
		next.Help = false
	case k.Special == "enter":
		if next.Tab == TabQuestions {
			next.Notice = "Select an option number to answer."
		}
	case k.Special == "up":
		if next.Selected > 0 {
			next.Selected--
		}
	case k.Special == "down":
		if next.Selected < rowCount(next)-1 {
			next.Selected++
		}
	case k.Rune == 'e' && next.Tab == TabBrief && !next.Snapshot.Frozen():
		// The brief is only writable before the document is frozen, which is
		// what the authority enforces for document edits.
		next.Editing = true
		next.Notice = "Editing the brief. Enter is a newline, Ctrl-S appends, Esc cancels."
	case k.Rune == '?':
		next.Help = !next.Help
	case k.Rune == 'q':
		next.quit = true
	case next.Tab == TabQuestions && k.Rune >= '1' && k.Rune <= '9' && len(next.Snapshot.Questions) > 0:
		// Answering outranks the tab shortcut: on the questions pane a digit is
		// an option, or no decision could ever be answered. Tabs stay reachable
		// with Tab/Shift-Tab and with digits on every other pane.
		next = answerQuestion(next, int(k.Rune-'0'), sock)
	case k.Rune >= '1' && k.Rune <= '5':
		next.Tab = Tabs[k.Rune-'1']
		next.Selected = 0
	case k.Rune == 'a':
		// Every field here is checked by the reducer: a wrong baseline, run,
		// synthesis revision or content hash is rejected outright, so the values
		// come from the snapshot rather than from a guess.
		cmd, err := approveCommand(next.Snapshot)
		if err != nil {
			next.Notice = err.Error()
			break
		}
		if err := sock.send(cmd); err != nil {
			return next, err
		}
		next.Notice = "Approval sent."
	case k.Rune == 's':
		if err := sendCommand(sock, next.Snapshot, relay.EventSynthesizeRequest, map[string]string{
			"run_id": next.Snapshot.runID(),
		}); err != nil {
			return next, err
		}
		next.Notice = "Synthesis requested."
	case k.Rune == 'c':
		if err := sendCommand(sock, next.Snapshot, relay.EventCancel, nil); err != nil {
			return next, err
		}
		next.Notice = "Cancellation requested."
	case k.Rune == 'r':
		if next.Snapshot.Banner == "" {
			next.Notice = "No failed stage to retry."
			break
		}
		if err := sendCommand(sock, next.Snapshot, relay.EventRetryStage, map[string]string{
			"run_id": next.Snapshot.runID(),
		}); err != nil {
			return next, err
		}
		next.Notice = "Retry requested."
	}
	return next, nil
}

// answerQuestion resolves the selected question with the chosen option.
func answerQuestion(v View, choice int, sock *socket) View {
	if v.Selected < 0 || v.Selected >= len(v.Snapshot.Questions) {
		v.Notice = "Select a question first."
		return v
	}
	q := v.Snapshot.Questions[v.Selected]
	if choice < 1 || choice > len(q.Options) {
		v.Notice = "No such option."
		return v
	}
	// Options are rendered as "id: label"; the id is what the authority wants.
	id, _, _ := strings.Cut(q.Options[choice-1], ":")
	// The reducer rejects an answer that does not echo the revision the card
	// opened at, so the value travels with the row.
	payload := map[string]string{
		"decision_id":    q.ID,
		"input_revision": strconv.FormatUint(q.InputRevision, 10),
		"option_id":      strings.TrimSpace(id),
	}
	if err := sendCommand(sock, v.Snapshot, relay.EventDecisionResolve, payload); err != nil {
		v.Notice = "Could not send the answer: " + err.Error()
		return v
	}
	v.Notice = "Answer sent for " + q.ID + "."
	return v
}

// rowCount is how many rows the active tab has, for selection bounds.
func rowCount(v View) int {
	switch v.Tab {
	case TabAgents:
		return len(v.Snapshot.Agents)
	case TabQuestions:
		return len(v.Snapshot.Questions)
	default:
		return 0
	}
}

// command wraps a payload in the envelope the authority accepts.
//
// The authority supplies the reply's sender, sequence and timestamps, but it
// strict-decodes the inbound frame first, so a command must still carry the
// protocol version, a session id and a unique event id. The browser fills the
// same fields in `commandEnvelope`; this is that shape, not a second one.
func command(sessionID string, event relay.WSEventType, payload any) (relay.WSEnvelope, error) {
	eventID, err := newEventID()
	if err != nil {
		return relay.WSEnvelope{}, err
	}
	env := relay.WSEnvelope{
		Version:   contracts.ProtocolVersion,
		SessionID: sessionID,
		SenderID:  "terminal",
		EventID:   eventID,
		EventType: event,
		Timestamp: time.Now().UTC(),
	}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return relay.WSEnvelope{}, err
		}
		env.Payload = raw
	}
	return env, nil
}

// Frozen reports whether the authority has closed the document for editing.
// docGate allows TEXT_* only in INGESTION, so every later phase is read-only.
func (s Snapshot) Frozen() bool {
	return s.Phase != "" && s.Phase != relay.PhaseIngestion
}

// appendBrief sends the typed text as an append to the PRD and clears the draft.
//
// The changes array is the collab authority's own encoding, not a new one: a
// retain covering the current document, then an insert covering the new text.
// Sending anything else is rejected as a bad change set, so the document length
// the terminal holds must match the authority's — which is why DocText and
// DocVersion are tracked from TEXT_SNAPSHOT rather than assumed.
func appendBrief(v View, sock *socket) (View, error) {
	text := strings.TrimRight(v.Draft, "\n")
	if strings.TrimSpace(text) == "" {
		v.Notice = "Nothing to add: the brief is empty."
		return v, nil
	}
	if sock == nil {
		v.Notice = "No session connection is open."
		return v, nil
	}
	// The collab authority's own encoder, so the terminal sends the identical
	// shape an ingest append does rather than a second interpretation of it.
	changes, err := collab.AppendChange(utf16Len(v.Snapshot.DocText), text)
	if err != nil {
		return v, err
	}
	payload := relay.TextDeltaPayload{
		DocumentID:  contracts.DocumentIDPRD,
		BaseVersion: v.Snapshot.DocVersion,
		Updates: []relay.Update{{
			// The authority requires the client id to start with this member's
			// id followed by a colon, and rejects the push otherwise.
			ClientID: v.Snapshot.MemberID + ":terminal",
			Changes:  changes,
		}},
	}
	cmd, err := command(v.Snapshot.sessionID(), relay.EventTextDelta, payload)
	if err != nil {
		return v, err
	}
	if err := sock.send(cmd); err != nil {
		return v, err
	}
	// Trust arrives as TEXT_ACK, which sets the new version; the draft is applied
	// optimistically to readable state only, never to DocVersion.
	v.Snapshot.DocText += text
	if v.Snapshot.DocText != "" && !strings.HasSuffix(v.Snapshot.DocText, "\n") {
		v.Snapshot.DocText += "\n"
	}
	v.Draft = ""
	v.Editing = false
	v.Notice = "Brief appended; waiting for the authority to accept it."
	return v, nil
}

// utf16Len counts a string the way the collab authority indexes it: UTF-16 code
// units, so an emoji is two and a plain ASCII character is one.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// approveCommand builds the APPROVE payload the reducer validates.
//
// The reducer checks, in order: baseline_rev, run_id, a synthesis draft exists,
// synthesis_rev, then content_sha256. Sending a guessed revision or an empty
// hash is refused as STALE_SYNTHESIS, so each value comes from the snapshot and
// a missing one is reported here instead of after a round trip.
func approveCommand(snap Snapshot) (relay.WSEnvelope, error) {
	switch {
	case snap.RunID == "":
		return relay.WSEnvelope{}, errors.New("no run to approve yet")
	case snap.SynthesisRev == 0:
		return relay.WSEnvelope{}, errors.New("no locked synthesis to approve yet")
	case snap.SynthesisHash == "":
		return relay.WSEnvelope{}, errors.New("the synthesis content hash has not arrived yet")
	}
	return command(snap.sessionID(), relay.EventApprove, map[string]string{
		"baseline_rev":   strconv.FormatUint(snap.BaselineRev, 10),
		"run_id":         snap.RunID,
		"synthesis_rev":  strconv.FormatUint(snap.SynthesisRev, 10),
		"content_sha256": snap.SynthesisHash,
	})
}

// sendCommand builds an envelope for this session and writes it.
func sendCommand(sock *socket, snap Snapshot, event relay.WSEventType, payload any) error {
	env, err := command(snap.sessionID(), event, payload)
	if err != nil {
		return err
	}
	return sock.send(env)
}

// newEventID is a random UUIDv4 in the canonical text form.
func newEventID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// socket is the authenticated connection plus its frame channel.
type socket struct {
	conn   *websocket.Conn
	frames chan relay.WSEnvelope

	mu      sync.Mutex
	readErr error
}

// dial connects, authenticates and starts reading.
func dial(ctx context.Context, opts Options) (*socket, error) {
	u, err := url.Parse(opts.Origin)
	if err != nil {
		return nil, fmt.Errorf("tui: bridge origin: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	u.Path = "/ws"

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, u.String(), &websocket.DialOptions{
		Subprotocols:    []string{relay.WSSubprotocol},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, fmt.Errorf("tui: dial %s: %w", u.Redacted(), err)
	}
	if got := conn.Subprotocol(); got != relay.WSSubprotocol {
		_ = conn.Close(websocket.StatusPolicyViolation, "subprotocol required")
		return nil, fmt.Errorf("tui: bridge negotiated %q, want %q", got, relay.WSSubprotocol)
	}
	conn.SetReadLimit(relay.MaxFrameBytes)

	auth, _ := json.Marshal(map[string]string{"type": "auth", "token": opts.Token})
	if err := conn.Write(dialCtx, websocket.MessageText, auth); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "auth write failed")
		return nil, fmt.Errorf("tui: authenticate: %w", err)
	}

	// HELLO is not optional: the hub refuses every other event until it arrives
	// (hub.go: `!hc.hello.Load() && env.EventType != EventHello`), and the room
	// answers it with the state snapshot and the document text. Without it the
	// terminal connects, authenticates, and then never receives a session.
	//
	// The envelope needs a session_id, and on the loopback bridge the value is
	// the socket URL: that is what the page puts there (syncStore.ts sends
	// state.serverUrl). The local bridge never compares it — only the remote hub
	// does, against its own room id.
	hello, err := command(u.String(), relay.EventHello, relay.HelloPayload{
		Proto:      contracts.ProtocolVersion,
		LastSeq:    0,
		DocVersion: 0,
		CanExecute: true,
	})
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "hello build failed")
		return nil, fmt.Errorf("tui: hello: %w", err)
	}
	if err := conn.Write(dialCtx, websocket.MessageText, mustJSON(hello)); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "hello write failed")
		return nil, fmt.Errorf("tui: hello: %w", err)
	}

	s := &socket{conn: conn, frames: make(chan relay.WSEnvelope, 64)}
	go s.read(ctx)
	return s, nil
}

// mustJSON marshals a value built by command, which cannot fail here.
func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

// read pumps frames until the socket closes.
func (s *socket) read(ctx context.Context) {
	defer close(s.frames)
	for {
		_, data, err := s.conn.Read(ctx)
		if err != nil {
			s.setErr(err)
			return
		}
		// The bridge's first answer is `{"type":"auth.ok"}`, which is not an
		// envelope: it carries no event_type. Skip it rather than failing.
		if !isEnvelope(data) {
			continue
		}
		env, err := relay.DecodeEnvelope(data)
		if err != nil {
			// A frame the terminal cannot read is reported, never fatal: the
			// next snapshot will fix the view.
			s.setErr(err)
			continue
		}
		select {
		case s.frames <- env:
		case <-ctx.Done():
			return
		}
	}
}

// isEnvelope reports whether raw carries an event_type, which every WSEnvelope
// does and the auth acknowledgement does not.
func isEnvelope(raw []byte) bool {
	var probe struct {
		EventType string `json:"event_type"`
	}
	return json.Unmarshal(raw, &probe) == nil && probe.EventType != ""
}

// send writes one command.
func (s *socket) send(env relay.WSEnvelope) error {
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.conn.Write(ctx, websocket.MessageText, raw)
}

func (s *socket) close() { _ = s.conn.Close(websocket.StatusNormalClosure, "") }

func (s *socket) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readErr = err
}

// readError is the last read failure, or nil.
func (s *socket) readError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readErr
}

// readKeys pumps key presses from the terminal into a channel.
func readKeys(ctx context.Context, term Terminal, out chan<- keyMsg) {
	defer close(out)
	for {
		k, err := term.ReadKey(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return
			}
			return
		}
		select {
		case out <- k:
		case <-ctx.Done():
			return
		}
	}
}
