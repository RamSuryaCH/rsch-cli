//go:build windows

package chatgpt

import "sync"

var refreshMu sync.Mutex

// lock serializes refreshes within this process. Cross-process locking is
// not implemented on Windows.
func (s Store) lock() (func(), error) {
	refreshMu.Lock()
	return refreshMu.Unlock, nil
}
