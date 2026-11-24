package tools

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// verifyFilePathAfterApproval checks that the canonical path shown to the user
// still resolves to the same file after an approval prompt. Content checks alone
// cannot detect a file or parent replaced by a symlink with identical content.
func verifyFilePathAfterApproval(path string) error {
	resolvedPath, err := resolveWorkspaceToolPath(path)
	if err != nil {
		return fmt.Errorf("file path changed after approval was requested: %w", err)
	}
	if resolvedPath != path {
		return fmt.Errorf("file path changed after approval was requested; read it again before modifying it")
	}
	return nil
}

// File record to track when files were read/written
type fileRecord struct {
	sessionID string
	path      string
	readTime  time.Time
	writeTime time.Time
}

type fileRecordKey struct {
	sessionID string
	path      string
}

var (
	fileRecords     = make(map[fileRecordKey]fileRecord)
	fileRecordMutex sync.RWMutex
)

func fileRecordSession(ctx context.Context) string {
	sessionID, _ := GetContextValues(ctx)
	return sessionID
}

func recordFileRead(ctx context.Context, path string) {
	fileRecordMutex.Lock()
	defer fileRecordMutex.Unlock()

	key := fileRecordKey{sessionID: fileRecordSession(ctx), path: path}
	record, exists := fileRecords[key]
	if !exists {
		record = fileRecord{sessionID: key.sessionID, path: path}
	}
	record.readTime = time.Now()
	fileRecords[key] = record
}

func getLastReadTime(ctx context.Context, path string) time.Time {
	fileRecordMutex.RLock()
	defer fileRecordMutex.RUnlock()

	record, exists := fileRecords[fileRecordKey{sessionID: fileRecordSession(ctx), path: path}]
	if !exists {
		return time.Time{}
	}
	return record.readTime
}

func recordFileWrite(ctx context.Context, path string) {
	fileRecordMutex.Lock()
	defer fileRecordMutex.Unlock()

	key := fileRecordKey{sessionID: fileRecordSession(ctx), path: path}
	record, exists := fileRecords[key]
	if !exists {
		record = fileRecord{sessionID: key.sessionID, path: path}
	}
	record.writeTime = time.Now()
	fileRecords[key] = record
}
