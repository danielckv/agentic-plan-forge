package storage

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// WriteFileAtomic replaces path with data: same-directory temporary file, fsync,
// atomic rename and a directory fsync. The directory must already exist. It is
// for files outside a repository's .plan/ (the approval ledger, notices); inside
// a repository use the stores, which also reject symlink components.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	return writeFileAtomic(path, data, perm)
}

// ReadFileBounded reads a regular, non-symlink file of at most max bytes.
func ReadFileBounded(path string, max int64) ([]byte, error) { return readBounded(path, max) }

// LockFile takes an exclusive advisory lock on path (creating it, mode 0600),
// polling until ctx ends or ten seconds pass. The returned function releases it.
func LockFile(ctx context.Context, path string) (func(), error) {
	deadline := time.Now().Add(lockTimeout)
	for {
		unlock, err := tryLock(path)
		if err == nil {
			return unlock, nil
		}
		if err != errLockHeld {
			return nil, artifactErr(filepath.Base(path), err)
		}
		if cerr := ctx.Err(); cerr != nil {
			return nil, contracts.Wrap(contracts.CodeLocked, cerr, "%s is held by another process", filepath.Base(path))
		}
		if time.Now().After(deadline) {
			return nil, contracts.Coded(contracts.CodeLocked, "%s is held by another process", filepath.Base(path))
		}
		select {
		case <-ctx.Done():
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// SHA256Hex returns the lower-case hex SHA-256 of b.
func SHA256Hex(b []byte) string { return sum(b) }
