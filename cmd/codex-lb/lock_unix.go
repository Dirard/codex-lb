//go:build unix

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Lock the database inode, not a removable PID marker or an unrelated lock path.
// Kernel releases the lock even after SIGKILL or a host restart.
func lockDatabase(path string) (*os.File, bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	created := err == nil
	if errors.Is(err, os.ErrExist) {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, false, errors.New("database must be a private regular file (mode 0600)")
		}
		file, err = os.OpenFile(path, os.O_RDWR, 0)
		if err == nil {
			opened, statErr := file.Stat()
			if statErr != nil || !os.SameFile(info, opened) {
				file.Close()
				return nil, false, errors.New("database changed while opening")
			}
		}
	}
	if err != nil {
		return nil, false, errors.New("cannot open private database")
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, false, errors.New("database is already in use; only one codex-lb process may own it")
	}
	return file, created, nil
}
