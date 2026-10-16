//go:build server && !client

package relay

// Loopback correctness tests for the relay hub. The simulated clients are built
// from coder/websocket and protocol.go only: they speak the wire protocol the
// way a real client does, so a hub that satisfies them satisfies one.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/session"
)

// --- harness ---------------------------------------------------------------------

// testRelayToken is a fixture constant, not a credential: it matches a value
// the test relay itself accepts.
const testRelayToken = "test-relay-token-0123456789"

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_800_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type harness struct {
	t    *testing.T
	hub  *Hub
	srv  *httptest.Server
	http *http.Client
}

func newHarness(t *testing.T, clock func() time.Time) *harness {
	t.Helper()
	hub := NewHub(HubConfig{
		Token: testRelayToken, Clock: clock,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	hub.Routes(mux)
	srv := httptest.NewServer(RejectBrowserOrigin(mux))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); hub.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		srv.Close()
	})
	return &harness{t: t, hub: hub, srv: srv, http: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (h *harness) do(method, path, bearer string, body any, hdr map[string]string) (int, []byte, http.Header) {
	h.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			h.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := h.http.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out, resp.Header
}

func (h *harness) createSession(opts any) createSessionResponse {
	h.t.Helper()
	code, body, _ := h.do("POST", "/sessions", testRelayToken, opts, nil)
	if code != http.StatusCreated {
		h.t.Fatalf("create session: %d %s", code, body)
	}
	var out createSessionResponse
	if err := json.Unmarshal(body, &out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) invite(sessionID, ownerSecret string, req inviteRequest) createInviteResponse {
	h.t.Helper()
	code, body, _ := h.do("POST", "/sessions/"+sessionID+"/invites", ownerSecret, req, nil)
	if code != http.StatusCreated {
		h.t.Fatalf("create invite: %d %s", code, body)
	}
	var out createInviteResponse
	if err := json.Unmarshal(body, &out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) join(sessionID, inviteSecret string) joinResponse {
	h.t.Helper()
	code, body, _ := h.do("POST", "/sessions/"+sessionID+"/join", inviteSecret, nil, nil)
	if code != http.StatusCreated {
		h.t.Fatalf("join: %d %s", code, body)
	}
	var out joinResponse
	if err := json.Unmarshal(body, &out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) room(sessionID string) *room {
	h.hub.mu.RLock()
	defer h.hub.mu.RUnlock()
	return h.hub.rooms[sessionID]
}

func (h *harness) wsURL(sessionID string) string {
	return "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/ws?session=" + sessionID
}

// --- simulated client ----------------------------------------------------------

type simFrame struct {
	env WSEnvelope
	at  time.Time
}

type latencies struct {
	mu      sync.Mutex
	control []time.Duration
	doc     []time.Duration
}

func (l *latencies) add(kind string, d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if kind == "doc" {
		l.doc = append(l.doc, d)
	} else {
		l.control = append(l.control, d)
	}
}

func percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(float64(len(s)-1) * p)
	return s[idx]
}

type simClient struct {
	t         *testing.T
	name      string
	sessionID string
	memberID  string
	conn      *websocket.Conn
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	lat       *latencies
	sentAt    *sync.Map // doc token -> send time (shared by every client of a test)

	mu     sync.Mutex
	frames []simFrame
	closed error

	docMu     sync.Mutex
	doc       string
	version   uint64
	snapshots int // TEXT_SNAPSHOT frames applied
	docErr    error
}

type dialOpts struct {
	lastSeq    uint64
	docVersion uint64
	canExecute bool
}

func (h *harness) dial(name, sessionID, secret string, o dialOpts, lat *latencies, sentAt *sync.Map) *simClient {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	conn, resp, err := websocket.Dial(ctx, h.wsURL(sessionID), &websocket.DialOptions{
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + secret}},
		Subprotocols: []string{"plan-forge.v1"},
	})
	if err != nil {
		cancel()
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		h.t.Fatalf("%s dial: %v (status %d)", name, err, status)
	}
	conn.SetReadLimit(MaxFrameBytes)
	c := &simClient{t: h.t, name: name, sessionID: sessionID, conn: conn, ctx: ctx, cancel: cancel, lat: lat, sentAt: sentAt}
	c.wg.Add(1)
	go c.readLoop()
	h.t.Cleanup(c.stop)
	c.send(EventHello, HelloPayload{Proto: 1, LastSeq: o.lastSeq, DocVersion: o.docVersion, CanExecute: o.canExecute}, 0)
	snap, _, ok := c.waitFor(5*time.Second, 0, isType(EventStateSnapshot))
	if !ok {
		h.t.Fatalf("%s: no STATE_SNAPSHOT after HELLO", name)
	}
	var sp StateSnapshotPayload
	if err := json.Unmarshal(snap.Payload, &sp); err != nil {
		h.t.Fatal(err)
	}
	c.memberID = sp.MemberID
	return c
}

func (c *simClient) stop() {
	_ = c.conn.CloseNow()
	c.cancel()
	c.wg.Wait()
}

func isType(t WSEventType) func(WSEnvelope) bool {
	return func(e WSEnvelope) bool { return e.EventType == t }
}

// typedDecode decodes a payload strictly into its protocol type. It is what
// proves the hub's conversion of reducer events (counters as decimal strings,
// bundle_id, error bodies) is faithful to protocol.go.
func typedDecode(env WSEnvelope) error {
	var dst any
	switch env.EventType {
	case EventPageTransition:
		dst = &PageTransitionPayload{}
	case EventRunAssigned:
		dst = &RunAssignedPayload{}
	case EventLeaseGranted:
		dst = &LeaseGrantedPayload{}
	case EventFenced:
		dst = &FencedPayload{}
	case EventError:
		dst = &ErrorPayload{}
	case EventStateSnapshot:
		dst = &StateSnapshotPayload{}
	case EventApprovalPrepared:
		dst = &ApprovalPreparedPayload{}
	case EventPlanApproved:
		dst = &PlanApprovedPayload{}
	case EventTextAck:
		dst = &TextAckPayload{}
	case EventTextReject:
		dst = &TextRejectPayload{}
	case EventTextUpdates:
		dst = &TextUpdatesPayload{}
	case EventTextSnapshot:
		dst = &TextSnapshotPayload{}
	case EventDecisionResolve:
		dst = &DecisionResolvePayload{}
	case EventRunSuperseded:
		return nil
	default:
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(env.Payload))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func (c *simClient) readLoop() {
	defer c.wg.Done()
	for {
		_, data, err := c.conn.Read(c.ctx)
		if err != nil {
			c.mu.Lock()
			c.closed = err
			c.mu.Unlock()
			return
		}
		env, derr := DecodeEnvelope(data)
		if derr != nil {
			c.t.Errorf("%s: relay sent an undecodable frame: %v", c.name, derr)
			continue
		}
		if terr := typedDecode(env); terr != nil {
			c.t.Errorf("%s: %s payload does not decode into its protocol type: %v (%s)", c.name, env.EventType, terr, env.Payload)
		}
		now := time.Now()
		c.mu.Lock()
		c.frames = append(c.frames, simFrame{env: env, at: now})
		c.mu.Unlock()
		c.observe(env, now)
	}
}

func (c *simClient) observe(env WSEnvelope, now time.Time) {
	switch env.EventType {
	case EventAmbiguityResolve:
		var p AmbiguityResolvePayload
		if json.Unmarshal(env.Payload, &p) == nil {
			if ns, err := strconv.ParseInt(p.Note, 10, 64); err == nil && c.lat != nil {
				c.lat.add("control", now.Sub(time.Unix(0, ns)))
			}
		}
	case EventTextUpdates:
		var p TextUpdatesPayload
		if json.Unmarshal(env.Payload, &p) != nil {
			return
		}
		c.applyRemote(p.FromVersion, p.Updates, now)
	case EventTextSnapshot:
		var p TextSnapshotPayload
		if json.Unmarshal(env.Payload, &p) != nil {
			return
		}
		c.docMu.Lock()
		c.doc, c.version = p.Text, p.Version
		c.snapshots++
		c.docMu.Unlock()
	}
}

// applyRemote applies updates the client has not seen yet. Updates already
// covered by its version (an update can arrive both as TEXT_UPDATES and inside a
// TEXT_REJECT) are skipped, which is the same dedup a CodeMirror client does by
// version.
func (c *simClient) applyRemote(from uint64, ups []Update, now time.Time) {
	c.docMu.Lock()
	defer c.docMu.Unlock()
	if from > c.version {
		c.docErr = fmt.Errorf("gap: update from %d but local version %d", from, c.version)
		return
	}
	skip := int(c.version - from)
	if skip >= len(ups) {
		return
	}
	for _, u := range ups[skip:] {
		next, err := applyChangesASCII(c.doc, u.Changes)
		if err != nil {
			c.docErr = err
			return
		}
		c.doc = next
		c.version++
		if c.lat != nil && c.sentAt != nil {
			if tok := insertedText(u.Changes); tok != "" {
				if v, ok := c.sentAt.Load(tok); ok {
					c.lat.add("doc", now.Sub(v.(time.Time)))
				}
			}
		}
	}
}

func applyChangesASCII(doc string, raw json.RawMessage) (string, error) {
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", err
	}
	var out strings.Builder
	pos := 0
	for _, p := range parts {
		var n int
		if json.Unmarshal(p, &n) == nil {
			if n < 0 || pos+n > len(doc) {
				return "", errors.New("retain past end of document")
			}
			out.WriteString(doc[pos : pos+n])
			pos += n
			continue
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(p, &arr); err != nil || len(arr) == 0 {
			return "", errors.New("malformed change part")
		}
		var del int
		if err := json.Unmarshal(arr[0], &del); err != nil {
			return "", err
		}
		pos += del
		lines := make([]string, 0, len(arr)-1)
		for _, l := range arr[1:] {
			var s string
			if err := json.Unmarshal(l, &s); err != nil {
				return "", err
			}
			lines = append(lines, s)
		}
		out.WriteString(strings.Join(lines, "\n"))
	}
	if pos != len(doc) {
		return "", fmt.Errorf("changes cover %d of %d units", pos, len(doc))
	}
	return out.String(), nil
}

func insertedText(raw json.RawMessage) string {
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		var arr []json.RawMessage
		if json.Unmarshal(p, &arr) != nil || len(arr) < 2 {
			continue
		}
		var lines []string
		for _, l := range arr[1:] {
			var s string
			_ = json.Unmarshal(l, &s)
			lines = append(lines, s)
		}
		sb.WriteString(strings.Join(lines, "\n"))
	}
	return sb.String()
}

func (c *simClient) send(t WSEventType, payload any, epoch uint64) string {
	c.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		c.t.Fatal(err)
	}
	id := newUUID()
	frame, err := Encode(WSEnvelope{
		Version: 1, SessionID: c.sessionID, EventID: id, EventType: t, Epoch: epoch,
		Timestamp: time.Now().UTC(), Payload: raw,
	})
	if err != nil {
		c.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, frame); err != nil {
		c.t.Fatalf("%s send %s: %v", c.name, t, err)
	}
	return id
}

