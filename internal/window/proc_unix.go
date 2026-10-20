//go:build client && !server && !windows

package window

import (
	"os/exec"
	"syscall"
)

// setProcAttr puts the opener in its own process group, so a Ctrl-C in the
// terminal that started the session does not also reach the browser the opener
// handed the URL to.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
