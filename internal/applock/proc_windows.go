//go:build client && !server && windows

package applock

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

const (
	stillActive         = 259
	processQueryLimited = 0x1000
)

func dirOf(path string) string { return filepath.Dir(path) }

// PIDAlive reports whether a process with that id is still running.
func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimited, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// lockGuardFile takes the cross-process exclusive lock on the file at path.
// LockFileEx without LOCKFILE_FAIL_IMMEDIATELY waits for the holder to release
// the byte range, so it serialises the stale-lock takeover.
func lockGuardFile(path string) (func(), error) {
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
