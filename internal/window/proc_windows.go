//go:build client && !server && windows

package window

import (
	"os/exec"
	"syscall"
)

const createNewProcessGroup = 0x00000200

// setProcAttr starts the opener in a new process group so a console Ctrl-C in
// the parent does not close the tab behind the user's back.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}
