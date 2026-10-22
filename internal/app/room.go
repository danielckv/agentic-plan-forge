//go:build client && !server

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/danielckv/agentic-plan-forge/internal/collab"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/session"
)

// maxLabelRunes bounds a source label spliced into a document heading.
const maxLabelRunes = 80

// uiCommands are the events the Plan-Forge tab may submit. Executor events
// (CYCLE_START, AVATAR_*, ARTIFACT_SAVED, lease messages) are produced only by
// this process, never by the page, so a compromised page cannot forge them.
var uiCommands = map[relay.WSEventType]bool{
	relay.EventHello: true, relay.EventReplayRequest: true, relay.EventTextDelta: true,
	relay.EventTextCheck: true, relay.EventIngestAppend: true, relay.EventPresence: true,
	relay.EventAmbiguityResolve: true, relay.EventFreezeRequest: true,
	relay.EventSynthesizeRequest: true, relay.EventRetryStage: true,
	relay.EventDecisionResolve: true, relay.EventApprove: true, relay.EventCancel: true,
	relay.EventEditBaseline: true,
}

// forwardAsIs lists events the reducer accepts without a broadcast of its own;
// the authority rebroadcasts the payload so every member (and the executor)
// observes them.
var forwardAsIs = map[relay.WSEventType]bool{
	relay.EventCycleStart: true, relay.EventRetryStage: true, relay.EventAmbiguityResolve: true,
}

// memberSent lists the events attributed to the acting member; everything else
// the reducer produces is attributed to the authority.
var memberSent = map[relay.WSEventType]bool{
	relay.EventAvatarDraft: true, relay.EventAvatarCritique: true, relay.EventDecisionPrompt: true,
	relay.EventDecisionResolve: true, relay.EventLeaseGranted: true, relay.EventCycleStart: true,
}

// pump is an unbounded, ordered hand-off from the authority to the executor.
// Authority events must never be dropped or block the authority, so it grows.
type pump struct {
	mu     sync.Mutex
	q      []relay.WSEnvelope
	wake   chan struct{}
	closed bool
}

func newPump() *pump { return &pump{wake: make(chan struct{}, 1)} }

func (p *pump) push(env relay.WSEnvelope) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.q = append(p.q, env)
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// next blocks for the next event, or reports false when ctx ends or the pump is
// closed and drained.
func (p *pump) next(ctx context.Context) (relay.WSEnvelope, bool) {
	for {
		p.mu.Lock()
		if len(p.q) > 0 {
			env := p.q[0]
			p.q[0] = relay.WSEnvelope{}
			p.q = p.q[1:]
			p.mu.Unlock()
			return env, true
		}
		closed := p.closed
		p.mu.Unlock()
		if closed {
			return relay.WSEnvelope{}, false
		}
		select {
		case <-ctx.Done():
			return relay.WSEnvelope{}, false
		case <-p.wake:
		}
	}
}

func (p *pump) close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// room is the Solo authority: the same session reducer and OT document
// authority the relay hub runs, wrapped around the single local window. Only
// transport differs from Remote mode.
type room struct {
	id      string
	clock   func() time.Time
	deliver func([]byte) error
	logger  *slog.Logger

	// docChanged schedules re-interpretation after an ingest append or an
	// ambiguity resolution.
	docChanged func()

	mu      sync.Mutex
	reducer *session.Reducer
	doc     *collab.Authority
	log     *frameLog
	seq     uint64
	live    bool
	local   map[string][]byte
	events  *pump
}

