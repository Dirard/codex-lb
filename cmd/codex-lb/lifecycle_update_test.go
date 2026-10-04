package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/application"
)

func TestUpdateDrainWaitsForHTTPAndRealtimeWork(t *testing.T) {
	for _, test := range []struct {
		name, method, path string
	}{
		{"sse", http.MethodPost, "/v1/responses"},
		{"chat", http.MethodPost, "/v1/chat/completions"},
		{"embeddings", http.MethodPost, "/v1/embeddings"},
		{"codex operation", http.MethodPost, "/backend-api/codex/responses/compact"},
		{"realtime websocket", http.MethodGet, "/v1/live/call"},
		{"non websocket response", http.MethodGet, "/v1/responses"},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy := application.NewProxy(nil, nil, nil, application.ProxyConfig{})
			var lifecycle requestLifecycle
			entered, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			handler := lifecycle.track(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				close(entered)
				<-finish
			}))
			request := httptest.NewRequest(test.method, test.path, nil)
			if test.name == "realtime websocket" {
				request.Header.Set("Upgrade", "websocket")
			}
			go func() {
				handler.ServeHTTP(httptest.NewRecorder(), request)
				close(done)
			}()
			<-entered
			if lifecycle.tryBeginUpdateDrain(proxy) {
				t.Fatal("active HTTP work passed the idle gate")
			}
			select {
			case <-proxy.DrainStarted():
				t.Fatal("failed idle check closed proxy admission")
			default:
			}
			close(finish)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("handler did not finish")
			}
			if !lifecycle.tryBeginUpdateDrain(proxy) {
				t.Fatal("idle runtime could not close admission")
			}
		})
	}
}

func TestUpdateDrainClosesIdleResponsesWebSocketAndRejectsNewHTTP(t *testing.T) {
	proxy := application.NewProxy(nil, nil, nil, application.ProxyConfig{})
	var lifecycle requestLifecycle
	entered, done := make(chan struct{}), make(chan struct{})
	handler := lifecycle.track(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-proxy.DrainStarted()
		close(done)
	}))
	request := httptest.NewRequest(http.MethodGet, "/backend-api/codex/responses/", nil)
	request.Header.Set("Upgrade", "websocket")
	go handler.ServeHTTP(httptest.NewRecorder(), request)
	<-entered
	if !lifecycle.tryBeginUpdateDrain(proxy) {
		t.Fatal("idle Responses WebSocket blocked update")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle Responses WebSocket did not observe drain")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("new HTTP request after gate: %d", response.Code)
	}
	lifecycle.active.Wait()
}

func TestUpdateDrainRejectsActiveTurnOnResponsesWebSocket(t *testing.T) {
	proxy := application.NewProxy(nil, nil, nil, application.ProxyConfig{})
	release, err := proxy.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var lifecycle requestLifecycle
	entered, done := make(chan struct{}), make(chan struct{})
	handler := lifecycle.track(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-proxy.DrainStarted()
		close(done)
	}))
	request := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	request.Header.Set("Upgrade", "websocket")
	go handler.ServeHTTP(httptest.NewRecorder(), request)
	<-entered
	if lifecycle.tryBeginUpdateDrain(proxy) {
		t.Fatal("active response.create passed idle gate")
	}
	release()
	if !lifecycle.tryBeginUpdateDrain(proxy) {
		t.Fatal("finished response.create still blocked idle gate")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WebSocket did not observe drain")
	}
	lifecycle.active.Wait()
}

func TestUpdateDrainCanResumeButShutdownCannot(t *testing.T) {
	proxy := application.NewProxy(nil, nil, nil, application.ProxyConfig{})
	var lifecycle requestLifecycle
	handler := lifecycle.track(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if !lifecycle.tryBeginUpdateDrain(proxy) {
		t.Fatal("idle update gate failed")
	}
	if !lifecycle.resumeUpdateDrain(proxy) {
		t.Fatal("cancelled update did not reopen HTTP and proxy admission")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("HTTP request after resume: %d", response.Code)
	}
	lifecycle.beginDrain()
	proxy.BeginDrain()
	if lifecycle.resumeUpdateDrain(proxy) || proxy.ResumeAfterIdleDrain() {
		t.Fatal("permanent shutdown was reopened")
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("HTTP request after permanent shutdown: %d", response.Code)
	}
}

func TestWorkerUpdateGateLeaseExpiresBeforeCommit(t *testing.T) {
	proxy := application.NewProxy(nil, nil, nil, application.ProxyConfig{})
	var lifecycle requestLifecycle
	var stopped atomic.Bool
	gate := workerUpdateGate{requests: &lifecycle, proxy: proxy, stop: func() { stopped.Store(true) }, timeout: 20 * time.Millisecond}
	defer gate.cancel()
	if !gate.prepare() {
		t.Fatal("idle update gate was not prepared")
	}
	handler := lifecycle.track(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
		if response.Code == http.StatusNoContent {
			if gate.commit() || stopped.Load() {
				t.Fatal("expired idle gate stopped the worker")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("expired idle gate did not restore public admission")
}

func TestWorkerUpdateGateCommitCannotResume(t *testing.T) {
	proxy := application.NewProxy(nil, nil, nil, application.ProxyConfig{})
	var lifecycle requestLifecycle
	var stopped atomic.Bool
	gate := workerUpdateGate{requests: &lifecycle, proxy: proxy, stop: func() { stopped.Store(true) }, timeout: time.Second}
	if !gate.prepare() || !gate.commit() || !stopped.Load() {
		t.Fatal("prepared worker stop was not committed")
	}
	gate.cancel()
	response := httptest.NewRecorder()
	lifecycle.track(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("permanently stopped worker admitted HTTP work")
	})).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("HTTP after committed stop: %d", response.Code)
	}
	if release, err := proxy.Acquire(context.Background()); release != nil || err == nil {
		t.Fatal("permanently stopped worker admitted proxy work")
	}
}
