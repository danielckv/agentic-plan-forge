//go:build client && !server && windows

package window

import (
	"os/exec"
	"syscall"
	"time"
)

const createNewProcessGroup = 0x00000200

// setProcAttr starts the browser in a new process group so a console Ctrl-C in
// the parent does not close the window behind the user's back.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// terminate kills the browser process. Windows has no portable graceful signal
// for a GUI process, and the throwaway profile holds nothing worth flushing.
func terminate(cmd *exec.Cmd, grace time.Duration, done <-chan error) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(grace):
	}
}
