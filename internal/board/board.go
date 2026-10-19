//go:build client && !server

// Package board implements the machine-local board overlay: per-task priority
// and context notes, an append-only event log and per-consumer cursors, kept as
// one JSON document per manifest (contract C3).
//
// The overlay is machine-local state, so it lives in the cache directory
// (config.EnsureCache()/board/<manifest_sha256>.json), never in the repository.
// The board window, the HTTP API and every `board pull` process write the same
// file, so each read-modify-write runs under a cross-process advisory lock and
// lands through an atomic same-directory rename. No update may be lost.
//
// Schema v2 (a v1 file loads and is upgraded in memory; the next write
// persists v2):
//
//	{"version":2,"manifest_sha256":"…","revision":N,
//	 "tasks":{"T-1":{"priority":"high","notes":[{"id":"n-1","at":"RFC3339","text":"…"}]}},
//	 "events":[{"seq":N,"at":"RFC3339","task_id":"T-1","kind":"priority","value":"high","by":"user"}],
//	 "cursors":{"claude:<sid>":{"seq":N,"seen_at":"RFC3339","last_next_up":"T-4"}},
//	 "activity":{"T-1":{"state":"active","by":"omp:x","source":"file_edit","detail":"…","at":"RFC3339"}},
//	 "discoveries":[{"id":"D-1","kind":"task","title":"…","paths":[],"parent_task_id":"T-1",
//	   "reason":"…","by":"omp:x","at":"RFC3339","status":"proposed","priority":"normal"}]}
//
// The default priority is normal and is not stored. revision equals the last
// event seq, and every edit increments it.
package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielckv/agentic-plan-forge/internal/config"
)

// Version is the schema version written to every overlay file.
const Version = 2

// versionV1 is the previous schema. A v1 file loads and upgrades in memory.
const versionV1 = 1

// Limits from contract C3.
const (
	// MaxEvents is the number of events retained; older events are dropped.
	MaxEvents = 500
	// MaxCursors is the number of consumers whose cursor is retained.
	MaxCursors = 64
	// MaxNotes is the number of notes a single task may hold.
	MaxNotes = 20
	// MaxNoteRunes is the greatest length of one note after trimming.
	MaxNoteRunes = 2000
	// MaxFileBytes is the largest overlay file the store will write.
	MaxFileBytes = 1 << 20
)

// Errors returned by the store. Callers map them onto HTTP 400/404/409.
var (
	// ErrManifestSHA reports a manifest digest that is not 64 lowercase hex
	// characters.
	ErrManifestSHA = errors.New("board: manifest sha256 must be 64 lowercase hex characters")
	// ErrTaskID reports a task id outside [A-Za-z0-9._-]{1,64}.
	ErrTaskID = errors.New("board: invalid task id")
	// ErrPriority reports a priority other than high, normal or low.
	ErrPriority = errors.New("board: invalid priority")
	// ErrNoteEmpty reports a note that is empty after trimming.
	ErrNoteEmpty = errors.New("board: empty note")
	// ErrNoteTooLong reports a note longer than MaxNoteRunes runes.
	ErrNoteTooLong = errors.New("board: note too long")
	// ErrNoteLimit reports a task that already holds MaxNotes notes.
	ErrNoteLimit = errors.New("board: note limit reached")
	// ErrNoteNotFound reports an unknown note id on a known task.
	ErrNoteNotFound = errors.New("board: unknown note")
	// ErrUnknownTask reports an edit to a task the overlay has never seen.
	ErrUnknownTask = errors.New("board: unknown task")
	// ErrConsumerID reports a consumer id outside ^(claude|omp):[A-Za-z0-9._-]{1,128}$.
	ErrConsumerID = errors.New("board: invalid consumer id")
	// ErrTooLarge reports an edit that would push the file over MaxFileBytes.
	ErrTooLarge = errors.New("board: overlay file would exceed 1 MiB")
)

// Priority is a task's board priority.
type Priority string

// The three priorities. PriorityNormal is the default and is not stored.
const (
	PriorityHigh   Priority = "high"
	PriorityNormal Priority = "normal"
	PriorityLow    Priority = "low"
)

