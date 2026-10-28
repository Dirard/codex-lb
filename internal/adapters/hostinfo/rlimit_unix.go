//go:build unix

package hostinfo

import "syscall"

// fileDescriptorLimit reports the soft descriptor limit; the Go runtime has
// already raised it to the hard limit at process start.
func fileDescriptorLimit() int64 {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil || limit.Cur > 1<<40 {
		return 0
	}
	return int64(limit.Cur)
}