func (c *simClient) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.frames)
}

func (c *simClient) all() []simFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.frames)
}

// waitFor scans frames from index from until pred matches or the timeout ends.
func (c *simClient) waitFor(timeout time.Duration, from int, pred func(WSEnvelope) bool) (WSEnvelope, int, bool) {
	deadline := time.Now().Add(timeout)
	for {
		c.mu.Lock()
		for i := from; i < len(c.frames); i++ {
			if pred(c.frames[i].env) {
				env := c.frames[i].env
				c.mu.Unlock()
				return env, i, true
			}
		}
		c.mu.Unlock()
		if time.Now().After(deadline) {
			return WSEnvelope{}, 0, false
		}
		time.Sleep(time.Millisecond)
	}
}

func (c *simClient) mustWait(from int, what string, pred func(WSEnvelope) bool) (WSEnvelope, int) {
	c.t.Helper()
	env, i, ok := c.waitFor(5*time.Second, from, pred)
	if !ok {
		c.t.Fatalf("%s: timed out waiting for %s", c.name, what)
	}
	return env, i
}

func (c *simClient) replyTo(id string) func(WSEnvelope) bool {
	return func(e WSEnvelope) bool { return e.EventID == id }
}

func (c *simClient) quiet(d time.Duration, from int, pred func(WSEnvelope) bool) bool {
	_, _, found := c.waitFor(d, from, pred)
	return !found
}

func (c *simClient) controlFrames() []WSEnvelope {
	var out []WSEnvelope
	for _, f := range c.all() {
		if f.env.Seq > 0 {
			out = append(out, f.env)
		}
	}
	return out
}

func (c *simClient) localDoc() (string, uint64) {
	c.docMu.Lock()
	defer c.docMu.Unlock()
	return c.doc, c.version
}

func shaHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (c *simClient) closeStatus() (websocket.StatusCode, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed == nil {
		return 0, false
	}
	return websocket.CloseStatus(c.closed), true
}

// pushToken appends tok at the end of the document with stop-and-wait OT:
// retry against the new head after a stale_base reject, the way a client
// rebases. It returns false if the push was refused for any other reason.
func (c *simClient) pushToken(tok string) bool {
	c.t.Helper()
	for range 50 {
		c.docMu.Lock()
		base, n := c.version, len(c.doc)
		c.docMu.Unlock()
		var changes json.RawMessage
		if n == 0 {
			changes = json.RawMessage(fmt.Sprintf(`[[0,%q]]`, tok))
		} else {
			changes = json.RawMessage(fmt.Sprintf(`[%d,[0,%q]]`, n, tok))
		}
		if c.sentAt != nil {
			c.sentAt.Store(tok, time.Now())
		}
		id := c.send(EventTextDelta, TextDeltaPayload{
			DocumentID: "prd", BaseVersion: base,
			Updates: []Update{{ClientID: c.memberID + ":1", Changes: changes}},
		}, 0)
		reply, _, ok := c.waitFor(5*time.Second, 0, func(e WSEnvelope) bool {
			return e.EventID == id && (e.EventType == EventTextAck || e.EventType == EventTextReject)
		})
		if !ok {
			c.t.Errorf("%s: no reply to push of %q", c.name, tok)
			return false
		}
		if reply.EventType == EventTextAck {
			var ack TextAckPayload
			_ = json.Unmarshal(reply.Payload, &ack)
			c.docMu.Lock()
			defer c.docMu.Unlock()
			if c.version == ack.Version-1 {
				next, err := applyChangesASCII(c.doc, changes)
				if err != nil {
					c.docErr = err
					return false
				}
				c.doc, c.version = next, ack.Version
			} else if c.version < ack.Version-1 {
				c.docErr = fmt.Errorf("ack %d but local version %d", ack.Version, c.version)
				return false
			}
			return true
		}
		var rej TextRejectPayload
		_ = json.Unmarshal(reply.Payload, &rej)
		if rej.Reason != "stale_base" {
			c.t.Errorf("%s: push of %q refused: %s", c.name, tok, reply.Payload)
			return false
		}
		if need := int64(rej.HeadVersion) - int64(base); need > 0 && int64(len(rej.Updates)) >= need {
			c.applyRemote(base, rej.Updates, time.Now())
		}
		// A reject that carried no usable updates is followed by a snapshot.
		time.Sleep(2 * time.Millisecond)
	}
	c.t.Errorf("%s: push of %q never landed", c.name, tok)
	return false
}

// --- load, ordering and convergence -----------------------------------------------------

func loadDuration() time.Duration {
	if v := os.Getenv("PLAN_FORGE_RELAY_LOAD_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	if testing.Short() {
		return 5 * time.Second
	}
	return 60 * time.Second
}

func p99Limit() time.Duration {
	if os.Getenv("CI") != "" {
		return 50 * time.Millisecond
	}
	return 20 * time.Millisecond
}

