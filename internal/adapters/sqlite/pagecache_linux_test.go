//go:build linux

package sqlite

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// dropPageCache evicts files from the OS page cache after flushing them, so
// the next reads come from disk. It reports whether the platform supports it.
func dropPageCache(paths ...string) (bool, error) {
	for _, path := range paths {
		file, err := os.Open(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		err = file.Sync()
		if err == nil {
			err = unix.Fadvise(int(file.Fd()), 0, 0, unix.FADV_DONTNEED)
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return false, err
		}
	}
	return true, nil
}
