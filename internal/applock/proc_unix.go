//go:build client && !server && !windows

package applock

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func dirOf(path string) string { return filepath.Dir(path) }

// PIDAlive reports whether a process with that id exists. A permission error
// still means the process exists.
func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM) || errors.Is(err, os.ErrPermission)
}

// lockGuardFile takes the cross-process advisory lock on the file at path,
// creating it when missing. It blocks until the lock is free, so it serialises
// the stale-lock takeover and two contenders never both replace a dead holder.
func lockGuardFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
