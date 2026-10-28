package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"codex-lb/internal/adapters/hostinfo"
	"codex-lb/internal/adapters/updatecontrol"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type workerUpdateStatus struct {
	Prepared bool   `json:"prepared"`
	Serving  bool   `json:"serving"`
	Version  string `json:"version"`
	Handoff  int    `json:"handoff,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// serveUpdateWorker prepares the data and listener while fenced, then waits for
// the owning launcher to commit activation. Control requests never enter track.
// A handoff worker serves on the launcher's listener and lock; others bind and
// lock themselves for launchers that lend nothing.
func serveUpdateWorker(ctx context.Context, cfg config, logger *slog.Logger) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	socket, parent, token := os.Getenv(workerSocketEnv), os.Getenv(parentSocketEnv), os.Getenv(controlTokenEnv)
	if socket == "" || parent == "" || len(token) != 64 {
		return errors.New("invalid private runtime control configuration")
	}
	handoff, err := inheritedHandoff(cfg.dataDir)
	if err != nil {
		return err
	}
	if handoff != nil {
		defer handoff.lifeline.Close()
		handoff.watchLifeline(stop)
	}
	cfg.handoff = handoff
	client := updatecontrol.NewClient(parent, token)
	defer client.Close()
	cfg.updater = client
	r, err := openRuntime(ctx, cfg, logger)
	if err != nil {
		if handoff != nil {
			handoff.listener.Close()
		}
		return err
	}
	var listener net.Listener
	if handoff != nil {
		listener = handoff.listener
	} else if listener, err = (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.listen); err != nil {
		_ = r.close()
		return listenError("managed listener", cfg.listen, err)
	}
	control, err := (&net.ListenConfig{}).Listen(ctx, "unix", socket)
	if err != nil {
		listener.Close()
		_ = r.close()
		return errors.New("cannot bind private runtime control")
	}
	defer control.Close()
	_ = os.Chmod(socket, 0600)
	activate := make(chan struct{})
	started := make(chan struct{})
	var activateOnce, startedOnce sync.Once
	var serving atomic.Bool
	gate := workerUpdateGate{requests: &r.requests, proxy: r.proxy, stop: stop, timeout: 15 * time.Second}
	defer gate.cancel()
	r.onServing = func() { serving.Store(true); startedOnce.Do(func() { close(started) }) }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, request *http.Request) {
		check, cancel := context.WithTimeout(request.Context(), time.Second)
		defer cancel()
		if r.data.store.Health(check) != nil {
			http.Error(w, "Not ready", http.StatusServiceUnavailable)
			return
		}
		status := workerUpdateStatus{Prepared: true, Serving: serving.Load(), Version: version}
		if handoff != nil {
			status.Handoff, status.Instance = domain.RuntimeHandoffProtocol, handoff.instance
		}
		_ = json.NewEncoder(w).Encode(status)
	})
	mux.HandleFunc("POST /activate", func(w http.ResponseWriter, request *http.Request) {
		activateOnce.Do(func() { close(activate) })
		select {
		case <-started:
			w.WriteHeader(http.StatusNoContent)
		case <-request.Context().Done():
		case <-ctx.Done():
			http.Error(w, "Stopping", http.StatusServiceUnavailable)
		}
	})
	mux.HandleFunc("POST /prepare-stop", func(w http.ResponseWriter, request *http.Request) {
		if request.Context().Err() != nil {
			return
		}
		if !gate.prepare() {
			http.Error(w, "Active work", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /cancel-stop", func(w http.ResponseWriter, request *http.Request) {
		gate.cancel()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, request *http.Request) {
		if !gate.commit() {
			http.Error(w, "Idle gate expired", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if handoff != nil {
		r.registerHandoffControl(mux, started)
	}
	server := &http.Server{Handler: updatecontrol.Protect(token, mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 75 * time.Second, MaxHeaderBytes: 4096}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(control) }()
	defer func() { _ = server.Close(); <-done }()
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	select {
	case <-activate:
		return r.serveListener(ctx, listener)
	case <-ctx.Done():
		listener.Close()
		return r.close()
	case <-timer.C:
		listener.Close()
		_ = r.close()
		return errors.New("managed activation timed out")
	}
}

// The prepared idle gate expires locally if either acknowledgement is lost.
// Committing stop and timer expiry share a lock: stop cannot race a reopened gate.
type workerUpdateGate struct {
	mu       sync.Mutex
	requests *requestLifecycle
	proxy    *application.Proxy
	stop     context.CancelFunc
	timeout  time.Duration
	timer    *time.Timer
}

func (g *workerUpdateGate) prepare() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.timer != nil {
		return true
	}
	if !g.requests.tryBeginUpdateDrain(g.proxy) {
		return false
	}
	g.timer = time.AfterFunc(g.timeout, g.cancel)
	return true
}

func (g *workerUpdateGate) cancel() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
		g.requests.resumeUpdateDrain(g.proxy)
	}
}

func (g *workerUpdateGate) commit() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.timer == nil || !g.timer.Stop() {
		return false
	}
	g.timer = nil
	g.requests.beginDrain()
	g.proxy.BeginDrain()
	g.stop()
	return true
}

// registerHandoffControl adds the launcher's handoff operations: moving
// background work, retiring, and exchanging admission load with peers.
func (r *runtime) registerHandoffControl(mux *http.ServeMux, started <-chan struct{}) {
	mux.HandleFunc("POST /background", func(w http.ResponseWriter, request *http.Request) {
		select {
		case <-started:
		default:
			http.Error(w, "Not serving", http.StatusConflict)
			return
		}
		r.startBackground()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /retire", func(w http.ResponseWriter, request *http.Request) {
		select {
		case <-started:
		default:
			http.Error(w, "Not serving", http.StatusConflict)
			return
		}
		if err := r.retire(request.Context()); err != nil {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /load", func(w http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(w).Encode(workerLoadReport{Load: r.proxy.Load(), ResidentBytes: hostinfo.ResidentBytes()})
	})
	mux.HandleFunc("PUT /peer-load", func(w http.ResponseWriter, request *http.Request) {
		var report workerLoadReport
		if json.NewDecoder(io.LimitReader(request.Body, 4<<20)).Decode(&report) != nil {
			http.Error(w, "Invalid load", http.StatusBadRequest)
			return
		}
		r.proxy.SetPeerLoad(report.Load)
		r.applyPeerMemory(report.ResidentBytes)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /peer-exited", func(w http.ResponseWriter, request *http.Request) {
		var exit peerExit
		if json.NewDecoder(io.LimitReader(request.Body, 64<<10)).Decode(&exit) != nil {
			http.Error(w, "Invalid peers", http.StatusBadRequest)
			return
		}
		for _, instance := range exit.Live {
			if !validInstance(instance) {
				http.Error(w, "Invalid peers", http.StatusBadRequest)
				return
			}
		}
		// This worker's own reservations are live whatever the launcher sent.
		live := append(exit.Live, r.data.store.InstanceID())
		interrupted, err := r.data.store.MarkInterruptedReservations(request.Context(), live)
		if err != nil {
			http.Error(w, "Cannot mark interrupted usage", http.StatusServiceUnavailable)
			return
		}
		if interrupted != 0 {
			r.logger.Warn("interrupted usage retained after a worker exited; use offline reconcile-usage before releasing unknown charges", "count", interrupted)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// applyPeerMemory leaves the planned Go memory limit to the host's workers
// together: peers' resident memory is subtracted, down to a quarter.
func (r *runtime) applyPeerMemory(peerResident int64) {
	planned := r.capacity.GoMemoryLimitBytes
	if planned <= 0 {
		return
	}
	limit := planned
	if peerResident > 0 {
		limit = max(planned/4, planned-peerResident)
	}
	debug.SetMemoryLimit(limit)
}