func TestHubLoadOrderingAndConvergence(t *testing.T) {
	dur := loadDuration()
	h := newHarness(t, nil)
	sess := h.createSession(nil)
	inv2 := h.invite(sess.RelaySessionID, sess.OwnerSecret, inviteRequest{Role: "editor"})
	bj := h.join(sess.RelaySessionID, sess.InviteSecret)
	cj := h.join(sess.RelaySessionID, inv2.InviteSecret)

	lat := &latencies{}
	sentAt := &sync.Map{}
	a := h.dial("A", sess.RelaySessionID, sess.OwnerSecret, dialOpts{canExecute: true}, lat, sentAt)
	b := h.dial("B", sess.RelaySessionID, bj.MemberSecret, dialOpts{}, lat, sentAt)
	c := h.dial("C", sess.RelaySessionID, cj.MemberSecret, dialOpts{}, lat, sentAt)
	clients := []*simClient{a, b, c}

	// Presence frames are ephemeral and sequenced separately; give them a moment
	// to settle so they cannot be mistaken for control events.
	time.Sleep(300 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()
	var wg sync.WaitGroup
	var sentControl atomic.Int64

	wg.Add(1)
	go func() { // 50 control events per second
		defer wg.Done()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				a.send(EventAmbiguityResolve, AmbiguityResolvePayload{
					AmbiguityID: "amb-" + strconv.Itoa(i), Choice: "product",
					Note: strconv.FormatInt(time.Now().UnixNano(), 10),
				}, 0)
				sentControl.Add(1)
			}
		}
	}()

	acked := map[string][]string{}
	var ackedMu sync.Mutex
	for _, w := range []*simClient{b, c} { // 2 x 10 = 20 document pushes per second
		wg.Add(1)
		go func(w *simClient) {
			defer wg.Done()
			tick := time.NewTicker(100 * time.Millisecond)
			defer tick.Stop()
			for i := 0; ; i++ {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					tok := fmt.Sprintf("%s%d;", w.name, i)
					if w.pushToken(tok) {
						ackedMu.Lock()
						acked[w.name] = append(acked[w.name], tok)
						ackedMu.Unlock()
					}
				}
			}
		}(w)
	}
	wg.Wait()
	time.Sleep(time.Second) // drain

	// Zero loss, zero duplicates, per-receiver order equals seq.
	want := int(sentControl.Load())
	var ref []string
	for _, cl := range clients {
		ctl := cl.controlFrames()
		if len(ctl) != want {
			t.Errorf("%s received %d control events, sent %d", cl.name, len(ctl), want)
		}
		ids := make([]string, 0, len(ctl))
		seen := map[string]bool{}
		for i, e := range ctl {
			if e.Seq != uint64(i+1) {
				t.Fatalf("%s: control event %d has seq %d, want %d (gap or reorder)", cl.name, i, e.Seq, i+1)
			}
			if seen[e.EventID] {
				t.Errorf("%s: duplicate control event %s", cl.name, e.EventID)
			}
			seen[e.EventID] = true
			ids = append(ids, e.EventID)
		}
		if ref == nil {
			ref = ids
		} else if !slices.Equal(ref, ids) {
			t.Errorf("%s saw a different event order than A", cl.name)
		}
		for _, f := range cl.all() {
			if f.env.EventType == EventError {
				t.Errorf("%s received an unexpected ERROR: %s", cl.name, f.env.Payload)
			}
		}
	}

	// Identical final text everywhere, equal to the authority's.
	room := h.room(sess.RelaySessionID)
	auth := room.doc.Snapshot()
	for _, cl := range clients {
		cl.docMu.Lock()
		derr := cl.docErr
		cl.docMu.Unlock()
		if derr != nil {
			t.Errorf("%s document error: %v", cl.name, derr)
		}
		text, ver := cl.localDoc()
		if shaHex(text) != auth.SHA256 || ver != auth.Version {
			t.Errorf("%s diverged: version %d hash %.12s, authority version %d hash %.12s", cl.name, ver, shaHex(text), auth.Version, auth.SHA256)
		}
	}
	for name, toks := range acked {
		for _, tok := range toks {
			if strings.Count(auth.Text, tok) != 1 {
				t.Errorf("%s token %q appears %d times in the final document", name, tok, strings.Count(auth.Text, tok))
			}
		}
	}
	if total := len(acked["B"]) + len(acked["C"]); uint64(total) != auth.Version {
		t.Errorf("accepted %d pushes but authority is at version %d", total, auth.Version)
	}

	// An in-sync TEXT_CHECK is silent; a wrong hash forces a snapshot.
	for _, cl := range clients {
		text, ver := cl.localDoc()
		before := cl.count()
		cl.send(EventTextCheck, TextCheckPayload{DocumentID: "prd", Version: ver, SHA256: shaHex(text)}, 0)
		if !cl.quiet(300*time.Millisecond, before, isType(EventTextSnapshot)) {
			t.Errorf("%s: a matching TEXT_CHECK produced a snapshot", cl.name)
		}
	}
	before := a.count()
	a.send(EventTextCheck, TextCheckPayload{DocumentID: "prd", Version: auth.Version, SHA256: strings.Repeat("0", 64)}, 0)
	a.mustWait(before, "TEXT_SNAPSHOT after a mismatching TEXT_CHECK", isType(EventTextSnapshot))

	lat.mu.Lock()
	defer lat.mu.Unlock()
	limit := p99Limit()
	for kind, ds := range map[string][]time.Duration{"control": lat.control, "doc": lat.doc} {
		if len(ds) == 0 {
			t.Errorf("no %s latency samples", kind)
			continue
		}
		p50, p95, p99 := percentile(ds, .50), percentile(ds, .95), percentile(ds, .99)
		t.Logf("%s send->receive over %d samples: p50=%v p95=%v p99=%v (limit %v)", kind, len(ds), p50, p95, p99, limit)
		if p99 > limit {
			t.Errorf("%s p99 %v exceeds %v", kind, p99, limit)
		}
	}
	t.Logf("load: %d control events, %d document pushes over %v", want, auth.Version, dur)
}

// --- replay, resync, document catch-up -----------------------------------------------

func TestHubReplayAndDocumentCatchup(t *testing.T) {
	h := newHarness(t, nil)
	sess := h.createSession(nil)
	a := h.dial("A", sess.RelaySessionID, sess.OwnerSecret, dialOpts{canExecute: true}, nil, nil)
	for i := range 30 {
		a.send(EventAmbiguityResolve, AmbiguityResolvePayload{AmbiguityID: "a" + strconv.Itoa(i), Choice: "dismiss"}, 0)
	}
	a.mustWait(0, "30th control event", func(e WSEnvelope) bool { return e.Seq == 30 })
	for i := range 5 {
		if !a.pushToken(fmt.Sprintf("t%d;", i)) {
			t.Fatal("push failed")
		}
	}

	// Inside the ring: snapshot, then exactly the missing events 11..30.
	d := h.dial("D", sess.RelaySessionID, sess.OwnerSecret, dialOpts{lastSeq: 10, docVersion: 2}, nil, nil)
	d.mustWait(0, "event 30", func(e WSEnvelope) bool { return e.Seq == 30 })
	var seqs []uint64
	for _, e := range d.controlFrames() {
		seqs = append(seqs, e.Seq)
	}
	if len(seqs) != 20 || seqs[0] != 11 || seqs[19] != 30 {
		t.Fatalf("replay after last_seq 10 returned %v, want 11..30", seqs)
	}
	if d.all()[0].env.EventType != EventStateSnapshot {
		t.Fatal("the snapshot must come before the replay")
	}
	// Document catch-up from version 2: three updates.
	up, _ := d.mustWait(0, "TEXT_UPDATES catch-up", isType(EventTextUpdates))
	var tu TextUpdatesPayload
	_ = json.Unmarshal(up.Payload, &tu)
	if tu.FromVersion != 2 || len(tu.Updates) != 3 {
		t.Fatalf("catch-up = from %d with %d updates, want from 2 with 3", tu.FromVersion, len(tu.Updates))
	}
	d.docMu.Lock()
	d.doc, d.version = "t0;t1;", 2
	d.docMu.Unlock()

	// A fresh client holds no history: a snapshot of the document, no event replay.
	e := h.dial("E", sess.RelaySessionID, sess.OwnerSecret, dialOpts{}, nil, nil)
	e.mustWait(0, "TEXT_SNAPSHOT for a fresh client", isType(EventTextSnapshot))
	if len(e.controlFrames()) != 0 {
		t.Errorf("a fresh client got %d replayed control events", len(e.controlFrames()))
	}
	if text, ver := e.localDoc(); text != "t0;t1;t2;t3;t4;" || ver != 5 {
		t.Errorf("fresh client document = %q at %d", text, ver)
	}

	// REPLAY_REQUEST on a live connection: a fresh snapshot, then the gap.
	from := a.count()
	id := a.send(EventReplayRequest, map[string]string{"last_seq": "25"}, 0)
	snap, _ := a.mustWait(from, "STATE_SNAPSHOT for REPLAY_REQUEST", a.replyTo(id))
	var sp StateSnapshotPayload
	_ = json.Unmarshal(snap.Payload, &sp)
	if sp.HeadSeq != 30 || sp.Phase != PhaseIngestion || sp.Role != RoleOwner || sp.HeadDocVersion != 5 {
		t.Errorf("snapshot = %+v", sp)
	}
	_, last := a.mustWait(from, "replayed event 30", func(e WSEnvelope) bool { return e.Seq == 30 })
	var again []uint64
	for _, f := range a.all()[from : last+1] {
		if f.env.Seq > 0 {
			again = append(again, f.env.Seq)
		}
	}
	if !slices.Equal(again, []uint64{26, 27, 28, 29, 30}) {
		t.Errorf("inline replay returned %v, want 26..30", again)
	}
}

