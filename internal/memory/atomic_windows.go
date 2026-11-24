//go:build windows

package memory

import "golang.org/x/sys/windows"

func replaceFile(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func syncDirectory(string) error {
	// Windows MoveFileEx with MOVEFILE_WRITE_THROUGH flushes the replacement.
	// Opening directories for fsync requires different privileges and is not
	// portable across supported Windows configurations.
	return nil
}
