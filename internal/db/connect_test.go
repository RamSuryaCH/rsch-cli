package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenReadOnlyAtDoesNotCreateMissingDatabase(t *testing.T) {
	root := t.TempDir()
	if _, err := OpenReadOnlyAt(filepath.Join(root, "state")); err == nil {
		t.Fatal("expected an uninitialized database error")
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatalf("read-only open created storage: %v", err)
	}
}

func TestConnectAtTightensStatePermissionsAndRejectsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits and symbolic links are platform-specific")
	}
	root := t.TempDir()
	state := filepath.Join(root, ".rsch")
	if err := os.Mkdir(state, 0o755); err != nil {
		t.Fatal(err)
	}
	database, err := ConnectAt(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS permission_probe(id INTEGER); INSERT INTO permission_probe(id) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Stat(state)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(filepath.Join(state, "rsch.db"))
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("state directory permissions = %o, want 700", got)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("database permissions = %o, want 600", got)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		info, err := os.Stat(filepath.Join(state, "rsch.db") + suffix)
		if err != nil {
			t.Fatalf("stat %s: %v", suffix, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s permissions = %o, want 600", suffix, got)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(state, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(state, "rsch.db"), 0o666); err != nil {
		t.Fatal(err)
	}
	database, err = ConnectAt(state)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if info, err := os.Stat(state); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("existing state directory was not tightened: info=%v err=%v", info, err)
	}
	if info, err := os.Stat(filepath.Join(state, "rsch.db")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("existing database was not tightened: info=%v err=%v", info, err)
	}

	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "state-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ConnectAt(link); err == nil {
		t.Fatal("expected symbolic-link data directory rejection")
	}
}

func TestConnectAtRejectsDatabaseAndSidecarSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links are platform-specific")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	stateWithDatabaseLink := filepath.Join(root, "db-link-state")
	if err := os.Mkdir(stateWithDatabaseLink, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(stateWithDatabaseLink, "rsch.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := ConnectAt(stateWithDatabaseLink); err == nil {
		t.Fatal("expected symbolic-link database rejection")
	}

	stateWithWALLink := filepath.Join(root, "wal-link-state")
	if err := os.Mkdir(stateWithWALLink, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(stateWithWALLink, "rsch.db-wal")); err != nil {
		t.Fatal(err)
	}
	if _, err := ConnectAt(stateWithWALLink); err == nil {
		t.Fatal("expected symbolic-link WAL rejection")
	}
}

func TestOpenReadOnlyAtReadsInitializedDatabase(t *testing.T) {
	root := t.TempDir()
	writer, err := ConnectAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenReadOnlyAt(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var tables int
	if err := reader.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables == 0 {
		t.Fatal("expected initialized tables")
	}
}

func TestOpenReadOnlyAtEscapesDatabasePath(t *testing.T) {
	for _, directory := range []string{"state?mode=rw", "state#fragment", "state%encoded"} {
		t.Run(directory, func(t *testing.T) {
			if runtime.GOOS == "windows" && directory == "state?mode=rw" {
				t.Skip("question marks are not valid Windows path characters")
			}
			root := filepath.Join(t.TempDir(), directory)
			writer, err := ConnectAt(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}

			reader, err := OpenReadOnlyAt(root)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			var count int
			if err := reader.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if _, err := reader.Exec(`CREATE TABLE readonly_probe(id INTEGER)`); err == nil {
				t.Fatal("read-only database accepted a write")
			}
		})
	}
}

func TestConnectAtPreservesForeignKeysAfterConnectionReplacement(t *testing.T) {
	database, err := ConnectAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	queries := New(database)
	if _, err := queries.CreateSession(ctx, CreateSessionParams{ID: "parent", Title: "parent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateMessage(ctx, CreateMessageParams{ID: "child", SessionID: "parent", Role: "user", Parts: "[]"}); err != nil {
		t.Fatal(err)
	}

	// Drop the initialized physical connection so the next query uses a new one.
	database.SetMaxIdleConns(0)
	if open := database.Stats().OpenConnections; open != 0 {
		t.Fatalf("open connections = %d, want 0 before replacement", open)
	}
	database.SetMaxIdleConns(1)
	var enabled int
	if err := database.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Error("foreign keys are disabled on the replacement connection")
	}
	if _, err := queries.CreateMessage(ctx, CreateMessageParams{ID: "orphan", SessionID: "missing", Role: "user", Parts: "[]"}); err == nil {
		t.Error("replacement connection allowed an orphaned message")
	}
	if err := queries.DeleteSession(ctx, "parent"); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.GetMessage(ctx, "child"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("replacement connection did not cascade message deletion: %v", err)
	}
}
