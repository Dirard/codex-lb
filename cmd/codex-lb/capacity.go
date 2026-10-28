package main

import (
	"log/slog"
	"math"
	"os"
	"runtime/debug"

	"codex-lb/internal/adapters/hostinfo"
	"codex-lb/internal/domain"
)

// planCapacity detects the host and applies operator overrides. An exported
// GOMEMLIMIT is kept as the runtime limit and used for sizing unless
// --memory-limit was given explicitly.
func planCapacity(cfg config, host domain.HostResources) domain.CapacityPlan {
	overrides := domain.CapacityOverrides{MemoryLimitBytes: cfg.memoryLimit, MaxGenerations: cfg.maxGenerations, MaxClientWebSockets: cfg.maxClientWebSockets,
		BodyBudgetBytes: cfg.bodyMemoryBudget, BodyBudgetKeySharePct: cfg.bodyMemoryKeyShare}
	if cfg.memoryLimit == 0 && os.Getenv("GOMEMLIMIT") != "" {
		if current := debug.SetMemoryLimit(-1); current > 0 && current < math.MaxInt64 {
			overrides.MemoryLimitBytes, overrides.MemoryLimitFromEnv = current, true
		}
	}
	return domain.PlanCapacity(host, overrides)
}

// applyCapacity sets the Go soft memory limit and records the plan; it runs
// before any listener opens.
func applyCapacity(plan domain.CapacityPlan, logger *slog.Logger) {
	if plan.GoMemoryLimitBytes > 0 {
		debug.SetMemoryLimit(plan.GoMemoryLimitBytes)
	}
	logger.Info("runtime capacity",
		"cpus", plan.Host.CPUs, "memory_bytes", plan.Host.MemoryBytes, "memory_source", plan.Host.MemorySource,
		"file_descriptors", plan.Host.FileDescriptors, "sizing_memory_bytes", plan.SizingMemoryBytes,
		"go_memory_limit_bytes", debug.SetMemoryLimit(-1), "max_generations", plan.MaxGenerations,
		"max_queued", plan.MaxQueued, "body_budget_bytes", plan.BodyBudgetBytes, "body_budget_key_bytes", plan.BodyBudgetKeyBytes,
		"client_websockets", plan.ClientWebSockets,
		"upstream_websockets", plan.UpstreamWebSockets, "sqlite_readers", plan.SQLiteReaders, "overridden", plan.Overridden)
}

func detectHost() domain.HostResources { return hostinfo.Detect() }
