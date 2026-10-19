//go:build client && !server && windows

package app

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockBoardGuard takes the cross-process exclusive lock on the file at path,
// creating it when missing. LockFileEx without LOCKFILE_FAIL_IMMEDIATELY waits
// for the holder to release the byte range. It serialises the stale-lock
// takeover of the board lock, so two contenders never both replace a dead
// holder.
func lockBoardGuard(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	var ol windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &ol); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
		_ = f.Close()
	}, nil
}
