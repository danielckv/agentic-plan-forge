// Package storage persists Plan-Forge state inside a repository's `.plan/`
// directory. Every path is checked for symlink components and traversal, every
// writer takes `.plan/lock`, and the approved spec/manifest pair is replaced
// crash-safely: after any interruption a reader sees the old pair or the new
// pair, never a mix of the two.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// File names inside .plan/.
const (
	PlanDirName      = ".plan"
	SpecFileName     = "synthesized_spec.md"
	ManifestFileName = "plan_manifest.json"
	StateFileName    = "state.json"
	LockFileName     = "lock"
	GitignoreName    = ".gitignore"
	TransactionsDir  = "transactions"
	HistoryDir       = "history"
	VerificationDir  = "verification"
	CacheDir         = "cache"
)

// PlanGitignore is the exact content of .plan/.gitignore: default-deny inside a
// user's repository, with the two approved artifacts left trackable.
const PlanGitignore = "*\n!.gitignore\n!synthesized_spec.md\n!plan_manifest.json\n"

// HistoryKeep is how many prior approved pairs are retained.
const HistoryKeep = 5

// Artifact error codes mirror relay.ArtifactEACCES and friends. They are
// redeclared here so storage never imports the relay.
const (
	CodeEACCES   = "EACCES"
	CodeENOSPC   = "ENOSPC"
	CodeEEXIST   = "EEXIST"
	CodeEJOURNAL = "EJOURNAL"
	CodeEHASH    = "EHASH"
	CodeESYMLINK = "ESYMLINK"
	CodeEOTHER   = "EOTHER"
)

// ArtifactError describes a failed persistence step without leaking absolute
// paths: RelPath is always relative to the repository root.
type ArtifactError struct {
	Code    string
	RelPath string
	Err     error
}

func (e *ArtifactError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s %s", e.Code, e.RelPath)
	}
	return fmt.Sprintf("%s %s: %v", e.Code, e.RelPath, scrub(e.Err))
}

func (e *ArtifactError) Unwrap() error { return e.Err }

// scrub removes the absolute path that *fs.PathError carries.
func scrub(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return fmt.Errorf("%s: %w", le.Op, le.Err)
	}
	return err
}

// ArtifactCode returns the artifact error code carried by err, or EOTHER.
func ArtifactCode(err error) string {
	var ae *ArtifactError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return CodeEOTHER
}

func classify(err error) string {
	switch {
	case err == nil:
		return CodeEOTHER
	case errors.Is(err, syscall.ENOSPC):
		return CodeENOSPC
	case errors.Is(err, fs.ErrPermission):
		return CodeEACCES
	case errors.Is(err, fs.ErrExist):
		return CodeEEXIST
	}
	return CodeEOTHER
}

func artifactErr(rel string, err error) error {
	var ae *ArtifactError
	if errors.As(err, &ae) {
		return err
	}
	return &ArtifactError{Code: classify(err), RelPath: rel, Err: err}
}

// ErrNoPlan reports that no approved plan exists in the repository.
var ErrNoPlan = errors.New("no approved plan in .plan/")

// ErrPairMismatch reports spec and manifest files whose hashes do not agree.
// Such a pair is never exposed to readers.
var ErrPairMismatch = errors.New(".plan/ spec and manifest do not match")

// planDir is a repository whose .plan/ directory this package may touch.
type planDir struct {
	root string // symlink-resolved absolute repository root
}

// openPlan resolves the repository root once. The root itself may be reached
// through symlinks (for example /tmp on macOS); everything below it may not.
func openPlan(repo string) (*planDir, error) {
	if repo == "" {
		return nil, contracts.Coded(contracts.CodeInvalidPayload, "repository path is empty")
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return nil, artifactErr(".", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, artifactErr(".", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, artifactErr(".", err)
	}
	if !info.IsDir() {
		return nil, contracts.Coded(contracts.CodeInvalidPayload, "repository path is not a directory")
	}
	return &planDir{root: real}, nil
}

func hostile(mode fs.FileMode) bool { return mode&(fs.ModeSymlink|fs.ModeIrregular) != 0 }

// path joins rel (slash-separated, repository-relative) below the root. It
// rejects absolute paths, traversal and any existing component that is a
// symlink or reparse point.
func (p *planDir) path(rel string) (string, error) {
	rel = filepath.FromSlash(rel)
	if !filepath.IsLocal(rel) {
		return "", &ArtifactError{Code: CodeESYMLINK, RelPath: filepath.ToSlash(rel), Err: errors.New("path escapes the repository")}
	}
	cur := p.root
	for _, comp := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, comp)
		info, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return filepath.Join(p.root, rel), nil
		case err != nil:
			return "", artifactErr(filepath.ToSlash(rel), err)
		case hostile(info.Mode()):
			return "", &ArtifactError{Code: CodeESYMLINK, RelPath: filepath.ToSlash(rel), Err: errors.New("a path component is a symbolic link")}
		}
	}
	return filepath.Join(p.root, rel), nil
}

