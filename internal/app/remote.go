//go:build client && !server

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// Remote link timings.
const (
	docFetchTimeout   = 10 * time.Second
	leaseClaimTimeout = 25 * time.Second
	leaseClaimWait    = 3 * time.Second
	emitRetryWindow   = 5 * time.Second
	emitRetryEvery    = 50 * time.Millisecond
	// selfFenceAfter is how long a renewal may go unacknowledged: the plan has
	// the executor cancel its LLM context at TTL-3s.
	selfFenceAfter = relay.DefaultLeaseTTL - 3*time.Second
	rememberIDs    = 1024
)

// remoteOnlyUI are the events a window may additionally submit in a Remote
// session.
var remoteOnlyUI = map[relay.WSEventType]bool{
	relay.EventInviteRevoke: true, relay.EventMemberRemove: true,
}

// epochChecked lists executor-class events the relay fences by epoch. The
// window never holds the lease, so the binary stamps the epoch it observed on
// the commands it forwards.
var epochChecked = map[relay.WSEventType]bool{
	relay.EventSynthesizeRequest: true, relay.EventRetryStage: true, relay.EventApprove: true,
}

// remoteLinkConfig configures a remote link.
type remoteLinkConfig struct {
	RelaySessionID string
	Client         *relay.RelayClient
	Secret         string
	// Owner is true for the machine that holds the repository: it executes.
	Owner   bool
	Deliver func([]byte) error
	Guard   *relay.EgressGuard
	Logger  *slog.Logger
	Clock   func() time.Time
	// DocChanged schedules re-interpretation after an ingest append.
	DocChanged func()
	// OnFatal receives the error that ended the relay connection for good.
	OnFatal func(error)
}

type helloRequest struct {
	lastSeq    uint64
	docVersion uint64
}

// remoteLink connects the local window to a relay room. The relay hub is the
// authority; this process is one member of the room. It forwards the window's
// commands, mirrors the control stream so a reconnecting window can be brought
// up to date, and — for the owner — acts as the executor.
type remoteLink struct {
	cfg    remoteLinkConfig
	log    *slog.Logger
	conn   *relay.RelayConn
	events *pump

	mu        sync.Mutex
	mirror    *frameLog
	local     map[string][]byte
	live      bool
	memberID  string
	connState relay.ConnState
	connError string
	pending   map[string]chan relay.WSEnvelope
	uiEvents  map[string]bool
	uiOrder   []string
	hello     map[string]helloRequest
	emitted   map[string]bool
	emitOrder []string
	emitErr   error
	leaseCh   chan struct{}
	renewAck  time.Time
	granted   bool
}

var _ authority = (*remoteLink)(nil)

func newRemoteLink(cfg remoteLinkConfig) (*remoteLink, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	l := &remoteLink{
		cfg: cfg, log: cfg.Logger, events: newPump(), mirror: newFrameLog(),
		local: map[string][]byte{}, memberID: ownerMemberID, connState: relay.StateConnecting,
		pending: map[string]chan relay.WSEnvelope{}, uiEvents: map[string]bool{},
		hello: map[string]helloRequest{}, emitted: map[string]bool{}, leaseCh: make(chan struct{}, 1),
	}
	conn, err := relay.NewRelayConn(relay.RelayConnConfig{
		Client: cfg.Client, SessionID: cfg.RelaySessionID, Secret: cfg.Secret, CanExecute: cfg.Owner,
		Guard: cfg.Guard, Logger: cfg.Logger, Clock: cfg.Clock,
		DocVersion: func() uint64 { return 0 },
		OnEnvelope: l.onEnvelope,
		OnState:    l.onState,
	})
	if err != nil {
		return nil, err
	}
	l.conn = conn
	return l, nil
}

// Run keeps the relay connection alive until ctx ends or a failure that
// retrying cannot fix.
func (l *remoteLink) Run(ctx context.Context) {
	err := l.conn.Run(ctx)
	if err != nil && ctx.Err() == nil && l.cfg.OnFatal != nil {
		l.cfg.OnFatal(err)
	}
}

func (l *remoteLink) onState(s relay.ConnState, err error) {
	l.mu.Lock()
	l.connState = s
	l.connError = ""
	if err != nil {
		l.connError = notice(err)
	}
	l.mu.Unlock()
}

// RelayStatus is the body of GET /api/relay.
type RelayStatus struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

func (l *remoteLink) RelayStatus() RelayStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return RelayStatus{State: string(l.connState), Error: l.connError}
}

func (l *remoteLink) Events() *pump { return l.events }

