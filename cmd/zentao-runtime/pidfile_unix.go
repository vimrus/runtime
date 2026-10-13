//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// processAlive reports whether a local process with the given PID exists.
// Signal 0 performs the permission and existence checks without delivering a
// signal; EPERM means the process exists but belongs to another user.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
