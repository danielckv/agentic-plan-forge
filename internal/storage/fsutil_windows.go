//go:build windows

package storage

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// replaceFile atomically replaces dst with src using MoveFileEx with
// MOVEFILE_REPLACE_EXISTING and MOVEFILE_WRITE_THROUGH, which is the Windows
// primitive for an atomic same-volume replace that reaches disk.
func replaceFile(src, dst string) error {
	from, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// syncDir is a no-op: Windows cannot fsync a directory handle, and
// MOVEFILE_WRITE_THROUGH already flushed the rename.
func syncDir(string) error { return nil }

// tryLock takes an exclusive, non-blocking lock on path with LockFileEx.
func tryLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		_ = f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, errLockHeld
		}
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(h, 0, 1, 0, ol)
		_ = f.Close()
	}, nil
}
