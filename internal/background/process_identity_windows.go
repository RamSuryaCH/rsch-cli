//go:build windows

package background

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// processIsZombie is a Unix concept; Windows process handles report exit
// directly, so an exited process never matches its identity there.
func processIsZombie(pid int) bool { return false }

func backgroundProcessIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid process id")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return "", err
	}
	return fmt.Sprintf("%08x%08x", created.HighDateTime, created.LowDateTime), nil
}

func replacePersistedFile(source, target string) error {
	return windows.Rename(source, target)
}
