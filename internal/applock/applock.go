//go:build client && !server

// Package applock is the single-instance pid lock shared by long-lived local
// processes (contract C14).
//
// The algorithm is the one the board window has used since v0.1.2: publish the
// pid file with a hard link from a same-directory temp file, so a reader never
// sees a partial file and exactly one contender wins; treat a holder whose
// process is gone as stale; and serialise every takeover behind a separate
// <lock>.guard advisory lock that re-reads the pid file first, so two
// contenders never both replace one dead holder and a holder that arrived in
// between is never removed.
//
// The lock file is machine-local state and lives in the cache directory at
// 0600. Holding it is not a guarantee a port is free: for a networked daemon
// the bind is the real mutex and this lock is what makes the *takeover*
// orderly.
package applock

import (
	"encoding/json"
	"errors"
	"os"
	"time"
)

// Lock states.
const (
	StateStarting = "starting"
	StateRunning  = "running"
)

// Record is the pid file's content. Port and Key are optional: a daemon
// records the port it bound so a probe can find it without guessing, and a
// per-resource lock records which resource it covers.
type Record struct {
	PID       int       `json:"pid"`
	State     string    `json:"state"`
	Port      int       `json:"port,omitempty"`
	Key       string    `json:"key,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// maxTakeovers bounds the retry loop. Each pass either wins, yields to a live
// holder, or removes one stale holder, so a small constant is enough and a
// pathological loop is reported rather than spun on.
const maxTakeovers = 4

// afterStaleCheck runs between deciding a lock is stale and taking the guard
// to remove it; tests only.
var afterStaleCheck func()

// Read returns the record at path. A missing, unparseable or non-positive-pid
// file reports false.
func Read(path string) (Record, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Record{}, false
	}
	var r Record
	if json.Unmarshal(raw, &r) != nil || r.PID <= 0 {
		return Record{}, false
	}
	return r, true
}

// Write replaces the record at path atomically, stamping UpdatedAt. It is how
// a holder moves itself from starting to running, or records its port.
func Write(path string, r Record) error {
	r.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return atomicWrite(path, append(raw, '\n'))
}

// Holder returns the record at path when its process is still alive. It is the
// read-only question "is one already running?".
func Holder(path string) (Record, bool) {
	if r, ok := Read(path); ok && PIDAlive(r.PID) {
		return r, true
	}
	return Record{}, false
}

// Acquire takes the lock at path for this process. It reports the winning
// record and whether this process won: a live holder other than this process
// yields (holder, false, nil), while a dead or unreadable holder is replaced
// under the guard. r supplies Port and Key; PID, State and UpdatedAt are set
// here so a caller cannot publish a record naming another process.
func Acquire(path string, r Record) (Record, bool, error) {
	mine := Record{
		PID:       os.Getpid(),
		State:     StateStarting,
		Port:      r.Port,
		Key:       r.Key,
		UpdatedAt: time.Now().UTC(),
	}
	raw, err := json.Marshal(mine)
	if err != nil {
		return Record{}, false, err
	}
	// The staging file is unique per call, not per process. Naming it after
	// the pid would make two concurrent Acquire calls in one process share one
	// temp file, and the first to finish would remove it from under the other.
	f, err := os.CreateTemp(dirOf(path), ".lock-*")
	if err != nil {
		return Record{}, false, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return Record{}, false, err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		_ = f.Close()
		return Record{}, false, err
	}
	if err := f.Close(); err != nil {
		return Record{}, false, err
	}

	for range maxTakeovers {
		err := os.Link(tmp, path)
		if err == nil {
			return mine, true, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return Record{}, false, err
		}
		held, ok := Read(path)
		if ok && held.PID == os.Getpid() {
			// Already ours. Acquire is idempotent within one process, so a
			// second caller is told it holds the lock rather than racing to
			// replace a record naming itself.
			return held, true, nil
		}
		if ok && PIDAlive(held.PID) {
			return held, false, nil
		}
		if afterStaleCheck != nil {
			afterStaleCheck()
		}
		if err := removeStale(path, held, ok); err != nil {
			return Record{}, false, err
		}
	}
	return Record{}, false, errors.New("applock: could not take the lock")
}

// LockGuard takes the advisory guard lock beside a lock file. It is exported
// so a caller that needs its own check-and-set to be atomic across processes
// can borrow the same serialisation the takeover uses.
func LockGuard(path string) (func(), error) { return lockGuardFile(path) }

// removeStale deletes the lock file Acquire found stale. It serialises
// takeovers with the <lock>.guard advisory lock and re-reads the file first: a
// lock that no longer names the same stale pid was replaced meanwhile and is
// left alone for the caller to re-evaluate.
func removeStale(path string, stale Record, readable bool) error {
	unlock, err := lockGuardFile(path + ".guard")
	if err != nil {
		return err
	}
	defer unlock()
	cur, ok := Read(path)
	if ok != readable || cur.PID != stale.PID {
		return nil
	}
	if ok && cur.PID != os.Getpid() && PIDAlive(cur.PID) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Release drops the lock, but only while this process still holds it: a lock
// another process took over after a stale sweep is not ours to delete.
func Release(path string) {
	if r, ok := Read(path); ok && r.PID == os.Getpid() {
		_ = os.Remove(path)
	}
}

// atomicWrite publishes data through a same-directory temp file and a rename,
// at mode 0600.
func atomicWrite(path string, data []byte) error {
	dir := dirOf(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
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
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
