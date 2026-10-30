//go:build client && !server && windows

package tui

import "os"

// notifyResizePlatform is a no-op on Windows: the console API reports no signal
// for a size change, and the redraw tick already covers it.
func notifyResizePlatform(_ chan os.Signal) {}
