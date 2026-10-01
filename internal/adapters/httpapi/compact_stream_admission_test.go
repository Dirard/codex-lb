package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
)

type compactEventNotice struct {
	*provider.Adapter
	first chan struct{}
	once  sync.Once
}

func (p *compactEventNotice) Compact(ctx context.Context, target application.CodexOperationTarget, body json.RawMessage) (application.CodexOperationResult, error) {
	first := target.OnFirstUpstreamEvent
	target.OnFirstUpstreamEvent = func() {
		if first != nil {
			first()
		}
		p.once.Do(func() { close(p.first) })
	}
	return p.Adapter.Compact(ctx, target, body)
}

func TestCompactStreamReleasesCreateButRetainsStreamCapacity(t *testing.T) {
	_, store, _ := wireFixtureWithProxy(t, nil)
	ctx := context.Background()
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	create, stream, reserve := 1, 2, 0
	settings.ProxyAccountResponseCreateLimitOverride = &create
	settings.ProxyAccountStreamLimitOverride = &stream
	settings.ProxyAccountStreamRecoveryReserveOverride = &reserve
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	unblock := make(chan struct{})
	var release sync.Once
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{}}\n\n")
		w.(http.Flusher).Flush()
		if id == 1 {
			select {
			case <-unblock:
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"compact_%d\",\"output\":[{\"type\":\"compaction\",\"encrypted_content\":\"opaque\"}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", id)
	}))
	defer upstream.Close()
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
	defer adapter.Close()
	notice := &compactEventNotice{Adapter: adapter, first: make(chan struct{})}
	proxy := application.NewProxy(store, adapter, nil, application.ProxyConfig{MaxStreams: 3, MaxQueued: 3, QueueTimeout: 50 * time.Millisecond})
	ops := application.NewCodexOperations(store, store, notice, time.Hour)
	ops.ConfigureAdmission(proxy)
	ops.ConfigureAccountSelection(proxy)
	mux := http.NewServeMux()
	httpapi.RegisterCodexOperationRoutes(mux, store, ops)
	server := httptest.NewServer(mux)
	defer server.Close()
	defer release.Do(func() { close(unblock) })
	post := func() (int, string) {
		return liteHTTPPost(t, server.URL+"/v1/responses/compact", `{"model":"gpt-6-luna","input":"history"}`)
	}
	firstDone := make(chan int, 1)
	go func() { status, _ := post(); firstDone <- status }()
	select {
	case <-notice.first:
	case <-time.After(3 * time.Second):
		t.Fatal("first compact event not received")
	}
	if status, body := post(); status != 200 {
		t.Fatalf("create capacity held until completion: %d %s", status, body)
	}
	stream = 1
	settings, err = store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.ProxyAccountStreamLimitOverride = &stream
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if status, _ := post(); status != 429 || calls.Load() != 2 {
		t.Fatalf("compact released stream capacity prematurely: status=%d calls=%d", status, calls.Load())
	}
	release.Do(func() { close(unblock) })
	if <-firstDone != 200 {
		t.Fatal("first compact failed")
	}
	if status, body := post(); status != 200 {
		t.Fatalf("compact leaked its stream slot: %d %s", status, body)
	}
}