func TestHubIngestAppendRolesAndDocument(t *testing.T) {
	h := newHarness(t, nil)
	sess := h.createSession(nil)
	bj := h.join(sess.RelaySessionID, sess.InviteSecret)
	a := h.dial("A", sess.RelaySessionID, sess.OwnerSecret, dialOpts{canExecute: true}, nil, nil)
	b := h.dial("B", sess.RelaySessionID, bj.MemberSecret, dialOpts{}, nil, nil)

	id := a.send(EventIngestAppend, IngestAppendPayload{Source: IngestSourceUpload, SourceName: "spec\n# evil.docx", Markdown: "# Goals\r\nShip it.\r\n"}, 0)
	b.mustWait(0, "TEXT_UPDATES for the upload", isType(EventTextUpdates))
	a.mustWait(0, "TEXT_UPDATES at the appender", isType(EventTextUpdates))
	_ = id
	want := "## Source: spec evil.docx\n\n# Goals\nShip it.\n"
	if text, _ := b.localDoc(); text != want {
		t.Fatalf("document = %q, want %q", text, want)
	}
	if b.docErr != nil || a.docErr != nil {
		t.Fatalf("replica errors: %v %v", a.docErr, b.docErr)
	}

	// An editor may append composer text, but not uploads.
	from := b.count()
	b.send(EventIngestAppend, IngestAppendPayload{Source: IngestSourceComposer, Markdown: "an idea"}, 0)
	a.mustWait(0, "second append", func(e WSEnvelope) bool {
		var p TextUpdatesPayload
		return e.EventType == EventTextUpdates && json.Unmarshal(e.Payload, &p) == nil && p.FromVersion == 1
	})
	if text, _ := a.localDoc(); !strings.HasSuffix(text, "\n\n## Source: composer\n\nan idea\n") {
		t.Errorf("composer append produced %q", text)
	}
	b.send(EventIngestAppend, IngestAppendPayload{Source: IngestSourceUpload, Markdown: "x"}, 0)
	e, _ := b.mustWait(from, "ROLE_FORBIDDEN for an editor upload", func(e WSEnvelope) bool { return e.EventType == EventError })
	var ep ErrorPayload
	_ = json.Unmarshal(e.Payload, &ep)
	if ep.Code != contracts.CodeRoleForbidden {
		t.Errorf("editor upload: %s", e.Payload)
	}

	// After the baseline freezes, appends and pushes are refused.
	a.send(EventFreezeRequest, struct{}{}, 0)
	a.mustWait(0, "PAGE_TRANSITION to DELIBERATION", func(e WSEnvelope) bool { return e.EventType == EventPageTransition })
	before := b.count()
	b.send(EventIngestAppend, IngestAppendPayload{Source: IngestSourceComposer, Markdown: "late"}, 0)
	e, _ = b.mustWait(before, "ILLEGAL_PHASE for a late append", func(e WSEnvelope) bool { return e.EventType == EventError })
	_ = json.Unmarshal(e.Payload, &ep)
	if ep.Code != contracts.CodeIllegalPhase {
		t.Errorf("late append: %s", e.Payload)
	}
	b.send(EventTextDelta, TextDeltaPayload{DocumentID: "prd", BaseVersion: 2, Updates: []Update{{ClientID: b.memberID + ":1", Changes: json.RawMessage(`[[0,"x"]]`)}}}, 0)
	rej, _ := b.mustWait(before, "TEXT_REJECT locked", func(e WSEnvelope) bool { return e.EventType == EventTextReject })
	var rp TextRejectPayload
	_ = json.Unmarshal(rej.Payload, &rp)
	if rp.Reason != "locked" {
		t.Errorf("push after freeze: %s", rej.Payload)
	}
}

// --- lease race and fencing ---------------------------------------------------------------

func countType(c *simClient, t WSEventType) int {
	n := 0
	for _, f := range c.all() {
		if f.env.EventType == t {
			n++
		}
	}
	return n
}

type stubGenerator struct{ calls atomic.Int32 }

func (g *stubGenerator) Generate() { g.calls.Add(1) }

func TestHubLeaseRaceAndFencing(t *testing.T) {
	h := newHarness(t, nil)
	sess := h.createSession(nil)
	bj := h.join(sess.RelaySessionID, sess.InviteSecret)
	a1 := h.dial("A1", sess.RelaySessionID, sess.OwnerSecret, dialOpts{canExecute: true}, nil, nil)
	a2 := h.dial("A2", sess.RelaySessionID, sess.OwnerSecret, dialOpts{canExecute: true}, nil, nil)
	b := h.dial("B", sess.RelaySessionID, bj.MemberSecret, dialOpts{}, nil, nil)

	// Two simultaneous claims: one wins, the other is fenced.
	var claim sync.WaitGroup
	start := make(chan struct{})
	ids := map[string]string{}
	var idMu sync.Mutex
	for _, cl := range []*simClient{a1, a2} {
		claim.Add(1)
		go func(cl *simClient) {
			defer claim.Done()
			<-start
			id := cl.send(EventLeaseClaim, struct{}{}, 0)
			idMu.Lock()
			ids[cl.name] = id
			idMu.Unlock()
		}(cl)
	}
	close(start)
	claim.Wait()
	for _, cl := range []*simClient{a1, a2, b} {
		cl.mustWait(0, "LEASE_GRANTED", isType(EventLeaseGranted))
	}
	time.Sleep(300 * time.Millisecond)
	for _, cl := range []*simClient{a1, a2, b} {
		if n := countType(cl, EventLeaseGranted); n != 1 {
			t.Errorf("%s saw %d LEASE_GRANTED, want exactly 1", cl.name, n)
		}
	}
	fenced := map[string]bool{}
	for _, cl := range []*simClient{a1, a2} {
		if _, _, ok := cl.waitFor(200*time.Millisecond, 0, func(e WSEnvelope) bool {
			return e.EventType == EventFenced && e.EventID == ids[cl.name]
		}); ok {
			fenced[cl.name] = true
		}
	}
	if len(fenced) != 1 {
		t.Fatalf("exactly one claimant must be fenced, got %v", fenced)
	}
	winner, loser := a1, a2
	if fenced["A1"] {
		winner, loser = a2, a1
	}
	gens := map[string]*stubGenerator{"A1": {}, "A2": {}, "B": {}}
	gens[winner.name].Generate() // only the holder runs a provider call

	// An editor can neither claim the lease nor emit executor events.
	before := b.count()
	b.send(EventLeaseClaim, struct{}{}, 0)
	e, _ := b.mustWait(before, "ROLE_FORBIDDEN lease claim", func(e WSEnvelope) bool { return e.EventType == EventError })
	var ep ErrorPayload
	_ = json.Unmarshal(e.Payload, &ep)
	if ep.Code != contracts.CodeRoleForbidden {
		t.Errorf("editor claim: %s", e.Payload)
	}

	// FREEZE assigns the run: PAGE_TRANSITION then RUN_ASSIGNED, in seq order.
	winner.send(EventFreezeRequest, struct{}{}, 0)
	ra, raIdx := b.mustWait(0, "RUN_ASSIGNED", isType(EventRunAssigned))
	_, ptIdx := b.mustWait(0, "PAGE_TRANSITION", isType(EventPageTransition))
	if ptIdx > raIdx {
		t.Error("PAGE_TRANSITION must precede RUN_ASSIGNED")
	}
	var run RunAssignedPayload
	_ = json.Unmarshal(ra.Payload, &run)
	if run.RunID == "" || run.BaselineRev != 1 || run.LeaseEpoch != 1 {
		t.Fatalf("RUN_ASSIGNED = %+v", run)
	}

	draft := func(epoch uint64, rev string) string {
		return winner.send(EventAvatarDraft, AvatarDraftPayload{
			RunID: run.RunID, AvatarID: "architect", Cycle: 1, DraftID: "d" + rev, DraftRevision: 1,
			SchemaID: "architect.v1", Content: json.RawMessage(`{}`), ContentHash: shaHex(rev), Complete: true,
		}, epoch)
	}
	// A stale epoch is fenced and the event is dropped.
	before = winner.count()
	id := draft(0, "stale")
	winner.mustWait(before, "FENCED for epoch 0", func(e WSEnvelope) bool { return e.EventType == EventFenced && e.EventID == id })
	if !b.quiet(300*time.Millisecond, 0, isType(EventAvatarDraft)) {
		t.Error("a fenced draft was broadcast")
	}
	// The current epoch is accepted and reaches everyone.
	draft(1, "ok")
	for _, cl := range []*simClient{winner, loser, b} {
		cl.mustWait(0, "AVATAR_DRAFT broadcast", isType(EventAvatarDraft))
	}
	// An editor with the right epoch is still not the executor.
	before = b.count()
	b.send(EventAvatarDraft, AvatarDraftPayload{RunID: run.RunID, AvatarID: "x", SchemaID: "x", ContentHash: shaHex("x"), Content: json.RawMessage(`{}`)}, 1)
	e, _ = b.mustWait(before, "ROLE_FORBIDDEN draft", func(e WSEnvelope) bool { return e.EventType == EventError })
	_ = json.Unmarshal(e.Payload, &ep)
	if ep.Code != contracts.CodeRoleForbidden {
		t.Errorf("editor draft: %s", e.Payload)
	}

	// Release and re-claim: the epoch moves to 2 and epoch 1 is now fenced.
	winner.send(EventLeaseRelease, struct{}{}, 0)
	b.mustWait(0, "LEASE_LOST after release", isType(EventLeaseLost))
	winner.send(EventLeaseClaim, struct{}{}, 0)
	winner.mustWait(0, "second LEASE_GRANTED", func(e WSEnvelope) bool {
		var lg LeaseGrantedPayload
		return e.EventType == EventLeaseGranted && json.Unmarshal(e.Payload, &lg) == nil && lg.Epoch == 2
	})
	before = winner.count()
	id = draft(1, "stale-again")
	e, _ = winner.mustWait(before, "FENCED for epoch 1", func(e WSEnvelope) bool { return e.EventType == EventFenced && e.EventID == id })
	var fp FencedPayload
	_ = json.Unmarshal(e.Payload, &fp)
	if fp.CurrentEpoch != 2 {
		t.Errorf("FENCED current_epoch = %d, want 2", fp.CurrentEpoch)
	}
	for name, g := range gens {
		if want := int32(0); name != winner.name && g.calls.Load() != want {
			t.Errorf("%s ran a provider call without holding the lease", name)
		}
	}
	if gens[winner.name].calls.Load() != 1 {
		t.Errorf("the lease holder ran %d provider calls, want 1", gens[winner.name].calls.Load())
	}
}

