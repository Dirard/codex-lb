package hostinfo

import (
	"testing"
	"testing/fstest"

	"codex-lb/internal/domain"
)

const meminfo4GB = "MemTotal:        3982336 kB\nMemFree:          100000 kB\n"

func TestDetectCgroupV2UsesSmallestLimitAlongPath(t *testing.T) {
	root := fstest.MapFS{
		"proc/meminfo":     {Data: []byte(meminfo4GB)},
		"proc/self/cgroup": {Data: []byte("0::/system.slice/codex-lb.service\n")},
		"sys/fs/cgroup/system.slice/codex-lb.service/memory.max":  {Data: []byte("max\n")},
		"sys/fs/cgroup/system.slice/codex-lb.service/memory.high": {Data: []byte("1073741824\n")},
		"sys/fs/cgroup/system.slice/memory.max":                   {Data: []byte("2147483648\n")},
	}
	host := DetectFS(root, 6, 65536)
	if host.MemoryBytes != 1<<30 || host.MemorySource != domain.MemorySourceCgroupV2 || host.CPUs != 6 || host.FileDescriptors != 65536 {
		t.Fatalf("host = %+v", host)
	}
}

func TestDetectCgroupV2WithoutLimitUsesPhysicalMemory(t *testing.T) {
	root := fstest.MapFS{
		"proc/meminfo":                        {Data: []byte(meminfo4GB)},
		"proc/self/cgroup":                    {Data: []byte("0::/user.slice\n")},
		"sys/fs/cgroup/user.slice/memory.max": {Data: []byte("max\n")},
	}
	host := DetectFS(root, 6, 0)
	if host.MemoryBytes != 3982336<<10 || host.MemorySource != domain.MemorySourcePhysical {
		t.Fatalf("host = %+v", host)
	}
}

func TestDetectCgroupV1LimitAndUnlimitedSentinel(t *testing.T) {
	limited := fstest.MapFS{
		"proc/meminfo":     {Data: []byte(meminfo4GB)},
		"proc/self/cgroup": {Data: []byte("12:cpu,cpuacct:/docker/abc\n4:memory:/docker/abc\n")},
		"sys/fs/cgroup/memory/docker/abc/memory.limit_in_bytes": {Data: []byte("536870912\n")},
	}
	if host := DetectFS(limited, 1, 0); host.MemoryBytes != 512<<20 || host.MemorySource != domain.MemorySourceCgroupV1 {
		t.Fatalf("limited = %+v", host)
	}
	unlimited := fstest.MapFS{
		"proc/meminfo":     {Data: []byte(meminfo4GB)},
		"proc/self/cgroup": {Data: []byte("4:memory:/\n")},
		"sys/fs/cgroup/memory/memory.limit_in_bytes": {Data: []byte("9223372036854771712\n")},
	}
	if host := DetectFS(unlimited, 1, 0); host.MemorySource != domain.MemorySourcePhysical {
		t.Fatalf("unlimited = %+v", host)
	}
}

func TestDetectWithoutProcLeavesMemoryUnknown(t *testing.T) {
	host := DetectFS(fstest.MapFS{}, 2, 0)
	if host.MemoryBytes != 0 || host.MemorySource != "" {
		t.Fatalf("host = %+v", host)
	}
	if plan := domain.PlanCapacity(host, domain.CapacityOverrides{}); plan.Host.MemorySource != domain.MemorySourceAssumed {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestDetectLiveHost(t *testing.T) {
	host := Detect()
	if host.CPUs < 1 {
		t.Fatalf("host = %+v", host)
	}
}
