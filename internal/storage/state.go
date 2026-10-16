package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// MaxStateBytes bounds state.json on read and write.
const MaxStateBytes = 4 << 20

// MaxRunLogEntries bounds the run log; the oldest entries are dropped.
const MaxRunLogEntries = 200

// State is the local mutable state of one repository, kept in .plan/state.json
// (mode 0600, ignored by git). It never holds relay, room, member or invite
// secrets, invite URLs, absolute paths or prompt text: Save refuses any state
// that does.
type State struct {
	SchemaVersion int `json:"schema_version"`
	// Revisions mirror the session authority's counters for resume checks.
	PRDRevision       uint64 `json:"prd_revision"`
	BaselineRevision  uint64 `json:"baseline_revision"`
	InputRevision     uint64 `json:"input_revision"`
	SynthesisRevision uint64 `json:"synthesis_revision"`
	// GraphSHA256 is the hash of the graph the last run was grounded on.
	GraphSHA256  string          `json:"graph_sha256,omitempty"`
	RunLog       []RunLogEntry   `json:"run_log"`
	LastApproval *ApprovalRecord `json:"last_approval,omitempty"`
}

// RunLogEntry is one line of the run history.
type RunLogEntry struct {
	At     time.Time `json:"at"`
	RunID  string    `json:"run_id,omitempty"`
	Cycle  int       `json:"cycle,omitempty"`
	Event  string    `json:"event"`
	Detail string    `json:"detail,omitempty"`
}

// ApprovalRecord names the last committed approval.
type ApprovalRecord struct {
	ApprovalID     string `json:"approval_id"`
	SpecSHA256     string `json:"spec_sha256"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

var (
	forbiddenKey   = regexp.MustCompile(`(?i)(token|secret|invite|password|passwd|credential|cred\b|prompt|relay|room|member|api[_-]?key|authorization)`)
	absolutePath   = regexp.MustCompile(`^(/|[A-Za-z]:[\\/]|\\\\)`)
	urlLike        = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://`)
	embeddedAbsRel = regexp.MustCompile(`(^|\s)(/(Users|home|root|var|tmp|etc|private|opt|mnt)/|[A-Za-z]:\\)`)
)

// Validate enforces the schema: no secret-looking keys, no URLs, no absolute
// paths and no oversized log, anywhere in the serialized state.
func (s State) Validate() error {
	if s.SchemaVersion != contracts.SchemaVersion {
		return contracts.Coded(contracts.CodeUnsupportedVer, "state schema_version %d is not supported", s.SchemaVersion)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return err
	}
	return walkState("", generic)
}

func walkState(path string, v any) error {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if forbiddenKey.MatchString(k) {
				return fmt.Errorf("state key %q is forbidden: state.json never holds secrets or prompts", path+k)
			}
			if err := walkState(path+k+".", child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range t {
			if err := walkState(path, child); err != nil {
				return err
			}
		}
	case string:
		switch {
		case absolutePath.MatchString(t), embeddedAbsRel.MatchString(t):
			return fmt.Errorf("state value at %q looks like an absolute path", strings.TrimSuffix(path, "."))
		case urlLike.MatchString(t):
			return fmt.Errorf("state value at %q looks like a URL", strings.TrimSuffix(path, "."))
		}
	}
	return nil
}

// StateStore reads and writes .plan/state.json for one repository.
type StateStore struct {
	plan *planDir
}

// NewStateStore returns an unbound store; Load binds it to a repository.
func NewStateStore() *StateStore { return &StateStore{} }

func (s *StateStore) path(ensure bool) (string, error) {
	if s.plan == nil {
		return "", errors.New("state store is not bound to a repository: call Load first")
	}
	if ensure {
		dir, err := s.plan.ensureDir(PlanDirName)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, StateFileName), nil
	}
	return s.plan.path(PlanDirName + "/" + StateFileName)
}

// Load binds the store to repo and returns its state. A missing file yields an
// empty current-version state. Malformed JSON is an error naming the byte
// offset and is never overwritten by Load.
func (s *StateStore) Load(ctx context.Context, repo string) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	p, err := openPlan(repo)
	if err != nil {
		return State{}, err
	}
	s.plan = p
	path, err := s.path(false)
	if err != nil {
		return State{}, err
	}
	raw, err := readBounded(path, MaxStateBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return State{SchemaVersion: contracts.SchemaVersion, RunLog: []RunLogEntry{}}, nil
	}
	if err != nil {
		return State{}, artifactErr(PlanDirName+"/"+StateFileName, err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			return State{}, fmt.Errorf("%s/%s is malformed at byte %d: %w", PlanDirName, StateFileName, se.Offset, err)
		}
		return State{}, fmt.Errorf("%s/%s is malformed: %w", PlanDirName, StateFileName, err)
	}
	if st.SchemaVersion != contracts.SchemaVersion {
		return State{}, contracts.Coded(contracts.CodeUnsupportedVer, "%s/%s has schema_version %d", PlanDirName, StateFileName, st.SchemaVersion)
	}
	if st.RunLog == nil {
		st.RunLog = []RunLogEntry{}
	}
	return st, nil
}

// Save validates and writes the state: lock, same-directory temp file, fsync,
// atomic replace. The file mode is 0600. On error the previous file is intact.
func (s *StateStore) Save(ctx context.Context, st State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.plan == nil {
		return errors.New("state store is not bound to a repository: call Load first")
	}
	if st.SchemaVersion == 0 {
		st.SchemaVersion = contracts.SchemaVersion
	}
	if st.RunLog == nil {
		st.RunLog = []RunLogEntry{}
	}
	if n := len(st.RunLog); n > MaxRunLogEntries {
		st.RunLog = st.RunLog[n-MaxRunLogEntries:]
	}
	if err := st.Validate(); err != nil {
		return contracts.Wrap(contracts.CodeInvalidPayload, err, "refusing to write state.json")
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > MaxStateBytes {
		return &contracts.ErrBound{Name: StateFileName, Limit: MaxStateBytes, Observed: int64(len(raw))}
	}
	unlock, err := s.plan.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	path, err := s.path(true)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && hostile(info.Mode()) {
		return &ArtifactError{Code: CodeESYMLINK, RelPath: PlanDirName + "/" + StateFileName, Err: errors.New("state file is a symbolic link")}
	}
	if err := writeFileAtomic(path, raw, 0o600); err != nil {
		return artifactErr(PlanDirName+"/"+StateFileName, err)
	}
	return nil
}
