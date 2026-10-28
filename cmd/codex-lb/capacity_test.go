package main

import (
	"context"
	"io"
	"log/slog"
	"runtime/debug"
	"testing"

	"codex-lb/internal/domain"
)

func TestParseMemoryLimit(t *testing.T) {
	for input, want := range map[string]int64{"auto": 0, "": 0, "off": -1, "OFF": -1, "3GiB": 3 << 30, "768MiB": 768 << 20, "1073741824": 1 << 30, "1048576KiB": 1 << 30} {
		got, err := parseMemoryLimit(input)
		if err != nil || got != want {
			t.Fatalf("parseMemoryLimit(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"10MiB", "-1GiB", "3GB", "lots", "0"} {
		if _, err := parseMemoryLimit(input); err == nil {
			t.Fatalf("parseMemoryLimit(%q) accepted", input)
		}
	}
}

func TestServeCapacityFlags(t *testing.T) {
	cfg, err := parseConfig("serve", []string{"--data-dir", t.TempDir(), "--memory-limit", "2GiB", "--max-generations", "300", "--max-client-websockets", "1000"}, io.Discard)
	if err != nil || cfg.memoryLimit != 2<<30 || cfg.maxGenerations != 300 || cfg.maxClientWebSockets != 1000 {
		t.Fatalf("cfg = %+v err %v", cfg, err)
	}
	if _, err := parseConfig("serve", []string{"--data-dir", t.TempDir(), "--max-generations", "0"}, io.Discard); err == nil {
		t.Fatal("zero max-generations accepted")
	}
}

func TestPlanCapacityHonorsExportedGOMEMLIMIT(t *testing.T) {
	previous := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(previous) })
	debug.SetMemoryLimit(768 << 20)
	t.Setenv("GOMEMLIMIT", "768MiB")
	host := domain.HostResources{CPUs: 6, MemoryBytes: 4 << 30, MemorySource: domain.MemorySourcePhysical}
	plan := planCapacity(config{}, host)
	if plan.GoMemoryLimitBytes != 0 || plan.SizingMemorySource != domain.MemorySourceGoLimit || plan.MaxGenerations != 512 {
		t.Fatalf("plan = %+v", plan)
	}
	explicit := planCapacity(config{memoryLimit: 3 << 30}, host)
	if explicit.GoMemoryLimitBytes != 3<<30 || explicit.SizingMemorySource != domain.MemorySourceFlag {
		t.Fatalf("explicit flag must win over GOMEMLIMIT: %+v", explicit)
	}
}

func TestOpenRuntimeAppliesCapacityOverrides(t *testing.T) {
	previous := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(previous) })
	cfg := testConfig(t)
	cfg.maxGenerations, cfg.maxClientWebSockets, cfg.memoryLimit = 40, 50, 512<<20
	r, err := openRuntime(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	if got := r.proxy.AdmissionCapacity(); got != 40+64 {
		t.Fatalf("admission capacity = %d, want 104", got)
	}
	if got := debug.SetMemoryLimit(-1); got != 512<<20 {
		t.Fatalf("Go memory limit = %d", got)
	}
}
