package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// Quarantine bounds and directory.
const (
	// quarantineDir holds damaged run records, so a broken file is preserved
	// rather than silently replaced.
	quarantineDir = "quarantine"
	// maxQuarantineFiles bounds how many broken records are kept. The newest
	// are kept: an older one has already been looked at or is not worth more
	// space.
	maxQuarantineFiles = 8
)

// QuarantinedError reports that the run file was unreadable and has been moved
// aside. The run can continue: the next save writes a fresh file. The bytes
// survive at QuarantinePath so nothing is silently lost.
type QuarantineError struct {
	// Path is repository-relative, never a host path.
	Path   string
	Reason string
	Err    error
}

func (e *QuarantineError) Error() string {
	return fmt.Sprintf("%s was moved aside (%s): %v", e.Path, e.Reason, e.Err)
}

func (e *QuarantineError) Unwrap() error { return e.Err }

// quarantine preserves the damaged bytes under .plan/quarantine/ and removes
// the active file, so the next save starts clean. It keeps the newest
// maxQuarantineFiles records.
func (s *RunStore) quarantine(raw []byte, reason string, cause error) error {
	dir, err := s.plan.ensureDir(PlanDirName + "/" + quarantineDir)
	if err != nil {
		// The bytes could not be preserved. Report the original problem and
		// leave the file alone: silently deleting the only copy is worse.
		return &QuarantineError{
			Path:   PlanDirName + "/" + RunStateFileName,
			Reason: "could not preserve the damaged record",
			Err:    errors.Join(cause, err),
		}
	}
	sum := sum(raw)
	name := quarantineName(time.Now().UTC(), sum)
	dst := filepath.Join(dir, name)
	if err := writeFileAtomic(dst, raw, 0o600); err != nil {
		return &QuarantineError{
			Path:   PlanDirName + "/" + RunStateFileName,
			Reason: "could not preserve the damaged record",
			Err:    errors.Join(cause, err),
		}
	}
	path, perr := s.path(false)
	if perr == nil {
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			return &QuarantineError{
				Path:   PlanDirName + "/" + RunStateFileName,
				Reason: "stored in " + PlanDirName + "/" + quarantineDir + "/" + name + " but could not be removed",
				Err:    errors.Join(cause, rerr),
			}
		}
	}
	pruneQuarantine(dir, maxQuarantineFiles)
	return &QuarantineError{
		Path:   PlanDirName + "/" + quarantineDir + "/" + name,
		Reason: reason,
		Err:    cause,
	}
}

// quarantineName orders preserved records by time, then by content hash, so a
// name collision cannot overwrite an earlier record.
func quarantineName(at time.Time, digest string) string {
	stamp := at.Format("20060102T150405.000Z")
	if len(digest) > 12 {
		digest = digest[:12]
	}
	return "run-" + stamp + "-" + digest + ".json"
}

// pruneQuarantine keeps the newest limit records. A pruning failure is ignored:
// it costs disk space, never correctness.
func pruneQuarantine(dir string, limit int) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= limit {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if drop := len(names) - limit; drop > 0 {
		for _, name := range names[:drop] {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

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
// oversized file is preserved under .plan/quarantine/ and removed, and the
// error is a *QuarantineError: the next Save then starts clean instead of
// failing on the same bytes forever.
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
		// A read failure is not a content failure: the file may be fine and the
		// cause (permissions, a missing directory) fixable, so it is not moved
		// aside and stays a plain error.
		return RunSnapshot{}, artifactErr(PlanDirName+"/"+RunStateFileName, err)
	}
	var snap RunSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return RunSnapshot{}, s.quarantine(raw, "run.json is not valid JSON", err)
	}
	if snap.SchemaVersion != contracts.SchemaVersion {
		return RunSnapshot{}, s.quarantine(raw, "run.json was written by another schema version", nil)
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
