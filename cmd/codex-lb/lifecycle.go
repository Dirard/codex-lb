package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

type requestLifecycle struct {
	mu       sync.Mutex
	draining bool
	active   sync.WaitGroup
}

func (l *requestLifecycle) beginDrain() {
	l.mu.Lock()
	l.draining = true
	l.mu.Unlock()
}

func (l *requestLifecycle) track(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		l.mu.Unlock()
		defer l.active.Done()
		next.ServeHTTP(w, r)
	})
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
