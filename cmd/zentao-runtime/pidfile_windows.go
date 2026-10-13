//go:build windows

package main

import "golang.org/x/sys/windows"

// stillActive is the STILL_ACTIVE exit code returned by GetExitCodeProcess
// for a running process.
const stillActive = 259

// processAlive reports whether a local process with the given PID exists.
func processAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)

	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == stillActive
}
