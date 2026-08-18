//go:build windows

package mysql

import (
	"os"
	"os/exec"

	platformwindows "github.com/vimrus/runtime/internal/platform/windows"
	syswindows "golang.org/x/sys/windows"
)

// Windows child isolation uses a Job Object assigned by the Windows service
// wrapper; the supervisor keeps the process handle so the service can kill
// the tree when the Job Object is closed.
func setProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = nil
}

func afterStart(command *exec.Cmd) (func(), error) {
	if command.Process == nil {
		return nil, nil
	}
	job, err := platformwindows.NewJobObject()
	if err != nil {
		return nil, err
	}
	processHandle, err := openProcessForJob(uint32(command.Process.Pid))
	if err != nil {
		_ = job.Close()
		return nil, err
	}
	if err := job.Assign(processHandle); err != nil {
		_ = syswindows.CloseHandle(processHandle)
		_ = job.Close()
		return nil, err
	}
	_ = syswindows.CloseHandle(processHandle)
	return func() {
		_ = job.Terminate(1)
		_ = job.Close()
	}, nil
}

func openProcessForJob(pid uint32) (syswindows.Handle, error) {
	return syswindows.OpenProcess(syswindows.PROCESS_SET_QUOTA|syswindows.PROCESS_TERMINATE, false, pid)
}

func stopProcess(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	return command.Process.Signal(os.Interrupt)
}

func killProcessGroup(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	_ = command.Process.Kill()
}