// ParsePriority maps s onto a Priority. Only the three stored values are
// accepted.
func ParsePriority(s string) (Priority, bool) {
	switch Priority(s) {
	case PriorityHigh, PriorityNormal, PriorityLow:
		return Priority(s), true
	}
	return "", false
}

// EventKind is the kind of a board event.
type EventKind string

// Event kinds. KindContext carries the note text in Value; KindContextRemoved
// carries the removed note id.
const (
	KindPriority       EventKind = "priority"
	KindContext        EventKind = "context"
	KindContextRemoved EventKind = "context_removed"
)

// Note is one context note on a task.
type Note struct {
	ID   string `json:"id"`
	At   string `json:"at"`
	Text string `json:"text"`
}

// TaskOverlay is the machine-local state of one task. An empty Priority means
// normal, which is never stored.
type TaskOverlay struct {
	Priority Priority `json:"priority,omitempty"`
	Notes    []Note   `json:"notes,omitempty"`
}

// Event is one entry of the append-only board log. By is the consumer id or
// "user" that made the edit; it is empty for events written before schema v2
// and for priority and context edits made through the plain Store methods.
type Event struct {
	Seq    uint64    `json:"seq"`
	At     string    `json:"at"`
	TaskID string    `json:"task_id"`
	Kind   EventKind `json:"kind"`
	Value  string    `json:"value"`
	By     string    `json:"by,omitempty"`
}

// Cursor is how far one consumer has consumed the log. LastNextUp is the head
// of the coordinator's "Up next" queue as of the consumer's last delivery.
type Cursor struct {
	Seq        uint64 `json:"seq"`
	SeenAt     string `json:"seen_at"`
	LastNextUp string `json:"last_next_up,omitempty"`
}

// Overlay is the whole board document for one manifest. Activity maps a task
// id to its latest harness activity; Discoveries holds the discoveries oldest
// first. DiscoverySeq is the highest D-<n> ever issued, so ids never repeat
// after an eviction.
type Overlay struct {
	Version        int                    `json:"version"`
	ManifestSHA256 string                 `json:"manifest_sha256"`
	Revision       uint64                 `json:"revision"`
	Tasks          map[string]TaskOverlay `json:"tasks"`
	Events         []Event                `json:"events"`
	Cursors        map[string]Cursor      `json:"cursors"`
	Activity       map[string]Activity    `json:"activity"`
	Discoveries    []Discovery            `json:"discoveries"`
	DiscoverySeq   uint64                 `json:"discovery_seq,omitempty"`
}

// normalize replaces nil collections with empty ones so every document
// marshals the same shape.
func (o *Overlay) normalize() {
	if o.Tasks == nil {
		o.Tasks = map[string]TaskOverlay{}
	}
	if o.Events == nil {
		o.Events = []Event{}
	}
	if o.Cursors == nil {
		o.Cursors = map[string]Cursor{}
	}
	if o.Activity == nil {
		o.Activity = map[string]Activity{}
	}
	if o.Discoveries == nil {
		o.Discoveries = []Discovery{}
	}
	for i := range o.Discoveries {
		if o.Discoveries[i].Paths == nil {
			o.Discoveries[i].Paths = []string{}
		}
	}
}

// Priority returns the effective priority of taskID. Tasks without a stored
// priority are normal.
func (o Overlay) Priority(taskID string) Priority {
	if p := o.Tasks[taskID].Priority; p != "" {
		return p
	}
	return PriorityNormal
}

var (
	taskIDRE   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	consumerRE = regexp.MustCompile(`^(claude|omp):[A-Za-z0-9._-]{1,128}$`)
)

// ValidTaskID reports whether id is 1 to 64 characters of [A-Za-z0-9._-].
func ValidTaskID(id string) bool { return taskIDRE.MatchString(id) }

// ValidConsumer reports whether id matches ^(claude|omp):[A-Za-z0-9._-]{1,128}$.
func ValidConsumer(id string) bool { return consumerRE.MatchString(id) }

