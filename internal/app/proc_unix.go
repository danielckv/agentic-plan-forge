//go:build client && !server && !windows

package app

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// detachAttrs puts the child in its own session and process group so it
// survives the host tool call that started it ending.
func detachAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// killGroup stops the child and everything it started. The child leads its own
// session, so its pid is also its process-group id.
func killGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

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
