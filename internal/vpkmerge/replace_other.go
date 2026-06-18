//go:build !windows

package vpkmerge

import "os"

func replaceFileAtomic(source, destination string) error {
	return os.Rename(source, destination)
}
