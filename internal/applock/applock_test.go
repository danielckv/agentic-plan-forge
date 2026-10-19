//go:build client && !server

package applock

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// deadPID is a pid high enough that no live process holds it, used to stage a
// stale lock without killing anything.
const deadPID = 2147483646

func lockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "daemon.pid")
}

// TestLockHolderHelper is the body of the live holder process spawned by
// spawnLiveHolder. It is a test only so the test binary can re-exec itself; it
// does nothing unless the environment variable selects it.
func TestLockHolderHelper(t *testing.T) {
	if os.Getenv("PLAN_FORGE_TEST_LOCK_HOLDER") == "" {
		t.Skip("helper process only")
	}
	time.Sleep(30 * time.Second)
}

func spawnLiveHolder(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHolderHelper$")
	cmd.Env = append(os.Environ(), "PLAN_FORGE_TEST_LOCK_HOLDER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the live holder: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

func TestAcquireTakesAFreeLockAndRecordsThePort(t *testing.T) {
	path := lockPath(t)
	got, held, err := Acquire(path, Record{Port: 8420, Key: "center"})
	if err != nil || !held {
		t.Fatalf("Acquire = held %v err %v", held, err)
	}
	if got.PID != os.Getpid() || got.Port != 8420 || got.Key != "center" || got.State != StateStarting {
		t.Errorf("record = %+v", got)
	}
	on, ok := Read(path)
	if !ok || on.PID != os.Getpid() || on.Port != 8420 {
		t.Errorf("on disk = %+v ok=%v", on, ok)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want 0600", perm)
	}
}

func TestAcquireYieldsToALiveHolder(t *testing.T) {
	path := lockPath(t)
	holder := spawnLiveHolder(t)
	if err := Write(path, Record{PID: holder, State: StateRunning, Port: 8420}); err != nil {
		t.Fatal(err)
	}
	got, held, err := Acquire(path, Record{Port: 8420})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if held {
		t.Fatal("took a lock a live process holds")
	}
	if got.PID != holder || got.Port != 8420 {
		t.Errorf("holder = %+v, want pid %d", got, holder)
	}
}

func TestAcquireReplacesADeadOrUnreadableHolder(t *testing.T) {
	for name, stage := range map[string]func(path string) error{
		"dead pid":    func(p string) error { return Write(p, Record{PID: deadPID, State: StateRunning}) },
		"unparseable": func(p string) error { return os.WriteFile(p, []byte("{not json"), 0o600) },
		"zero pid":    func(p string) error { return os.WriteFile(p, []byte(`{"pid":0}`), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			path := lockPath(t)
			if err := stage(path); err != nil {
				t.Fatal(err)
			}
			got, held, err := Acquire(path, Record{Port: 8420})
			if err != nil || !held {
				t.Fatalf("Acquire = held %v err %v", held, err)
			}
			if got.PID != os.Getpid() {
				t.Errorf("record = %+v, want this process", got)
			}
		})
	}
}

// Between one contender's stale check and its removal another contender can
// finish its own takeover. The first must notice, leave the new holder's lock
// alone and report that holder.
func TestTakeoverRereadsUnderGuard(t *testing.T) {
	path := lockPath(t)
	holder := spawnLiveHolder(t)
	if err := Write(path, Record{PID: deadPID, State: StateRunning}); err != nil {
		t.Fatal(err)
	}
	afterStaleCheck = func() {
		afterStaleCheck = nil
		if err := Write(path, Record{PID: holder, State: StateRunning}); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterStaleCheck = nil })

	got, held, err := Acquire(path, Record{})
	if err != nil || held || got.PID != holder {
		t.Errorf("Acquire = %+v held %v err %v; want the new holder %d and no takeover", got, held, err, holder)
	}
	if on, ok := Read(path); !ok || on.PID != holder {
		t.Errorf("the new holder's lock was removed: %+v %v", on, ok)
	}
}

// The guard is a cross-handle exclusive lock: a second taker waits for the first.
func TestGuardSerializes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock.guard")
	unlock, err := lockGuardFile(path)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		u, err := lockGuardFile(path)
		if err != nil {
			t.Error(err)
			return
		}
		close(acquired)
		u()
	}()
	select {
	case <-acquired:
		t.Fatal("a second guard was taken while the first was held")
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("the guard was not released")
	}
}

func TestHolderOnlyReportsALiveProcess(t *testing.T) {
	path := lockPath(t)
	if _, ok := Holder(path); ok {
		t.Error("reported a holder with no lock file")
	}
	if err := Write(path, Record{PID: deadPID, State: StateRunning}); err != nil {
		t.Fatal(err)
	}
	if _, ok := Holder(path); ok {
		t.Error("reported a dead holder as live")
	}
	if err := Write(path, Record{PID: os.Getpid(), State: StateRunning, Port: 8420}); err != nil {
		t.Fatal(err)
	}
	got, ok := Holder(path)
	if !ok || got.Port != 8420 {
		t.Errorf("Holder = %+v ok=%v", got, ok)
	}
}

func TestReleaseOnlyDropsOurOwnLock(t *testing.T) {
	path := lockPath(t)
	if _, _, err := Acquire(path, Record{}); err != nil {
		t.Fatal(err)
	}
	Release(path)
	if _, ok := Read(path); ok {
		t.Error("our own lock survived Release")
	}

	// A lock another process took over is not ours to delete.
	if err := Write(path, Record{PID: deadPID, State: StateRunning}); err != nil {
		t.Fatal(err)
	}
	Release(path)
	if _, ok := Read(path); !ok {
		t.Error("Release deleted another process's lock")
	}
}

func TestWriteMovesAHolderToRunning(t *testing.T) {
	path := lockPath(t)
	got, held, err := Acquire(path, Record{Port: 8420})
	if err != nil || !held {
		t.Fatalf("Acquire = held %v err %v", held, err)
	}
	got.State = StateRunning
	if err := Write(path, got); err != nil {
		t.Fatalf("Write: %v", err)
	}
	on, ok := Read(path)
	if !ok || on.State != StateRunning || on.Port != 8420 {
		t.Errorf("on disk = %+v ok=%v", on, ok)
	}
	if on.UpdatedAt.IsZero() {
		t.Error("Write did not stamp UpdatedAt")
	}
}

// Acquire is idempotent within one process: concurrent callers all end up
// holding the same lock, none errors, and the file names this process once.
func TestConcurrentAcquireInOneProcessIsSafe(t *testing.T) {
	path := lockPath(t)
	const contenders = 6
	type outcome struct {
		held bool
		err  error
	}
	results := make(chan outcome, contenders)
	for range contenders {
		go func() {
			_, held, err := Acquire(path, Record{Port: 8420})
			results <- outcome{held, err}
		}()
	}
	for range contenders {
		got := <-results
		if got.err != nil {
			t.Errorf("Acquire: %v", got.err)
		}
		if !got.held {
			t.Error("a caller was refused a lock its own process holds")
		}
	}
	on, ok := Read(path)
	if !ok || on.PID != os.Getpid() {
		t.Errorf("lock = %+v ok=%v, want this process", on, ok)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" || len(e.Name()) > 6 && e.Name()[:6] == ".lock-" {
			t.Errorf("a staging file was left behind: %s", e.Name())
		}
	}
}
