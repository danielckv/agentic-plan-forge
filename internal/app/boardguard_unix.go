//go:build client && !server && !windows

package app

import (
	"os"
	"syscall"
)

// lockBoardGuard takes the cross-process advisory lock on the file at path,
// creating it when missing. It blocks until the lock is free. It serialises the
// stale-lock takeover of the board lock, so two contenders never both replace a
// dead holder.
func lockBoardGuard(path string) (func(), error) {
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
