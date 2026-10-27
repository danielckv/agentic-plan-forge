//go:build client && !server

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

// A bundle commit publishes the generated plan, specification and decision
// records into the repository's own documentation directories. It is the one
// multi-file write in this package, so it follows the same rule as the approved
// pair: a reader sees the previous set or the new one, never a mix.
//
// The commit marker is a manifest written last. Until it lands, a crash leaves
// the previous state readable, and re-running the same commit finishes the job.
// A different generation for the same approval is a mismatch, never a merge.

// Bundle bounds.
const (
	// MaxBundleEntries bounds one generated set.
	MaxBundleEntries = 200
	// MaxBundleFileBytes bounds one generated document.
	MaxBundleFileBytes = 2 << 20
	// MaxBundleTotalBytes bounds the whole set.
	MaxBundleTotalBytes = 20 << 20
	// BundleDirName holds the per-approval journals.
	BundleDirName = "bundles"
	// BundleManifestName is the commit marker.
	BundleManifestName = "bundle_manifest.json"
)

// BundleCommit reports what landed.
type BundleCommit struct {
	Committed    []string `json:"committed"`
	ManifestPath string   `json:"manifest_path"`
}

// bundleManifest is the commit marker: every path written, and its hash.
type bundleManifest struct {
	SchemaVersion int                   `json:"schema_version"`
	ApprovalID    string                `json:"approval_id"`
	CreatedAt     time.Time             `json:"created_at"`
	Entries       []bundleManifestEntry `json:"entries"`
}

type bundleManifestEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// CommitBundle writes the generated documents and publishes the marker last.
func (s *ArtifactStore) CommitBundle(ctx context.Context, meta contracts.ApprovalMeta, entries []synthesizer.BundleEntry) (BundleCommit, error) {
	if err := ctx.Err(); err != nil {
		return BundleCommit{}, err
	}
	if err := checkBundleBounds(entries); err != nil {
		return BundleCommit{}, err
	}
	approval := safeName(meta.ApprovalID)
	if approval == "" {
		return BundleCommit{}, contracts.Coded(contracts.CodeInvalidPayload, "a bundle commit needs an approval id")
	}
	if s.plan == nil {
		return BundleCommit{}, errors.New("artifact store is not bound to a repository")
	}

	unlock, err := s.plan.lock(ctx)
	if err != nil {
		return BundleCommit{}, err
	}
	defer unlock()

	journalDir, err := s.plan.ensureDir(PlanDirName + "/" + BundleDirName + "/" + approval)
	if err != nil {
		return BundleCommit{}, err
	}
	marker := filepath.Join(journalDir, BundleManifestName)
	prior, hadPrior, err := readBundleManifest(marker)
	if err != nil {
		return BundleCommit{}, err
	}
	want := manifestFor(meta, entries)
	if hadPrior {
		if sameBundleManifest(prior, want) {
			// Already published: a re-run is success, not a second write.
			return bundleResult(want, approval), nil
		}
		return BundleCommit{}, fmt.Errorf("bundle: approval %s already holds a different commit", approval)
	}

	// Refuse before writing anything: a destination that exists is a document a
	// person wrote, and a generated file never replaces one.
	for _, entry := range entries {
		full, perr := s.plan.path(entry.Path)
		if perr != nil {
			return BundleCommit{}, perr
		}
		if _, serr := os.Lstat(full); serr == nil {
			return BundleCommit{}, &ArtifactError{
				Code: CodeEEXIST, RelPath: entry.Path,
				Err: errors.New("a document already exists at this path"),
			}
		}
	}

	// Write in path order, tracking what landed so a failure can undo it.
	var created []string
	undo := func() {
		for _, rel := range created {
			if full, perr := s.plan.path(rel); perr == nil {
				_ = os.Remove(full)
			}
		}
	}
	ordered := append([]synthesizer.BundleEntry(nil), entries...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	for _, entry := range ordered {
		if dir := path.Dir(entry.Path); dir != "." {
			if derr := ensureRepoDir(s.plan, dir); derr != nil {
				undo()
				return BundleCommit{}, derr
			}
		}
		full, perr := s.plan.path(entry.Path)
		if perr != nil {
			undo()
			return BundleCommit{}, perr
		}
		if werr := writeFileAtomic(full, entry.Body, 0o644); werr != nil {
			undo()
			return BundleCommit{}, artifactErr(entry.Path, werr)
		}
		created = append(created, entry.Path)
	}

	// The marker lands last, so an interrupted commit leaves the previous state
	// readable rather than a half-set of documents.
	raw, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		undo()
		return BundleCommit{}, err
	}
	if err := writeFileAtomic(marker, append(raw, '\n'), 0o600); err != nil {
		undo()
		return BundleCommit{}, artifactErr(PlanDirName+"/"+BundleDirName+"/"+approval+"/"+BundleManifestName, err)
	}
	return bundleResult(want, approval), nil
}

