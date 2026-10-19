//go:build client && !server && !windows

package board

import (
	"os"
	"syscall"
)

// lock takes the cross-process advisory lock that serialises every
// read-modify-write of the overlay. It blocks until the lock is free, so a
// concurrent writer loses no update.
func (s *Store) lock() (func(), error) {
	f, err := os.OpenFile(s.lockPath(), os.O_RDWR|os.O_CREATE, 0o600)
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
