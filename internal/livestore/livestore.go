//go:build client && !server

// Package livestore records the harness sessions no approved plan governs
// (contract C13).
//
// internal/board is keyed by manifest digest and refuses to exist without one
// (PrepareBoard returns ErrNoApprovedPlan), so an agent working in a repository
// that was never planned leaves no trace. This store is keyed by repository
// instead, so a session is visible from the moment a harness starts, before any
// plan exists and whether or not one ever will.
//
// It is machine-local state, so it lives in the cache directory
// (config.EnsureCache()/live/<sha256(realpath(repo))>.json), never in the
// repository. Hook processes, the command center daemon and the CLI all write
// the same file, so each read-modify-write runs under a cross-process advisory
// lock and lands through an atomic same-directory rename. No update is lost.
//
// Schema v1:
//
//	{"version":1,"repo_key":"…","repo_name":"…","seq":N,
//	 "sessions":{"claude:abc":{"id":"claude:abc","harness":"claude-code","developer":"d",
//	   "branch":"main","mode":"UNPLANNED","last_tool":"Edit","last_command":"",
//	   "dirty_files":[],"started_at":"RFC3339","last_heartbeat":"RFC3339","seq":N}},
//	 "events":[{"seq":N,"at":"RFC3339","session_id":"claude:abc","kind":"tool","detail":"…"}]}
//
// The repository's absolute path is deliberately absent: repo_name and the key
// are what reach the UI, as the board already ensures. Every string is
// sanitized and rune-bounded on write, so a reader never has to trust the file.
//
// This package deliberately does not import internal/board for its consumer
// regex. Under cgo that package reaches planstate, tree-sitter and the host
// adapters through its coordinator, and nothing on the telemetry ingest path
// should carry that weight to validate an identifier.
package livestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
)

// Version is the schema version of the document on disk.
const Version = 1

// Mode is whether an approved plan governs the session.
type Mode string

// Session modes.
const (
	ModePlanned   Mode = "PLANNED"
	ModeUnplanned Mode = "UNPLANNED"
)

// Event kinds.
const (
	KindSessionStart = "session_start"
	KindTool         = "tool"
	KindCommand      = "command"
	KindUntracked    = "untracked"
	KindIdle         = "idle"
	KindSessionEnd   = "session_end"
)

// Bounds. They mirror internal/board's so one subsystem cannot grow a cache
// entry the other would have refused.
const (
	MaxSessions      = 64
	MaxEvents        = 500
	MaxDirtyFiles    = 50
	MaxFileBytes     = 1 << 20
	MaxPathRunes     = 256
	MaxCommandRunes  = 300
	MaxToolRunes     = 64
	MaxBranchRunes   = 120
	MaxNameRunes     = 64
	MaxDetailRunes   = 200
	MaxRepoNameRunes = 120
)

// Lifetimes. StaleAfter only changes how a session renders; EvictAfter and
// Retention remove data. Retention is a privacy property, not hygiene: command
// lines and file paths do not outlive it.
const (
	StaleAfter = 90 * time.Second
	EvictAfter = 24 * time.Hour
	Retention  = 7 * 24 * time.Hour
)

// ErrTooLarge is returned when the document exceeds MaxFileBytes with a single
// event left, i.e. when the sessions alone do not fit.
var ErrTooLarge = errors.New("livestore: document exceeds the size cap")

// now is a variable so a test can drive eviction without sleeping.
var now = func() time.Time { return time.Now().UTC() }

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// sessionIDRE matches the consumer identifiers the hooks already use, with agy
// accepted for the day its hook reports telemetry; it reports none today.
var sessionIDRE = regexp.MustCompile(`^(claude|omp|agy):[A-Za-z0-9._-]{1,128}$`)

// ValidSessionID reports whether id is a harness-qualified session identifier.
func ValidSessionID(id string) bool { return sessionIDRE.MatchString(id) }

// Session is one harness session seen in this repository.
type Session struct {
	ID          string   `json:"id"`
	Harness     string   `json:"harness"`
	Developer   string   `json:"developer"`
	Branch      string   `json:"branch"`
	Mode        Mode     `json:"mode"`
	TaskID      string   `json:"task_id,omitempty"`
	LastTool    string   `json:"last_tool,omitempty"`
	LastCommand string   `json:"last_command,omitempty"`
	DirtyFiles  []string `json:"dirty_files"`
	StartedAt   string   `json:"started_at"`
	LastBeat    string   `json:"last_heartbeat"`
	Seq         uint64   `json:"seq"`
}

