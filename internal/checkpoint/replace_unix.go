//go:build !windows

package checkpoint

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}