func (l *remoteLink) close() { l.events.close() }

// noteRejected records an event the executor queue refused. The relay stream
// cannot be replayed from this side, so the window refreshes its snapshot; the
// log names the event so the gap is diagnosable rather than invisible.
func (l *remoteLink) noteRejected(kept bool, t relay.WSEventType) {
	if kept {
		return
	}
	l.log.Warn("the executor queue could not take an event; the window must resynchronise", "event", string(t))
}

// --- frames from the relay -----------------------------------------------------------------

func remember(set map[string]bool, order *[]string, id string) {
	if id == "" || set[id] {
		return
	}
	set[id] = true
	*order = append(*order, id)
	if len(*order) > rememberIDs {
		delete(set, (*order)[0])
		*order = (*order)[1:]
	}
}

func (l *remoteLink) sendLocked(raw []byte) {
	if raw != nil && l.cfg.Deliver != nil {
		_ = l.cfg.Deliver(raw)
	}
}

func (l *remoteLink) onEnvelope(env relay.WSEnvelope) {
	raw, _ := relay.Encode(env)
	l.mu.Lock()
	defer l.mu.Unlock()

	if env.Seq == 0 {
		if ch, ok := l.pending[env.EventID]; ok {
			delete(l.pending, env.EventID)
			ch <- env
			return
		}
	}
	switch env.EventType {
	case relay.EventStateSnapshot:
		var p relay.StateSnapshotPayload
		if json.Unmarshal(env.Payload, &p) == nil && p.MemberID != "" {
			l.memberID = p.MemberID
		}
		if req, ok := l.hello[env.EventID]; ok {
			delete(l.hello, env.EventID)
			go l.finishHello(req, raw)
			return
		}
	case relay.EventLeaseGranted:
		l.granted = true
		select {
		case l.leaseCh <- struct{}{}:
		default:
		}
	case relay.EventLeaseRenew:
		l.renewAck = l.cfg.Clock()
		return
	case relay.EventError, relay.EventFenced, relay.EventAlreadyResolved:
		if l.emitted[env.EventID] {
			l.refusedLocked(env)
			return
		}
	case relay.EventTextUpdates, relay.EventTextAck:
		// Any accepted change to the document, from an upload, a collaborator
		// or the owner's own typing, schedules a re-interpretation; the
		// executor debounces the burst.
		if l.cfg.DocChanged != nil {
			go l.cfg.DocChanged()
		}
	case relay.EventAvatarThought:
		var p struct {
			AvatarID string `json:"avatar_id"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		if env.Seq == 0 && raw != nil {
			l.local["thought:"+sanitizeLabel(p.AvatarID)] = raw
		}
	case relay.EventRunAssigned, relay.EventPageTransition:
		if env.EventType == relay.EventRunAssigned {
			for k := range l.local {
				if strings.HasPrefix(k, "thought:") {
					delete(l.local, k)
				}
			}
		}
	}
	if env.Seq > 0 {
		l.mirror.note(env, raw)
		l.noteRejected(l.events.push(env), env.EventType)
	} else if env.EventType == relay.EventPresence {
		l.noteRejected(l.events.push(env), env.EventType)
	}
	if l.live {
		l.sendLocked(raw)
	}
}

// refusedLocked records an authority refusal of an executor event. FENCED means
// the lease is gone; any other refusal aborts the stage that emitted it.
func (l *remoteLink) refusedLocked(env relay.WSEnvelope) {
	switch env.EventType {
	case relay.EventFenced:
		l.emitErr = errors.New("FENCED: the executor lease is no longer held")
		l.noteRejected(l.events.push(env), env.EventType)
	default:
		var p relay.ErrorPayload
		_ = json.Unmarshal(env.Payload, &p)
		l.emitErr = fmt.Errorf("%s: %s", p.Code, p.Message)
	}
}

// --- the window's side ---------------------------------------------------------------------

func (l *remoteLink) uiReply(replyTo string, t relay.WSEventType, payload any) {
	env := relay.WSEnvelope{
		Version: contracts.ProtocolVersion, SessionID: l.cfg.RelaySessionID, SenderID: authorityID,
		EventID: replyTo, EventType: t, Timestamp: l.cfg.Clock().UTC(), Payload: payloadFor(payload),
	}
	if env.EventID == "" {
		env.EventID = newUUID()
	}
	raw, err := relay.Encode(env)
	if err != nil {
		return
	}
	l.mu.Lock()
	l.sendLocked(raw)
	l.mu.Unlock()
}

func (l *remoteLink) uiError(replyTo string, code contracts.ErrorCode, msg string) {
	l.uiReply(replyTo, relay.EventError, relay.ErrorPayload{Code: code, Message: msg})
}

// HandleUI processes one text frame from the window.
func (l *remoteLink) HandleUI(raw []byte) {
	env, err := relay.DecodeEnvelope(raw)
	if err != nil {
		l.uiError("", contracts.CodeInvalidPayload, "the window sent an invalid frame")
		return
	}
	if !uiCommands[env.EventType] && !remoteOnlyUI[env.EventType] {
		l.uiError(env.EventID, contracts.CodeInvalidPayload, "event "+string(env.EventType)+" is not accepted from the window")
		return
	}
	switch env.EventType {
	case relay.EventHello:
		var hp relay.HelloPayload
		if json.Unmarshal(env.Payload, &hp) != nil || hp.Proto != contracts.ProtocolVersion {
			l.uiError(env.EventID, contracts.CodeUnsupportedVer, "protocol version is not supported")
			return
		}
		l.startHello(env.EventID, hp.LastSeq, hp.DocVersion)
	case relay.EventReplayRequest:
		var rp struct {
			LastSeq uint64 `json:"last_seq,string"`
		}
		if json.Unmarshal(env.Payload, &rp) != nil {
			l.uiError(env.EventID, contracts.CodeInvalidPayload, "REPLAY_REQUEST needs last_seq")
			return
		}
		l.startHello(env.EventID, rp.LastSeq, 0)
	default:
		l.forward(env)
	}
}

// startHello answers the window's HELLO or REPLAY_REQUEST. The relay already
// said HELLO for this member, so the window's is never forwarded: a fresh state
// snapshot is requested instead (its reply echoes the window's event_id), then
// the control frames the window missed, the window-only frames and the document
// follow from this process's mirror.
func (l *remoteLink) startHello(eventID string, lastSeq, docVersion uint64) {
	l.mu.Lock()
	l.hello[eventID] = helloRequest{lastSeq: lastSeq, docVersion: docVersion}
	l.mu.Unlock()
	err := l.conn.Send(relay.WSEnvelope{
		EventID: eventID, EventType: relay.EventReplayRequest,
		Payload: payloadFor(map[string]string{"last_seq": fmt.Sprintf("%d", l.conn.LastSeq())}),
	})
	if err != nil {
		l.mu.Lock()
		delete(l.hello, eventID)
		l.mu.Unlock()
		l.uiError(eventID, contracts.CodeInternal, "not connected to the relay: "+notice(err))
	}
}

func (l *remoteLink) finishHello(req helloRequest, snapshot []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), docFetchTimeout)
	defer cancel()
	text, version, docErr := l.fetchDoc(ctx)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.sendLocked(snapshot)
	for _, f := range l.mirror.missed(req.lastSeq) {
		l.sendLocked(f)
	}
	keys := make([]string, 0, len(l.local))
	for k := range l.local {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		l.sendLocked(l.local[k])
	}
	if docErr == nil && version != req.docVersion {
		sum := sha256.Sum256([]byte(text))
		env := relay.WSEnvelope{
			Version: contracts.ProtocolVersion, SessionID: l.cfg.RelaySessionID, SenderID: authorityID,
			EventID: newUUID(), EventType: relay.EventTextSnapshot, Timestamp: l.cfg.Clock().UTC(),
			Payload: payloadFor(relay.TextSnapshotPayload{
				DocumentID: contracts.DocumentIDPRD, Version: version, Text: text, SHA256: hex.EncodeToString(sum[:]),
			}),
		}
		if raw, err := relay.Encode(env); err == nil {
			l.sendLocked(raw)
		}
	}
	l.live = true
}

func (l *remoteLink) windowGone() {
	l.mu.Lock()
	l.live = false
	l.mu.Unlock()
}

// forward sends one window command to the relay, stamping the epoch for the
// executor-class commands and refusing payloads that carry a local path.
func (l *remoteLink) forward(env relay.WSEnvelope) {
	if epochChecked[env.EventType] {
		env.Epoch = l.conn.Epoch()
	}
	l.mu.Lock()
	remember(l.uiEvents, &l.uiOrder, env.EventID)
	l.mu.Unlock()
	switch err := l.conn.Send(env); {
	case err == nil:
	case errors.Is(err, relay.ErrLocalPath):
		l.uiError(env.EventID, contracts.CodeInvalidPayload, err.Error())
	case errors.Is(err, relay.ErrNotConnected):
		l.uiError(env.EventID, contracts.CodeInternal, "the relay is unreachable; edits are kept locally")
	default:
		l.uiError(env.EventID, contracts.CodeInternal, notice(err))
	}
}

// --- authority interface (executor side) -------------------------------------------------

func (l *remoteLink) request(ctx context.Context, env relay.WSEnvelope) (relay.WSEnvelope, error) {
	if env.EventID == "" {
		env.EventID = newUUID()
	}
	ch := make(chan relay.WSEnvelope, 1)
	l.mu.Lock()
	l.pending[env.EventID] = ch
	l.mu.Unlock()
	cleanup := func() {
		l.mu.Lock()
		delete(l.pending, env.EventID)
		l.mu.Unlock()
	}
	if err := l.conn.Send(env); err != nil {
		cleanup()
		return relay.WSEnvelope{}, err
	}
	timer := time.NewTimer(docFetchTimeout)
	defer timer.Stop()
	select {
	case reply := <-ch:
		return reply, nil
	case <-ctx.Done():
		cleanup()
		return relay.WSEnvelope{}, ctx.Err()
	case <-timer.C:
		cleanup()
		return relay.WSEnvelope{}, errors.New("the relay did not answer in time")
	}
}

// fetchDoc reads the shared document from the relay. A TEXT_CHECK that cannot
// match forces a TEXT_SNAPSHOT for this connection alone.
func (l *remoteLink) fetchDoc(ctx context.Context) (string, uint64, error) {
	reply, err := l.request(ctx, relay.WSEnvelope{
		EventType: relay.EventTextCheck,
		Payload: payloadFor(relay.TextCheckPayload{
			DocumentID: contracts.DocumentIDPRD, Version: 0, SHA256: strings.Repeat("0", 64),
		}),
	})
	if err != nil {
		return "", 0, err
	}
	if reply.EventType != relay.EventTextSnapshot {
		return "", 0, fmt.Errorf("the relay answered %s to a document request", reply.EventType)
	}
	var p relay.TextSnapshotPayload
	if err := json.Unmarshal(reply.Payload, &p); err != nil {
		return "", 0, err
	}
	return p.Text, p.Version, nil
}

func (l *remoteLink) Doc(ctx context.Context) (string, uint64, error) { return l.fetchDoc(ctx) }

func (l *remoteLink) Ingest(p relay.IngestAppendPayload) error {
	err := l.conn.Send(relay.WSEnvelope{EventType: relay.EventIngestAppend, Payload: payloadFor(p)})
	if err != nil {
		return fmt.Errorf("send to the relay: %w", err)
	}
	return nil
}

func (l *remoteLink) Local(t relay.WSEventType, payload any, sticky string) {
	env := relay.WSEnvelope{
		Version: contracts.ProtocolVersion, SessionID: l.cfg.RelaySessionID, SenderID: authorityID,
		EventID: newUUID(), EventType: t, Timestamp: l.cfg.Clock().UTC(), Payload: payloadFor(payload),
	}
	raw, err := relay.Encode(env)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if sticky != "" {
		l.local[sticky] = raw
	}
	if l.live {
		l.sendLocked(raw)
	}
}

// redactEgress applies the plan's egress rules to an executor event: local
// paths are redacted from public summaries and non-synthesis draft bodies before
// their hash is computed, and every other event is refused by the guard when it
// carries one.
func (l *remoteLink) redactEgress(t relay.WSEventType, raw json.RawMessage) json.RawMessage {
	g := l.cfg.Guard
	if g == nil {
		return raw
	}
	switch t {
	case relay.EventAvatarThought:
		var p relay.AvatarThoughtPayload
		if json.Unmarshal(raw, &p) == nil {
			p.PublicSummary = g.Redact(p.PublicSummary)
			if out, err := json.Marshal(p); err == nil {
				return out
			}
		}
	case relay.EventAvatarDraft:
		var p relay.AvatarDraftPayload
		if json.Unmarshal(raw, &p) != nil || p.SchemaID == schemaSynthesisV1 {
			return raw
		}
		red, changed, err := g.RedactJSON(p.Content)
		if err != nil || !changed {
			return raw
		}
		p.Content = red
		sum := sha256.Sum256(red)
		p.ContentHash = hex.EncodeToString(sum[:])
		if out, err := json.Marshal(p); err == nil {
			return out
		}
	}
	return raw
}

func (l *remoteLink) Emit(t relay.WSEventType, payload any) error {
	l.mu.Lock()
	if err := l.emitErr; err != nil {
		l.emitErr = nil
		l.mu.Unlock()
		return err
	}
	l.mu.Unlock()
	raw, err := marshalPayload(payload)
	if err != nil {
		return err
	}
	raw = l.redactEgress(t, raw)
	env := relay.WSEnvelope{EventID: newUUID(), EventType: t, Epoch: l.conn.Epoch(), Payload: raw}
	l.mu.Lock()
	remember(l.emitted, &l.emitOrder, env.EventID)
	l.mu.Unlock()
	deadline := time.Now().Add(emitRetryWindow)
	for {
		err := l.conn.Send(env)
		if err == nil {
			return nil
		}
		if !errors.Is(err, relay.ErrSendQueueFull) && !errors.Is(err, relay.ErrNotConnected) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(emitRetryEvery)
	}
}

// HoldLease claims the executor lease (waiting out a previous process's lease
// if need be) and keeps it renewed. A renewal that goes unacknowledged for
// TTL-3 s self-fences the executor; the loop then tries to claim again.
func (l *remoteLink) HoldLease(ctx context.Context, lost func(error)) error {
	if !l.cfg.Owner {
		return errors.New("only the session owner can hold the executor lease")
	}
	if err := l.claim(ctx); err != nil {
		return err
	}
	go func() {
		for ctx.Err() == nil {
			err := l.renewLoop(ctx)
			if ctx.Err() != nil {
				return
			}
			lost(err)
			if err := l.claim(ctx); err != nil {
				if ctx.Err() == nil {
					lost(err)
				}
				return
			}
		}
	}()
	return nil
}

func (l *remoteLink) claim(ctx context.Context) error {
	deadline := time.Now().Add(leaseClaimTimeout)
	for {
		l.mu.Lock()
		l.granted = false
		select {
		case <-l.leaseCh:
		default:
		}
		l.mu.Unlock()
		id := newUUID()
		l.mu.Lock()
		remember(l.emitted, &l.emitOrder, id)
		l.mu.Unlock()
		if err := l.conn.Send(relay.WSEnvelope{EventID: id, EventType: relay.EventLeaseClaim, Payload: json.RawMessage(`{}`)}); err == nil {
			select {
			case <-l.leaseCh:
				l.mu.Lock()
				l.renewAck = l.cfg.Clock()
				l.emitErr = nil
				l.mu.Unlock()
				return nil
			case <-time.After(leaseClaimWait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if time.Now().After(deadline) {
			return errors.New("could not take the executor lease from the relay")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (l *remoteLink) renewLoop(ctx context.Context) error {
	t := time.NewTicker(leaseRenewEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		_ = l.conn.Send(relay.WSEnvelope{
			EventType: relay.EventLeaseRenew, Epoch: l.conn.Epoch(),
			Payload: payloadFor(map[string]string{"epoch": fmt.Sprintf("%d", l.conn.Epoch())}),
		})
		l.mu.Lock()
		stale := l.cfg.Clock().Sub(l.renewAck) > selfFenceAfter
		l.mu.Unlock()
		if stale {
			return errors.New("lease renewal was not acknowledged in time")
		}
	}
}

// Epoch is the lease epoch the relay reported most recently.
func (l *remoteLink) Epoch() uint64 { return l.conn.Epoch() }

// Upload publishes the committed artifacts so followers can fetch and verify
// them: spec first, then the manifest, each bound to the current lease epoch.
func (l *remoteLink) Upload(ctx context.Context, approvalID string, spec, manifest []byte) error {
	for _, a := range []struct {
		name string
		body []byte
	}{{"synthesized_spec.md", spec}, {"plan_manifest.json", manifest}} {
		if err := l.cfg.Client.PutArtifact(ctx, l.cfg.RelaySessionID, l.cfg.Secret, approvalID, a.name, l.conn.Epoch(), a.body); err != nil {
			return err
		}
	}
	return nil
}

// SendSynced reports a follower's artifact persistence result to the room.
func (l *remoteLink) SendSynced(ok bool, code, relPath string) error {
	p := map[string]any{"ok": ok}
	if !ok {
		p["error"] = relay.ArtifactError{Code: code, RelPath: relPath}
	}
	return l.conn.Send(relay.WSEnvelope{EventType: relay.EventArtifactSynced, Payload: payloadFor(p)})
}

// Leave tells the room this executor's window is gone.
func (l *remoteLink) Leave() {
	_ = l.conn.Send(relay.WSEnvelope{EventType: relay.EventExecutorLeft, Epoch: l.conn.Epoch(), Payload: json.RawMessage(`{}`)})
}
