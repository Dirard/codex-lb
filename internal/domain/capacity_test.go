package domain

import (
	"slices"
	"testing"
)

const mib = int64(1 << 20)

func TestPlanCapacityTiers(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		memory                       int64
		cpus                         int
		generations, queued, sockets int
		sqliteReaders                int
		bodyBudget, bodyKey          int64
	}{
		{name: "256 MiB container keeps the 64 MiB floor", memory: 256 * mib, cpus: 1, generations: 256, queued: 64, sockets: 2048, sqliteReaders: 8, bodyBudget: 64 * mib, bodyKey: 32 * mib},
		{name: "512 MiB container", memory: 512 * mib, cpus: 1, generations: 256, queued: 64, sockets: 2048, sqliteReaders: 8, bodyBudget: 64 * mib, bodyKey: 32 * mib},
		{name: "1 GB VPS keeps the measured defaults", memory: 960 * mib, cpus: 1, generations: 512, queued: 128, sockets: 4096, sqliteReaders: 8, bodyBudget: 120 * mib, bodyKey: 60 * mib},
		{name: "2 GB VPS", memory: 1990 * mib, cpus: 2, generations: 1024, queued: 256, sockets: 8192, sqliteReaders: 8, bodyBudget: 1990 * mib / 8, bodyKey: 1990 * mib / 16},
		{name: "4 GB VPS with 6 cores", memory: 3890 * mib, cpus: 6, generations: 2048, queued: 512, sockets: 16384, sqliteReaders: 16, bodyBudget: 3890 * mib / 8, bodyKey: 3890 * mib / 16},
		{name: "16 GB host", memory: 16 << 30, cpus: 16, generations: 4096, queued: 1024, sockets: 32768, sqliteReaders: 32, bodyBudget: 2 << 30, bodyKey: 1 << 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := PlanCapacity(HostResources{CPUs: tc.cpus, MemoryBytes: tc.memory, MemorySource: MemorySourcePhysical}, CapacityOverrides{})
			if plan.MaxGenerations != tc.generations || plan.MaxQueued != tc.queued || plan.BodyBudgetBytes != tc.bodyBudget || plan.BodyBudgetKeyBytes != tc.bodyKey ||
				plan.ClientWebSockets != tc.sockets || plan.SQLiteReaders != tc.sqliteReaders {
				t.Fatalf("plan = %+v", plan)
			}
			if plan.UpstreamWebSockets != tc.generations || plan.UpstreamConnsPerHost != tc.generations {
				t.Fatalf("upstream bounds must follow generations: %+v", plan)
			}
			if plan.GoMemoryLimitBytes != tc.memory*3/4 || len(plan.Overridden) != 0 {
				t.Fatalf("memory limit = %d overridden=%v", plan.GoMemoryLimitBytes, plan.Overridden)
			}
		})
	}
}

func TestPlanCapacityUnknownHostAssumesOneGiB(t *testing.T) {
	plan := PlanCapacity(HostResources{}, CapacityOverrides{})
	if plan.Host.MemorySource != MemorySourceAssumed || plan.Host.CPUs != 1 || plan.MaxGenerations != 512 || plan.ClientWebSockets != 4096 ||
		plan.BodyBudgetBytes != 128*mib || plan.BodyBudgetKeyBytes != 64*mib {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanCapacityDescriptorLimitBoundsSockets(t *testing.T) {
	plan := PlanCapacity(HostResources{CPUs: 6, MemoryBytes: 4 << 30, FileDescriptors: 8192}, CapacityOverrides{})
	if want := 8192 - 2*2048 - reservedFileDescriptors; plan.ClientWebSockets != want {
		t.Fatalf("sockets = %d, want %d", plan.ClientWebSockets, want)
	}
	tiny := PlanCapacity(HostResources{CPUs: 1, MemoryBytes: 1 << 30, FileDescriptors: 1024}, CapacityOverrides{})
	if tiny.ClientWebSockets != 64 {
		t.Fatalf("sockets floor = %d", tiny.ClientWebSockets)
	}
}

func TestPlanCapacityOverrides(t *testing.T) {
	host := HostResources{CPUs: 6, MemoryBytes: 4 << 30, MemorySource: MemorySourceCgroupV2, FileDescriptors: 1 << 20}
	explicit := PlanCapacity(host, CapacityOverrides{MemoryLimitBytes: 768 * mib, MaxClientWebSockets: 1000})
	if explicit.GoMemoryLimitBytes != 768*mib || explicit.SizingMemorySource != MemorySourceFlag || explicit.MaxGenerations != 512 || explicit.ClientWebSockets != 1000 {
		t.Fatalf("explicit = %+v", explicit)
	}
	if !slices.Equal(explicit.Overridden, []string{"memory_limit", "max_client_websockets"}) {
		t.Fatalf("overridden = %v", explicit.Overridden)
	}
	env := PlanCapacity(host, CapacityOverrides{MemoryLimitBytes: 3 << 30, MemoryLimitFromEnv: true})
	if env.GoMemoryLimitBytes != 0 || env.SizingMemorySource != MemorySourceGoLimit || env.MaxGenerations != 2048 {
		t.Fatalf("GOMEMLIMIT sizing = %+v", env)
	}
	off := PlanCapacity(host, CapacityOverrides{MemoryLimitBytes: -1, MaxGenerations: 300})
	if off.GoMemoryLimitBytes != 0 || off.MaxGenerations != 300 || off.MaxQueued != 75 || off.UpstreamWebSockets != 300 {
		t.Fatalf("off = %+v", off)
	}
}

func TestPlanCapacityBodyBudgetOverrides(t *testing.T) {
	host := HostResources{CPUs: 6, MemoryBytes: 4 << 30, MemorySource: MemorySourcePhysical}
	budget := PlanCapacity(host, CapacityOverrides{BodyBudgetBytes: 1 << 30})
	if budget.BodyBudgetBytes != 1<<30 || budget.BodyBudgetKeyBytes != 512*mib || !slices.Equal(budget.Overridden, []string{"body_memory_budget"}) {
		t.Fatalf("budget override = %+v", budget)
	}
	// A small share is floored so one idle key can still send a maximum-size body.
	small := PlanCapacity(host, CapacityOverrides{BodyBudgetKeySharePct: 5})
	if small.BodyBudgetBytes != 512*mib || small.BodyBudgetKeyBytes != MaxRequestBodyBytes || !slices.Equal(small.Overridden, []string{"body_memory_key_share"}) {
		t.Fatalf("small share = %+v", small)
	}
	whole := PlanCapacity(host, CapacityOverrides{BodyBudgetBytes: 100 * mib, BodyBudgetKeySharePct: 100})
	if whole.BodyBudgetKeyBytes != whole.BodyBudgetBytes {
		t.Fatalf("100%% share must disable the per-key bound: %+v", whole)
	}
	tight := PlanCapacity(host, CapacityOverrides{BodyBudgetBytes: MaxRequestBodyBytes, BodyBudgetKeySharePct: 25})
	if tight.BodyBudgetKeyBytes != MaxRequestBodyBytes {
		t.Fatalf("per-key bound must admit one maximum-size body: %+v", tight)
	}
}
