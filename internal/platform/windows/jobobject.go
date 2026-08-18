//go:build windows

package windows

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// JobObject wraps a Windows Job Object with KILL_ON_JOB_CLOSE so every
// assigned child process is terminated when the Runtime Host exits, even on
// an abnormal crash.
type JobObject struct {
	handle windows.Handle
}

func NewJobObject() (*JobObject, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		handle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return &JobObject{handle: handle}, nil
}

func (j *JobObject) Assign(process windows.Handle) error {
	if j == nil || j.handle == 0 {
		return errors.New("job object is not initialized")
	}
	return windows.AssignProcessToJobObject(j.handle, process)
}

func (j *JobObject) Terminate(exitCode uint32) error {
	if j == nil || j.handle == 0 {
		return nil
	}
	return windows.TerminateJobObject(j.handle, exitCode)
}

func (j *JobObject) Close() error {
	if j == nil || j.handle == 0 {
		return nil
	}
	err := windows.CloseHandle(j.handle)
	j.handle = 0
	return err
}
