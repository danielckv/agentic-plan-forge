//go:build client && !server

package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

const (
	// authorityID is the sender of events the authority itself produces.
	authorityID = "authority"
	// ownerMemberID is the Solo session's only member.
	ownerMemberID = "owner"
	// replayWindow is how many control frames a UI that reconnects can replay.
	replayWindow = 2000
)

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// NewSessionID returns a fresh local session identifier (UUIDv4).
func NewSessionID() string { return newUUID() }

// ValidSessionID reports whether s is a canonical lowercase UUIDv4. Session IDs
// become file names, so every entry point that takes one checks it first.
func ValidSessionID(s string) bool { return uuidV4Re.MatchString(s) }

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; continuing with a
		// predictable identifier would be worse than stopping.
		panic("app: crypto/rand unavailable: " + err.Error())
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// counterKeys are the payload fields the protocol carries as decimal strings.
var counterKeys = map[string]bool{
	"expected_revision": true, "approved_revision": true, "baseline_rev": true,
	"lease_epoch": true, "epoch": true, "current_epoch": true, "input_revision": true,
	"synthesis_rev": true, "draft_revision": true, "head_version": true, "from_version": true,
	"version": true, "last_seq": true, "doc_version": true, "head_seq": true,
	"head_doc_version": true, "revision": true,
}

// payloadFor renders an event payload as wire JSON. Authority events carry
// numeric counters in maps; the protocol carries counters as decimal strings, so
// known counter keys are quoted. Raw JSON passes through unchanged.
func payloadFor(p any) json.RawMessage {
	switch v := p.(type) {
	case nil:
		return json.RawMessage(`{}`)
	case json.RawMessage:
		if len(v) == 0 {
			return json.RawMessage(`{}`)
		}
		return v
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return stringifyCounters(raw)
}

func stringifyCounters(raw json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return raw
	}
	changed := false
	for k, v := range obj {
		if counterKeys[k] && len(v) > 0 && v[0] >= '0' && v[0] <= '9' {
			obj[k] = json.RawMessage(strconv.Quote(string(v)))
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}

// sha256Hex returns the lowercase hex SHA-256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// encodeFrame builds one wire frame.
func encodeFrame(sessionID, sender, eventID string, t relay.WSEventType, seq, revision, epoch uint64, ts time.Time, payload json.RawMessage) ([]byte, error) {
	if eventID == "" {
		eventID = newUUID()
	}
	return relay.Encode(relay.WSEnvelope{
		Version: contracts.ProtocolVersion, SessionID: sessionID, SenderID: sender, EventID: eventID,
		EventType: t, Seq: seq, Revision: revision, Epoch: epoch, Timestamp: ts.UTC(), Payload: payload,
	})
}

// ErrorFrame builds an ERROR frame for the local UI. It carries a coded
// message, never a local path.
func errorPayload(code contracts.ErrorCode, msg string) json.RawMessage {
	return payloadFor(relay.ErrorPayload{Code: code, Message: msg})
}

// --- replay log ------------------------------------------------------------------------

type logEntry struct {
	seq uint64
	raw []byte
}

// frameLog keeps what a reconnecting UI needs: the last control frames and the
// sticky state beyond them (the latest draft per avatar, decisions, run and
// approval markers). It mirrors the relay hub's replay behaviour so the UI sees
// the same stream in Solo and Remote mode.
type frameLog struct {
	ring      []logEntry
	sticky    map[string]logEntry
	draftHash map[string]string
}

func newFrameLog() *frameLog {
	return &frameLog{sticky: map[string]logEntry{}, draftHash: map[string]string{}}
}

// note records one control frame (seq > 0).
func (l *frameLog) note(env relay.WSEnvelope, raw []byte) {
	if env.Seq == 0 {
		return
	}
	l.ring = append(l.ring, logEntry{seq: env.Seq, raw: raw})
	if len(l.ring) > replayWindow {
		l.ring[0] = logEntry{}
		l.ring = l.ring[len(l.ring)-replayWindow:]
	}
	var p struct {
		AvatarID    string `json:"avatar_id"`
		SchemaID    string `json:"schema_id"`
		ContentHash string `json:"content_hash"`
		DecisionID  string `json:"decision_id"`
	}
	_ = json.Unmarshal(env.Payload, &p)
	entry := logEntry{seq: env.Seq, raw: raw}
	switch env.EventType {
	case relay.EventAvatarDraft:
		l.sticky["draft:"+p.AvatarID+":"+p.SchemaID] = entry
		l.draftHash[p.AvatarID] = p.ContentHash
	case relay.EventAvatarCritique:
		l.sticky["critique"] = entry
	case relay.EventDecisionPrompt:
		l.sticky["decision:"+p.DecisionID] = entry
	case relay.EventDecisionResolve:
		l.sticky["resolve:"+p.DecisionID] = entry
	case relay.EventRunAssigned:
		l.clearRun()
		l.sticky["type:"+string(env.EventType)] = entry
	case relay.EventCycleStart, relay.EventApprovalPrepared, relay.EventPlanApproved, relay.EventPageTransition,
		relay.EventLeaseGranted, relay.EventArtifactSynced:
		l.sticky["type:"+string(env.EventType)] = entry
	}
}

// clearRun forgets the previous run's drafts, critiques and decisions.
func (l *frameLog) clearRun() {
	for k := range l.sticky {
		if strings.HasPrefix(k, "draft:") || strings.HasPrefix(k, "decision:") ||
			strings.HasPrefix(k, "resolve:") || k == "critique" || k == "type:"+string(relay.EventCycleStart) {
			delete(l.sticky, k)
		}
	}
	l.draftHash = map[string]string{}
}

// missed returns the control frames a UI that last saw lastSeq has not seen: the
// exact tail when the ring still covers it, otherwise the sticky state.
func (l *frameLog) missed(lastSeq uint64) [][]byte {
	if len(l.ring) == 0 || lastSeq >= l.ring[len(l.ring)-1].seq {
		return nil
	}
	if lastSeq > 0 && l.ring[0].seq <= lastSeq+1 {
		var out [][]byte
		for _, e := range l.ring {
			if e.seq > lastSeq {
				out = append(out, e.raw)
			}
		}
		return out
	}
	entries := make([]logEntry, 0, len(l.sticky))
	for _, e := range l.sticky {
		if e.seq > lastSeq {
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].seq < entries[j].seq })
	out := make([][]byte, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.raw)
	}
	return out
}

func (l *frameLog) hashes() map[string]string {
	out := make(map[string]string, len(l.draftHash))
	for k, v := range l.draftHash {
		out[k] = v
	}
	return out
}
