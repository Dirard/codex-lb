package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	goruntime "runtime"
	"runtime/metrics"
	"sort"
	"time"

	"codex-lb/internal/adapters/hostinfo"
	"codex-lb/internal/adapters/httpapi"
	registry "codex-lb/internal/adapters/metrics"
)

// registerGauges exposes load against the capacity plan, process memory,
// account states and database size. Collectors run only at scrape time.
func (r *runtime) registerGauges(responses interface{ ClientWebSockets() (int, int) }, bodies *httpapi.BodyBudget) {
	one := func(value float64) []registry.Sample { return []registry.Sample{{Value: value}} }
	r.metrics.Gauge("codex_lb_build_info", "Build version of the running server.", func() []registry.Sample {
		return []registry.Sample{{Labels: map[string]string{"version": version}, Value: 1}}
	})
	r.metrics.Gauge("codex_lb_generations_active", "Generations currently executing.", func() []registry.Sample {
		active, _ := r.proxy.AdmissionUsage()
		return one(float64(active))
	})
	r.metrics.Gauge("codex_lb_generations_queued", "Admitted generations waiting for a slot.", func() []registry.Sample {
		_, queued := r.proxy.AdmissionUsage()
		return one(float64(queued))
	})
	r.metrics.Gauge("codex_lb_generations_limit", "Configured concurrent generation bound.", func() []registry.Sample {
		return one(float64(r.capacity.MaxGenerations))
	})
	r.metrics.Gauge("codex_lb_queue_limit", "Configured generation queue bound.", func() []registry.Sample {
		return one(float64(r.capacity.MaxQueued))
	})
	r.metrics.Gauge("codex_lb_client_websockets", "Open client WebSockets.", func() []registry.Sample {
		open, _ := responses.ClientWebSockets()
		return one(float64(open))
	})
	r.metrics.Gauge("codex_lb_client_websockets_limit", "Configured client WebSocket bound.", func() []registry.Sample {
		_, limit := responses.ClientWebSockets()
		return one(float64(limit))
	})
	r.metrics.Gauge("codex_lb_request_body_bytes", "Request-body bytes retained by in-flight proxy requests.", func() []registry.Sample {
		return one(float64(bodies.Usage().UsedBytes))
	})
	r.metrics.Gauge("codex_lb_request_body_budget_bytes", "Configured request-body budget.", func() []registry.Sample {
		return one(float64(bodies.Usage().LimitBytes))
	})
	r.metrics.Gauge("codex_lb_request_body_rejections", "Request bodies refused by the body budget since start.", func() []registry.Sample {
		return one(float64(bodies.Usage().Rejected))
	})
	r.metrics.Gauge("codex_lb_process_resident_memory_bytes", "Resident set size of the server process.", func() []registry.Sample {
		return one(float64(hostinfo.ResidentBytes()))
	})
	r.metrics.Gauge("codex_lb_go_memory_bytes", "Go runtime memory by class.", func() []registry.Sample {
		samples := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/total:bytes"}, {Name: "/gc/gomemlimit:bytes"}}
		metrics.Read(samples)
		out := make([]registry.Sample, 0, len(samples))
		for i, class := range []string{"heap_objects", "total", "limit"} {
			if samples[i].Value.Kind() == metrics.KindUint64 {
				out = append(out, registry.Sample{Labels: map[string]string{"class": class}, Value: float64(samples[i].Value.Uint64())})
			}
		}
		return out
	})
	r.metrics.Gauge("codex_lb_goroutines", "Live goroutines.", func() []registry.Sample { return one(float64(goruntime.NumGoroutine())) })
	r.metrics.Gauge("codex_lb_uptime_seconds", "Seconds since the server opened its runtime.", func() []registry.Sample {
		return one(time.Since(r.started).Seconds())
	})
	r.metrics.Gauge("codex_lb_accounts", "Upstream accounts by status.", func() []registry.Sample {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		accounts, err := r.data.store.ListAccounts(ctx)
		if err != nil {
			return nil
		}
		counts := map[string]int{}
		for _, account := range accounts {
			counts[string(account.Status)]++
		}
		statuses := make([]string, 0, len(counts))
		for status := range counts {
			statuses = append(statuses, status)
		}
		sort.Strings(statuses)
		out := make([]registry.Sample, 0, len(statuses))
		for _, status := range statuses {
			out = append(out, registry.Sample{Labels: map[string]string{"status": status}, Value: float64(counts[status])})
		}
		return out
	})
	r.metrics.Gauge("codex_lb_database_bytes", "SQLite database and WAL file sizes.", func() []registry.Sample {
		out := []registry.Sample{}
		for file, label := range map[string]string{"codex-lb.sqlite3": "database", "codex-lb.sqlite3-wal": "wal"} {
			if info, err := os.Stat(filepath.Join(r.config.dataDir, file)); err == nil {
				out = append(out, registry.Sample{Labels: map[string]string{"file": label}, Value: float64(info.Size())})
			}
		}
		return out
	})
}

// startMetrics serves /metrics and /debug/pprof on the optional, separate
// metrics listener. It is never part of the public listener.
func (r *runtime) startMetrics() error {
	if r.config.metricsListen == "" {
		return nil
	}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", r.config.metricsListen)
	if err != nil {
		return listenError("metrics listen address", r.config.metricsListen, err)
	}
	if host, _, _ := net.SplitHostPort(r.config.metricsListen); !isLoopbackHost(host) {
		r.logger.Warn("metrics listener is reachable beyond loopback and has no authentication", "address", listener.Addr().String())
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", r.metrics.Handler())
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	r.metricsMu.Lock()
	r.metricsServer = server
	r.metricsAddress = listener.Addr().String()
	r.metricsMu.Unlock()
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			r.logger.Warn("metrics listener stopped", "reason", err.Error())
		}
	}()
	r.logger.Info("metrics listener ready", "address", listener.Addr().String())
	return nil
}

// stopMetrics may race startMetrics: background work can start from the
// private control listener while the worker begins to drain.
func (r *runtime) stopMetrics() {
	r.metricsMu.Lock()
	server := r.metricsServer
	r.metricsMu.Unlock()
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (r *runtime) metricsAddr() string {
	r.metricsMu.Lock()
	defer r.metricsMu.Unlock()
	return r.metricsAddress
}
