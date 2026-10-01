package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/application"
)

func TestSSEKeepaliveWaitsForAdmissionIncludingCompactTrigger(t *testing.T) {
	_, store := wireFixture(t, nil)
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	create := 1
	settings.ProxyAccountResponseCreateLimitOverride = &create
	if err := store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	upstream := wireProvider(func(ctx context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		close(entered)
		select {
		case <-release:
			return wireComplete("held_response", emit)
		case <-ctx.Done():
			return application.ResponseResult{}, ctx.Err()
		}
	})
	// The real keepalive ticks at 10s. Both queue rejections happen after that
	// first tick; shortening the wait would miss the externally failing path.
	proxy := application.NewProxy(store, upstream, vault, application.ProxyConfig{
		MaxStreams: 4, MaxQueued: 4, QueueTimeout: 10200 * time.Millisecond,
	})
	operations := application.NewCodexOperations(store, store, nil, time.Hour)
	operations.ConfigureAdmission(proxy)
	operations.ConfigureAccountSelection(proxy)
	proxy.ConfigureCompaction(operations)
	server := httptest.NewServer(httpapi.NewProxyHandler(store, proxy, nil))
	t.Cleanup(server.Close)
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	type response struct {
		status int
		header http.Header
		body   string
		err    error
	}
	post := func(path, body string) response {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, strings.NewReader(body))
		if err != nil {
			return response{err: err}
		}
		req.Header.Set("Authorization", "Bearer synthetic-key")
		req.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			return response{err: err}
		}
		defer res.Body.Close()
		payload, err := io.ReadAll(res.Body)
		return response{res.StatusCode, res.Header, string(payload), err}
	}
	const ordinary = `{"model":"gpt-6-sol","input":"hello","stream":true}`
	first := make(chan response, 1)
	go func() { first <- post("/v1/responses", ordinary) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first request was not dispatched")
	}
	rejected := make(chan response, 2)
	go func() { rejected <- post("/v1/responses", ordinary) }()
	go func() {
		rejected <- post("/backend-api/codex/responses", `{"model":"gpt-6-sol","input":[{"role":"user","content":"compact"},{"type":"compaction_trigger"}],"stream":true}`)
	}()
	for range 2 {
		got := <-rejected
		if got.err != nil || got.status != 429 || got.header.Get("Retry-After") == "" || !strings.Contains(got.body, "account_response_create_cap") || strings.Contains(got.body, ": keepalive") {
			t.Fatalf("keepalive committed HTTP success before admission: %+v", got)
		}
	}
	once.Do(func() { close(release) })
	got := <-first
	if got.err != nil || got.status != 200 || !strings.Contains(got.body, ": keepalive") || !strings.Contains(got.body, "response.completed") {
		t.Fatalf("admitted upstream wait lost keepalive or completion: %+v", got)
	}
	totals, err := store.UsageTotals(context.Background(), "wire-key", "")
	if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 10 {
		t.Fatalf("capacity refusal affected accounting: %+v %v", totals, err)
	}
}
