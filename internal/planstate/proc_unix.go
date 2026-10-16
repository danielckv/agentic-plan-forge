//go:build client && !server && !windows

package planstate

import (
	"os"
	"os/exec"
	"syscall"
)

// procGroup is a started command and every process it spawns.
type procGroup struct{ process *os.Process }

// startGroup starts cmd as the leader of its own process group, so that killing
// the group ends the whole tree.
func startGroup(cmd *exec.Cmd) (*procGroup, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &procGroup{process: cmd.Process}, nil
}

// Kill ends the command and its descendants.
func (g *procGroup) Kill() {
	if g == nil || g.process == nil {
		return
	}
	_ = syscall.Kill(-g.process.Pid, syscall.SIGKILL)
	_ = g.process.Kill()
}

// Close releases the group.
func (g *procGroup) Close() {}
