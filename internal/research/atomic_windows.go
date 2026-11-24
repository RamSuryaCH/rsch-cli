//go:build windows

package research

import "os"

func syncDirectory(*os.Root) error {
	// Windows does not support syncing directory handles through os.File.Sync.
	// The temporary file's contents are synced before the rooted replacement.
	return nil
}
