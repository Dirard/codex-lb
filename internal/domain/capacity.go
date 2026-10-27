package domain

// Host memory sources reported with a capacity plan.
const (
	MemorySourceCgroupV2 = "cgroup_v2"
	MemorySourceCgroupV1 = "cgroup_v1"
	MemorySourcePhysical = "physical"
	MemorySourceAssumed  = "assumed"
	MemorySourceGoLimit  = "gomemlimit"
	MemorySourceFlag     = "memory_limit_flag"
)

// AssumedHostMemoryBytes sizes a host whose memory cannot be detected as a
// 1 GiB VPS, the smallest supported production profile.
const AssumedHostMemoryBytes int64 = 1 << 30

// HostResources describes what the process may use, not what the machine has:
// cgroup limits win over physical memory.
type HostResources struct {
	CPUs            int
	MemoryBytes     int64
	MemorySource    string
	FileDescriptors int64 // soft RLIMIT_NOFILE; 0 when unknown
}

// CapacityOverrides are operator choices; zero means "derive from the host".
type CapacityOverrides struct {
	// MemoryLimitBytes is the Go soft memory limit: >0 explicit, 0 derived,
	// <0 leaves the runtime limit unset.
	MemoryLimitBytes    int64
	MemoryLimitFromEnv  bool // GOMEMLIMIT was exported; MemoryLimitBytes holds its value
	MaxGenerations      int
	MaxClientWebSockets int
}

// CapacityPlan holds the effective runtime limits derived from a host.
type CapacityPlan struct {
	Host               HostResources
	SizingMemoryBytes  int64
	SizingMemorySource string
	// GoMemoryLimitBytes is the soft limit to apply; 0 leaves the runtime
	// setting untouched (disabled, or already provided through GOMEMLIMIT).
	GoMemoryLimitBytes   int64
	MaxGenerations       int
	MaxQueued            int
	BodyReaders          int
	ClientWebSockets     int
	UpstreamWebSockets   int
	UpstreamConnsPerHost int
	SQLiteReaders        int
	Overridden           []string
}

const reservedFileDescriptors = 256

type capacityTier struct {
	belowBytes  int64
	generations int
	websockets  int
}

// capacityTiers keep the measured and administrator-approved 1 GB defaults
// (512 generations, 4096 client sockets) for 768 MiB–1.75 GiB hosts.
var capacityTiers = []capacityTier{
	{belowBytes: 768 << 20, generations: 256, websockets: 2048},
	{belowBytes: 1792 << 20, generations: 512, websockets: 4096},
	{belowBytes: 3584 << 20, generations: 1024, websockets: 8192},
	{belowBytes: 7 << 30, generations: 2048, websockets: 16384},
	{belowBytes: 0, generations: 4096, websockets: 32768},
}

// PlanCapacity sizes admission, sockets, pools and the Go soft memory limit
// from the host resources available to the process and operator overrides.
func PlanCapacity(host HostResources, overrides CapacityOverrides) CapacityPlan {
	if host.CPUs < 1 {
		host.CPUs = 1
	}
	if host.MemoryBytes <= 0 {
		host.MemoryBytes, host.MemorySource = AssumedHostMemoryBytes, MemorySourceAssumed
	}
	plan := CapacityPlan{Host: host, SizingMemoryBytes: host.MemoryBytes, SizingMemorySource: host.MemorySource}
	switch {
	case overrides.MemoryLimitBytes > 0 && overrides.MemoryLimitFromEnv:
		// The runtime already applied GOMEMLIMIT; size from it without resetting it.
		plan.SizingMemoryBytes = min(host.MemoryBytes, overrides.MemoryLimitBytes*4/3)
		plan.SizingMemorySource = MemorySourceGoLimit
		plan.Overridden = append(plan.Overridden, "memory_limit")
	case overrides.MemoryLimitBytes > 0:
		plan.GoMemoryLimitBytes = overrides.MemoryLimitBytes
		plan.SizingMemoryBytes = min(host.MemoryBytes, overrides.MemoryLimitBytes*4/3)
		plan.SizingMemorySource = MemorySourceFlag
		plan.Overridden = append(plan.Overridden, "memory_limit")
	case overrides.MemoryLimitBytes < 0:
		plan.Overridden = append(plan.Overridden, "memory_limit")
	default:
		plan.GoMemoryLimitBytes = host.MemoryBytes * 3 / 4
	}

	tier := capacityTiers[len(capacityTiers)-1]
	for _, candidate := range capacityTiers {
		if candidate.belowBytes > 0 && plan.SizingMemoryBytes < candidate.belowBytes {
			tier = candidate
			break
		}
	}
	plan.MaxGenerations, plan.ClientWebSockets = tier.generations, tier.websockets
	if overrides.MaxGenerations > 0 {
		plan.MaxGenerations = overrides.MaxGenerations
		plan.Overridden = append(plan.Overridden, "max_generations")
	}
	if overrides.MaxClientWebSockets > 0 {
		plan.ClientWebSockets = overrides.MaxClientWebSockets
		plan.Overridden = append(plan.Overridden, "max_client_websockets")
	} else if host.FileDescriptors > 0 {
		budget := host.FileDescriptors - int64(2*plan.MaxGenerations) - reservedFileDescriptors
		if budget < int64(plan.ClientWebSockets) {
			plan.ClientWebSockets = int(max(64, budget))
		}
	}
	plan.MaxQueued = min(1024, max(64, plan.MaxGenerations/4))
	plan.BodyReaders = max(64, plan.MaxGenerations/4)
	plan.UpstreamWebSockets = plan.MaxGenerations
	plan.UpstreamConnsPerHost = plan.MaxGenerations
	plan.SQLiteReaders = min(32, max(8, 2*host.CPUs+4))
	return plan
}
