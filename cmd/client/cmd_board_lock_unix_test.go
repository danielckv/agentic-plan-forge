//go:build client && !server && !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestBoardAckWaitingOnAStuckLockIsBoundedAndNeverLands holds the store lock
// the way a stuck writer would. The ack must give up inside its bound without
// error, and once the lock frees the abandoned ack must not move the cursor
// behind the caller's back: the events stay pullable.
func TestBoardAckWaitingOnAStuckLockIsBoundedAndNeverLands(t *testing.T) {
	repo, store := boardWithEdit(t)
	const who = "claude:stuck"
	out, ack := boardDelta(context.Background(), repo, who, "text", 2*time.Second)
	if out == "" {
		t.Fatal("no delta to ack")
	}

	lockPath := strings.TrimSuffix(store.Path(), ".json") + ".lock"
	if filepath.Dir(lockPath) != filepath.Dir(store.Path()) {
		t.Fatalf("unexpected lock path %s", lockPath)
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	moved, finished := ackWithin(context.Background(), 150*time.Millisecond, ack)
	if moved || finished {
		t.Fatalf("ack under a held lock: moved=%v finished=%v", moved, finished)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("ack took %v, want it bounded near 150ms", took)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // let the abandoned ack take the lock and refuse
	if got := cursorSeq(t, store, who); got != 0 {
		t.Fatalf("an abandoned ack advanced the cursor to %d", got)
	}
	if got := pull(t, repo, who, "text", false); !strings.Contains(got, "priority -> HIGH") {
		t.Fatalf("events were not pullable after the expiry: %q", got)
	}
}
