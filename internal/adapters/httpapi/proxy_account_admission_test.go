package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestResponsesRouteAppliesLiveAccountCapAndLocal429(t *testing.T) {
	_, store, _ := wireFixtureWithProxy(t, nil)
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "lease.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	create, stream, reserve := 2, 1, 0
	settings.ProxyAccountResponseCreateLimitOverride = &create
	settings.ProxyAccountStreamLimitOverride = &stream
	settings.ProxyAccountStreamRecoveryReserveOverride = &reserve
	settings.RoutingStrategy, settings.SingleAccountID = "single_account", "wire-account"
	settings.HTTPTransportPolicy = "always_http"
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	provider := wireProvider(func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, _ func(application.ResponseEvent) error) (application.ResponseResult, error) {
		call := calls.Add(1)
		if target.OnFirstUpstreamEvent != nil {
			target.OnFirstUpstreamEvent()
		}
		if call == 1 {
			close(entered)
			<-release
		}
		id := "resp_admission_" + string(rune('0'+call))
		response := []byte(`{"id":"` + id + `","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
		return application.ResponseResult{ResponseID: id, Response: response, Usage: domain.UsageAmount{InputTokens: 1, OutputTokens: 1}, UsageKnown: true}, nil
	})
	proxy := application.NewProxy(store, provider, vault, application.ProxyConfig{MaxStreams: 3, MaxQueued: 3, QueueTimeout: 40 * time.Millisecond})
	server := httptest.NewServer(httpapi.NewProxyHandler(store, proxy, nil))
	defer server.Close()
	post := func() (int, http.Header, string) {
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-6-sol","input":"hello"}`))
		req.Header.Set("Authorization", "Bearer synthetic-key")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Error(err)
			return 0, nil, ""
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header, string(body)
	}
	firstDone := make(chan int, 1)
	go func() { status, _, _ := post(); firstDone <- status }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("first upstream attempt did not start")
	}
	status, header, body := post()
	if status != 429 || header.Get("Retry-After") == "" || !strings.Contains(body, `"code":"account_stream_cap"`) ||
		!strings.Contains(body, `"type":"rate_limit_error"`) || calls.Load() != 1 {
		close(release)
		t.Fatalf("account cap did not stop second upstream attempt: status=%d body=%s calls=%d", status, body, calls.Load())
	}
	settings, err = store.LoadSettings(ctx)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	unlimited := 0
	settings.ProxyAccountStreamLimitOverride = &unlimited
	if err := store.SaveSettings(ctx, settings); err != nil {
		close(release)
		t.Fatal(err)
	}
	status, _, body = post()
	close(release)
	if status != 200 || calls.Load() != 2 || <-firstDone != 200 {
		t.Fatalf("live settings did not govern next admission: status=%d body=%s calls=%d", status, body, calls.Load())
	}
}

func TestResponsesRouteReleasesCreateCapAtActualUpstreamEvent(t *testing.T) {
	_, store, _ := wireFixtureWithProxy(t, nil)
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "create.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	create, stream, reserve := 1, 2, 0
	settings.ProxyAccountResponseCreateLimitOverride = &create
	settings.ProxyAccountStreamLimitOverride = &stream
	settings.ProxyAccountStreamRecoveryReserveOverride = &reserve
	settings.RoutingStrategy, settings.SingleAccountID = "single_account", "wire-account"
	settings.HTTPTransportPolicy = "always_http"
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	firstCreated, release := make(chan struct{}), make(chan struct{})
	var createdOnce sync.Once
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		id := strconv.Itoa(int(calls.Add(1)))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+`{"type":"response.created","response":{"id":"resp_`+id+`"}}`+"\n\n")
		w.(http.Flusher).Flush()
		if id == "1" {
			<-release
		}
		_, _ = io.WriteString(w, "data: "+`{"type":"response.completed","response":{"id":"resp_`+id+`","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
	}))
	defer upstream.Close()
	var adapter *provider.Adapter
	bridge := wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		first := target.OnFirstUpstreamEvent
		target.OnFirstUpstreamEvent = func() {
			if first != nil {
				first()
			}
			createdOnce.Do(func() { close(firstCreated) })
		}
		return adapter.Respond(ctx, target, body, emit)
	})
	adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
	defer adapter.Close()
	proxy := application.NewProxy(store, bridge, vault, application.ProxyConfig{MaxStreams: 3, MaxQueued: 3, QueueTimeout: 100 * time.Millisecond})
	server := httptest.NewServer(httpapi.NewProxyHandler(store, proxy, nil))
	defer server.Close()
	post := func() int {
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-6-sol","input":"hello"}`))
		req.Header.Set("Authorization", "Bearer synthetic-key")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Error(err)
			return 0
		}
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}
	firstDone := make(chan int, 1)
	go func() { firstDone <- post() }()
	select {
	case <-firstCreated:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("first upstream event did not release create cap")
	}
	second := post()
	close(release)
	if second != 200 || <-firstDone != 200 || calls.Load() != 2 {
		t.Fatalf("create cap was held for full stream: second=%d calls=%d", second, calls.Load())
	}
}
