//go:build !windows

package mysql

import (
	"os/exec"
	"syscall"
)

func setProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func afterStart(*exec.Cmd) (func(), error) {
	return nil, nil
}

func stopProcess(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	return command.Process.Signal(syscall.SIGTERM)
}

func killProcessGroup(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}