// ensureDir creates a directory below the root, one checked component at a
// time. Creating anything under .plan/ first creates .plan/.gitignore.
func (p *planDir) ensureDir(rel string) (string, error) {
	rel = filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	cur := ""
	for _, comp := range strings.Split(rel, "/") {
		if cur == "" {
			cur = comp
		} else {
			cur = cur + "/" + comp
		}
		full, err := p.path(cur)
		if err != nil {
			return "", err
		}
		info, err := os.Lstat(full)
		switch {
		case err == nil && !info.IsDir():
			return "", &ArtifactError{Code: CodeEEXIST, RelPath: cur, Err: errors.New("not a directory")}
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(full, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return "", artifactErr(cur, err)
			}
		case err != nil:
			return "", artifactErr(cur, err)
		}
		if cur == PlanDirName {
			if err := p.ensureGitignore(); err != nil {
				return "", err
			}
		}
	}
	return p.path(rel)
}

// ensureGitignore creates .plan/.gitignore with the four exact lines when it is
// absent. An existing file is never rewritten: it may carry the user's edits.
func (p *planDir) ensureGitignore() error {
	full, err := p.path(PlanDirName + "/" + GitignoreName)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return artifactErr(PlanDirName+"/"+GitignoreName, err)
	}
	_, werr := f.WriteString(PlanGitignore)
	if serr := f.Sync(); werr == nil {
		werr = serr
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return artifactErr(PlanDirName+"/"+GitignoreName, werr)
	}
	return syncDir(filepath.Dir(full))
}

// writeTemp creates a uniquely named file in dir (O_EXCL, so it never follows a
// planted symlink), writes data, fsyncs and closes it.
func writeTemp(dir, name string, data []byte, perm fs.FileMode) (string, error) {
	f, err := os.CreateTemp(dir, name)
	if err != nil {
		return "", err
	}
	path := f.Name()
	fail := func(err error) (string, error) {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Chmod(perm); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// writeFileAtomic replaces path with data: temporary file in the same
// directory, fsync, atomic rename, directory fsync.
func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := writeTemp(dir, "."+filepath.Base(path)+".tmp-*", data, perm)
	if err != nil {
		return err
	}
	if err := replaceFile(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(dir)
}

// readBounded reads a regular file of at most max bytes.
func readBounded(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if hostile(info.Mode()) || !info.Mode().IsRegular() {
		return nil, &ArtifactError{Code: CodeESYMLINK, RelPath: filepath.Base(path), Err: errors.New("not a regular file")}
	}
	if info.Size() > max {
		return nil, &contracts.ErrBound{Name: filepath.Base(path), Limit: max, Observed: info.Size()}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max+1))
}

// lockTimeout bounds how long a writer waits for .plan/.lock.
const lockTimeout = 10 * time.Second

// lock takes .plan/lock, polling until ctx ends or lockTimeout passes. A held
// lock is reported as LOCKED so callers can show "another Plan-Forge process is
// writing".
func (p *planDir) lock(ctx context.Context) (func(), error) {
	dir, err := p.ensureDir(PlanDirName)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, LockFileName)
	if info, err := os.Lstat(path); err == nil && hostile(info.Mode()) {
		return nil, &ArtifactError{Code: CodeESYMLINK, RelPath: PlanDirName + "/" + LockFileName, Err: errors.New("lock file is a symbolic link")}
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		unlock, err := tryLock(path)
		if err == nil {
			return unlock, nil
		}
		if !errors.Is(err, errLockHeld) {
			return nil, artifactErr(PlanDirName+"/"+LockFileName, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, contracts.Wrap(contracts.CodeLocked, err, ".plan/lock is held by another process")
		}
		if time.Now().After(deadline) {
			return nil, contracts.Coded(contracts.CodeLocked, ".plan/lock is held by another process")
		}
		select {
		case <-ctx.Done():
		case <-time.After(25 * time.Millisecond):
		}
	}
}

var errLockHeld = errors.New("lock held")
