package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/application"
)

func TestSourceCapacityAndTimeoutReleaseWithoutDispatchOrQuotaPenalty(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			close(started)
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()
	source, credential := zaiSource(upstream.URL)
	source.MaxConcurrency, source.TimeoutSeconds = 1, 1
	adapter := New(&sourceStore{source: source, credential: credential}, tokenSource{}, testCipher{}, Config{HTTPClient: upstream.Client()})
	defer adapter.Close()
	target := application.ResponseTarget{Account: source.Account(), KeyID: "synthetic-key"}
	body := mustJSON(map[string]any{"model": source.Models[0].Model, "input": "test"})
	done := make(chan error, 1)
	go func() { _, err := adapter.Respond(context.Background(), target, body, nil); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not receive first request")
	}
	_, err := adapter.Respond(context.Background(), target, body, nil)
	var failure *application.ProviderFailure
	if !errors.As(err, &failure) || failure.Code != "model_source_capacity" || failure.Status != 503 || failure.Dispatched || failure.QuotaRefused || calls.Load() != 1 {
		t.Fatalf("source capacity was not an undispatched local rejection: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("configured source timeout not enforced: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("source request retained its slot after timeout")
	}
	if _, err := adapter.Respond(context.Background(), target, body, nil); err != nil {
		t.Fatalf("source slot did not recover: %v", err)
	}
	adapter.sourceMu.Lock()
	defer adapter.sourceMu.Unlock()
	if len(adapter.sourceActive) != 0 {
		t.Fatal("inactive source state retained")
	}
}

func TestAncillaryClientNeverFollowsBodyRedirect(t *testing.T) {
	var redirected atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer sink.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", sink.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := server.Client()
	adapter := New(&sourceStore{}, tokenSource{}, testCipher{}, Config{HTTPClient: client})
	defer adapter.Close()
	result, err := adapter.operation(context.Background(), "POST", server.URL, []byte(`{"private":"synthetic"}`), "application/json", nil, nil)
	if err != nil || result.Status != 307 || redirected.Load() != 0 || client.CheckRedirect != nil {
		t.Fatal("redirect replayed a provider body or mutated caller client")
	}
}
