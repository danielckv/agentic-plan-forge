//go:build client && !server

package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/storage"
)

// A run file that cannot be parsed must not stop the next write. Previously the
// parse error was swallowed and the file was kept, so the first save of the
// next run failed the same way and the run had no durable record at all.
func TestCheckpointQuarantinesAnUnreadableRun(t *testing.T) {
	repo := t.TempDir()
	runPath := filepath.Join(repo, "docs", "plans", ".plan", "run.json")
	if err := os.MkdirAll(filepath.Dir(runPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	sink := newRunCheckpointSink(repo)
	if _, _, ok := sink.Get(); ok {
		t.Fatal("a malformed run was reported as resumable")
	}
	if sink.Notice() == "" {
		t.Fatal("a damaged run produced no explanation")
	}

	if err := sink.Put("run-2", json.RawMessage(`{"cycle":1}`)); err != nil {
		t.Fatalf("the first save after a damaged record failed: %v", err)
	}
	id, _, ok := sink.Get()
	if !ok || id != "run-2" {
		t.Fatalf("Get = %q, %v after a successful save; want run-2", id, ok)
	}
}

// The damaged bytes survive: a deletion without a copy loses the only record of
// what the previous run had done.
func TestCheckpointPreservesTheDamagedRecord(t *testing.T) {
	repo := t.TempDir()
	runPath := filepath.Join(repo, "docs", "plans", ".plan", "run.json")
	if err := os.MkdirAll(filepath.Dir(runPath), 0o700); err != nil {
		t.Fatal(err)
	}
	garbage := []byte(`{"schema_version":1,"run_id":`)
	if err := os.WriteFile(runPath, garbage, 0o600); err != nil {
		t.Fatal(err)
	}

	sink := newRunCheckpointSink(repo)
	_, _, _ = sink.Get()

	if _, err := os.Stat(runPath); !os.IsNotExist(err) {
		t.Fatal("the damaged file is still in place, so every later load fails the same way")
	}
	dir := filepath.Join(repo, "docs", "plans", ".plan", "quarantine")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("nothing was preserved: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(garbage) {
		t.Errorf("preserved bytes = %q, want the original", raw)
	}
}

// A write failure in the plan directory must still be an error: reporting
// success for a checkpoint that never reached disk is the failure this whole
// path exists to avoid.
func TestCheckpointReportsAWriteFailure(t *testing.T) {
	repo := t.TempDir()
	sink := newRunCheckpointSink(repo)
	if err := sink.Put("", json.RawMessage(`{}`)); err == nil {
		t.Fatal("a snapshot with no run id was accepted")
	}
}

// A repository with no plan directory is a normal empty run, not a warning:
// every first session starts there.
func TestCheckpointOnAFreshRepository(t *testing.T) {
	sink := newRunCheckpointSink(t.TempDir())
	if _, _, ok := sink.Get(); ok {
		t.Fatal("a repository with no plan directory reported a run")
	}
	if notice := sink.Notice(); notice != "" {
		t.Fatalf("a fresh repository warned: %q", notice)
	}
}

// The quarantine keeps bounded records: a repeatedly crashing run cannot fill
// the plan directory with history.
func TestCheckpointBoundsPreservedRecords(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "docs", "plans", ".plan")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Eleven separate damaged loads, each through its own store, since one sink
	// loads once per process by design.
	for range 11 {
		if err := os.WriteFile(filepath.Join(dir, storage.RunStateFileName), []byte("{bad"), 0o600); err != nil {
			t.Fatal(err)
		}
		sink := newRunCheckpointSink(repo)
		_, _, _ = sink.Get()
	}
	entries, err := os.ReadDir(filepath.Join(dir, "quarantine"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 8 {
		t.Errorf("quarantine holds %d records, want at most the documented bound", len(entries))
	}
}

// A saved run is offered back, so a restart never pays twice for the same stage.
func TestCheckpointRestoresTheSavedRun(t *testing.T) {
	repo := t.TempDir()
	sink := newRunCheckpointSink(repo)
	state := json.RawMessage(`{"PM":{"Cycle":1}}`)
	if err := sink.Put("run-7", state); err != nil {
		t.Fatalf("Put: %v", err)
	}

	restarted := newRunCheckpointSink(repo)
	id, got, ok := restarted.Get()
	if !ok {
		t.Fatal("a saved run was not offered back after a restart")
	}
	if id != "run-7" {
		t.Errorf("run id = %q, want run-7", id)
	}
	if string(got) != string(state) {
		t.Errorf("state = %s, want %s", got, state)
	}
}
