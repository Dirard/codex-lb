// Package hostinfo detects the CPU, memory and descriptor budget available to
// the process, honoring cgroup limits so containers and systemd MemoryMax are
// sized correctly.
package hostinfo

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"

	"codex-lb/internal/domain"
)

// v1 cgroups report "unlimited" as a page-aligned value near 2^63.
const cgroupV1Unlimited int64 = 1 << 60

// Detect reads the live host through the root filesystem.
func Detect() domain.HostResources {
	return DetectFS(os.DirFS("/"), runtime.GOMAXPROCS(0), fileDescriptorLimit())
}

// DetectFS detects memory from root (a filesystem rooted at "/"); cpus and
// descriptors are supplied by the caller so tests stay deterministic.
func DetectFS(root fs.FS, cpus int, descriptors int64) domain.HostResources {
	host := domain.HostResources{CPUs: cpus, FileDescriptors: descriptors}
	physical := memTotal(root)
	if physical > 0 {
		host.MemoryBytes, host.MemorySource = physical, domain.MemorySourcePhysical
	}
	if limit, source := cgroupMemoryLimit(root); limit > 0 && (host.MemoryBytes == 0 || limit < host.MemoryBytes) {
		host.MemoryBytes, host.MemorySource = limit, source
	}
	return host
}

// ResidentBytes reports the process resident set size; 0 when unavailable.
func ResidentBytes() int64 {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	return statusKiB(data, "VmRSS:")
}

func memTotal(root fs.FS) int64 {
	data, err := fs.ReadFile(root, "proc/meminfo")
	if err != nil {
		return 0
	}
	return statusKiB(data, "MemTotal:")
}

func statusKiB(data []byte, key string) int64 {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == key {
			value, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil || value <= 0 {
				return 0
			}
			return value << 10
		}
	}
	return 0
}

// cgroupMemoryLimit returns the smallest memory limit along the process
// cgroup path. A limit on any ancestor constrains the process too.
func cgroupMemoryLimit(root fs.FS) (int64, string) {
	data, err := fs.ReadFile(root, "proc/self/cgroup")
	if err != nil {
		return 0, ""
	}
	var v2Path, v1Path string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "0" && parts[1] == "" {
			v2Path = parts[2]
		}
		for _, controller := range strings.Split(parts[1], ",") {
			if controller == "memory" {
				v1Path = parts[2]
			}
		}
	}
	if v1Path != "" {
		if limit := smallestAlong(root, "sys/fs/cgroup/memory", v1Path, []string{"memory.limit_in_bytes"}); limit > 0 {
			return limit, domain.MemorySourceCgroupV1
		}
	}
	if v2Path != "" {
		if limit := smallestAlong(root, "sys/fs/cgroup", v2Path, []string{"memory.max", "memory.high"}); limit > 0 {
			return limit, domain.MemorySourceCgroupV2
		}
	}
	return 0, ""
}

func smallestAlong(root fs.FS, mount, cgroupPath string, files []string) int64 {
	var smallest int64
	dir := path.Clean("/" + cgroupPath)
	for {
		for _, name := range files {
			if value := readLimit(root, strings.TrimPrefix(path.Join(mount, dir, name), "/")); value > 0 && (smallest == 0 || value < smallest) {
				smallest = value
			}
		}
		if dir == "/" {
			return smallest
		}
		dir = path.Dir(dir)
	}
}

func readLimit(root fs.FS, name string) int64 {
	data, err := fs.ReadFile(root, name)
	if err != nil {
		return 0
	}
	text := strings.TrimSpace(string(data))
	if text == "max" {
		return 0
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value <= 0 || value >= cgroupV1Unlimited {
		return 0
	}
	return value
}
