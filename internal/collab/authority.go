// Package collab implements the central operational-transform authority for
// the single shared document. It follows the CodeMirror collab model: the
// server is the source of truth for versions, clients push uncomposed updates
// and rebase through rejected pushes.
//
// This package deliberately imports only internal/contracts (and stdlib).
// The relay hub lives in package relay and wraps this authority with network
// transport; if this package imported relay the two packages would form an
// import cycle under the server build tag. Wire shapes are mirrored here and
// the hub converts between them; tests assert the conversion is faithful.
package collab

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
)

// DocumentIDPRD is the v1 editable document. Unknown IDs are rejected by the
// transport before reaching the authority.
const DocumentIDPRD = "prd"

// Bounds mirrored from relay.MaxInsertBytes, relay.MaxDocumentBytes and
// relay.MaxIngestAppendBytes. They are restated here because this package
// cannot import relay; the hub tests assert the values agree.
const (
	maxInsertBytes    = 256 << 10
	maxDocumentBytes  = 512 << 10
	maxIngestAppendB  = 256 << 10
	logMaxEntries     = 10_000
	logMaxBytes       = 8 << 20
	rejectReasonCRLF  = "crlf"
	rejectReasonLarge = "too_large"
	rejectReasonBad   = "invalid"
	rejectReasonLock  = "locked"
	rejectReasonStale = "stale_base"
)

// Update is one uncomposed client update, mirroring the wire shape of
// relay.Update ({"client_id", "changes"}). Changes holds a CodeMirror
// ChangeSet.toJSON() array.
type Update struct {
	ClientID string          `json:"client_id"`
	Changes  json.RawMessage `json:"changes"`
}

