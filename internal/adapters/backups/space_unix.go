//go:build unix

package backups

import "golang.org/x/sys/unix"

func filesystemSpace(path string) (free, total uint64, err error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	size := uint64(stat.Bsize)
	return stat.Bavail * size, stat.Blocks * size, nil
}