// Event is one entry in the bounded activity log.
type Event struct {
	Seq       uint64 `json:"seq"`
	At        string `json:"at"`
	SessionID string `json:"session_id"`
	Kind      string `json:"kind"`
	Detail    string `json:"detail,omitempty"`
}

// Doc is the whole per-repository document.
//
// RepoPath is kept on disk because the daemon needs it to scan a repository
// for plan evidence, and the file is machine-local at 0600. It never reaches a
// browser: everything served to a tab goes through Public first.
type Doc struct {
	Version  int                `json:"version"`
	RepoKey  string             `json:"repo_key"`
	RepoName string             `json:"repo_name"`
	RepoPath string             `json:"repo_path,omitempty"`
	Seq      uint64             `json:"seq"`
	Sessions map[string]Session `json:"sessions"`
	Events   []Event            `json:"events"`
}

// Public is the document as a tab may see it: the absolute repository path is
// removed, leaving the key and the base name, which is what the board already
// does for the same reason.
func (d Doc) Public() Doc {
	d.RepoPath = ""
	return d
}

// Record is one telemetry report. SessionID, Harness and Kind are required;
// every other field updates the session only when non-empty, so a tool ping
// never erases the branch a session_start established.
type Record struct {
	SessionID  string
	Harness    string
	Developer  string
	Branch     string
	Mode       Mode
	TaskID     string
	Tool       string
	Command    string
	DirtyFiles []string
	Kind       string
	Detail     string
}

// Store is the document for one repository.
type Store struct {
	dir  string
	key  string
	name string
	repo string
	path string
}

// Key is the document key for a repository path: the digest of its resolved
// path, matching the board's convention so the same repository maps to the
// same name in both caches.
func Key(repoReal string) string {
	sum := sha256.Sum256([]byte(repoReal))
	return hex.EncodeToString(sum[:])
}

// Open returns the store for a repository under the machine's cache directory.
func Open(repo string) (*Store, error) {
	cache, err := config.EnsureCache()
	if err != nil {
		return nil, err
	}
	return OpenAt(filepath.Join(cache, "live"), repo)
}

// OpenAt returns the store for a repository under an explicit directory,
// creating it at 0700. repo should already be resolved; OpenAt resolves it
// again so two callers that disagree about symlinks still share one document.
func OpenAt(dir, repo string) (*Store, error) {
	if strings.TrimSpace(repo) == "" {
		return nil, errors.New("livestore: empty repository path")
	}
	if dir == "" {
		return nil, errors.New("livestore: empty store directory")
	}
	real, err := filepath.EvalSymlinks(repo)
	if err != nil {
		real = filepath.Clean(repo)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	key := Key(real)
	return &Store{
		dir:  dir,
		key:  key,
		name: clamp(filepath.Base(real), MaxRepoNameRunes),
		repo: real,
		path: filepath.Join(dir, key+".json"),
	}, nil
}

// Path is the document file.
func (s *Store) Path() string { return s.path }

// lockPath is the advisory lock guarding the document. It is a separate file
// so the rename that publishes an update never replaces the lock.
func (s *Store) lockPath() string { return filepath.Join(s.dir, s.key+".lock") }

// clamp sanitizes an untrusted string and bounds it to n runes.
func clamp(s string, n int) string {
	return sanitize.Truncate(sanitize.Terminal(strings.TrimSpace(s)), n)
}

func (s *Store) empty() Doc {
	d := Doc{Version: Version, RepoKey: s.key, RepoName: s.name, RepoPath: s.repo}
	d.normalize()
	return d
}

// normalize fills the nil maps and slices so callers and json.Marshal never
// see a null.
func (d *Doc) normalize() {
	if d.Sessions == nil {
		d.Sessions = map[string]Session{}
	}
	if d.Events == nil {
		d.Events = []Event{}
	}
	for id, sess := range d.Sessions {
		if sess.DirtyFiles == nil {
			sess.DirtyFiles = []string{}
			d.Sessions[id] = sess
		}
	}
}

// Load returns the current document. A missing file is an empty document;
// Load never creates the file.
func (s *Store) Load() (Doc, error) {
	unlock, err := s.lock()
	if err != nil {
		return Doc{}, err
	}
	defer unlock()
	return s.load()
}

// load reads the document. The caller holds the lock.
func (s *Store) load() (Doc, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s.empty(), nil
	}
	if err != nil {
		return Doc{}, err
	}
	var d Doc
	if err := json.Unmarshal(raw, &d); err != nil {
		return Doc{}, fmt.Errorf("livestore: parse %s: %w", s.path, err)
	}
	if d.Version != Version {
		return Doc{}, fmt.Errorf("livestore: %s has schema version %d, want %d", s.path, d.Version, Version)
	}
	if d.RepoKey != "" && d.RepoKey != s.key {
		return Doc{}, fmt.Errorf("livestore: %s belongs to repository %s", s.path, d.RepoKey)
	}
	d.RepoKey = s.key
	if s.name != "" {
		d.RepoName = s.name
	}
	d.normalize()
	return d, nil
}

