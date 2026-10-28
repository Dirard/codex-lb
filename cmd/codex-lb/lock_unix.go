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

// verifyDatabaseLock proves that an inherited descriptor is the database file
// and holds its lock: flock on the shared open file description succeeds only
// for the owner, while another installation's lock would refuse it.
func verifyDatabaseLock(file *os.File, path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("database must be a private regular file (mode 0600)")
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("inherited database lock is not the database file")
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("inherited database lock is not held")
	}
	return nil
}
