package config

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	// InitFlagFilename is the name of the file that indicates whether the project has been initialized
	InitFlagFilename = "init"
)

// ProjectInitFlag represents the initialization status for a project directory
type ProjectInitFlag struct {
	Initialized bool `json:"initialized"`
}

// ShouldShowInitDialog checks if the initialization dialog should be shown for the current directory
func ShouldShowInitDialog() (bool, error) {
	if cfg == nil {
		return false, fmt.Errorf("config not loaded")
	}

	// Create the flag file path
	flagFilePath := filepath.Join(cfg.Data.Directory, InitFlagFilename)

	// Check if the flag file exists
	_, err := os.Stat(flagFilePath)
	if err == nil {
		// File exists, don't show the dialog
		return false, nil
	}

	// If the error is not "file not found", return the error
	if !os.IsNotExist(err) {
		return false, fmt.Errorf("failed to check init flag file: %w", err)
	}

	// File doesn't exist, show the dialog
	return true, nil
}

// MarkProjectInitialized marks the current project as initialized
func MarkProjectInitialized() error {
	if cfg == nil {
		return fmt.Errorf("config not loaded")
	}
	// Create the flag file path
	flagFilePath := filepath.Join(cfg.Data.Directory, InitFlagFilename)

	if info, err := os.Lstat(cfg.Data.Directory); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("data directory cannot be a symbolic link")
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to inspect data directory: %w", err)
	}
	if err := os.MkdirAll(cfg.Data.Directory, 0o700); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}
	if err := os.Chmod(cfg.Data.Directory, 0o700); err != nil {
		return fmt.Errorf("failed to secure data directory: %w", err)
	}
	// Create an owner-only file to mark the project as initialized.
	if info, err := os.Lstat(flagFilePath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("init flag file cannot be a symbolic link")
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to inspect init flag file: %w", err)
	}
	file, err := os.OpenFile(flagFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create init flag file: %w", err)
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("failed to secure init flag file: %w", err)
	}

	return nil
}