func TestHubLeaseExpiryAnnouncesExecutorLost(t *testing.T) {
	clock := newFakeClock()
	h := newHarness(t, clock.Now)
	sess := h.createSession(nil)
	bj := h.join(sess.RelaySessionID, sess.InviteSecret)
	a := h.dial("A", sess.RelaySessionID, sess.OwnerSecret, dialOpts{canExecute: true}, nil, nil)
	b := h.dial("B", sess.RelaySessionID, bj.MemberSecret, dialOpts{}, nil, nil)

	a.send(EventLeaseClaim, struct{}{}, 0)
	a.mustWait(0, "LEASE_GRANTED", isType(EventLeaseGranted))
	// A renewal inside the TTL is acknowledged.
	clock.Advance(6 * time.Second)
	id := a.send(EventLeaseRenew, map[string]string{"epoch": "1"}, 1)
	a.mustWait(0, "LEASE_RENEW ack", func(e WSEnvelope) bool { return e.EventType == EventLeaseRenew && e.EventID == id })

	// No renewal for longer than the TTL: everyone learns the executor is gone.
	clock.Advance(16 * time.Second)
	b.mustWait(0, "LEASE_LOST", isType(EventLeaseLost))
	lost, _ := b.mustWait(0, "EXECUTOR_LOST", isType(EventExecutorLost))
	var el executorLostPayload
	_ = json.Unmarshal(lost.Payload, &el)
	if el.ExecutorID != "owner" || el.Epoch != 1 {
		t.Errorf("EXECUTOR_LOST = %+v", el)
	}
	// The authority expires leases but never elects: the owner claims again.
	a.send(EventLeaseClaim, struct{}{}, 0)
	a.mustWait(0, "LEASE_GRANTED epoch 2", func(e WSEnvelope) bool {
		var lg LeaseGrantedPayload
		return e.EventType == EventLeaseGranted && json.Unmarshal(e.Payload, &lg) == nil && lg.Epoch == 2
	})
}

// --- approval ordering and artifacts ------------------------------------------------------------

type approvalRig struct {
	t       *testing.T
	h       *harness
	sess    createSessionResponse
	a, b, c *simClient
	bj      joinResponse
	run     RunAssignedPayload
	epoch   uint64
	synth   string
}

func newApprovalRig(t *testing.T, clock func() time.Time) *approvalRig {
	h := newHarness(t, clock)
	sess := h.createSession(nil)
	inv2 := h.invite(sess.RelaySessionID, sess.OwnerSecret, inviteRequest{Role: "viewer"})
	bj := h.join(sess.RelaySessionID, sess.InviteSecret)
	cj := h.join(sess.RelaySessionID, inv2.InviteSecret)
	r := &approvalRig{t: t, h: h, sess: sess, bj: bj, epoch: 1}
	r.a = h.dial("A", sess.RelaySessionID, sess.OwnerSecret, dialOpts{canExecute: true}, nil, nil)
	r.b = h.dial("B", sess.RelaySessionID, bj.MemberSecret, dialOpts{}, nil, nil)
	r.c = h.dial("C", sess.RelaySessionID, cj.MemberSecret, dialOpts{}, nil, nil)

	r.a.send(EventLeaseClaim, struct{}{}, 0)
	r.a.mustWait(0, "LEASE_GRANTED", isType(EventLeaseGranted))
	r.a.send(EventFreezeRequest, struct{}{}, 0)
	ra, _ := r.a.mustWait(0, "RUN_ASSIGNED", isType(EventRunAssigned))
	_ = json.Unmarshal(ra.Payload, &r.run)

	r.a.send(EventAvatarDraft, AvatarDraftPayload{
		RunID: r.run.RunID, AvatarID: "architect", Cycle: 1, DraftID: "d1", DraftRevision: 1,
		SchemaID: "architect.v1", Content: json.RawMessage(`{}`), ContentHash: shaHex("architect"), Complete: true,
	}, r.epoch)
	r.a.mustWait(0, "AVATAR_DRAFT", isType(EventAvatarDraft))
	r.a.send(EventSynthesizeRequest, SynthesizeRequestPayload{RunID: r.run.RunID, InputRevision: 1}, r.epoch)
	r.a.mustWait(0, "PAGE_TRANSITION to SYNTHESIS", func(e WSEnvelope) bool {
		var p PageTransitionPayload
		return e.EventType == EventPageTransition && json.Unmarshal(e.Payload, &p) == nil && p.To == PhaseSynthesis
	})
	r.synth = shaHex("compiled synthesis")
	r.a.send(EventAvatarDraft, AvatarDraftPayload{
		RunID: r.run.RunID, AvatarID: "synthesizer", Cycle: 1, DraftID: "s1", DraftRevision: 1,
		SchemaID: "synthesis.v1", Content: json.RawMessage(`{"spec_markdown":"# Spec","manifest_json":"{}"}`),
		ContentHash: r.synth, Complete: true,
	}, r.epoch)
	return r
}

func (r *approvalRig) approve(from int) ApprovalPreparedPayload {
	r.t.Helper()
	r.a.send(EventApprove, ApprovePayload{BaselineRev: 1, RunID: r.run.RunID, SynthesisRev: 1, ContentSHA256: r.synth}, r.epoch)
	e, _ := r.a.mustWait(from, "APPROVAL_PREPARED", isType(EventApprovalPrepared))
	var p ApprovalPreparedPayload
	_ = json.Unmarshal(e.Payload, &p)
	if !uuidRe.MatchString(p.ApprovalID) {
		r.t.Fatalf("approval id %q is not a UUID", p.ApprovalID)
	}
	return p
}

func (r *approvalRig) put(secret, approvalID, name string, epoch uint64, body []byte) int {
	code, _, _ := r.h.do("PUT", "/sessions/"+r.sess.RelaySessionID+"/artifacts/"+approvalID+"/"+name, secret, body,
		map[string]string{"X-Plan-Forge-Epoch": strconv.FormatUint(epoch, 10)})
	return code
}

func (r *approvalRig) phase() Phase {
	r.t.Helper()
	from := r.a.count()
	id := r.a.send(EventReplayRequest, map[string]string{"last_seq": strconv.FormatUint(r.h.room(r.sess.RelaySessionID).seq, 10)}, 0)
	snap, _ := r.a.mustWait(from, "STATE_SNAPSHOT", r.a.replyTo(id))
	var sp StateSnapshotPayload
	_ = json.Unmarshal(snap.Payload, &sp)
	return sp.Phase
}

func countFrom(c *simClient, from int, t WSEventType) int {
	n := 0
	for i, f := range c.all() {
		if i >= from && f.env.EventType == t {
			n++
		}
	}
	return n
}

