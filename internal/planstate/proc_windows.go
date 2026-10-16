//go:build client && !server && windows

package planstate

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procGroup is a started command and every process it spawns, held in a job
// object that kills the tree when closed.
type procGroup struct {
	process windows.Handle
	job     windows.Handle
}

func startGroup(cmd *exec.Cmd) (*procGroup, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
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
	return &procGroup{process: process, job: job}, nil
}

// Kill ends the command and its descendants.
func (g *procGroup) Kill() {
	if g == nil || g.job == 0 {
		return
	}
	_ = windows.TerminateJobObject(g.job, 1)
}

// Close releases the handles.
func (g *procGroup) Close() {
	if g == nil {
		return
	}
	if g.process != 0 {
		_ = windows.CloseHandle(g.process)
		g.process = 0
	}
	if g.job != 0 {
		_ = windows.CloseHandle(g.job)
		g.job = 0
	}
}