// newRoom builds the Solo authority. brief seeds the shared PRD document, so a
// brief agreed with the user before launch is what the ingestion view opens on
// and what interpret() normalizes; empty starts the document blank.
func newRoom(id string, clock func() time.Time, deliver func([]byte) error, logger *slog.Logger, brief string) *room {
	if clock == nil {
		clock = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	r := &room{
		id: id, clock: clock, deliver: deliver, logger: logger,
		reducer: session.New(clock), doc: collab.NewAuthority(brief),
		log: newFrameLog(), local: map[string][]byte{}, events: newPump(),
	}
	r.reducer.Join(ownerMemberID, session.RoleOwner)
	return r
}

// Events is the authority-ordered stream the executor consumes.
func (r *room) Events() *pump { return r.events }

func (r *room) close() { r.events.close() }

// --- framing --------------------------------------------------------------------------

func (r *room) frameLocked(t relay.WSEventType, sender, eventID string, seq uint64, payload json.RawMessage) (relay.WSEnvelope, []byte) {
	snap := r.reducer.Snapshot()
	env := relay.WSEnvelope{
		Version: contracts.ProtocolVersion, SessionID: r.id, SenderID: sender, EventID: eventID,
		EventType: t, Seq: seq, Revision: snap.Revision, Epoch: snap.LeaseEpoch,
		Timestamp: r.clock().UTC(), Payload: payload,
	}
	if env.EventID == "" {
		env.EventID = newUUID()
	}
	raw, err := relay.Encode(env)
	if err != nil {
		r.logger.Warn("dropping unencodable frame", "event", string(t), "err", err)
		return env, nil
	}
	return env, raw
}

// sendLocked writes one frame to the window. A frame that does not land means
// the window is no longer reachable, so the room stops pushing at it: the
// control frames are already in the replay log and the window collects them on
// its next HELLO. Dropping this error on the floor is what let a window that
// stopped draining die silently and end the session at exit 130.
func (r *room) sendLocked(raw []byte) {
	if raw == nil || r.deliver == nil {
		return
	}
	err := r.deliver(raw)
	if err == nil {
		return
	}
	r.live = false
	if errors.Is(err, relay.ErrUIBackpressure) {
		r.logger.Warn("window stopped draining its socket; frames held for replay", "bytes", len(raw))
	}
}

// broadcastLocked sequences one control event: it joins the replay log, reaches
// the window when it is live, and reaches the executor.
func (r *room) broadcastLocked(t relay.WSEventType, sender string, payload json.RawMessage) {
	seq := r.seq + 1
	env, raw := r.frameLocked(t, sender, "", seq, payload)
	if raw == nil {
		return
	}
	r.seq = seq
	r.log.note(env, raw)
	if r.live {
		r.sendLocked(raw)
	}
	r.events.push(env)
}

// unicastLocked answers the window directly with seq 0, echoing the command's
// event_id so the page can correlate the reply.
func (r *room) unicastLocked(replyTo string, t relay.WSEventType, payload any) {
	_, raw := r.frameLocked(t, authorityID, replyTo, 0, payloadFor(payload))
	r.sendLocked(raw)
}

// localLocked sends a frame that exists only between this process and its
// window. Frames with a sticky key are replayed when the window reconnects.
func (r *room) localLocked(t relay.WSEventType, payload any, sticky string) {
	_, raw := r.frameLocked(t, authorityID, "", 0, payloadFor(payload))
	if raw == nil {
		return
	}
	if sticky != "" {
		r.local[sticky] = raw
	}
	if r.live {
		r.sendLocked(raw)
	}
}

// Local publishes a window-only frame (normalized PRD, secret and approval
// notices, executor errors).
func (r *room) Local(t relay.WSEventType, payload any, sticky string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.localLocked(t, payload, sticky)
}

func (r *room) docFrameLocked(t relay.WSEventType, payload any) {
	if !r.live {
		return
	}
	_, raw := r.frameLocked(t, authorityID, "", 0, payloadFor(payload))
	r.sendLocked(raw)
}

func errorPayloadFrom(p any) relay.ErrorPayload {
	raw, _ := json.Marshal(p)
	var out relay.ErrorPayload
	_ = json.Unmarshal(raw, &out)
	if out.Code == "" {
		out.Code = contracts.CodeInternal
	}
	if out.Message == "" {
		out.Message = string(out.Code)
	}
	return out
}

// --- reducer effects ------------------------------------------------------------------------

func (r *room) rejectLocked(replyTo string, res session.Result) {
	sent := map[string]bool{}
	for _, ev := range append([]session.Event{res.Ack}, res.Broadcast...) {
		if (ev.Type == string(relay.EventFenced) || ev.Type == string(relay.EventAlreadyResolved)) && !sent[ev.Type] {
			r.unicastLocked(replyTo, relay.WSEventType(ev.Type), ev.Payload)
			sent[ev.Type] = true
		}
	}
	if len(sent) > 0 {
		return
	}
	if res.Rejected.Code == contracts.CodeFenced {
		r.unicastLocked(replyTo, relay.EventFenced, relay.FencedPayload{
			CurrentEpoch: res.Epoch, RunID: r.reducer.Snapshot().RunID,
		})
		return
	}
	r.unicastLocked(replyTo, relay.EventError, relay.ErrorPayload{Code: res.Rejected.Code, Message: res.Rejected.Msg})
}

func (r *room) emitLocked(ev session.Event, actor, replyTo string, replyUI bool) {
	t := relay.WSEventType(ev.Type)
	if ev.UnicastTo != "" || t == relay.EventFenced || t == relay.EventAlreadyResolved {
		if replyUI {
			r.unicastLocked(replyTo, t, ev.Payload)
		}
		return
	}
	payload := ev.Payload
	if t == relay.EventError {
		payload = errorPayloadFrom(payload)
	}
	wire := payloadFor(payload)
	if ev.Ephemeral {
		// Latest-value slot: delivered live with seq 0, replayed from the
		// sticky map, never part of the durable ring.
		var p struct {
			AvatarID string `json:"avatar_id"`
		}
		_ = json.Unmarshal(wire, &p)
		_, raw := r.frameLocked(t, actor, "", 0, wire)
		if raw != nil {
			r.local["thought:"+sanitizeLabel(p.AvatarID)] = raw
			if r.live {
				r.sendLocked(raw)
			}
		}
		return
	}
	sender := authorityID
	if memberSent[t] {
		sender = actor
	}
	r.broadcastLocked(t, sender, wire)
}

// applyLocked runs one command through the reducer and emits its effects.
// replyUI is true when the command came from the window, which then receives
// refusals and acknowledgements; executor commands report through the returned
// result instead.
func (r *room) applyLocked(actor, eventID string, t relay.WSEventType, payload json.RawMessage, replyUI bool) session.Result {
	res := r.reducer.Apply(session.Command{EventID: eventID, ActorID: actor, Type: string(t), Payload: payload})
	if res.Rejected != nil {
		if replyUI {
			r.rejectLocked(eventID, res)
		}
		return res
	}
	if res.Deduplicated {
		return res
	}
	switch t {
	case relay.EventFreezeRequest:
		r.doc.Freeze()
	case relay.EventEditBaseline:
		r.doc.Unfreeze()
		r.log.clearRun()
		for k := range r.local {
			if strings.HasPrefix(k, "thought:") {
				delete(r.local, k)
			}
		}
	}
	emitted := map[string]bool{}
	for _, ev := range res.Broadcast {
		r.emitLocked(ev, actor, eventID, replyUI)
		emitted[ev.Type] = true
	}
	if len(res.Broadcast) == 0 && forwardAsIs[t] {
		r.broadcastLocked(t, actor, payload)
	}
	if replyUI && res.Ack.Type != "" && !emitted[res.Ack.Type] {
		r.unicastLocked(eventID, relay.WSEventType(res.Ack.Type), res.Ack.Payload)
	}
	return res
}

// --- executor side --------------------------------------------------------------------------

// Emit submits one executor event as the owner member. A refusal (FENCED, a
// stale run, an illegal phase) is returned so the pipeline stops.
func (r *room) Emit(t relay.WSEventType, payload any) error {
	raw, err := marshalPayload(payload)
	if err != nil {
		return err
	}
	r.mu.Lock()
	res := r.applyLocked(ownerMemberID, newUUID(), t, raw, false)
	r.mu.Unlock()
	if res.Rejected != nil {
		return res.Rejected
	}
	return nil
}

func marshalPayload(payload any) (json.RawMessage, error) {
	switch v := payload.(type) {
	case nil:
		return json.RawMessage(`{}`), nil
	case json.RawMessage:
		return v, nil
	}
	return json.Marshal(payload)
}

// ClaimLease takes the executor lease for the owner member.
func (r *room) ClaimLease() error { return r.Emit(relay.EventLeaseClaim, nil) }

// RenewLease extends the executor lease.
func (r *room) RenewLease() error { return r.Emit(relay.EventLeaseRenew, nil) }

// ReleaseLease gives the lease up; the reducer refuses while an approval is in
// flight, which is the intended behaviour.
func (r *room) ReleaseLease() error { return r.Emit(relay.EventLeaseRelease, nil) }

// Snapshot returns the authoritative session state.
func (r *room) Snapshot() session.Snapshot { return r.reducer.Snapshot() }

// Doc returns the shared document.
func (r *room) Doc() (string, uint64) {
	s := r.doc.Snapshot()
	return s.Text, s.Version
}

func fromCollab(us []collab.Update) []relay.Update {
	out := make([]relay.Update, len(us))
	for i, u := range us {
		out[i] = relay.Update{ClientID: u.ClientID, Changes: u.Changes}
	}
	return out
}

func toCollab(us []relay.Update) []collab.Update {
	out := make([]collab.Update, len(us))
	for i, u := range us {
		out[i] = collab.Update{ClientID: u.ClientID, Changes: u.Changes}
	}
	return out
}

// Ingest appends parsed or composed Markdown to the shared document under a
// separator heading, exactly as INGEST_APPEND does when the window sends it.
func (r *room) Ingest(p relay.IngestAppendPayload) error {
	r.mu.Lock()
	err := r.ingestLocked(newUUID(), p, false)
	r.mu.Unlock()
	if err == nil && r.docChanged != nil {
		r.docChanged()
	}
	return err
}

// windowGone records that the window's socket closed. A window that reconnects
// says HELLO again and is brought up to date from the replay log, so nothing
// is delivered to it in between.
func (r *room) windowGone() {
	r.mu.Lock()
	r.live = false
	r.mu.Unlock()
}

// --- window side ---------------------------------------------------------------------------

// HandleUI processes one text frame from the window. It never blocks.
func (r *room) HandleUI(raw []byte) {
	env, err := relay.DecodeEnvelope(raw)
	if err != nil {
		r.mu.Lock()
		r.unicastLocked("", relay.EventError, relay.ErrorPayload{
			Code: contracts.CodeInvalidPayload, Message: "the window sent an invalid frame",
		})
		r.mu.Unlock()
		return
	}
	if !uiCommands[env.EventType] {
		r.mu.Lock()
		r.unicastLocked(env.EventID, relay.EventError, relay.ErrorPayload{
			Code: contracts.CodeInvalidPayload, Message: "event " + string(env.EventType) + " is not accepted from the window",
		})
		r.mu.Unlock()
		return
	}
	switch env.EventType {
	case relay.EventHello:
		r.onHello(env)
	case relay.EventReplayRequest:
		r.onReplay(env)
	case relay.EventTextDelta:
		r.onTextDelta(env)
	case relay.EventTextCheck:
		r.onTextCheck(env)
	case relay.EventIngestAppend:
		r.onIngest(env)
	case relay.EventPresence:
		// A Solo session has one member; there is no one to show presence to.
	default:
		r.mu.Lock()
		res := r.applyLocked(ownerMemberID, env.EventID, env.EventType, env.Payload, true)
		r.mu.Unlock()
		if env.EventType == relay.EventAmbiguityResolve && res.Rejected == nil && r.docChanged != nil {
			r.docChanged()
		}
	}
}

func (r *room) snapshotPayloadLocked() relay.StateSnapshotPayload {
	snap := r.reducer.Snapshot()
	pending := append([]string(nil), snap.PendingDecisions...)
	sort.Strings(pending)
	return relay.StateSnapshotPayload{
		MemberID: ownerMemberID, Role: relay.RoleOwner, HeadSeq: r.seq,
		HeadDocVersion: r.doc.Snapshot().Version, Phase: relay.Phase(snap.Phase),
		BaselineRev: snap.BaselineRev, BaselineHash: snap.BaselineHash, RunID: snap.RunID,
		InputRevision: snap.InputRevision, SynthesisRev: snap.SynthesisRev,
		LeaseEpoch: snap.LeaseEpoch, LeaseHolder: snap.LeaseHolder,
		PendingDecisions: pending, DraftHashes: r.log.hashes(),
	}
}

// resyncLocked brings the window up to date: a state snapshot, the control
// frames it missed, the window-only frames, and the document.
func (r *room) resyncLocked(replyTo string, lastSeq, docVersion uint64) {
	r.unicastLocked(replyTo, relay.EventStateSnapshot, r.snapshotPayloadLocked())
	for _, f := range r.log.missed(lastSeq) {
		r.sendLocked(f)
	}
	keys := make([]string, 0, len(r.local))
	for k := range r.local {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r.sendLocked(r.local[k])
	}
	snap := r.doc.Snapshot()
	switch {
	case docVersion == snap.Version:
	case docVersion > 0 && docVersion < snap.Version && len(r.doc.UpdatesSince(docVersion)) > 0:
		r.unicastLocked("", relay.EventTextUpdates, relay.TextUpdatesPayload{
			DocumentID: contracts.DocumentIDPRD, FromVersion: docVersion, Updates: fromCollab(r.doc.UpdatesSince(docVersion)),
		})
	default:
		r.unicastLocked("", relay.EventTextSnapshot, relay.TextSnapshotPayload{
			DocumentID: contracts.DocumentIDPRD, Version: snap.Version, Text: snap.Text, SHA256: snap.SHA256,
		})
	}
}

func (r *room) onHello(env relay.WSEnvelope) {
	var hp relay.HelloPayload
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := json.Unmarshal(env.Payload, &hp); err != nil {
		r.unicastLocked(env.EventID, relay.EventError, relay.ErrorPayload{Code: contracts.CodeInvalidPayload, Message: "invalid HELLO payload"})
		return
	}
	if hp.Proto != contracts.ProtocolVersion {
		r.unicastLocked(env.EventID, relay.EventError, relay.ErrorPayload{
			Code: contracts.CodeUnsupportedVer, Message: fmt.Sprintf("protocol version %d is not supported", hp.Proto),
		})
		return
	}
	r.resyncLocked(env.EventID, hp.LastSeq, hp.DocVersion)
	r.live = true
}

func (r *room) onReplay(env relay.WSEnvelope) {
	var rp struct {
		LastSeq uint64 `json:"last_seq,string"`
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := json.Unmarshal(env.Payload, &rp); err != nil {
		r.unicastLocked(env.EventID, relay.EventError, relay.ErrorPayload{Code: contracts.CodeInvalidPayload, Message: "REPLAY_REQUEST needs last_seq"})
		return
	}
	r.resyncLocked(env.EventID, rp.LastSeq, r.doc.Snapshot().Version)
}

const (
	maxTextDeltaPayload = 1 << 20
	maxTextDeltaUpdates = 256
)

func (r *room) onTextDelta(env relay.WSEnvelope) {
	// An accepted edit schedules a re-interpretation once the lock is
	// released: deferred calls run last-in first-out, so this one runs after
	// the unlock below.
	accepted := false
	defer func() {
		if accepted && r.docChanged != nil {
			r.docChanged()
		}
	}()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(env.Payload) > maxTextDeltaPayload {
		r.unicastLocked(env.EventID, relay.EventError, relay.ErrorPayload{Code: contracts.CodeInvalidPayload, Message: "TEXT_DELTA exceeds 1 MiB"})
		return
	}
	res := r.reducer.Apply(session.Command{EventID: env.EventID, ActorID: ownerMemberID, Type: string(env.EventType), Payload: env.Payload})
	if res.Rejected != nil {
		if res.Rejected.Code == contracts.CodeIllegalPhase {
			r.unicastLocked(env.EventID, relay.EventTextReject, relay.TextRejectPayload{
				DocumentID: contracts.DocumentIDPRD, HeadVersion: r.doc.Snapshot().Version, Reason: "locked",
			})
			return
		}
		r.rejectLocked(env.EventID, res)
		return
	}
	var p relay.TextDeltaPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.DocumentID != contracts.DocumentIDPRD ||
		len(p.Updates) == 0 || len(p.Updates) > maxTextDeltaUpdates {
		r.unicastLocked(env.EventID, relay.EventTextReject, relay.TextRejectPayload{
			DocumentID: p.DocumentID, HeadVersion: r.doc.Snapshot().Version, Reason: "invalid",
		})
		return
	}
	pushed, rej := r.doc.Push(ownerMemberID, p.BaseVersion, toCollab(p.Updates))
	if rej != nil {
		resync := rej.Resync || (rej.Reason == "stale_base" && len(rej.Updates) == 0)
		r.unicastLocked(env.EventID, relay.EventTextReject, relay.TextRejectPayload{
			DocumentID: p.DocumentID, HeadVersion: rej.HeadVersion, Updates: fromCollab(rej.Updates),
			Resync: resync, Reason: rej.Reason,
		})
		if resync {
			snap := r.doc.Snapshot()
			r.unicastLocked(env.EventID, relay.EventTextSnapshot, relay.TextSnapshotPayload{
				DocumentID: contracts.DocumentIDPRD, Version: snap.Version, Text: snap.Text, SHA256: snap.SHA256,
			})
		}
		return
	}
	r.unicastLocked(env.EventID, relay.EventTextAck, relay.TextAckPayload{DocumentID: p.DocumentID, Version: pushed.NewVersion})
	accepted = true
}

func (r *room) onTextCheck(env relay.WSEnvelope) {
	var p relay.TextCheckPayload
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.DocumentID != contracts.DocumentIDPRD {
		r.unicastLocked(env.EventID, relay.EventError, relay.ErrorPayload{
			Code: contracts.CodeInvalidPayload, Message: "TEXT_CHECK needs document_id prd, version and sha256",
		})
		return
	}
	if r.doc.Check(p.Version, p.SHA256) {
		return
	}
	snap := r.doc.Snapshot()
	r.unicastLocked(env.EventID, relay.EventTextSnapshot, relay.TextSnapshotPayload{
		DocumentID: contracts.DocumentIDPRD, Version: snap.Version, Text: snap.Text, SHA256: snap.SHA256,
	})
}

func (r *room) onIngest(env relay.WSEnvelope) {
	var p relay.IngestAppendPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		r.mu.Lock()
		r.unicastLocked(env.EventID, relay.EventError, relay.ErrorPayload{Code: contracts.CodeInvalidPayload, Message: "invalid INGEST_APPEND payload"})
		r.mu.Unlock()
		return
	}
	r.mu.Lock()
	err := r.ingestLocked(env.EventID, p, true)
	r.mu.Unlock()
	if err == nil && r.docChanged != nil {
		r.docChanged()
	}
}

var errIngestRefused = errors.New("ingest refused")

// ingestLocked validates one append, lets the reducer apply the role and phase
// rules and assign the ingest sequence, and appends the text to the document
// without replacing anything. replyUI sends refusals to the window.
func (r *room) ingestLocked(eventID string, p relay.IngestAppendPayload, replyUI bool) error {
	payload, _ := json.Marshal(p)
	res := r.reducer.Apply(session.Command{EventID: eventID, ActorID: ownerMemberID, Type: string(relay.EventIngestAppend), Payload: payload})
	if res.Rejected != nil {
		if replyUI {
			r.rejectLocked(eventID, res)
		}
		return res.Rejected
	}
	if res.Deduplicated {
		return nil
	}
	var verr error
	switch {
	case p.Source != relay.IngestSourceUpload && p.Source != relay.IngestSourceComposer:
		verr = errors.New("source must be upload or composer")
	case strings.TrimSpace(p.Markdown) == "":
		verr = errors.New("markdown is empty")
	case len(p.Markdown) > relay.MaxIngestAppendBytes:
		verr = errors.New("markdown exceeds 256 KiB")
	case !utf8.ValidString(p.Markdown):
		verr = errors.New("markdown is not valid UTF-8")
	}
	if verr != nil {
		if replyUI {
			r.unicastLocked(eventID, relay.EventError, relay.ErrorPayload{Code: contracts.CodeInvalidPayload, Message: verr.Error()})
		}
		return fmt.Errorf("%w: %v", errIngestRefused, verr)
	}
	text := composeIngest(r.doc.Snapshot().Text == "", p)
	pushed, rej := r.doc.Append(res.IngestClientID, text)
	if rej != nil {
		if replyUI {
			r.unicastLocked(eventID, relay.EventTextReject, relay.TextRejectPayload{
				DocumentID: contracts.DocumentIDPRD, HeadVersion: rej.HeadVersion, Reason: rej.Reason,
			})
		}
		return fmt.Errorf("%w: document refused the append (%s)", errIngestRefused, rej.Reason)
	}
	// The appender applies a system append as a remote update too: nobody has a
	// pending local update to confirm.
	r.docFrameLocked(relay.EventTextUpdates, relay.TextUpdatesPayload{
		DocumentID: contracts.DocumentIDPRD, FromVersion: pushed.NewVersion - uint64(len(pushed.Applied)),
		Updates: fromCollab(pushed.Applied),
	})
	return nil
}

// composeIngest renders an append as a separator heading plus body, normalizing
// line endings because the OT authority refuses "\r".
func composeIngest(docEmpty bool, p relay.IngestAppendPayload) string {
	body := strings.ReplaceAll(p.Markdown, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	body = strings.TrimRight(body, "\n")
	label := sanitizeLabel(p.SourceName)
	if label == "" {
		label = p.Source
	}
	var b strings.Builder
	if !docEmpty {
		b.WriteString("\n\n")
	}
	b.WriteString("## Source: ")
	b.WriteString(label)
	b.WriteString("\n\n")
	b.WriteString(body)
	b.WriteString("\n")
	return b.String()
}

// sanitizeLabel makes a user-supplied name safe to splice into a Markdown
// heading: control characters and Markdown/HTML punctuation are dropped,
// whitespace collapses and the result is capped.
func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == utf8.RuneError:
			b.WriteRune(' ')
		case strings.ContainsRune("#`<>[]|*_\\", r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(out); len(r) > maxLabelRunes {
		out = string(r[:maxLabelRunes])
	}
	return out
}
