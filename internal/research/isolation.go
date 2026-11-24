package research

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// openDirectory pins the state directory for an entire operation. Rooted IO
// cannot escape the workspace if a path component is concurrently replaced.
func (s *Store) openDirectory(create bool) (*os.Root, error) {
	workspace, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, err
	}
	defer workspace.Close()
	for _, name := range []string{".rsch", filepath.Join(".rsch", "research")} {
		info, err := workspace.Lstat(name)
		if os.IsNotExist(err) && create {
			if err := workspace.Mkdir(name, 0o700); err != nil && !os.IsExist(err) {
				return nil, err
			}
			info, err = workspace.Lstat(name)
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("research state path is not a directory: %s", name)
		}
	}
	directory, err := workspace.OpenRoot(filepath.Join(".rsch", "research"))
	if err != nil {
		return nil, err
	}
	for _, name := range []string{storeFileName, storeFileName + ".bak", storeFileName + ".lock"} {
		if err := validateStoreFile(directory, name); err != nil {
			_ = directory.Close()
			return nil, err
		}
	}
	return directory, nil
}

func validateStoreFile(directory *os.Root, name string) error {
	info, err := directory.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("research state path is not a regular file: %s", name)
	}
	return nil
}

func readStoreFile(directory *os.Root, name string) ([]byte, error) {
	if err := validateStoreFile(directory, name); err != nil {
		return nil, err
	}
	return directory.ReadFile(name)
}

// The stable lock file is deliberately retained. Unlinking it would allow a
// waiting process and a new process to lock different inodes concurrently.
// Closing the handle releases the OS lock, including after a process crash.
func (s *Store) acquireFileLock() (*os.Root, func(), error) {
	directory, err := s.openDirectory(false)
	if err != nil {
		return nil, nil, err
	}
	file, err := directory.OpenFile(storeFileName+".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if os.IsExist(err) {
		file, err = directory.OpenFile(storeFileName+".lock", os.O_RDWR, 0)
	}
	if err != nil {
		_ = directory.Close()
		return nil, nil, err
	}
	release := func() {
		_ = file.Close()
		_ = directory.Close()
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		release()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, errors.New("research lock path is not a regular file")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		locked, err := tryFileLock(file)
		if err != nil {
			release()
			return nil, nil, fmt.Errorf("lock research store: %w", err)
		}
		if locked {
			current, err := directory.Lstat(storeFileName + ".lock")
			if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
				release()
				return nil, nil, errors.New("research lock path changed while acquiring lock")
			}
			return directory, release, nil
		}
		if time.Now().After(deadline) {
			release()
			return nil, nil, errors.New("timed out waiting for research store lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
