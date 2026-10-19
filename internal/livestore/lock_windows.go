//go:build client && !server && windows

package livestore

import (
	"os"

	"golang.org/x/sys/windows"
)

// lock takes the cross-process exclusive lock that serialises every
// read-modify-write of the document. LockFileEx without
// LOCKFILE_FAIL_IMMEDIATELY waits for the holder to release the byte range, so
// a concurrent writer loses no update.
func (s *Store) lock() (func(), error) {
	f, err := os.OpenFile(s.lockPath(), os.O_RDWR|os.O_CREATE, 0o600)
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
