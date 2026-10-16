package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

// Size bounds for the committed artifacts. They match the synthesis.v1 draft
// bound so anything approved also fit through the draft channel.
const (
	MaxArtifactBytes  = 1_000_000
	maxJournalBytes   = 64 << 10
	journalFileName   = "journal.json"
	stagePrefixSpec   = "." + SpecFileName + ".new-"
	stagePrefixManif  = "." + ManifestFileName + ".new-"
	journalStatePrep  = "prepared"
	oldSpecBackup     = "spec.old"
	oldManifestBackup = "manifest.old"
)

// CommitResult reports the hashes of the committed pair.
type CommitResult struct {
	SpecSHA256     string
	ManifestSHA256 string
}

// Pair is a committed spec and manifest whose hashes agree.
type Pair struct {
	Spec           []byte
	Manifest       []byte
	SpecSHA256     string
	ManifestSHA256 string
	Meta           contracts.PlanManifest
}

// ArtifactStore commits and reads the approved spec/manifest pair of one
// repository.
type ArtifactStore struct {
	plan *planDir
	// hook, when set, is called before and after every write and rename with
	// "<step>:before" or "<step>:after". Tests use it to inject failures and
	// simulated crashes.
	hook func(point string) error
}

// NewArtifactStore binds a store to a repository root. It creates nothing.
func NewArtifactStore(repoRoot string) (*ArtifactStore, error) {
	p, err := openPlan(repoRoot)
	if err != nil {
		return nil, err
	}
	return &ArtifactStore{plan: p}, nil
}

// errCrash simulates process death in tests: Commit returns at once and runs no
// cleanup, leaving the on-disk state exactly as a killed process would.
var errCrash = errors.New("simulated crash")

func (s *ArtifactStore) at(point string) error {
	if s.hook == nil {
		return nil
	}
	return s.hook(point)
}