func validSHA(sha string) bool {
	if len(sha) != 64 {
		return false
	}
	for i := range len(sha) {
		c := sha[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Store is the board overlay of one manifest. It holds no cached state: every
// operation re-reads the file under the lock, so several Store values — in one
// process or in several — may write the same document concurrently.
type Store struct {
	dir  string
	sha  string
	path string
}

// Open opens the overlay of the manifest digest manifestSHA256 in
// config.EnsureCache()/board. The directory is created with mode 0700.
func Open(manifestSHA256 string) (*Store, error) {
	if !validSHA(manifestSHA256) {
		return nil, ErrManifestSHA
	}
	base, err := config.EnsureCache()
	if err != nil {
		return nil, err
	}
	return OpenAt(filepath.Join(base, "board"), manifestSHA256)
}

// OpenAt opens the overlay of sha inside dir, creating dir with mode 0700 when
// it is missing. It is the test seam for Open.
func OpenAt(dir, sha string) (*Store, error) {
	if !validSHA(sha) {
		return nil, ErrManifestSHA
	}
	if dir == "" {
		return nil, errors.New("board: empty overlay directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir, sha: sha, path: filepath.Join(dir, sha+".json")}, nil
}

// Path is the overlay file.
func (s *Store) Path() string { return s.path }

// lockPath is the advisory lock guarding the overlay file. It is a separate
// file so that the rename that publishes an update never replaces the lock.
func (s *Store) lockPath() string { return filepath.Join(s.dir, s.sha+".lock") }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func empty(sha string) Overlay {
	o := Overlay{Version: Version, ManifestSHA256: sha}
	o.normalize()
	return o
}

// Load returns the current overlay. A missing file is an empty overlay; Load
// never creates the file.
func (s *Store) Load() (Overlay, error) {
	unlock, err := s.lock()
	if err != nil {
		return Overlay{}, err
	}
	defer unlock()
	return s.load()
}

// load reads the overlay. The caller holds the lock.
func (s *Store) load() (Overlay, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return empty(s.sha), nil
	}
	if err != nil {
		return Overlay{}, err
	}
	var o Overlay
	if err := json.Unmarshal(raw, &o); err != nil {
		return Overlay{}, fmt.Errorf("board: parse %s: %w", s.path, err)
	}
	if o.Version != Version && o.Version != versionV1 {
		return Overlay{}, fmt.Errorf("board: %s has schema version %d, want %d or %d", s.path, o.Version, versionV1, Version)
	}
	if o.ManifestSHA256 != "" && o.ManifestSHA256 != s.sha {
		return Overlay{}, fmt.Errorf("board: %s belongs to manifest %s", s.path, o.ManifestSHA256)
	}
	o.ManifestSHA256 = s.sha
	o.Version = Version
	o.normalize()
	return o, nil
}

// update runs fn against the on-disk overlay while holding the cross-process
// lock, then publishes the result atomically. fn reports whether it changed
// anything; a no-op leaves the file and its revision untouched. When fn
// fails or the write is refused, the error is returned alongside the
// pre-edit overlay (re-read from disk, which the failed edit never touched),
// not the half-edited copy fn worked on.
func (s *Store) update(fn func(*Overlay) (bool, error)) (Overlay, error) {
	unlock, err := s.lock()
	if err != nil {
		return Overlay{}, err
	}
	defer unlock()

	o, err := s.load()
	if err != nil {
		return Overlay{}, err
	}
	pristine := func() Overlay {
		if pre, err := s.load(); err == nil {
			return pre
		}
		return Overlay{}
	}
	changed, err := fn(&o)
	if err != nil {
		return pristine(), err
	}
	if !changed {
		return o, nil
	}
	if err := s.writeTrim(&o); err != nil {
		return pristine(), err
	}
	return o, nil
}

// write publishes o with an atomic same-directory temp file and a rename. The
// file mode is 0600. See writeTrim for the size cap.
func (s *Store) write(o Overlay) error {
	return s.writeTrim(&o)
}

// writeTrim publishes *o. When the document would exceed MaxFileBytes it first
// drops the oldest events, always keeping at least one, until it fits: a size
// overflow caused by old history must never block a cursor write or a user
// edit. It reports ErrTooLarge only when the document is over the cap with a
// single event left, i.e. when the tasks and notes alone do not fit. *o holds
// the document as written, so callers see the trimmed log.
func (s *Store) writeTrim(o *Overlay) error {
	o.Version = Version
	o.ManifestSHA256 = s.sha
	o.normalize()
	data, err := json.Marshal(o)
	if err != nil {
		return err
	}
	for len(data) > MaxFileBytes && len(o.Events) > 1 {
		over := len(data) - MaxFileBytes
		drop, freed := 0, 0
		for drop < len(o.Events)-1 && freed < over {
			b, err := json.Marshal(o.Events[drop])
			if err != nil {
				return err
			}
			freed += len(b) + 1
			drop++
		}
		o.Events = append([]Event(nil), o.Events[drop:]...)
		if data, err = json.Marshal(o); err != nil {
			return err
		}
	}
	if len(data) > MaxFileBytes {
		return ErrTooLarge
	}
	return s.writeFile(data)
}

func (s *Store) writeFile(data []byte) error {
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// SetPriority stores p as the priority of taskID and appends a priority event.
// Setting the current value is a no-op: no event, no revision bump.
func (s *Store) SetPriority(taskID string, p Priority) (Overlay, error) {
	if !ValidTaskID(taskID) {
		return Overlay{}, ErrTaskID
	}
	if _, ok := ParsePriority(string(p)); !ok {
		return Overlay{}, ErrPriority
	}
	return s.update(func(o *Overlay) (bool, error) {
		if o.Priority(taskID) == p {
			return false, nil
		}
		t := o.Tasks[taskID]
		if p == PriorityNormal {
			t.Priority = ""
		} else {
			t.Priority = p
		}
		o.setTask(taskID, t)
		o.syncDiscoveryPriority(taskID, p)
		o.appendEvent(taskID, KindPriority, string(p))
		return true, nil
	})
}

// AddNote appends a context note to taskID and logs a context event. The text
// is trimmed; empty text and text longer than MaxNoteRunes runes are rejected,
// as is a task that already holds MaxNotes notes.
func (s *Store) AddNote(taskID, text string) (Overlay, error) {
	return s.AddNoteBy("", taskID, text)
}

// AddNoteBy is AddNote with the author recorded on the context event: by is a
// consumer id, "user" or empty. A note a consumer wrote is not echoed back to
// that consumer by RenderDeltaFor.
func (s *Store) AddNoteBy(by, taskID, text string) (Overlay, error) {
	if !validBy(by) {
		return Overlay{}, ErrConsumerID
	}
	if !ValidTaskID(taskID) {
		return Overlay{}, ErrTaskID
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Overlay{}, ErrNoteEmpty
	}
	if utf8.RuneCountInString(text) > MaxNoteRunes {
		return Overlay{}, ErrNoteTooLong
	}
	return s.update(func(o *Overlay) (bool, error) {
		t := o.Tasks[taskID]
		if len(t.Notes) >= MaxNotes {
			return false, ErrNoteLimit
		}
		t.Notes = append(t.Notes, Note{ID: o.nextNoteID(taskID), At: now(), Text: text})
		o.setTask(taskID, t)
		o.appendEventBy(taskID, KindContext, text, by)
		return true, nil
	})
}

// DeleteNote removes noteID from taskID and logs a context_removed event
// carrying the note id.
func (s *Store) DeleteNote(taskID, noteID string) (Overlay, error) {
	if !ValidTaskID(taskID) {
		return Overlay{}, ErrTaskID
	}
	return s.update(func(o *Overlay) (bool, error) {
		t, ok := o.Tasks[taskID]
		if !ok {
			return false, ErrUnknownTask
		}
		idx := -1
		for i := range t.Notes {
			if t.Notes[i].ID == noteID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return false, ErrNoteNotFound
		}
		t.Notes = append(t.Notes[:idx:idx], t.Notes[idx+1:]...)
		o.setTask(taskID, t)
		o.appendEvent(taskID, KindContextRemoved, noteID)
		return true, nil
	})
}

// Pull returns the events after consumer's cursor. Unless peek is set it also
// records the consumer at the current revision, so a later pull returns only
// newer events. An invalid consumer id is refused.
func (s *Store) Pull(consumer string, peek bool) ([]Event, Overlay, error) {
	if !ValidConsumer(consumer) {
		return nil, Overlay{}, ErrConsumerID
	}
	unlock, err := s.lock()
	if err != nil {
		return nil, Overlay{}, err
	}
	defer unlock()

	o, err := s.load()
	if err != nil {
		return nil, Overlay{}, err
	}
	after := o.Cursors[consumer].Seq
	out := []Event{}
	for _, ev := range o.Events {
		if ev.Seq > after {
			out = append(out, ev)
		}
	}
	if peek {
		return out, o, nil
	}
	if c, ok := o.Cursors[consumer]; ok && c.Seq >= o.Revision && c.fresh() {
		return out, o, nil // already at the revision and seen recently: no write
	}
	cur := o.Cursors[consumer]
	cur.Seq, cur.SeenAt = o.Revision, now()
	o.Cursors = withCursor(o.Cursors, consumer, cur)
	if err := s.writeTrim(&o); err != nil {
		return out, o, err
	}
	return out, o, nil
}

// cursorFreshFor is how long a cursor's seen_at counts as current. A pull or
// ack that would change nothing but seen_at within this window skips the write.
const cursorFreshFor = 60 * time.Second

// fresh reports whether the cursor was seen less than cursorFreshFor ago.
func (c Cursor) fresh() bool {
	t, err := time.Parse(time.RFC3339, c.SeenAt)
	if err != nil {
		return false
	}
	d := time.Since(t)
	return d >= 0 && d < cursorFreshFor
}

// withCursor returns a pruned copy of in with consumer set to c.
func withCursor(in map[string]Cursor, consumer string, c Cursor) map[string]Cursor {
	cursors := make(map[string]Cursor, len(in)+1)
	for id, old := range in {
		cursors[id] = old
	}
	cursors[consumer] = c
	return pruneCursors(cursors)
}

// Ack records that consumer has consumed every event up to seq: its cursor
// becomes max(existing, seq), clamped to the current revision, with seen_at set
// to now. It writes nothing when the cursor is already at or past seq and was
// seen less than a minute ago. Ack is the second half of at-least-once
// delivery: a consumer peeks, delivers, then acks. It keeps the cursor's
// LastNextUp.
func (s *Store) Ack(consumer string, seq uint64) error {
	return s.ack(consumer, seq, nil)
}

// AckNextUp is Ack that also records nextUp, the head of the "Up next" queue
// that was delivered, as the cursor's LastNextUp. An empty nextUp clears it, so
// the next non-empty head counts as a change. A non-empty nextUp must be a
// valid task id.
func (s *Store) AckNextUp(consumer string, seq uint64, nextUp string) error {
	if nextUp != "" && !ValidTaskID(nextUp) {
		return ErrTaskID
	}
	return s.ack(consumer, seq, &nextUp)
}

func (s *Store) ack(consumer string, seq uint64, nextUp *string) error {
	if !ValidConsumer(consumer) {
		return ErrConsumerID
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()

	o, err := s.load()
	if err != nil {
		return err
	}
	c, ok := o.Cursors[consumer]
	sameHead := nextUp == nil || c.LastNextUp == *nextUp
	if ok && c.Seq >= seq && c.fresh() && sameHead {
		return nil
	}
	if seq > o.Revision {
		seq = o.Revision
	}
	c.Seq = max(c.Seq, seq)
	c.SeenAt = now()
	if nextUp != nil {
		c.LastNextUp = *nextUp
	}
	o.Cursors = withCursor(o.Cursors, consumer, c)
	return s.writeTrim(&o)
}

// AckAdvance is Ack as a claim: it compares the cursor with seq and advances
// it under one lock hold, and reports true only when the cursor actually moved
// forward to seq (seq clamped to the current revision). It reports false, and
// writes nothing, when the cursor was already at or past seq: another
// consumer process acked those events first, so exactly one of several
// concurrent callers sees true. Callers that deliver on true get each event
// once; callers that ack after delivering with Ack get at-least-once.
func (s *Store) AckAdvance(consumer string, seq uint64) (bool, error) {
	return s.ackAdvance(context.Background(), consumer, seq, nil)
}

// AckAdvanceNextUp is AckAdvance that also records nextUp as the cursor's
// LastNextUp, like AckNextUp. The cursor counts as moved when its sequence
// advanced or its LastNextUp changed, so a coordinator-only delta (no new
// event, a new head) is claimed by one caller as well.
func (s *Store) AckAdvanceNextUp(consumer string, seq uint64, nextUp string) (bool, error) {
	return s.AckAdvanceNextUpCtx(context.Background(), consumer, seq, nextUp)
}

// AckAdvanceNextUpCtx is AckAdvanceNextUp for callers on a time budget. The
// lock wait itself is not interruptible, so ctx is checked once the lock is
// held: a call whose ctx ended while it waited returns (false, ctx.Err())
// and writes nothing. A caller that gave up on the call therefore never
// finds the cursor advanced behind its back.
func (s *Store) AckAdvanceNextUpCtx(ctx context.Context, consumer string, seq uint64, nextUp string) (bool, error) {
	if nextUp != "" && !ValidTaskID(nextUp) {
		return false, ErrTaskID
	}
	return s.ackAdvance(ctx, consumer, seq, &nextUp)
}

func (s *Store) ackAdvance(ctx context.Context, consumer string, seq uint64, nextUp *string) (bool, error) {
	if !ValidConsumer(consumer) {
		return false, ErrConsumerID
	}
	unlock, err := s.lock()
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}

	o, err := s.load()
	if err != nil {
		return false, err
	}
	if seq > o.Revision {
		seq = o.Revision
	}
	c := o.Cursors[consumer]
	moved := c.Seq < seq || (nextUp != nil && c.LastNextUp != *nextUp)
	if !moved {
		return false, nil
	}
	c.Seq = max(c.Seq, seq)
	c.SeenAt = now()
	if nextUp != nil {
		c.LastNextUp = *nextUp
	}
	o.Cursors = withCursor(o.Cursors, consumer, c)
	if err := s.writeTrim(&o); err != nil {
		return false, err
	}
	return true, nil
}

// setTask stores t, dropping the entry entirely when it holds nothing.
func (o *Overlay) setTask(taskID string, t TaskOverlay) {
	if t.Priority == "" && len(t.Notes) == 0 {
		delete(o.Tasks, taskID)
		return
	}
	o.Tasks[taskID] = t
}

// appendEvent bumps the revision and appends one event with no author,
// keeping the newest MaxEvents.
func (o *Overlay) appendEvent(taskID string, kind EventKind, value string) {
	o.appendEventBy(taskID, kind, value, "")
}

// appendEventBy is appendEvent with the author set to by.
func (o *Overlay) appendEventBy(taskID string, kind EventKind, value, by string) {
	o.Revision++
	o.Events = append(o.Events, Event{
		Seq:    o.Revision,
		At:     now(),
		TaskID: taskID,
		Kind:   kind,
		Value:  value,
		By:     by,
	})
	if len(o.Events) > MaxEvents {
		o.Events = append([]Event(nil), o.Events[len(o.Events)-MaxEvents:]...)
	}
}

// nextNoteID returns the next n-<seq> for taskID. The highest id already used
// by a live note or by a retained context_removed event wins, so an id is not
// reused while it is still visible in the log.
func (o *Overlay) nextNoteID(taskID string) string {
	max := 0
	for _, n := range o.Tasks[taskID].Notes {
		if v, ok := noteSeq(n.ID); ok && v > max {
			max = v
		}
	}
	for _, ev := range o.Events {
		if ev.TaskID != taskID || ev.Kind != KindContextRemoved {
			continue
		}
		if v, ok := noteSeq(ev.Value); ok && v > max {
			max = v
		}
	}
	return "n-" + strconv.Itoa(max+1)
}

func noteSeq(id string) (int, bool) {
	if !strings.HasPrefix(id, "n-") {
		return 0, false
	}
	v, err := strconv.Atoi(id[2:])
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// pruneCursors keeps the MaxCursors most recently seen consumers. Ties break by
// the greater id, so the result is deterministic.
func pruneCursors(in map[string]Cursor) map[string]Cursor {
	if len(in) <= MaxCursors {
		return in
	}
	ids := make([]string, 0, len(in))
	for id := range in {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := in[ids[i]], in[ids[j]]
		if a.SeenAt != b.SeenAt {
			return a.SeenAt > b.SeenAt
		}
		return ids[i] > ids[j]
	})
	out := make(map[string]Cursor, MaxCursors)
	for _, id := range ids[:MaxCursors] {
		out[id] = in[id]
	}
	return out
}
