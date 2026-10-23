package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// RunStateFileName is the unfinished deliberation of one repository, inside
// docs/plans/.plan/. The directory's .gitignore keeps it untracked.
const RunStateFileName = "run.json"

// MaxRunStateBytes bounds run.json on read and write. It holds the validated
// drafts of one run: the stage bound is 2 MiB each, and a run keeps the
// architect draft plus one draft per workstream.
const MaxRunStateBytes = 16 << 20

// RunSnapshot is one unfinished run, as written to disk. State is the opaque
// checkpoint the deliberation package owns; this package never reads into it.
type RunSnapshot struct {
	SchemaVersion int             `json:"schema_version"`
	RunID         string          `json:"run_id"`
	SavedAt       time.Time       `json:"saved_at"`
	State         json.RawMessage `json:"state"`
}

// Present reports whether a snapshot holds a run.
func (s RunSnapshot) Present() bool { return s.RunID != "" && len(s.State) > 0 }

// RunStore reads, writes and removes docs/plans/.plan/run.json. One repository
// keeps one unfinished run: a new run replaces it, and approving a plan clears
// it. Drafts cost model calls, so they survive a crash, a cancelled window and
// a restart; nothing else in the file is durable state.
type RunStore struct {
	plan *planDir
}

// NewRunStore returns an unbound store; Load binds it to a repository.
func NewRunStore() *RunStore { return &RunStore{} }

func (s *RunStore) path(ensure bool) (string, error) {
	if s.plan == nil {
		return "", errors.New("run store is not bound to a repository: call Load first")
	}
	if ensure {
		dir, err := s.plan.ensureDir(PlanDirName)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, RunStateFileName), nil
	}
	return s.plan.path(PlanDirName + "/" + RunStateFileName)
}

// Load binds the store to repo and returns the unfinished run, if there is
// one. A missing file is an empty snapshot, not an error. A malformed or
// oversized file is reported and left on disk for the caller to clear.
func (s *RunStore) Load(ctx context.Context, repo string) (RunSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return RunSnapshot{}, err
	}
	p, err := openPlan(repo)
	if err != nil {
		return RunSnapshot{}, err
	}
	s.plan = p
	path, err := s.path(false)
	if err != nil {
		return RunSnapshot{}, err
	}
	raw, err := readBounded(path, MaxRunStateBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return RunSnapshot{}, nil
	}
	if err != nil {
		return RunSnapshot{}, artifactErr(PlanDirName+"/"+RunStateFileName, err)
	}
	var snap RunSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return RunSnapshot{}, fmt.Errorf("%s/%s is malformed: %w", PlanDirName, RunStateFileName, err)
	}
	if snap.SchemaVersion != contracts.SchemaVersion {
		return RunSnapshot{}, contracts.Coded(contracts.CodeUnsupportedVer,
			"%s/%s has schema_version %d", PlanDirName, RunStateFileName, snap.SchemaVersion)
	}
	return snap, nil
}

// Save writes the snapshot: same-directory temp file, fsync, atomic replace,
// mode 0600. On error the previous file is intact.
func (s *RunStore) Save(ctx context.Context, snap RunSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.plan == nil {
		return errors.New("run store is not bound to a repository: call Load first")
	}
	if snap.RunID == "" || len(snap.State) == 0 {
		return contracts.Coded(contracts.CodeInvalidPayload, "a run snapshot needs a run id and a state")
	}
	snap.SchemaVersion = contracts.SchemaVersion
	if snap.SavedAt.IsZero() {
		snap.SavedAt = time.Now().UTC()
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > MaxRunStateBytes {
		return &contracts.ErrBound{Name: RunStateFileName, Limit: MaxRunStateBytes, Observed: int64(len(raw))}
	}
	path, err := s.path(true)
	if err != nil {
		return err
	}
	// The directory denies everything by default, so an unfinished plan is
	// never committed to the user's repository.
	if err := s.plan.ensureGitignore(); err != nil {
		return err
	}
	if err := writeFileAtomic(path, raw, 0o600); err != nil {
		return artifactErr(PlanDirName+"/"+RunStateFileName, err)
	}
	return nil
}

// Clear removes the unfinished run. A file that is already gone is success:
// the plan it belonged to is finished either way.
func (s *RunStore) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.plan == nil {
		return nil
	}
	path, err := s.path(false)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return artifactErr(PlanDirName+"/"+RunStateFileName, err)
	}
	return nil
}
