package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// An unfinished run survives the process that made it: drafts cost model
// calls, so the next session reads them back instead of paying again.
func TestRunStoreRoundTripAndMode(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()

	fresh, err := NewRunStore().Load(ctx, repo)
	if err != nil {
		t.Fatalf("fresh Load: %v", err)
	}
	if fresh.Present() {
		t.Fatalf("a repository with no run reported one: %+v", fresh)
	}

	s := NewRunStore()
	if _, err := s.Load(ctx, repo); err != nil {
		t.Fatal(err)
	}
	state := json.RawMessage(`{"PM":{"Raw":{"epics":[]},"Cycle":1}}`)
	if err := s.Save(ctx, RunSnapshot{RunID: "r1", State: state}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path := filepath.Join(repo, filepath.FromSlash(PlanDirName), RunStateFileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("run.json mode = %v, want 0600", info.Mode().Perm())
	}

	again, err := NewRunStore().Load(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Present() || again.RunID != "r1" || string(again.State) != string(state) {
		t.Fatalf("round trip lost the run: %+v", again)
	}
	if again.SavedAt.IsZero() {
		t.Error("the snapshot has no saved_at")
	}
}

// Approving a plan clears the unfinished run; clearing twice is still success.
func TestRunStoreClearRemovesTheRun(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	s := NewRunStore()
	if _, err := s.Load(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, RunSnapshot{RunID: "r1", State: json.RawMessage(`{"Cycle":1}`)}); err != nil {
		t.Fatal(err)
	}

	if err := s.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if err := s.Clear(ctx); err != nil {
		t.Fatalf("second Clear: %v", err)
	}

	got, err := NewRunStore().Load(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Present() {
		t.Error("Clear left the run on disk")
	}
}

// The run file lives inside the plan directory, which denies everything by
// default, so an unfinished plan is never committed to the user's repository.
func TestRunStoreWritesInsideTheIgnoredPlanDirectory(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	s := NewRunStore()
	if _, err := s.Load(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, RunSnapshot{RunID: "r1", State: json.RawMessage(`{"Cycle":1}`)}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(PlanDirName), GitignoreName))
	if err != nil {
		t.Fatalf("the plan directory has no .gitignore: %v", err)
	}
	if string(raw) != PlanGitignore {
		t.Errorf("plan .gitignore = %q, want the default-deny content", raw)
	}
}

// A snapshot with no state is a programming error, not a file to write.
func TestRunStoreRefusesAnEmptySnapshot(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	s := NewRunStore()
	if _, err := s.Load(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, RunSnapshot{RunID: "r1"}); err == nil {
		t.Error("Save accepted a snapshot with no state")
	}
}

// A file from an older schema is reported, never adopted.
func TestRunStoreRefusesAnotherSchemaVersion(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	dir := filepath.Join(repo, filepath.FromSlash(PlanDirName))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"schema_version":99,"run_id":"r1","state":{}}`)
	if err := os.WriteFile(filepath.Join(dir, RunStateFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewRunStore().Load(ctx, repo)
	if err == nil {
		t.Fatal("Load accepted a run file from another schema version")
	}
	if got := contracts.CodeOf(err); got != contracts.CodeUnsupportedVer {
		t.Errorf("error code = %q, want %q", got, contracts.CodeUnsupportedVer)
	}
}
