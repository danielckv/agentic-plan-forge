//go:build client && !server && windows

package host

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processGroup struct {
	process *windows.Handle
	job     windows.Handle
}

func startProcessGroup(cmd *exec.Cmd) (*processGroup, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = cmd.Process.Kill()
		windows.CloseHandle(job)
		return nil, err
	}
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		windows.CloseHandle(process)
		_ = cmd.Process.Kill()
		windows.CloseHandle(job)
		return nil, err
	}
	return &processGroup{process: &process, job: job}, nil
}

func (g *processGroup) Kill() error {
	if g == nil || g.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(g.job, 1)
}

func (g *processGroup) Close() error {
	if g == nil {
		return nil
	}
	if g.process != nil && *g.process != 0 {
		_ = windows.CloseHandle(*g.process)
		*g.process = 0
	}
	if g.job != 0 {
		err := windows.CloseHandle(g.job)
		g.job = 0
		return err
	}
	return nil
}
