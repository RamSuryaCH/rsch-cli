//go:build !windows

package background

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// processIsZombie reports whether pid has exited but has not been reaped yet.
// kill(pid, 0) still succeeds for such a process and Linux ps keeps its
// original lstart and comm, so without this check a stopped job whose parent
// has not called wait(2) looks like it is still running.
func processIsZombie(pid int) bool {
	if pid <= 0 {
		return false
	}
	for _, candidate := range []string{"/bin/ps", "/usr/bin/ps"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			output, err := exec.Command(candidate, "-p", strconv.Itoa(pid), "-o", "stat=").Output()
			if err != nil {
				return false
			}
			return strings.HasPrefix(strings.TrimSpace(string(output)), "Z")
		}
	}
	return false
}

func backgroundProcessIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid process id")
	}
	// lstart identifies the process incarnation while comm helps detect a
	// replacement executable. Unlike command, comm does not persist prompts or
	// other potentially sensitive arguments in the job metadata.
	psPath := ""
	for _, candidate := range []string{"/bin/ps", "/usr/bin/ps"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			psPath = candidate
			break
		}
	}
	if psPath == "" {
		return "", errors.New("system ps executable is unavailable")
	}
	output, err := exec.Command(psPath, "-p", strconv.Itoa(pid), "-o", "lstart=", "-o", "comm=").Output()
	if err != nil {
		return "", err
	}
	identity := strings.Join(strings.Fields(string(output)), " ")
	if identity == "" {
		return "", fmt.Errorf("process %d has no inspectable identity", pid)
	}
	return identity, nil
}

func replacePersistedFile(source, target string) error {
	return os.Rename(source, target)
}