// ensureRepoDir creates a repository-relative directory one checked component
// at a time. The plan directory's own helper only guarantees .plan/, and a
// bundle writes into the project's documentation directories.
func ensureRepoDir(p *planDir, rel string) error {
	cur := ""
	for _, comp := range strings.Split(rel, "/") {
		if comp == "" || comp == "." {
			continue
		}
		if cur == "" {
			cur = comp
		} else {
			cur += "/" + comp
		}
		full, err := p.path(cur)
		if err != nil {
			return err
		}
		info, err := os.Lstat(full)
		switch {
		case err == nil && !info.IsDir():
			return &ArtifactError{Code: CodeEEXIST, RelPath: cur, Err: errors.New("not a directory")}
		case err == nil:
			// Already there.
		case errors.Is(err, os.ErrNotExist):
			if mkerr := os.Mkdir(full, 0o755); mkerr != nil && !errors.Is(mkerr, os.ErrExist) {
				return artifactErr(cur, mkerr)
			}
		default:
			return artifactErr(cur, err)
		}
	}
	return nil
}

// checkBundleBounds enforces the documented limits before any write.
func checkBundleBounds(entries []synthesizer.BundleEntry) error {
	if len(entries) == 0 {
		return contracts.Coded(contracts.CodeInvalidPayload, "a bundle needs at least one document")
	}
	if len(entries) > MaxBundleEntries {
		return &contracts.ErrBound{Name: "bundle entries", Limit: MaxBundleEntries, Observed: int64(len(entries))}
	}
	var total int64
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.Path == "" {
			return contracts.Coded(contracts.CodeInvalidPayload, "a bundle entry has no path")
		}
		if seen[entry.Path] {
			return contracts.Coded(contracts.CodeInvalidPayload, "two bundle entries claim %s", entry.Path)
		}
		seen[entry.Path] = true
		if len(entry.Body) == 0 {
			return contracts.Coded(contracts.CodeInvalidPayload, "%s has no content", entry.Path)
		}
		if int64(len(entry.Body)) > MaxBundleFileBytes {
			return &contracts.ErrBound{Name: entry.Path, Limit: MaxBundleFileBytes, Observed: int64(len(entry.Body))}
		}
		total += int64(len(entry.Body))
	}
	if total > MaxBundleTotalBytes {
		return &contracts.ErrBound{Name: "bundle total", Limit: MaxBundleTotalBytes, Observed: total}
	}
	return nil
}

// manifestFor is the marker a commit of these entries would publish.
func manifestFor(meta contracts.ApprovalMeta, entries []synthesizer.BundleEntry) bundleManifest {
	manifest := bundleManifest{
		SchemaVersion: contracts.SchemaVersion,
		ApprovalID:    meta.ApprovalID,
		CreatedAt:     meta.CreatedAt.UTC(),
		Entries:       make([]bundleManifestEntry, 0, len(entries)),
	}
	for _, entry := range entries {
		manifest.Entries = append(manifest.Entries, bundleManifestEntry{Path: entry.Path, SHA256: entry.SHA256})
	}
	sort.SliceStable(manifest.Entries, func(i, j int) bool { return manifest.Entries[i].Path < manifest.Entries[j].Path })
	return manifest
}

// sameBundleManifest reports whether two markers describe the same commit. The
// creation time is ignored: a re-run of the same set is the same commit.
func sameBundleManifest(a, b bundleManifest) bool {
	if a.ApprovalID != b.ApprovalID || len(a.Entries) != len(b.Entries) {
		return false
	}
	for i := range a.Entries {
		if a.Entries[i] != b.Entries[i] {
			return false
		}
	}
	return true
}

func bundleResult(manifest bundleManifest, approval string) BundleCommit {
	paths := make([]string, 0, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		paths = append(paths, entry.Path)
	}
	return BundleCommit{
		Committed:    paths,
		ManifestPath: PlanDirName + "/" + BundleDirName + "/" + approval + "/" + BundleManifestName,
	}
}

// readBundleManifest reads a marker. A missing file is no prior commit; a
// malformed one is an error, because guessing which generation it described is
// how two generations end up mixed.
func readBundleManifest(path string) (bundleManifest, bool, error) {
	raw, err := readBounded(path, MaxBundleTotalBytes)
	if errors.Is(err, os.ErrNotExist) {
		return bundleManifest{}, false, nil
	}
	if err != nil {
		return bundleManifest{}, false, artifactErr(PlanDirName+"/"+BundleDirName, err)
	}
	var manifest bundleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return bundleManifest{}, false, artifactErr(PlanDirName+"/"+BundleDirName,
			fmt.Errorf("the bundle manifest is malformed: %w", err))
	}
	return manifest, true, nil
}
