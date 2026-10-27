package httpapi

import (
	"net/http"
	"runtime"
	"runtime/debug"
	"runtime/metrics"

	"codex-lb/internal/domain"
)

// CapacityUsage reports live load against the planned limits.
type CapacityUsage struct {
	ActiveGenerations int
	QueuedGenerations int
	ClientWebSockets  int
	ResidentBytes     int64
}

// RuntimeCapacitySource supplies the startup plan and current usage.
type RuntimeCapacitySource struct {
	Plan  domain.CapacityPlan
	Usage func() CapacityUsage
}

type runtimeCapacityHost struct {
	CPUs            int    `json:"cpus"`
	MemoryBytes     int64  `json:"memoryBytes"`
	MemorySource    string `json:"memorySource"`
	FileDescriptors int64  `json:"fileDescriptors"`
}

type runtimeCapacityLimits struct {
	GoMemoryLimitBytes   int64 `json:"goMemoryLimitBytes"`
	MaxGenerations       int   `json:"maxGenerations"`
	MaxQueued            int   `json:"maxQueued"`
	BodyReaders          int   `json:"bodyReaders"`
	ClientWebSockets     int   `json:"clientWebSockets"`
	UpstreamWebSockets   int   `json:"upstreamWebSockets"`
	UpstreamConnsPerHost int   `json:"upstreamConnectionsPerHost"`
	SQLiteReaders        int   `json:"sqliteReaders"`
}

type runtimeCapacityUsage struct {
	ActiveGenerations int   `json:"activeGenerations"`
	QueuedGenerations int   `json:"queuedGenerations"`
	ClientWebSockets  int   `json:"clientWebSockets"`
	GoHeapBytes       int64 `json:"goHeapBytes"`
	GoTotalBytes      int64 `json:"goTotalBytes"`
	ResidentBytes     int64 `json:"residentBytes"`
	Goroutines        int   `json:"goroutines"`
}

type runtimeCapacityView struct {
	Host               runtimeCapacityHost   `json:"host"`
	SizingMemoryBytes  int64                 `json:"sizingMemoryBytes"`
	SizingMemorySource string                `json:"sizingMemorySource"`
	Limits             runtimeCapacityLimits `json:"limits"`
	Overridden         []string              `json:"overridden"`
	Usage              runtimeCapacityUsage  `json:"usage"`
}

// ConfigureRuntimeCapacity exposes the host-derived plan on the admin API.
func (s *Server) ConfigureRuntimeCapacity(source RuntimeCapacitySource) { s.capacity = &source }

func (s *Server) registerRuntimeCapacityRoutes(admin *http.ServeMux) {
	for _, path := range []string{"/api/runtime/capacity", "/api/runtime/capacity/{$}"} {
		admin.HandleFunc("GET "+path, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, s.runtimeCapacity())
		})
	}
}

func (s *Server) runtimeCapacity() runtimeCapacityView {
	plan := s.capacity.Plan
	view := runtimeCapacityView{
		Host:               runtimeCapacityHost{plan.Host.CPUs, plan.Host.MemoryBytes, plan.Host.MemorySource, plan.Host.FileDescriptors},
		SizingMemoryBytes:  plan.SizingMemoryBytes,
		SizingMemorySource: plan.SizingMemorySource,
		Limits: runtimeCapacityLimits{
			// Report the limit actually in force, including GOMEMLIMIT.
			GoMemoryLimitBytes: effectiveMemoryLimit(),
			MaxGenerations:     plan.MaxGenerations, MaxQueued: plan.MaxQueued, BodyReaders: plan.BodyReaders,
			ClientWebSockets: plan.ClientWebSockets, UpstreamWebSockets: plan.UpstreamWebSockets,
			UpstreamConnsPerHost: plan.UpstreamConnsPerHost, SQLiteReaders: plan.SQLiteReaders,
		},
		Overridden: append([]string{}, plan.Overridden...),
	}
	if s.capacity.Usage != nil {
		usage := s.capacity.Usage()
		view.Usage = runtimeCapacityUsage{ActiveGenerations: usage.ActiveGenerations, QueuedGenerations: usage.QueuedGenerations,
			ClientWebSockets: usage.ClientWebSockets, ResidentBytes: usage.ResidentBytes}
	}
	samples := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/total:bytes"}}
	metrics.Read(samples)
	if samples[0].Value.Kind() == metrics.KindUint64 {
		view.Usage.GoHeapBytes = int64(samples[0].Value.Uint64())
	}
	if samples[1].Value.Kind() == metrics.KindUint64 {
		view.Usage.GoTotalBytes = int64(samples[1].Value.Uint64())
	}
	view.Usage.Goroutines = runtime.NumGoroutine()
	return view
}

// effectiveMemoryLimit returns the Go soft memory limit in force; 0 when unset.
func effectiveMemoryLimit() int64 {
	limit := debug.SetMemoryLimit(-1)
	if limit <= 0 || limit == 1<<63-1 {
		return 0
	}
	return limit
}
