//go:build client && !server && !windows

package tui

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// notifyResizePlatform installs the SIGWINCH watcher. A terminal reports a size
// change as this signal; polling the size would redraw forever on a terminal
// that never changes, and miss a change until the next tick on one that does.
func notifyResizePlatform(ch chan os.Signal) {
	signal.Notify(ch, syscall.SIGWINCH)
}

// Keep the sync import used when the platform helper is the only user in a
// build that never calls it.
var _ = sync.Once{}
