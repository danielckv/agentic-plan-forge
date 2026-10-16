//go:build client && !server && !windows

package host

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type processGroup struct {
	process *os.Process
}

func startProcessGroup(cmd *exec.Cmd) (*processGroup, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &processGroup{process: cmd.Process}, nil
}

func (g *processGroup) Kill() error {
	if g == nil || g.process == nil {
		return nil
	}
	if err := syscall.Kill(-g.process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return g.process.Kill()
	}
	return nil
}

func (g *processGroup) Close() error { return nil }
