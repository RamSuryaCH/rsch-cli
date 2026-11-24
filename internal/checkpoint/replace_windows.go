//go:build windows

package checkpoint

import (
	"os"
	"path/filepath"
)

func replaceFile(source, target string) error {
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		return os.Rename(source, target)
	} else if err != nil {
		return err
	}
	backup, err := os.CreateTemp(filepath.Dir(target), ".rsch-restore-backup-*")
	if err != nil {
		return err
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return err
	}
	if err := os.Rename(target, backupPath); err != nil {
		return err
	}
	if err := os.Rename(source, target); err != nil {
		if restoreErr := os.Rename(backupPath, target); restoreErr != nil {
			return &replaceError{replace: err, restore: restoreErr}
		}
		return err
	}
	return os.Remove(backupPath)
}

type replaceError struct {
	replace error
	restore error
}

func (e *replaceError) Error() string {
	return "replace failed: " + e.replace.Error() + "; backup restore failed: " + e.restore.Error()
}

func (e *replaceError) Unwrap() error {
	return e.replace
}
