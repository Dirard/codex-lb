//go:build !unix

package backups

import "errors"

func filesystemSpace(string) (free, total uint64, err error) {
	return 0, 0, errors.New("filesystem space is unavailable on this platform")
}