func (s *ArtifactStore) step(name string, do func() error) error {
	if err := s.at(name + ":before"); err != nil {
		return err
	}
	if err := do(); err != nil {
		return err
	}
	return s.at(name + ":after")
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type journal struct {
	ID              string `json:"id"`
	State           string `json:"state"`
	ApprovalID      string `json:"approval_id"`
	NewSpecSHA      string `json:"new_spec_sha256"`
	NewManifestSHA  string `json:"new_manifest_sha256"`
	HadSpec         bool   `json:"had_spec"`
	HadManifest     bool   `json:"had_manifest"`
	OldSpecSHA      string `json:"old_spec_sha256,omitempty"`
	OldManifestSHA  string `json:"old_manifest_sha256,omitempty"`
	StagedSpec      string `json:"staged_spec"`
	StagedManifest  string `json:"staged_manifest"`
	RotateToHistory string `json:"rotate_to_history,omitempty"`
}

func newTxID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Commit writes the approved pair. Same-directory staging, a journal, a backup
// of the prior generation, fsync, spec replacement, then manifest replacement
// as the commit marker: readers expose only hash-matching pairs, and recovery
// finishes a transaction whose two new hashes are both on disk or otherwise
// restores the prior pair. An error leaves the old approved pair intact.
//
// The manifest must be valid JSON whose spec_sha256 equals the SHA-256 of
// specBytes and whose approval_id equals meta.ApprovalID.
func (s *ArtifactStore) Commit(ctx context.Context, meta contracts.ApprovalMeta, specBytes, manifestBytes []byte) (CommitResult, error) {
	res := CommitResult{SpecSHA256: sum(specBytes), ManifestSHA256: sum(manifestBytes)}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if len(specBytes) == 0 || len(specBytes) > MaxArtifactBytes || len(manifestBytes) == 0 || len(manifestBytes) > MaxArtifactBytes {
		return res, &ArtifactError{Code: CodeEHASH, RelPath: PlanDirName, Err: fmt.Errorf("artifact size must be 1..%d bytes", MaxArtifactBytes)}
	}
	var m contracts.PlanManifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return res, &ArtifactError{Code: CodeEHASH, RelPath: PlanDirName + "/" + ManifestFileName, Err: fmt.Errorf("manifest is not valid JSON: %w", err)}
	}
	if m.SpecSHA256 != res.SpecSHA256 {
		return res, &ArtifactError{Code: CodeEHASH, RelPath: PlanDirName + "/" + SpecFileName, Err: errors.New("manifest spec_sha256 does not match the spec bytes")}
	}
	if m.ApprovalID != meta.ApprovalID {
		return res, &ArtifactError{Code: CodeEHASH, RelPath: PlanDirName + "/" + ManifestFileName, Err: errors.New("manifest approval_id does not match the approval")}
	}

	unlock, err := s.plan.lock(ctx)
	if err != nil {
		return res, err
	}
	defer unlock()
	if err := s.at("lock:after"); err != nil {
		return res, err
	}
	if err := s.recoverLocked(); err != nil {
		return res, err
	}
	if err := s.at("recovered:after"); err != nil {
		return res, err
	}

	dir, err := s.plan.ensureDir(PlanDirName)
	if err != nil {
		return res, err
	}
	txRoot, err := s.plan.ensureDir(PlanDirName + "/" + TransactionsDir)
	if err != nil {
		return res, err
	}
	specPath := filepath.Join(dir, SpecFileName)
	manifestPath := filepath.Join(dir, ManifestFileName)
	for _, rel := range []string{PlanDirName + "/" + SpecFileName, PlanDirName + "/" + ManifestFileName} {
		if _, err := s.plan.path(rel); err != nil {
			return res, err
		}
	}

	oldSpec, hadSpec, err := readOptional(specPath)
	if err != nil {
		return res, artifactErr(PlanDirName+"/"+SpecFileName, err)
	}
	oldManifest, hadManifest, err := readOptional(manifestPath)
	if err != nil {
		return res, artifactErr(PlanDirName+"/"+ManifestFileName, err)
	}

	id := newTxID()
	j := journal{
		ID: id, State: journalStatePrep, ApprovalID: meta.ApprovalID,
		NewSpecSHA: res.SpecSHA256, NewManifestSHA: res.ManifestSHA256,
		HadSpec: hadSpec, HadManifest: hadManifest,
		StagedSpec: stagePrefixSpec + id, StagedManifest: stagePrefixManif + id,
	}
	if hadSpec {
		j.OldSpecSHA = sum(oldSpec)
	}
	if hadManifest {
		j.OldManifestSHA = sum(oldManifest)
	}
	if hadSpec && hadManifest {
		j.RotateToHistory = historyName(oldManifest)
	}
	txDir := filepath.Join(txRoot, id)

	// A failure that is not a simulated crash rolls the transaction back.
	committed := false
	defer func() {
		if committed || errors.Is(err, errCrash) {
			return
		}
		_ = s.rollbackTx(txDir, j)
	}()

	err = s.step("stage-spec", func() error {
		return writeStaged(dir, j.StagedSpec, specBytes, 0o644)
	})
	if err != nil {
		return res, artifactErr(PlanDirName+"/"+SpecFileName, err)
	}
	if err = s.step("stage-manifest", func() error {
		return writeStaged(dir, j.StagedManifest, manifestBytes, 0o644)
	}); err != nil {
		return res, artifactErr(PlanDirName+"/"+ManifestFileName, err)
	}
	if err = s.step("backup", func() error {
		if err := os.Mkdir(txDir, 0o700); err != nil {
			return err
		}
		if hadSpec {
			if err := writeNew(filepath.Join(txDir, oldSpecBackup), oldSpec); err != nil {
				return err
			}
		}
		if hadManifest {
			if err := writeNew(filepath.Join(txDir, oldManifestBackup), oldManifest); err != nil {
				return err
			}
		}
		return syncDir(txDir)
	}); err != nil {
		return res, artifactErr(PlanDirName+"/"+TransactionsDir, err)
	}
	if err = ctx.Err(); err != nil {
		return res, err
	}
	if err = s.step("journal", func() error {
		raw, err := json.Marshal(j)
		if err != nil {
			return err
		}
		if err := writeFileAtomic(filepath.Join(txDir, journalFileName), raw, 0o600); err != nil {
			return err
		}
		return syncDir(txRoot)
	}); err != nil {
		return res, &ArtifactError{Code: CodeEJOURNAL, RelPath: PlanDirName + "/" + TransactionsDir, Err: err}
	}

	// From here on the transaction is durable: finish it whatever the context
	// says, because abandoning it midway is what recovery exists for.
	if err = s.step("replace-spec", func() error {
		return replaceFile(filepath.Join(dir, j.StagedSpec), specPath)
	}); err != nil {
		return res, artifactErr(PlanDirName+"/"+SpecFileName, err)
	}
	if err = s.step("sync-dir-1", func() error { return syncDir(dir) }); err != nil {
		return res, artifactErr(PlanDirName, err)
	}
	if err = s.step("replace-manifest", func() error {
		return replaceFile(filepath.Join(dir, j.StagedManifest), manifestPath)
	}); err != nil {
		return res, artifactErr(PlanDirName+"/"+ManifestFileName, err)
	}
	if err = s.step("sync-dir-2", func() error { return syncDir(dir) }); err != nil {
		return res, artifactErr(PlanDirName, err)
	}
	committed = true
	if err = s.finishTx(txDir, j); err != nil {
		if errors.Is(err, errCrash) {
			return res, err
		}
		// The new pair is committed and durable; only housekeeping failed.
		// Recovery completes it on the next run, so this is not a failed commit.
		err = nil
	}
	return res, nil
}