// Reject tells the transport to answer a push with TEXT_REJECT. Updates
// carries the entries the pushing client missed so it can rebase; Resync
// demands a full snapshot instead.
type Reject struct {
	HeadVersion uint64   `json:"head_version"`
	Updates     []Update `json:"updates,omitempty"`
	Resync      bool     `json:"resync,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

// PushResult reports an accepted push.
type PushResult struct {
	NewVersion uint64
	// Applied echoes the batch in the order it was applied.
	Applied []Update
}

// Snapshot is the full document state for TEXT_SNAPSHOT and divergence checks.
type Snapshot struct {
	Version uint64
	Text    string
	SHA256  string
}

type logEntry struct {
	// Version is the document version produced by this entry. Each entry
	// holds exactly one update, so versions increase by one per entry.
	Version uint64
	Updates []Update
	Bytes   int64
}

// Authority owns one document: its UTF-16 text, its version and the bounded
// update log other members rebase against. All methods are safe for
// concurrent use.
type Authority struct {
	mu      sync.Mutex
	text    []uint16
	version uint64
	log     []logEntry
	logSize int64
	frozen  bool
}

// NewAuthority creates an authority holding initial (UTF-8) text at version 0.
func NewAuthority(initial string) *Authority {
	return &Authority{text: encodeUTF16(initial)}
}

// Push applies a batch of uncomposed updates from one member. The batch is
// validated as a whole on a scratch copy and applied all-or-nothing.
// Rejection rules, in check order:
//
//  1. the document is frozen (reason "locked");
//  2. any update's ClientID is not prefixed by "<memberID>:";
//  3. baseVersion is not the current version (stale, with missed updates);
//  4. a changes array is not a valid ChangeSet, its sections do not cover the
//     document exactly, an insert contains "\r", one insert exceeds
//     maxInsertBytes, or the resulting document exceeds maxDocumentBytes.
func (a *Authority) Push(memberID string, baseVersion uint64, updates []Update) (PushResult, *Reject) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pushLocked(memberID, baseVersion, updates, false)
}

func (a *Authority) pushLocked(memberID string, baseVersion uint64, updates []Update, system bool) (PushResult, *Reject) {
	if a.frozen {
		return PushResult{}, &Reject{HeadVersion: a.version, Resync: false, Reason: rejectReasonLock}
	}
	if !system {
		if memberID == "" {
			return PushResult{}, &Reject{HeadVersion: a.version, Reason: rejectReasonBad}
		}
		prefix := memberID + ":"
		for _, u := range updates {
			if !strings.HasPrefix(u.ClientID, prefix) {
				return PushResult{}, &Reject{HeadVersion: a.version, Reason: rejectReasonBad}
			}
		}
	}
	if len(updates) == 0 {
		return PushResult{}, &Reject{HeadVersion: a.version, Reason: rejectReasonBad}
	}
	if baseVersion != a.version {
		return PushResult{}, &Reject{
			HeadVersion: a.version,
			Updates:     a.updatesSinceLocked(baseVersion),
			Reason:      rejectReasonStale,
		}
	}

	scratch := make([]uint16, len(a.text))
	copy(scratch, a.text)
	for _, u := range updates {
		next, reject := applyChangeSet(scratch, u.Changes)
		if reject != nil {
			reject.HeadVersion = a.version
			return PushResult{}, reject
		}
		scratch = next
	}
	if utf8ByteLen(scratch) > maxDocumentBytes {
		return PushResult{}, &Reject{HeadVersion: a.version, Reason: rejectReasonLarge}
	}

	a.text = scratch
	a.appendLog(updates) // advances version by len(updates)
	return PushResult{NewVersion: a.version, Applied: updates}, nil
}

// Append adds system-generated text (INGEST_APPEND). It never replaces human
// text: the transport wraps it in a separator heading and a system client ID.
// The authority stays dumb about headings; it only enforces the bounds and the
// freeze.
func (a *Authority) Append(clientID string, text string) (PushResult, *Reject) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.frozen {
		return PushResult{}, &Reject{HeadVersion: a.version, Reason: rejectReasonLock}
	}
	if len(text) > maxIngestAppendB {
		return PushResult{}, &Reject{HeadVersion: a.version, Reason: rejectReasonLarge}
	}
	if strings.ContainsRune(text, '\r') {
		return PushResult{}, &Reject{HeadVersion: a.version, Reason: rejectReasonCRLF}
	}
	ins := encodeUTF16(text)
	if utf8ByteLen(append(append([]uint16{}, a.text...), ins...)) > maxDocumentBytes {
		return PushResult{}, &Reject{HeadVersion: a.version, Reason: rejectReasonLarge}
	}
	changes, err := insertAtJSON(len(a.text), text)
	if err != nil {
		return PushResult{}, &Reject{HeadVersion: a.version, Reason: rejectReasonBad}
	}
	upd := Update{ClientID: clientID, Changes: changes}
	a.text = append(a.text, ins...)
	a.appendLog([]Update{upd})
	return PushResult{NewVersion: a.version, Applied: []Update{upd}}, nil
}

// Freeze locks the document against further pushes (baseline confirmed).
func (a *Authority) Freeze() {
	a.mu.Lock()
	a.frozen = true
	a.mu.Unlock()
}

// Unfreeze reopens the document (EDIT_BASELINE rewinds to INGESTION).
func (a *Authority) Unfreeze() {
	a.mu.Lock()
	a.frozen = false
	a.mu.Unlock()
}

// Frozen reports whether pushes are locked.
func (a *Authority) Frozen() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.frozen
}

// Snapshot returns the whole document.
func (a *Authority) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Snapshot{Version: a.version, Text: decodeUTF16(a.text), SHA256: hashUTF16(a.text)}
}

// Check compares an idle client's (version, sha256) pair against the head.
func (a *Authority) Check(version uint64, sha string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return version == a.version && sha == hashUTF16(a.text)
}

// UpdatesSince returns the applied updates with version > from, for a client
// that must rebase. It returns nil when the log no longer covers from, which
// the transport turns into a resync demand.
func (a *Authority) UpdatesSince(from uint64) []Update {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.updatesSinceLocked(from)
}

func (a *Authority) updatesSinceLocked(from uint64) []Update {
	var out []Update
	covered := true
	for _, e := range a.log {
		if e.Version > from {
			out = append(out, e.Updates...)
		}
		if e.Version == from+1 {
			covered = true
		}
	}
	// The log is compacted from the front; if the entries no longer chain
	// back to from, the client cannot rebase and must resync.
	if !covered || (len(a.log) > 0 && from < a.log[0].Version-1) {
		return nil
	}
	return out
}

// CompactBelow drops log entries at or below minAck, the minimum acknowledged
// version among connected members. It keeps the log bounded (<logMaxEntries
// entries, <logMaxBytes) without losing updates any member still needs.
func (a *Authority) CompactBelow(minAck uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.compactBelowLocked(minAck)
}

// compactBelowLocked is CompactBelow for callers already holding a.mu.
func (a *Authority) compactBelowLocked(minAck uint64) {
	drop := 0
	for drop < len(a.log) && a.log[drop].Version <= minAck {
		a.logSize -= a.log[drop].Bytes
		drop++
	}
	if drop > 0 {
		a.log = append(a.log[:0], a.log[drop:]...)
	}
}

// appendLog advances the head version by one per update and records one log
// entry per update, so a client's base version always equals the number of
// updates it has seen (CodeMirror's receiveUpdates confirms own updates
// one-to-one). The caller must hold a.mu.
func (a *Authority) appendLog(updates []Update) {
	for _, u := range updates {
		a.version++
		bytes := int64(len(u.Changes))
		a.log = append(a.log, logEntry{Version: a.version, Updates: []Update{u}, Bytes: bytes})
		a.logSize += bytes
	}
	// Compact against the oldest surviving version; members still behind
	// that point resync instead of rebasing. a.mu is already held, so use
	// the locked variant.
	for len(a.log) > 1 && (len(a.log) > logMaxEntries || a.logSize > logMaxBytes) {
		a.compactBelowLocked(a.log[0].Version)
	}
}

// --- ChangeSet application -------------------------------------------------
//
// A ChangeSet.toJSON() array holds parts. A numeric part is a retained run.
// An array part is [deleteLen, insertLine1, insertLine2, ...]: deleteLen units
// are removed and the lines (joined by "\n") inserted. A one-element array is
// a pure deletion. The sections must cover the document exactly, matching
// ChangeSet.apply's length assertion.

// rejectBad, rejectCRLF and rejectLarge are the machine-readable reject
// reasons carried on TEXT_REJECT.
func rejectBad() *Reject  { return &Reject{Reason: rejectReasonBad} }
func rejectCRLF() *Reject { return &Reject{Reason: rejectReasonCRLF} }
func rejectLarge() *Reject {
	return &Reject{Reason: rejectReasonLarge}
}

// applyChangeSet applies raw changes to doc, returning the new document.
func applyChangeSet(doc []uint16, raw json.RawMessage) ([]uint16, *Reject) {
	var parts []json.RawMessage
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&parts); err != nil {
		return nil, rejectBad()
	}
	if dec.More() {
		return nil, rejectBad()
	}
	if len(parts) == 0 {
		return nil, rejectBad()
	}
	out := make([]uint16, 0, len(doc))
	posA := 0
	for pi, part := range parts {
		var num json.Number
		if err := json.Unmarshal(part, &num); err == nil {
			n, ok := int64Of(num)
			if !ok {
				return nil, rejectBad()
			}
			if n < 0 || posA+int(n) > len(doc) {
				return nil, rejectBad()
			}
			out = append(out, doc[posA:posA+int(n)]...)
			posA += int(n)
			continue
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(part, &arr); err != nil || len(arr) == 0 {
			return nil, rejectBad()
		}
		if err := json.Unmarshal(arr[0], &num); err != nil {
			return nil, rejectBad()
		}
		del, ok := int64Of(num)
		if !ok || del < 0 || posA+int(del) > len(doc) {
			return nil, rejectBad()
		}
		posA += int(del)
		if len(arr) == 1 {
			continue // pure deletion
		}
		lines := make([]string, 0, len(arr)-1)
		for _, lineRaw := range arr[1:] {
			var s string
			if err := json.Unmarshal(lineRaw, &s); err != nil {
				return nil, rejectBad()
			}
			lines = append(lines, s)
		}
		ins := strings.Join(lines, "\n")
		if strings.ContainsRune(ins, '\r') {
			return nil, rejectCRLF()
		}
		if len(ins) > maxInsertBytes {
			return nil, rejectLarge()
		}
		_ = pi // part index kept for diagnostics symmetry only
		out = append(out, encodeUTF16(ins)...)
	}
	if posA != len(doc) {
		return nil, rejectBad()
	}
	return out, nil
}

// int64Of strictly parses a section length: integer syntax only.
func int64Of(n json.Number) (int64, bool) {
	i, err := n.Int64()
	if err != nil {
		return 0, false
	}
	if n.String() != fmt.Sprintf("%d", i) {
		return 0, false // rejects "1e3", "1.0" and similar
	}
	return i, true
}

// AppendChange builds the ChangeSet array that appends text to a document of
// the given UTF-16 length. It is exported so a second client (the terminal)
// sends the identical encoding instead of reimplementing it.
func AppendChange(docLen int, text string) (json.RawMessage, error) {
	return insertAtJSON(docLen, text)
}

// insertAtJSON builds a ChangeSet.toJSON() array that inserts text at the end
// of a document of the given length. It is the single constructor for system
// appends, so every caller builds them the same way.
//
// The leading retain is required: a ChangeSet's sections must cover the whole
// document (ChangeSet.apply asserts that the length matches), so an append to
// a document of length N must first retain N units and then insert, giving
// [N, [0, line1, line2, ...]]. Only an empty document (N == 0) has nothing to
// retain, which yields [[0, line1, line2, ...]]. Empty text produces a pure
// retain [N], an identity change.
func insertAtJSON(docLen int, text string) (json.RawMessage, error) {
	if docLen < 0 {
		return nil, fmt.Errorf("collab: negative document length %d", docLen)
	}
	if text == "" {
		return json.Marshal([]any{docLen})
	}
	lines := strings.Split(text, "\n")
	part := make([]any, 0, len(lines)+1)
	part = append(part, any(0))
	for _, l := range lines {
		part = append(part, l)
	}
	if docLen == 0 {
		return json.Marshal([]any{part})
	}
	return json.Marshal([]any{docLen, part})
}

// --- UTF-16 helpers ---------------------------------------------------------
func encodeUTF16(s string) []uint16 {
	if s == "" {
		return []uint16{}
	}
	return utf16.Encode([]rune(s))
}

func decodeUTF16(u []uint16) string {
	if len(u) == 0 {
		return ""
	}
	return string(utf16.Decode(u))
}

func utf8ByteLen(u []uint16) int {
	n := 0
	for i := 0; i < len(u); {
		if utf16.IsSurrogate(rune(u[i])) && i+1 < len(u) && utf16.IsSurrogate(rune(u[i+1])) {
			n += 4
			i += 2
			continue
		}
		if sz := utf8.RuneLen(rune(u[i])); sz >= 0 {
			n += sz
		} else {
			n += 3 // unpaired surrogate becomes U+FFFD
		}
		i++
	}
	return n
}

// HashUTF8 returns the hex SHA-256 of the text's UTF-8 encoding.
func HashUTF8(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hashUTF16(u []uint16) string {
	return HashUTF8(decodeUTF16(u))
}