func TestHubApprovalOrderingAndArtifacts(t *testing.T) {
	r := newApprovalRig(t, nil)
	spec, manifest := []byte("# Spec\n"), []byte(`{"schema_version":1}`)

	// Attempt 1: the executor cannot write the files and reports it.
	prep := r.approve(0)
	if got := r.phase(); got != PhaseApproving {
		t.Fatalf("phase after APPROVE = %s", got)
	}
	from := r.a.count()
	r.a.send(EventArtifactSaved, ArtifactSavedPayload{OK: false, Error: &ArtifactError{Code: ArtifactENOSPC, RelPath: ".plan/synthesized_spec.md"}}, r.epoch)
	ev, _ := r.a.mustWait(from, "ERROR APPROVAL_FAILED", isType(EventError))
	var ep ErrorPayload
	_ = json.Unmarshal(ev.Payload, &ep)
	if ep.Code != contracts.CodeApprovalFailed || ep.RelPath != ".plan/synthesized_spec.md" || !strings.Contains(ep.Message, ArtifactENOSPC) {
		t.Errorf("failure broadcast = %s", ev.Payload)
	}
	if got := r.phase(); got != PhaseSynthesis {
		t.Fatalf("a failed write must return to SYNTHESIS, got %s", got)
	}
	for _, cl := range []*simClient{r.a, r.b, r.c} {
		if countType(cl, EventPlanApproved) != 0 {
			t.Fatalf("%s saw PLAN_APPROVED after a failed write", cl.name)
		}
	}

	// Attempt 2: a fresh approval id; PLAN_APPROVED only after ARTIFACT_SAVED ok.
	mark := r.a.count()
	prep = r.approve(mark)
	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	specSum, manSum := sum(spec), sum(manifest)

	// Claiming success before uploading anything is refused.
	from = r.a.count()
	r.a.send(EventArtifactSaved, ArtifactSavedPayload{OK: true, SpecSHA256: specSum, ManifestSHA256: manSum}, r.epoch)
	ev, _ = r.a.mustWait(from, "ERROR before upload", isType(EventError))
	_ = json.Unmarshal(ev.Payload, &ep)
	if ep.Code != contracts.CodeApprovalFailed {
		t.Errorf("success before upload: %s", ev.Payload)
	}

	// Upload rules: owner secret, current epoch, the approval in progress, known names.
	if got := r.put(r.bj.MemberSecret, prep.ApprovalID, "synthesized_spec.md", r.epoch, spec); got != http.StatusUnauthorized {
		t.Errorf("member PUT = %d, want 401", got)
	}
	if got := r.put(r.sess.OwnerSecret, prep.ApprovalID, "synthesized_spec.md", r.epoch+5, spec); got != http.StatusConflict {
		t.Errorf("wrong-epoch PUT = %d, want 409", got)
	}
	if got := r.put(r.sess.OwnerSecret, newUUID(), "synthesized_spec.md", r.epoch, spec); got != http.StatusConflict {
		t.Errorf("other-approval PUT = %d, want 409", got)
	}
	if got := r.put(r.sess.OwnerSecret, prep.ApprovalID, "evil.sh", r.epoch, spec); got != http.StatusBadRequest {
		t.Errorf("unknown-name PUT = %d, want 400", got)
	}
	if got := r.put(r.sess.OwnerSecret, "not-a-uuid", "synthesized_spec.md", r.epoch, spec); got != http.StatusBadRequest {
		t.Errorf("non-UUID PUT = %d, want 400", got)
	}
	if got := r.put(r.sess.OwnerSecret, prep.ApprovalID, "synthesized_spec.md", r.epoch, bytes.Repeat([]byte("x"), maxArtifactBytes+1)); got != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized PUT = %d, want 413", got)
	}
	if got := r.put(r.sess.OwnerSecret, prep.ApprovalID, "synthesized_spec.md", r.epoch, spec); got != http.StatusNoContent {
		t.Fatalf("spec PUT = %d", got)
	}
	if got := r.put(r.sess.OwnerSecret, prep.ApprovalID, "plan_manifest.json", r.epoch, manifest); got != http.StatusNoContent {
		t.Fatalf("manifest PUT = %d", got)
	}

	// Reported hashes must equal the stored ones.
	from = r.a.count()
	r.a.send(EventArtifactSaved, ArtifactSavedPayload{OK: true, SpecSHA256: specSum, ManifestSHA256: sum([]byte("tampered"))}, r.epoch)
	ev, _ = r.a.mustWait(from, "ERROR for mismatching hashes", isType(EventError))
	_ = json.Unmarshal(ev.Payload, &ep)
	if ep.Code != contracts.CodeApprovalFailed {
		t.Errorf("mismatching hashes: %s", ev.Payload)
	}
	if got := r.phase(); got != PhaseApproving {
		t.Fatalf("phase = %s, want APPROVING until ARTIFACT_SAVED ok", got)
	}

	// No client has seen PLAN_APPROVED so far.
	cut := map[string]int{}
	for _, cl := range []*simClient{r.a, r.b, r.c} {
		cut[cl.name] = cl.count()
		if countType(cl, EventPlanApproved) != 0 {
			t.Fatalf("%s saw PLAN_APPROVED before ARTIFACT_SAVED ok", cl.name)
		}
	}
	r.a.send(EventArtifactSaved, ArtifactSavedPayload{OK: true, SpecSHA256: strings.ToUpper(specSum), ManifestSHA256: manSum}, r.epoch)
	var approved PlanApprovedPayload
	for _, cl := range []*simClient{r.a, r.b, r.c} {
		ev, _ := cl.mustWait(cut[cl.name], "PLAN_APPROVED", isType(EventPlanApproved))
		if err := json.Unmarshal(ev.Payload, &approved); err != nil {
			t.Fatal(err)
		}
		if approved.ApprovalID != prep.ApprovalID || approved.SpecSHA256 != specSum || approved.ManifestSHA256 != manSum || approved.PersistedBy != "owner" {
			t.Errorf("%s PLAN_APPROVED = %+v", cl.name, approved)
		}
	}
	raw := append(mustHex(t, specSum), mustHex(t, manSum)...)
	if want := sum(raw); approved.BundleID != want {
		t.Errorf("bundle_id = %s, want sha256(spec||manifest) = %s", approved.BundleID, want)
	}

	// Followers fetch both artifacts with their member secret and verify the hash.
	for name, want := range map[string]string{"synthesized_spec.md": specSum, "plan_manifest.json": manSum} {
		code, body, hdr := r.h.do("GET", "/sessions/"+r.sess.RelaySessionID+"/artifacts/"+prep.ApprovalID+"/"+name, r.bj.MemberSecret, nil, nil)
		if code != http.StatusOK || sum(body) != want || hdr.Get("X-Plan-Forge-SHA256") != want {
			t.Errorf("GET %s = %d, hash %s", name, code, sum(body))
		}
	}
	if code, _, _ := r.h.do("GET", "/sessions/"+r.sess.RelaySessionID+"/artifacts/"+prep.ApprovalID+"/plan_manifest.json", r.sess.InviteSecret, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("invite-secret GET = %d, want 401", code)
	}
	// A follower reports persistence; every member sees it, with an allowlisted error.
	before := r.a.count()
	r.b.send(EventArtifactSynced, map[string]any{"ok": false, "error": map[string]string{"code": "EACCES", "rel_path": "/Users/x/secret"}}, 0)
	ev, _ = r.a.mustWait(before, "ARTIFACT_SYNCED", isType(EventArtifactSynced))
	if ev.SenderID != r.b.memberID || !strings.Contains(string(ev.Payload), `"code":"EACCES"`) || strings.Contains(string(ev.Payload), "/Users/") {
		t.Errorf("ARTIFACT_SYNCED = sender %s payload %s", ev.SenderID, ev.Payload)
	}
	if got := r.phase(); got != PhaseApproved {
		t.Errorf("final phase = %s", got)
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHubExecutorLostMidApprovalResolvesIdempotently(t *testing.T) {
	clock := newFakeClock()
	r := newApprovalRig(t, clock.Now)
	spec, manifest := []byte("# Spec\n"), []byte(`{"schema_version":1}`)
	prep := r.approve(0)
	if got := r.put(r.sess.OwnerSecret, prep.ApprovalID, "synthesized_spec.md", r.epoch, spec); got != http.StatusNoContent {
		t.Fatalf("PUT = %d", got)
	}

	// The executor disappears mid-write.
	from := r.b.count()
	clock.Advance(16 * time.Second)
	r.b.mustWait(from, "EXECUTOR_LOST", isType(EventExecutorLost))
	pt, _ := r.b.mustWait(from, "PAGE_TRANSITION to APPROVING_UNKNOWN", func(e WSEnvelope) bool {
		var p PageTransitionPayload
		return e.EventType == EventPageTransition && json.Unmarshal(e.Payload, &p) == nil && p.To == PhaseApprovingUnknown
	})
	_ = pt
	if countType(r.b, EventPlanApproved) != 0 {
		t.Fatal("PLAN_APPROVED must not be emitted for an unknown write state")
	}

	// A returning executor claims a new epoch, finishes the upload and reports.
	r.a.send(EventLeaseClaim, struct{}{}, 0)
	r.a.mustWait(0, "LEASE_GRANTED epoch 2", func(e WSEnvelope) bool {
		var lg LeaseGrantedPayload
		return e.EventType == EventLeaseGranted && json.Unmarshal(e.Payload, &lg) == nil && lg.Epoch == 2
	})
	if got := r.put(r.sess.OwnerSecret, prep.ApprovalID, "plan_manifest.json", 2, manifest); got != http.StatusNoContent {
		t.Fatalf("PUT after reclaim = %d", got)
	}
	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	r.a.send(EventArtifactSaved, ArtifactSavedPayload{OK: true, SpecSHA256: sum(spec), ManifestSHA256: sum(manifest)}, 2)
	r.b.mustWait(from, "PLAN_APPROVED after the executor returned", isType(EventPlanApproved))
	if got := r.phase(); got != PhaseApproved {
		t.Errorf("phase = %s", got)
	}
}

// --- credentials, origin, transport rules ---------------------------------------------------------

func TestHubCredentialsOriginAndTransportRules(t *testing.T) {
	h := newHarness(t, nil)

	// Relay token: required, wrong one refused, nothing in the body.
	if code, _, _ := h.do("POST", "/sessions", "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("POST /sessions without a token = %d", code)
	}
	if code, _, _ := h.do("POST", "/sessions", "wrong", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("POST /sessions with a wrong token = %d", code)
	}
	// Browsers never talk to the relay: any Origin is refused on every route.
	for _, path := range []string{"/healthz", "/join/" + strings.Repeat("a", 32)} {
		if code, _, _ := h.do("GET", path, "", nil, map[string]string{"Origin": "http://evil.example"}); code != http.StatusForbidden {
			t.Errorf("GET %s with an Origin = %d, want 403", path, code)
		}
	}
	if code, _, _ := h.do("POST", "/sessions", testRelayToken, nil, map[string]string{"Origin": "http://127.0.0.1"}); code != http.StatusForbidden {
		t.Errorf("POST /sessions with an Origin = %d, want 403", code)
	}
	// The join landing page is static and never needs the fragment.
	if code, body, _ := h.do("GET", "/join/"+strings.Repeat("0", 32), "", nil, nil); code != http.StatusOK || !strings.Contains(string(body), "plan-forge join") {
		t.Errorf("join landing = %d %q", code, body)
	}

	sess := h.createSession(inviteRequest{Role: "editor", MaxUses: 1})
	id := sess.RelaySessionID
	if len(id) != 32 || len(sess.OwnerSecret) != 64 || len(sess.InviteSecret) != 64 {
		t.Fatalf("ids are not 128/256-bit hex: %d %d %d", len(id), len(sess.OwnerSecret), len(sess.InviteSecret))
	}

	// An invite secret is rejected on /ws and works once on /join.
	if _, resp, err := websocket.Dial(context.Background(), h.wsURL(id), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + sess.InviteSecret}}, Subprotocols: []string{"plan-forge.v1"},
	}); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("invite secret on /ws: err=%v resp=%v", err, resp)
	}
	member := h.join(id, sess.InviteSecret)
	if code, _, _ := h.do("POST", "/sessions/"+id+"/join", sess.InviteSecret, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("second join with a max_uses=1 invite = %d, want 401", code)
	}
	// A member secret cannot join, mint invites or upload; an owner secret cannot join.
	if code, _, _ := h.do("POST", "/sessions/"+id+"/join", member.MemberSecret, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("member secret on /join = %d", code)
	}
	if code, _, _ := h.do("POST", "/sessions/"+id+"/invites", member.MemberSecret, inviteRequest{Role: "viewer"}, nil); code != http.StatusUnauthorized {
		t.Errorf("member secret on /invites = %d", code)
	}
	// Another room's credential is no credential here, and a malformed id is a 400.
	other := h.createSession(nil)
	if code, _, _ := h.do("POST", "/sessions/"+id+"/invites", other.OwnerSecret, inviteRequest{Role: "viewer"}, nil); code != http.StatusUnauthorized {
		t.Errorf("another room's owner secret = %d", code)
	}
	if code, _, _ := h.do("POST", "/sessions/xyz/join", sess.InviteSecret, nil, nil); code != http.StatusBadRequest {
		t.Errorf("malformed session id = %d", code)
	}
	// Bounds and revocation.
	for _, bad := range []inviteRequest{{Role: "owner"}, {Role: "viewer", MaxUses: 11}, {Role: "viewer", TTLSeconds: 86401}} {
		if code, _, _ := h.do("POST", "/sessions/"+id+"/invites", sess.OwnerSecret, bad, nil); code != http.StatusBadRequest {
			t.Errorf("invite %+v = %d, want 400", bad, code)
		}
	}
	inv := h.invite(id, sess.OwnerSecret, inviteRequest{Role: "viewer", MaxUses: 5})
	if code, _, _ := h.do("DELETE", "/sessions/"+id+"/invites/"+inv.InviteID, sess.OwnerSecret, nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke = %d", code)
	}
	if code, _, _ := h.do("POST", "/sessions/"+id+"/join", inv.InviteSecret, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("join with a revoked invite = %d", code)
	}

	// The subprotocol is mandatory, and HELLO must be the first frame.
	conn, _, err := websocket.Dial(context.Background(), h.wsURL(id), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + sess.OwnerSecret}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(context.Background()); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Errorf("no subprotocol: close status %v, want 1008", websocket.CloseStatus(err))
	}
	conn, _, err = websocket.Dial(context.Background(), h.wsURL(id), &websocket.DialOptions{
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + sess.OwnerSecret}},
		Subprotocols: []string{"plan-forge.v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	frame, _ := Encode(WSEnvelope{Version: 1, SessionID: id, EventID: newUUID(), EventType: EventTextCheck, Timestamp: time.Now(), Payload: json.RawMessage(`{}`)})
	_ = conn.Write(context.Background(), websocket.MessageText, frame)
	if _, _, err := conn.Read(context.Background()); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Errorf("first frame not HELLO: close status %v, want 1008", websocket.CloseStatus(err))
	}
	// An unsupported protocol version closes with the version reason.
	conn, _, err = websocket.Dial(context.Background(), h.wsURL(id), &websocket.DialOptions{
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + sess.OwnerSecret}},
		Subprotocols: []string{"plan-forge.v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hello, _ := json.Marshal(HelloPayload{Proto: 2})
	frame, _ = Encode(WSEnvelope{Version: 1, SessionID: id, EventID: newUUID(), EventType: EventHello, Timestamp: time.Now(), Payload: hello})
	_ = conn.Write(context.Background(), websocket.MessageText, frame)
	_, _, err = conn.Read(context.Background())
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.StatusPolicyViolation || ce.Reason != "UNSUPPORTED_VERSION" {
		t.Errorf("proto 2 HELLO: %v", err)
	}

	// Removing a member closes their socket with 1008 and kills their credential.
	owner := h.dial("owner", id, sess.OwnerSecret, dialOpts{canExecute: true}, nil, nil)
	m := h.dial("member", id, member.MemberSecret, dialOpts{}, nil, nil)
	owner.send(EventMemberRemove, map[string]string{"member_id": member.MemberID}, 0)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if status, ok := m.closeStatus(); ok {
			if status != websocket.StatusPolicyViolation {
				t.Errorf("removed member close status = %d, want 1008", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the removed member's socket stayed open")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, resp, err := websocket.Dial(context.Background(), h.wsURL(id), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + member.MemberSecret}}, Subprotocols: []string{"plan-forge.v1"},
	}); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("removed member can still connect: %v", err)
	}
	// A viewer's role is carried in its snapshot and cannot edit.
	v := h.join(id, h.invite(id, sess.OwnerSecret, inviteRequest{Role: "viewer"}).InviteSecret)
	vc := h.dial("viewer", id, v.MemberSecret, dialOpts{}, nil, nil)
	before := vc.count()
	vc.send(EventTextDelta, TextDeltaPayload{DocumentID: "prd", Updates: []Update{{ClientID: vc.memberID + ":1", Changes: json.RawMessage(`[[0,"x"]]`)}}}, 0)
	e, _ := vc.mustWait(before, "ROLE_FORBIDDEN for a viewer push", isType(EventError))
	var ep ErrorPayload
	_ = json.Unmarshal(e.Payload, &ep)
	if ep.Code != contracts.CodeRoleForbidden {
		t.Errorf("viewer push: %s", e.Payload)
	}
}

