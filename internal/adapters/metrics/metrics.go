// Package metrics keeps a small set of runtime counters and serves them in
// the Prometheus text exposition format without external dependencies.
// Labels are fixed, low-cardinality values; account and key IDs never appear.
package metrics

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// durationBuckets cover short API calls up to long generation streams.
var durationBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 1800}

type requestKey struct{ group, class string }

type histogram struct {
	counts []uint64 // cumulative per bucket, plus +Inf at the end
	sum    float64
}

type taskStats struct {
	ok, failed, panicked uint64
	lastSuccess          time.Time
	lastDuration         time.Duration
}

// Sample is one gauge value with optional fixed labels.
type Sample struct {
	Labels map[string]string
	Value  float64
}

type gauge struct {
	name, help string
	collect    func() []Sample
}

// Registry is safe for concurrent use.
type Registry struct {
	inFlight atomic.Int64
	mu       sync.Mutex
	requests map[requestKey]uint64
	duration map[string]*histogram
	tasks    map[string]*taskStats
	gauges   []gauge
}

func NewRegistry() *Registry {
	return &Registry{requests: map[requestKey]uint64{}, duration: map[string]*histogram{}, tasks: map[string]*taskStats{}}
}

// RequestStarted marks an in-flight HTTP request; call the returned function
// once with its final status class (for example "2xx").
func (r *Registry) RequestStarted() func(group, class string, elapsed time.Duration) {
	r.inFlight.Add(1)
	return func(group, class string, elapsed time.Duration) {
		r.inFlight.Add(-1)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.requests[requestKey{group, class}]++
		h := r.duration[group]
		if h == nil {
			h = &histogram{counts: make([]uint64, len(durationBuckets)+1)}
			r.duration[group] = h
		}
		seconds := elapsed.Seconds()
		h.sum += seconds
		for i, bound := range durationBuckets {
			if seconds <= bound {
				h.counts[i]++
			}
		}
		h.counts[len(durationBuckets)]++
	}
}

// TaskFinished records one background-task run.
func (r *Registry) TaskFinished(task string, elapsed time.Duration, failed, panicked bool, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stats := r.tasks[task]
	if stats == nil {
		stats = &taskStats{}
		r.tasks[task] = stats
	}
	stats.lastDuration = elapsed
	switch {
	case panicked:
		stats.panicked++
	case failed:
		stats.failed++
	default:
		stats.ok++
		stats.lastSuccess = at
	}
}

// Gauge registers a value read at scrape time. Register gauges before serving.
func (r *Registry) Gauge(name, help string, collect func() []Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges = append(r.gauges, gauge{name: name, help: help, collect: collect})
}

// Handler serves the registry in the Prometheus text format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = r.Write(w)
	})
}

// Write renders every metric; gauge collectors run outside the registry lock.
func (r *Registry) Write(out io.Writer) error {
	var b strings.Builder
	r.mu.Lock()
	gauges := slices.Clone(r.gauges)
	writeHeader(&b, "codex_lb_http_requests_total", "HTTP requests by route group and status class.", "counter")
	for _, key := range slices.SortedFunc(maps.Keys(r.requests), func(a, b requestKey) int {
		return strings.Compare(a.group+"\x00"+a.class, b.group+"\x00"+b.class)
	}) {
		writeSample(&b, "codex_lb_http_requests_total", map[string]string{"group": key.group, "code": key.class}, float64(r.requests[key]))
	}
	writeHeader(&b, "codex_lb_http_request_duration_seconds", "HTTP request duration, including streams, by route group.", "histogram")
	for _, group := range slices.Sorted(maps.Keys(r.duration)) {
		h := r.duration[group]
		for i, bound := range durationBuckets {
			writeSample(&b, "codex_lb_http_request_duration_seconds_bucket", map[string]string{"group": group, "le": strconv.FormatFloat(bound, 'g', -1, 64)}, float64(h.counts[i]))
		}
		writeSample(&b, "codex_lb_http_request_duration_seconds_bucket", map[string]string{"group": group, "le": "+Inf"}, float64(h.counts[len(durationBuckets)]))
		writeSample(&b, "codex_lb_http_request_duration_seconds_sum", map[string]string{"group": group}, h.sum)
		writeSample(&b, "codex_lb_http_request_duration_seconds_count", map[string]string{"group": group}, float64(h.counts[len(durationBuckets)]))
	}
	writeHeader(&b, "codex_lb_background_runs_total", "Background task runs by result.", "counter")
	tasks := slices.Sorted(maps.Keys(r.tasks))
	for _, task := range tasks {
		stats := r.tasks[task]
		writeSample(&b, "codex_lb_background_runs_total", map[string]string{"task": task, "result": "ok"}, float64(stats.ok))
		writeSample(&b, "codex_lb_background_runs_total", map[string]string{"task": task, "result": "error"}, float64(stats.failed))
		writeSample(&b, "codex_lb_background_runs_total", map[string]string{"task": task, "result": "panic"}, float64(stats.panicked))
	}
	writeHeader(&b, "codex_lb_background_last_success_timestamp_seconds", "Unix time of the last successful background run.", "gauge")
	for _, task := range tasks {
		if last := r.tasks[task].lastSuccess; !last.IsZero() {
			writeSample(&b, "codex_lb_background_last_success_timestamp_seconds", map[string]string{"task": task}, float64(last.UnixMilli())/1000)
		}
	}
	writeHeader(&b, "codex_lb_background_last_duration_seconds", "Duration of the most recent background run.", "gauge")
	for _, task := range tasks {
		writeSample(&b, "codex_lb_background_last_duration_seconds", map[string]string{"task": task}, r.tasks[task].lastDuration.Seconds())
	}
	r.mu.Unlock()
	writeHeader(&b, "codex_lb_http_in_flight_requests", "HTTP requests and WebSockets currently being served.", "gauge")
	writeSample(&b, "codex_lb_http_in_flight_requests", nil, float64(r.inFlight.Load()))
	for _, g := range gauges {
		writeHeader(&b, g.name, g.help, "gauge")
		for _, sample := range g.collect() {
			writeSample(&b, g.name, sample.Labels, sample.Value)
		}
	}
	_, err := io.WriteString(out, b.String())
	return err
}

func writeHeader(b *strings.Builder, name, help, kind string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
}

func writeSample(b *strings.Builder, name string, labels map[string]string, value float64) {
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i, key := range slices.Sorted(maps.Keys(labels)) {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(key)
			b.WriteString(`="`)
			b.WriteString(escapeLabel(labels[key]))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	b.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	b.WriteByte('\n')
}

func escapeLabel(value string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(value)
}
