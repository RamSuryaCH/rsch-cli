//go:build windows

package process

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func configure(_ *exec.Cmd) {}

// attach places every authorized process in a native Job Object. Closing the
// job handle after Wait terminates any descendants that outlive the parent;
// this is the Windows equivalent of the Unix process-group boundary.
func attach(command *exec.Cmd, detached bool) (func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	closeJob := func() { _ = windows.CloseHandle(job) }
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	if !detached {
		limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		closeJob()
		return nil, err
	}
	processHandle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(command.Process.Pid))
	if err != nil {
		closeJob()
		return nil, err
	}
	defer windows.CloseHandle(processHandle)
	if err := windows.AssignProcessToJobObject(job, processHandle); err != nil {
		closeJob()
		return nil, err
	}
	return closeJob, nil
}

func terminate(command *exec.Cmd) error {
	if command.Process != nil {
		return command.Process.Kill()
	}
	return nil
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	result, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(result), `,"`+strconv.Itoa(pid)+`",`)
}

func interrupt(pid int) error {
	if pid <= 0 {
		return errors.New("invalid process id")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	// Windows has no portable SIGINT equivalent for arbitrary processes;
	// Signal(os.Interrupt) is the best graceful request and Terminate below
	// remains the bounded fallback.
	return process.Signal(os.Interrupt)
}

func terminatePID(pid int) error {
	if pid <= 0 {
		return errors.New("invalid process id")
	}
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}

func interruptChildren(pid int) error {
	if pid <= 0 {
		return errors.New("invalid process id")
	}
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}
