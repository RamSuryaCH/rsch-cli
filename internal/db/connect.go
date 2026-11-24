package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/performance"

	"github.com/pressly/goose/v3"
)

func Connect() (*sql.DB, error) {
	dataDir := config.Get().Data.Directory
	if dataDir == "" {
		return nil, fmt.Errorf("data.dir is not set")
	}
	return ConnectAt(dataDir)
}

func ConnectAt(dataDir string) (*sql.DB, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("data directory is not set")
	}
	if err := secureStateDirectory(dataDir); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "rsch.db")
	if err := secureSQLiteStateFiles(dbPath, true); err != nil {
		return nil, err
	}
	// Open the SQLite database
	db, err := performance.OpenSQLite(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Verify connection
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	if err := secureSQLiteStateFiles(dbPath, false); err != nil {
		db.Close()
		return nil, err
	}

	// Set pragmas for better performance
	pragmas := []string{
		"PRAGMA foreign_keys = ON;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA page_size = 4096;",
		"PRAGMA cache_size = -8000;",
		"PRAGMA synchronous = NORMAL;",
	}

	for _, pragma := range pragmas {
		if _, err = db.Exec(pragma); err != nil {
			logging.Error("Failed to set pragma", pragma, err)
		} else {
			logging.Debug("Set pragma", "pragma", pragma)
		}
	}
	if err := secureSQLiteStateFiles(dbPath, false); err != nil {
		db.Close()
		return nil, err
	}

	goose.SetBaseFS(FS)
	goose.SetLogger(goose.NopLogger())

	if err := goose.SetDialect("sqlite3"); err != nil {
		logging.Error("Failed to set dialect", "error", err)
		db.Close()
		return nil, fmt.Errorf("failed to set dialect: %w", err)
	}

	if err := goose.Up(db, "migrations"); err != nil {
		logging.Error("Failed to apply migrations", "error", err)
		db.Close()
		return nil, fmt.Errorf("failed to apply migrations: %w", err)
	}
	if err := secureSQLiteStateFiles(dbPath, false); err != nil {
		db.Close()
		return nil, err
	}
	if err := performance.AttachSQLiteTrace(db); err != nil {
		logging.Warn("SQLite query tracing unavailable", "error", err)
	}
	return db, nil
}

func secureStateDirectory(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("failed to create data directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect data directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("data directory cannot be a symbolic link")
	}
	if !info.IsDir() {
		return fmt.Errorf("data directory is not a directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("failed to secure data directory: %w", err)
	}
	return nil
}

func secureSQLiteStateFiles(dbPath string, createDatabase bool) error {
	paths := []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + "-journal"}
	for index, path := range paths {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) && index == 0 && createDatabase {
			file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			if createErr != nil {
				return fmt.Errorf("create database file securely: %w", createErr)
			}
			if closeErr := file.Close(); closeErr != nil {
				return fmt.Errorf("close database file: %w", closeErr)
			}
			info, err = os.Lstat(path)
		}
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect database state file: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("database state file cannot be a symbolic link: %s", filepath.Base(path))
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("database state file is not regular: %s", filepath.Base(path))
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("failed to secure database state file %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

// OpenReadOnlyAt opens an already initialized database without creating the
// directory, database file, or applying migrations. It is intended for
// inspection commands that promise not to mutate workspace state.
func OpenReadOnlyAt(dataDir string) (*sql.DB, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("data directory is not set")
	}
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve data directory: %w", err)
	}
	if info, err := os.Lstat(absolute); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("data directory cannot be a symbolic link")
	} else if err != nil {
		return nil, fmt.Errorf("database is not initialized: %w", err)
	}
	dbPath := filepath.Join(absolute, "rsch.db")
	if info, err := os.Lstat(dbPath); err != nil {
		return nil, fmt.Errorf("database is not initialized: %w", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("database file cannot be a symbolic link")
	}

	dataSource := url.URL{Scheme: "file", Path: filepath.ToSlash(dbPath), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite3", dataSource.String())
	if err != nil {
		return nil, fmt.Errorf("failed to open read-only database: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to read-only database: %w", err)
	}
	return db, nil
}