// update runs fn against the on-disk document under the cross-process lock,
// then publishes the result atomically. A failed edit returns the pre-edit
// document re-read from disk, never the half-edited copy fn worked on.
func (s *Store) update(fn func(*Doc) (bool, error)) (Doc, error) {
	unlock, err := s.lock()
	if err != nil {
		return Doc{}, err
	}
	defer unlock()

	d, err := s.load()
	if err != nil {
		return Doc{}, err
	}
	pristine := func() Doc {
		if pre, err := s.load(); err == nil {
			return pre
		}
		return Doc{}
	}
	changed, err := fn(&d)
	if err != nil {
		return pristine(), err
	}
	if !changed {
		return d, nil
	}
	if err := s.writeTrim(&d); err != nil {
		return pristine(), err
	}
	return d, nil
}

// Record upserts the session the record names and appends one event. An
// invalid session id, harness or kind is refused rather than stored: this file
// feeds a UI, and a reader must be able to trust what it holds.
func (s *Store) Record(r Record) (Doc, error) {
	if !ValidSessionID(r.SessionID) {
		return Doc{}, fmt.Errorf("livestore: invalid session id %q", sanitize.Truncate(sanitize.Terminal(r.SessionID), 64))
	}
	if strings.TrimSpace(r.Harness) == "" {
		return Doc{}, errors.New("livestore: empty harness")
	}
	if !validKind(r.Kind) {
		return Doc{}, fmt.Errorf("livestore: unknown event kind %q", sanitize.Truncate(sanitize.Terminal(r.Kind), 32))
	}
	return s.update(func(d *Doc) (bool, error) {
		at := stamp(now())
		d.Seq++
		sess, ok := d.Sessions[r.SessionID]
		if !ok {
			sess = Session{ID: r.SessionID, StartedAt: at, Mode: ModeUnplanned}
		}
		sess.Harness = clamp(r.Harness, MaxNameRunes)
		sess.LastBeat = at
		sess.Seq = d.Seq
		if v := clamp(r.Developer, MaxNameRunes); v != "" {
			sess.Developer = v
		}
		if v := clamp(r.Branch, MaxBranchRunes); v != "" {
			sess.Branch = v
		}
		// Mode only ever moves up: a plan approved mid-session upgrades the
		// session in place, and a later ping with no manifest must not undo it.
		if r.Mode == ModePlanned {
			sess.Mode = ModePlanned
		} else if sess.Mode == "" {
			sess.Mode = ModeUnplanned
		}
		if v := clamp(r.TaskID, MaxNameRunes); v != "" {
			sess.TaskID = v
		}
		if v := clamp(r.Tool, MaxToolRunes); v != "" {
			sess.LastTool = v
		}
		if v := clamp(r.Command, MaxCommandRunes); v != "" {
			sess.LastCommand = v
		}
		if r.DirtyFiles != nil {
			sess.DirtyFiles = cleanPaths(r.DirtyFiles)
		}
		d.Sessions[r.SessionID] = sess
		d.Events = append(d.Events, Event{
			Seq:       d.Seq,
			At:        at,
			SessionID: r.SessionID,
			Kind:      r.Kind,
			Detail:    clamp(r.Detail, MaxDetailRunes),
		})
		d.prune(now())
		return true, nil
	})
}

// Sweep prunes stale sessions and expired events without recording anything.
// The daemon calls it on a timer so a repository nobody has touched for a week
// does not keep its command lines.
func (s *Store) Sweep() (Doc, error) {
	return s.update(func(d *Doc) (bool, error) {
		before := len(d.Sessions) + len(d.Events)
		d.prune(now())
		return len(d.Sessions)+len(d.Events) != before, nil
	})
}

func validKind(k string) bool {
	switch k {
	case KindSessionStart, KindTool, KindCommand, KindUntracked, KindIdle, KindSessionEnd:
		return true
	}
	return false
}

