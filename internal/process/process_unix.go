//go:build !windows

package process

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func configure(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func attach(_ *exec.Cmd, _ bool) (func(), error) { return nil, nil }

func terminate(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	// Kill the process group so a shell-less tool that starts a child cannot
	// survive a request timeout.
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return command.Process.Kill()
	}
	return nil
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func interrupt(pid int) error {
	if pid <= 0 {
		return syscall.ESRCH
	}
	// Background jobs are started in their own process group by the shared
	// process boundary, so interrupt the group to avoid orphaned children.
	if err := syscall.Kill(-pid, syscall.SIGINT); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return err
	}
	return nil
}

func terminatePID(pid int) error {
	if pid <= 0 {
		return syscall.ESRCH
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return syscall.Kill(pid, syscall.SIGKILL)
	}
	return nil
}

func interruptChildren(pid int) error {
	if pid <= 0 {
		return syscall.ESRCH
	}
	output, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	if err != nil {
		return err
	}
	for _, value := range strings.Split(string(output), "\n") {
		child, scanErr := strconv.Atoi(strings.TrimSpace(value))
		if scanErr != nil || child <= 0 {
			continue
		}
		if process, findErr := os.FindProcess(child); findErr == nil {
			_ = process.Signal(os.Interrupt)
		}
	}
	return nil
}
