package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"codex-lb/internal/adapters/updatecontrol"
	"codex-lb/internal/application"
)

type workerUpdateStatus struct {
	Prepared bool   `json:"prepared"`
	Serving  bool   `json:"serving"`
	Version  string `json:"version"`
}

// serveUpdateWorker prepares the data and listener while fenced, then waits for
// the owning launcher to commit activation. Control requests never enter track.
func serveUpdateWorker(ctx context.Context, cfg config, logger *slog.Logger) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	socket, parent, token := os.Getenv(workerSocketEnv), os.Getenv(parentSocketEnv), os.Getenv(controlTokenEnv)
	if socket == "" || parent == "" || len(token) != 64 {
		return errors.New("invalid private runtime control configuration")
	}
	client := updatecontrol.NewClient(parent, token)
	defer client.Close()
	cfg.updater = client
	r, err := openRuntime(ctx, cfg, logger)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		_ = r.close()
		return errors.New("cannot bind managed listener")
	}
	control, err := net.Listen("unix", socket)
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
		_ = json.NewEncoder(w).Encode(workerUpdateStatus{Prepared: true, Serving: serving.Load(), Version: version})
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
	server := &http.Server{Handler: updatecontrol.Protect(token, mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, MaxHeaderBytes: 4096}
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
