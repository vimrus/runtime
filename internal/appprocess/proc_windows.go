//go:build windows

package appprocess

import (
	"os/exec"

	platformwindows "github.com/vimrus/runtime/internal/platform/windows"
	syswindows "golang.org/x/sys/windows"
)

func prepareCommand(*exec.Cmd) {}

func afterStart(command *exec.Cmd) (func(), error) {
	if command.Process == nil {
		return nil, nil
	}
	job, err := platformwindows.NewJobObject()
	if err != nil {
		return nil, err
	}
	processHandle, err := syswindows.OpenProcess(syswindows.PROCESS_SET_QUOTA|syswindows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
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
		_ = job.Close()
	}, nil
}

func killProcessTree(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Kill()
}
