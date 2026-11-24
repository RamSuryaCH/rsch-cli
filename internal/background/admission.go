package background

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

const admissionLockName = ".admission.lock"

// Each CLI invocation and HTTP request can create its own Manager. An OS file
// lock keeps the count-and-start operation atomic across all of those managers.
// Keep the lock file in place: removing it would let waiters lock an old inode
// while a new caller locks a replacement. Closing the file releases the lock.
func (m *Manager) acquireAdmission(ctx context.Context) (func(), error) {
	directory, err := m.openRoot()
	if err != nil {
		return nil, err
	}
	info, err := directory.Lstat(admissionLockName)
	if err == nil && !info.Mode().IsRegular() {
		_ = directory.Close()
		return nil, errors.New("background admission lock must be a regular file")
	}
	if err != nil && !os.IsNotExist(err) {
		_ = directory.Close()
		return nil, err
	}
	file, err := directory.OpenFile(admissionLockName, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if os.IsExist(err) {
		file, err = directory.OpenFile(admissionLockName, os.O_RDWR, 0)
	}
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = file.Close()
			_ = directory.Close()
		})
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		release()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("background admission lock must be a regular file")
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		locked, err := tryAdmissionLock(file)
		if err != nil {
			release()
			return nil, fmt.Errorf("lock background admission: %w", err)
		}
		if locked {
			current, err := directory.Lstat(admissionLockName)
			if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
				release()
				return nil, errors.New("background admission lock changed while acquiring it")
			}
			return release, nil
		}
		select {
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