// cleanPaths sanitizes, bounds and de-duplicates a dirty-file list. An
// absolute path is dropped: these are repository-relative by contract, and an
// absolute one would put a machine path on the wire.
func cleanPaths(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		v := clamp(p, MaxPathRunes)
		if v == "" || filepath.IsAbs(v) || strings.HasPrefix(v, "/") || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
		if len(out) == MaxDirtyFiles {
			break
		}
	}
	sort.Strings(out)
	return out
}

// prune drops sessions and events that have outlived their bounds, then caps
// what remains. Eviction order is deterministic so two processes racing to
// write agree on the result: oldest heartbeat first, ties broken by the
// greater id, matching the board's cursor pruning.
func (d *Doc) prune(t time.Time) {
	for id, sess := range d.Sessions {
		if beat, err := time.Parse(time.RFC3339, sess.LastBeat); err == nil && t.Sub(beat) > EvictAfter {
			delete(d.Sessions, id)
		}
	}
	for len(d.Sessions) > MaxSessions {
		var victim string
		var victimBeat time.Time
		for id, sess := range d.Sessions {
			beat, err := time.Parse(time.RFC3339, sess.LastBeat)
			if err != nil {
				victim, victimBeat = id, time.Time{}
				break
			}
			if victim == "" || beat.Before(victimBeat) || (beat.Equal(victimBeat) && id > victim) {
				victim, victimBeat = id, beat
			}
		}
		delete(d.Sessions, victim)
	}

	kept := d.Events[:0]
	for _, ev := range d.Events {
		if at, err := time.Parse(time.RFC3339, ev.At); err == nil && t.Sub(at) > Retention {
			continue
		}
		kept = append(kept, ev)
	}
	d.Events = kept
	if len(d.Events) > MaxEvents {
		d.Events = append([]Event(nil), d.Events[len(d.Events)-MaxEvents:]...)
	}
}

// Stale reports whether the session has missed heartbeats long enough to
// render as idle. It never removes anything.
func (sess Session) Stale(t time.Time) bool {
	beat, err := time.Parse(time.RFC3339, sess.LastBeat)
	if err != nil {
		return true
	}
	return t.Sub(beat) > StaleAfter
}

// writeTrim publishes *d, dropping the oldest events until the document fits
// MaxFileBytes and always keeping at least one. A size overflow caused by old
// history must never block a session upsert. *d holds the document as written,
// so callers see the trimmed log.
func (s *Store) writeTrim(d *Doc) error {
	d.Version = Version
	d.RepoKey = s.key
	if s.name != "" {
		d.RepoName = s.name
	}
	if s.repo != "" {
		d.RepoPath = s.repo
	}
	d.normalize()
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	for len(data) > MaxFileBytes && len(d.Events) > 1 {
		over := len(data) - MaxFileBytes
		drop, freed := 0, 0
		for drop < len(d.Events)-1 && freed < over {
			b, err := json.Marshal(d.Events[drop])
			if err != nil {
				return err
			}
			freed += len(b) + 1
			drop++
		}
		d.Events = append([]Event(nil), d.Events[drop:]...)
		if data, err = json.Marshal(d); err != nil {
			return err
		}
	}
	if len(data) > MaxFileBytes {
		return ErrTooLarge
	}
	return s.writeFile(data)
}

// writeFile publishes data through a same-directory temp file and a rename, at
// mode 0600.
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

// MaxRepos bounds how many repository documents a single snapshot carries, so
// a machine with a long history of repositories cannot make one response
// unbounded.
const MaxRepos = 32

// LoadAll returns the documents under dir, most recently written first and at
// most MaxRepos of them. A document that fails to parse is skipped rather than
// failing the whole snapshot: one corrupt file must not blind the reader to
// every other repository.
func LoadAll(dir string) ([]Doc, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type aged struct {
		path string
		mod  time.Time
	}
	var files []aged
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, aged{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	if len(files) > MaxRepos {
		files = files[:MaxRepos]
	}
	out := make([]Doc, 0, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f.path)
		if err != nil {
			continue
		}
		var d Doc
		if json.Unmarshal(raw, &d) != nil || d.Version != Version {
			continue
		}
		d.normalize()
		out = append(out, d)
	}
	return out, nil
}

// Dir is the directory Open uses under the machine's cache.
func Dir() (string, error) {
	cache, err := config.EnsureCache()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "live"), nil
}