func readOptional(path string) ([]byte, bool, error) {
	data, err := readBounded(path, MaxArtifactBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	return data, true, nil
}

func writeStaged(dir, name string, data []byte, perm fs.FileMode) error {
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if serr := f.Sync(); werr == nil {
		werr = serr
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
		return werr
	}
	return syncDir(dir)
}

func writeNew(path string, data []byte) error {
	return writeStaged(filepath.Dir(path), filepath.Base(path), data, 0o600)
}

// historyName names the history directory for a prior approved pair from its
// manifest: creation time then approval ID, so names sort chronologically.
func historyName(oldManifest []byte) string {
	var m contracts.PlanManifest
	ts, id := "00000000T000000Z", "unknown"
	if json.Unmarshal(oldManifest, &m) == nil {
		if !m.CreatedAt.IsZero() {
			ts = m.CreatedAt.UTC().Format("20060102T150405Z")
		}
		if clean := safeName(m.ApprovalID); clean != "" {
			id = clean
		}
	}
	return ts + "-" + id + "-" + sum(oldManifest)[:8]
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
		if b.Len() >= 36 {
			break
		}
	}
	return b.String()
}

// finishTx completes a committed transaction: rotate the prior pair into
// history, prune to HistoryKeep, and delete staging files and the journal. Every
// step is idempotent so recovery can repeat it after a crash.
func (s *ArtifactStore) finishTx(txDir string, j journal) error {
	dir, err := s.plan.path(PlanDirName)
	if err != nil {
		return err
	}
	if j.RotateToHistory != "" {
		if err := s.step("history", func() error { return s.rotate(txDir, j) }); err != nil {
			return err
		}
	}
	if err := s.step("cleanup", func() error {
		for _, name := range []string{j.StagedSpec, j.StagedManifest} {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return os.RemoveAll(txDir)
	}); err != nil {
		return err
	}
	return syncDir(filepath.Dir(txDir))
}

func (s *ArtifactStore) rotate(txDir string, j journal) error {
	histRoot, err := s.plan.ensureDir(PlanDirName + "/" + HistoryDir)
	if err != nil {
		return err
	}
	target := filepath.Join(histRoot, j.RotateToHistory)
	if _, err := os.Lstat(target); errors.Is(err, fs.ErrNotExist) {
		stage := filepath.Join(histRoot, "."+j.RotateToHistory+".tmp-"+j.ID)
		_ = os.RemoveAll(stage)
		if err := os.Mkdir(stage, 0o755); err != nil {
			return err
		}
		for _, f := range [][2]string{{oldSpecBackup, SpecFileName}, {oldManifestBackup, ManifestFileName}} {
			data, err := readBounded(filepath.Join(txDir, f[0]), MaxArtifactBytes)
			if err != nil {
				_ = os.RemoveAll(stage)
				return err
			}
			if err := writeNew(filepath.Join(stage, f[1]), data); err != nil {
				_ = os.RemoveAll(stage)
				return err
			}
		}
		if err := replaceFile(stage, target); err != nil {
			_ = os.RemoveAll(stage)
			return err
		}
		if err := syncDir(histRoot); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return pruneHistory(histRoot)
}

func pruneHistory(histRoot string) error {
	entries, err := os.ReadDir(histRoot)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > HistoryKeep {
		if err := os.RemoveAll(filepath.Join(histRoot, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

// rollbackTx restores the prior generation after a failed commit: each of the
// two files returns to its backed-up content, or is removed when it did not
// exist before. Hashes are verified before anything is restored.
func (s *ArtifactStore) rollbackTx(txDir string, j journal) error {
	dir, err := s.plan.path(PlanDirName)
	if err != nil {
		return err
	}
	restore := func(name, backup string, had bool, wantSHA, newSHA string) error {
		path := filepath.Join(dir, name)
		cur, exists, err := readOptional(path)
		if err != nil {
			return err
		}
		if !had {
			if exists && sum(cur) == newSHA {
				return os.Remove(path)
			}
			return nil
		}
		if exists && sum(cur) == wantSHA {
			return nil
		}
		old, err := readBounded(filepath.Join(txDir, backup), MaxArtifactBytes)
		if err != nil {
			return fmt.Errorf("backup of %s is unreadable: %w", name, err)
		}
		if sum(old) != wantSHA {
			return fmt.Errorf("backup of %s fails its hash", name)
		}
		return writeFileAtomic(path, old, 0o644)
	}
	var first error
	keep := func(err error) {
		if first == nil {
			first = err
		}
	}
	keep(restore(SpecFileName, oldSpecBackup, j.HadSpec, j.OldSpecSHA, j.NewSpecSHA))
	keep(restore(ManifestFileName, oldManifestBackup, j.HadManifest, j.OldManifestSHA, j.NewManifestSHA))
	if first != nil {
		// Keep the transaction directory: the backup is the only copy.
		return first
	}
	for _, name := range []string{j.StagedSpec, j.StagedManifest} {
		if name != "" {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	if txDir != "" {
		_ = os.RemoveAll(txDir)
	}
	return nil
}

// recoverLocked repairs every unfinished transaction. The caller holds the lock.
// A transaction whose journal never became durable made no replacement, so its
// staging files are simply removed.
func (s *ArtifactStore) recoverLocked() error {
	dir, err := s.plan.path(PlanDirName)
	if err != nil {
		return err
	}
	txRoot := filepath.Join(dir, TransactionsDir)
	entries, err := os.ReadDir(txRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return s.sweepStaging(dir)
	}
	if err != nil {
		return artifactErr(PlanDirName+"/"+TransactionsDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		txDir := filepath.Join(txRoot, e.Name())
		if info, err := os.Lstat(txDir); err != nil || hostile(info.Mode()) {
			return &ArtifactError{Code: CodeESYMLINK, RelPath: PlanDirName + "/" + TransactionsDir, Err: errors.New("transaction entry is not a directory")}
		}
		raw, err := readBounded(filepath.Join(txDir, journalFileName), maxJournalBytes)
		var j journal
		if err != nil || json.Unmarshal(raw, &j) != nil || j.ID == "" || j.ID != e.Name() {
			// No durable journal: nothing was replaced. Drop the transaction.
			_ = os.RemoveAll(txDir)
			continue
		}
		spec, hasSpec, err1 := readOptional(filepath.Join(dir, SpecFileName))
		man, hasMan, err2 := readOptional(filepath.Join(dir, ManifestFileName))
		if err1 != nil || err2 != nil {
			return &ArtifactError{Code: CodeEJOURNAL, RelPath: PlanDirName, Err: errors.Join(err1, err2)}
		}
		if hasSpec && hasMan && sum(spec) == j.NewSpecSHA && sum(man) == j.NewManifestSHA {
			if err := s.finishTx(txDir, j); err != nil {
				return artifactErr(PlanDirName+"/"+TransactionsDir, err)
			}
			continue
		}
		if err := s.rollbackTx(txDir, j); err != nil {
			return &ArtifactError{Code: CodeEJOURNAL, RelPath: PlanDirName + "/" + TransactionsDir, Err: err}
		}
	}
	return s.sweepStaging(dir)
}

// sweepStaging removes leftover staging files. It runs after every transaction
// has been finished or rolled back, so none is still in use.
func (s *ArtifactStore) sweepStaging(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return artifactErr(PlanDirName, err)
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, stagePrefixSpec) || strings.HasPrefix(n, stagePrefixManif) {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
	return nil
}

// Recover repairs an interrupted commit. It takes the lock.
func (s *ArtifactStore) Recover(ctx context.Context) error {
	unlock, err := s.plan.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return s.recoverLocked()
}

// Read recovers any interrupted commit under the lock, then returns the
// committed pair. It returns ErrNoPlan when none exists.
func (s *ArtifactStore) Read(ctx context.Context) (Pair, error) {
	dir, err := s.plan.path(PlanDirName)
	if err != nil {
		return Pair{}, err
	}
	if _, err := os.Lstat(filepath.Join(dir, TransactionsDir)); err == nil {
		if err := s.Recover(ctx); err != nil {
			return Pair{}, err
		}
	}
	return ReadPair(s.plan.root)
}

// ReadPair reads the committed pair without writing anything: no lock, no
// recovery, no directory creation. It is what hooks and viewers use on
// untrusted repositories. Only a hash-matching pair is returned; a missing
// plan is ErrNoPlan and a disagreeing pair is ErrPairMismatch.
func ReadPair(repoRoot string) (Pair, error) {
	var pair Pair
	var err error
	// A commit in flight replaces the spec, then the manifest; a lock-free
	// reader can land between the two. Look again before reporting a mismatch.
	for attempt := 0; attempt < 4; attempt++ {
		if pair, err = readPairOnce(repoRoot); !errors.Is(err, ErrPairMismatch) {
			return pair, err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return pair, err
}

func readPairOnce(repoRoot string) (Pair, error) {
	p, err := openPlan(repoRoot)
	if err != nil {
		return Pair{}, err
	}
	specPath, err := p.path(PlanDirName + "/" + SpecFileName)
	if err != nil {
		return Pair{}, err
	}
	manifestPath, err := p.path(PlanDirName + "/" + ManifestFileName)
	if err != nil {
		return Pair{}, err
	}
	var pair Pair
	var hasSpec, hasMan bool
	if pair.Manifest, hasMan, err = readOptional(manifestPath); err != nil {
		return Pair{}, artifactErr(PlanDirName+"/"+ManifestFileName, err)
	}
	if pair.Spec, hasSpec, err = readOptional(specPath); err != nil {
		return Pair{}, artifactErr(PlanDirName+"/"+SpecFileName, err)
	}
	if !hasSpec && !hasMan {
		return Pair{}, ErrNoPlan
	}
	if !hasSpec || !hasMan {
		return Pair{}, ErrPairMismatch
	}
	pair.SpecSHA256, pair.ManifestSHA256 = sum(pair.Spec), sum(pair.Manifest)
	if err := json.Unmarshal(pair.Manifest, &pair.Meta); err != nil {
		return Pair{}, fmt.Errorf("%w: manifest is not valid JSON", ErrPairMismatch)
	}
	if pair.Meta.SpecSHA256 != pair.SpecSHA256 {
		return Pair{}, ErrPairMismatch
	}
	return pair, nil
}