func TestHubRateLimitsHTTPPerIP(t *testing.T) {
	h := newHarness(t, nil)
	limited := 0
	for range 80 {
		if code, _, _ := h.do("POST", "/sessions", "wrong", nil, nil); code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Error("80 rapid requests from one IP were never rate limited")
	}
}

func TestHubRetriedEventIDGetsOriginalAnswer(t *testing.T) {
	h := newHarness(t, nil)
	sess := h.createSession(nil)
	a := h.dial("A", sess.RelaySessionID, sess.OwnerSecret, dialOpts{canExecute: true}, nil, nil)

	// The same event_id with identical content replays the acknowledgement and
	// changes nothing; with different content it is EVENT_ID_CONFLICT.
	changes := json.RawMessage(`[[0,"hello"]]`)
	payload := TextDeltaPayload{DocumentID: "prd", BaseVersion: 0, Updates: []Update{{ClientID: "owner:1", Changes: changes}}}
	raw, _ := json.Marshal(payload)
	id := newUUID()
	sendRaw := func(raw []byte) {
		frame, _ := Encode(WSEnvelope{Version: 1, SessionID: sess.RelaySessionID, EventID: id, EventType: EventTextDelta, Timestamp: time.Now(), Payload: raw})
		if err := a.conn.Write(context.Background(), websocket.MessageText, frame); err != nil {
			t.Fatal(err)
		}
	}
	sendRaw(raw)
	a.mustWait(0, "first TEXT_ACK", func(e WSEnvelope) bool { return e.EventID == id && e.EventType == EventTextAck })
	sendRaw(raw)
	time.Sleep(200 * time.Millisecond)
	acks := 0
	for _, f := range a.all() {
		if f.env.EventID == id && f.env.EventType == EventTextAck {
			acks++
		}
	}
	if acks != 2 {
		t.Errorf("a retried push got %d acknowledgements, want the original replayed (2 total)", acks)
	}
	if snap := h.room(sess.RelaySessionID).doc.Snapshot(); snap.Text != "hello" || snap.Version != 1 {
		t.Errorf("a retried push changed the document: %q at %d", snap.Text, snap.Version)
	}
	changed, _ := json.Marshal(TextDeltaPayload{DocumentID: "prd", BaseVersion: 0, Updates: []Update{{ClientID: "owner:1", Changes: json.RawMessage(`[[0,"other"]]`)}}})
	before := a.count()
	sendRaw(changed)
	e, _ := a.mustWait(before, "EVENT_ID_CONFLICT", isType(EventError))
	var ep ErrorPayload
	_ = json.Unmarshal(e.Payload, &ep)
	if ep.Code != contracts.CodeEventIDConflict {
		t.Errorf("changed content under a reused event_id: %s", e.Payload)
	}
}

