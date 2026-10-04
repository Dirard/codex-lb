package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"codex-lb/internal/application"
)

type requestLifecycle struct {
	mu         sync.Mutex
	draining   bool
	stopping   bool
	activeWork int
	active     sync.WaitGroup
}

func (l *requestLifecycle) beginDrain() {
	l.mu.Lock()
	l.draining = true
	l.stopping = true
	l.mu.Unlock()
}

// tryBeginUpdateDrain requires its caller to be outside track (the private
// control listener). Idle Responses WebSockets can close after the proxy gate.
func (l *requestLifecycle) tryBeginUpdateDrain(proxy *application.Proxy) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.draining || l.activeWork != 0 || !proxy.TryBeginIdleDrain() {
		return false
	}
	l.draining = true
	return true
}

func (l *requestLifecycle) resumeUpdateDrain(proxy *application.Proxy) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.draining || l.stopping || !proxy.ResumeAfterIdleDrain() {
		return false
	}
	l.draining = false
	return true
}

func (l *requestLifecycle) track(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idleResponseSocket := idleResponsesWebSocket(r)
		l.mu.Lock()
		if l.draining {
			l.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"server_draining","message":"Server is shutting down"}}`))
			return
		}
		l.active.Add(1)
		if !idleResponseSocket {
			l.activeWork++
		}
		l.mu.Unlock()
		defer func() {
			l.mu.Lock()
			if !idleResponseSocket {
				l.activeWork--
			}
			l.active.Done()
			l.mu.Unlock()
		}()
		next.ServeHTTP(w, r)
	})
}

func idleResponsesWebSocket(r *http.Request) bool {
	if r.Method != http.MethodGet || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	switch r.URL.Path {
	case "/v1/responses", "/v1/responses/", "/backend-api/codex/responses", "/backend-api/codex/responses/":
		return true
	default:
		return false
	}
}

func (r *runtime) health(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	status := "ok"
	if request.URL.Path == "/health/ready" {
		ctx, cancel := context.WithTimeout(request.Context(), time.Second)
		defer cancel()
		if r.data.store.Health(ctx) != nil {
			status = "unavailable"
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}
	_ = json.NewEncoder(w).Encode(struct {
		Status string `json:"status"`
	}{status})
}
