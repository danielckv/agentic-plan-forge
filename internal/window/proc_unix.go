//go:build client && !server && !windows

package window

import (
	"os/exec"
	"syscall"
	"time"
)

// setProcAttr puts the browser in its own process group so terminating it also
// reaches the helper processes it spawns.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate asks the browser's process group to exit, then kills it when it has
// not exited within grace. done is the channel fed by cmd.Wait.
func terminate(cmd *exec.Cmd, grace time.Duration, done <-chan error) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-done:
		return
	case <-time.After(grace):
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	select {
	case <-done:
	case <-time.After(grace):
	}
}