// --- units ------------------------------------------------------------------------------------------

func TestOutboxBoundAndReplayPriority(t *testing.T) {
	o := newOutbox()
	for i := range writeQueueDepth {
		if !o.pushLive([]byte{byte(i)}) {
			t.Fatalf("live frame %d refused below the bound", i)
		}
	}
	if o.pushLive([]byte("overflow")) {
		t.Fatal("the 257th live frame must be refused (the connection is closed with 1013)")
	}
	// Replay frames do not count against the live bound and drain first.
	o.pushReplay([]byte("replay-1"))
	o.pushReplay([]byte("replay-2"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, want := range []string{"replay-1", "replay-2"} {
		got, ok := o.next(ctx)
		if !ok || string(got) != want {
			t.Fatalf("next = %q, want %q", got, want)
		}
	}
	got, _ := o.next(ctx)
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("live frames must drain in order after replay, got %v", got)
	}
}

func TestStringifyCountersAndBundleID(t *testing.T) {
	out := payloadFor(map[string]any{
		"expected_revision": uint64(7), "lease_epoch": 2, "ttl_ms": 15000, "from": "A", "epoch": "9",
	})
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["expected_revision"] != "7" || m["lease_epoch"] != "2" || m["epoch"] != "9" {
		t.Errorf("counters are not decimal strings: %s", out)
	}
	if m["ttl_ms"] != float64(15000) || m["from"] != "A" {
		t.Errorf("non-counter fields were altered: %s", out)
	}
	raw := json.RawMessage(`{"expected_revision":7}`)
	if string(payloadFor(raw)) != string(raw) {
		t.Error("client-supplied raw payloads must pass through untouched")
	}

	spec, man := strings.Repeat("ab", 32), strings.Repeat("cd", 32)
	got, ok := bundleID(spec, man)
	want := sha256.Sum256(append(mustHexNoT(spec), mustHexNoT(man)...))
	if !ok || got != hex.EncodeToString(want[:]) {
		t.Errorf("bundleID = %s (%v)", got, ok)
	}
	if _, ok := bundleID("zz", man); ok {
		t.Error("bundleID accepted a non-hex digest")
	}
	if _, ok := bundleID(spec[:62], man); ok {
		t.Error("bundleID accepted a short digest")
	}
}

func mustHexNoT(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func TestComposeIngestAndLabels(t *testing.T) {
	got := composeIngest(false, IngestAppendPayload{Source: IngestSourceUpload, SourceName: "a/b\\c\n# x`y<z>.docx", Markdown: "line\r\nnext\rlast\n\n"})
	if got != "\n\n## Source: a/b c x y z .docx\n\nline\nnext\nlast\n" {
		t.Errorf("composeIngest = %q", got)
	}
	if strings.Contains(composeIngest(true, IngestAppendPayload{Source: "composer", Markdown: "x"}), "\r") {
		t.Error("CR leaked into the document")
	}
	if label := sanitizeLabel(strings.Repeat("é", 200)); len([]rune(label)) != maxLabelRunes {
		t.Errorf("label length %d", len([]rune(label)))
	}
}

func TestMirroredNamesAndPhasesAgree(t *testing.T) {
	pairs := [][2]string{
		{string(PhaseIngestion), string(session.PhaseIngestion)},
		{string(PhaseDeliberation), string(session.PhaseDeliberation)},
		{string(PhaseSynthesis), string(session.PhaseSynthesis)},
		{string(PhaseApproving), string(session.PhaseApproving)},
		{string(PhaseApprovingUnknown), string(session.PhaseApprovingUnknown)},
		{string(PhaseApproved), string(session.PhaseApproved)},
		{string(PhaseCancelled), string(session.PhaseCancelled)},
		{string(RoleOwner), string(session.RoleOwner)},
		{string(RoleEditor), string(session.RoleEditor)},
		{string(RoleViewer), string(session.RoleViewer)},
		{StagePM, session.StagePM},
		{StageArchitect, session.StageArchitect},
		{StageCritic, session.StageCritic},
		{StageSynthesizer, session.StageSynthesizer},
		{StageSpecialistPrefix, session.StageSpecialistPrefix},
		{IngestSourceUpload, session.IngestSourceUpload},
		{IngestSourceComposer, session.IngestSourceComposer},
	}
	for _, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("relay %q != session %q", p[0], p[1])
		}
	}
	if session.MaxIngestBytes != MaxIngestAppendBytes {
		t.Errorf("ingest cap: session %d, relay %d", session.MaxIngestBytes, MaxIngestAppendBytes)
	}
	if session.LeaseTTL != DefaultLeaseTTL || session.LeaseRenew != DefaultLeaseRenew {
		t.Error("lease timings differ between session and relay")
	}
	if contracts.DocumentIDPRD != "prd" {
		t.Error("document id changed")
	}
}

func TestClientIPHonorsOnlyTrustedProxies(t *testing.T) {
	mk := func(cfg HubConfig, remote, xff string) string {
		hub := NewHub(cfg)
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return hub.clientIP(r)
	}
	if got := mk(HubConfig{}, "203.0.113.9:5000", "198.51.100.1"); got != "203.0.113.9" {
		t.Errorf("XFF honored without --behind-proxy: %s", got)
	}
	if got := mk(HubConfig{BehindProxy: true}, "10.0.0.2:5000", "1.2.3.4, 198.51.100.7"); got != "198.51.100.7" {
		t.Errorf("rightmost XFF entry not used behind a private proxy: %s", got)
	}
	if got := mk(HubConfig{BehindProxy: true}, "203.0.113.9:5000", "198.51.100.7"); got != "203.0.113.9" {
		t.Errorf("XFF from an untrusted peer was honored: %s", got)
	}
	_, cidr, _ := net.ParseCIDR("203.0.113.0/24")
	if got := mk(HubConfig{BehindProxy: true, TrustedProxy: cidr}, "203.0.113.9:5000", "198.51.100.7"); got != "198.51.100.7" {
		t.Errorf("trusted CIDR proxy: %s", got)
	}
}
