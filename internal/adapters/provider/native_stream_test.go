package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const nativeUsageFrame = `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1},"service_tier":"default"}` + "\n\n"

func TestNativeStreamStopsAtDoneBeforeUpstreamCloses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, nativeUsageFrame+"data: [DONE]\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done() // Only closing the upstream response ends this handler.
	}))
	defer upstream.Close()
	adapter := New(nil, nil, nil, Config{HTTPClient: upstream.Client()})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := adapter.nativeChatStream(ctx, domain.ModelSource{}, upstream.URL, []byte(`{}`), http.Header{}, func(application.NativeAPIEvent) error { return nil })
	if err != nil || !result.UsageKnown || result.Usage.InputTokens != 2 || result.Usage.OutputTokens != 1 || result.ServiceTier != "default" {
		t.Fatalf("terminal marker waited for HTTP EOF or lost usage: %+v %v", result, err)
	}
}

func TestNativeStreamFailuresKeepObservedUsage(t *testing.T) {
	for _, test := range []struct {
		name, prefix, tail, code string
		input                    int64
		known                    bool
	}{
		{"truncated after finish", nativeUsageFrame, "", "stream_incomplete", 2, true},
		{"malformed later frame", nativeUsageFrame, "data: {invalid\n\n", "invalid_stream", 2, true},
		{"missing usage", "", "data: [DONE]\n\n", "usage_unavailable", 0, false},
		{"empty usage", "", "data: {\"usage\":{}}\n\n", "invalid_upstream_usage", 0, false},
		{"overflow", nativeUsageFrame, "data: {\"usage\":{\"prompt_tokens\":18446744073709551615,\"completion_tokens\":1}}\n\n", "invalid_upstream_usage", 2, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, test.prefix+test.tail)
			}))
			defer upstream.Close()
			adapter := New(nil, nil, nil, Config{HTTPClient: upstream.Client()})
			doneSent := false
			result, err := adapter.nativeChatStream(context.Background(), domain.ModelSource{}, upstream.URL, []byte(`{}`), http.Header{}, func(event application.NativeAPIEvent) error {
				doneSent = doneSent || event.Type == "chat.done"
				return nil
			})
			var failure *application.ProviderFailure
			if !errors.As(err, &failure) || failure.Code != test.code || result.Usage.InputTokens != test.input ||
				result.UsageKnown != test.known || doneSent {
				t.Fatalf("unexpected outcome: %+v %v done=%v", result, err, doneSent)
			}
		})
	}
}

func TestNativeStreamCancellationRetainsUsageAndNoDoneModeRemainsExplicit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, nativeUsageFrame)
	}))
	defer upstream.Close()
	adapter := New(nil, nil, nil, Config{HTTPClient: upstream.Client()})
	ctx, cancel := context.WithCancel(context.Background())
	result, err := adapter.nativeChatStream(ctx, domain.ModelSource{}, upstream.URL, []byte(`{}`), http.Header{}, func(application.NativeAPIEvent) error { cancel(); return context.Canceled })
	if !errors.Is(err, context.Canceled) || result.Usage.InputTokens != 2 {
		t.Fatalf("cancellation erased observed usage: %+v %v", result, err)
	}
	var source domain.ModelSource
	source.ProviderConfig.AllowNoSSEDone = true
	doneSent := false
	result, err = adapter.nativeChatStream(context.Background(), source, upstream.URL, []byte(`{}`), http.Header{}, func(event application.NativeAPIEvent) error {
		doneSent = doneSent || event.Type == "chat.done"
		return nil
	})
	if err != nil || result.Usage.InputTokens != 2 || !doneSent {
		t.Fatalf("explicit no-DONE compatibility failed: %+v %v done=%v", result, err, doneSent)
	}
}

func TestNativeStreamErrorFramePreservesUsageAndTierBeforeFailure(t *testing.T) {
	for _, test := range []struct {
		name, frames, code string
		known              bool
		tier               string
	}{
		{name: "charged error", frames: `event: error` + "\n" + `data: {"error":{"code":"synthetic_failure","message":"secret-token"},"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"prompt_cache_hit_tokens":1,"prompt_cache_miss_tokens":2},"service_tier":"priority"}` + "\n\n", code: "synthetic_failure", known: true, tier: "priority"},
		{name: "conflicting tier", frames: nativeUsageFrame + `event: error` + "\n" + `data: {"error":{"code":"synthetic_failure"},"service_tier":"priority"}` + "\n\n", code: "invalid_upstream_usage", known: false, tier: "default"},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, test.frames)
			}))
			defer upstream.Close()
			headers := http.Header{"Authorization": {"Bearer secret-token"}}
			adapter := New(nil, nil, nil, Config{HTTPClient: upstream.Client()})
			result, err := adapter.nativeChatStream(context.Background(), domain.ModelSource{}, upstream.URL, []byte(`{}`), headers, func(application.NativeAPIEvent) error { return nil })
			var failure *application.ProviderFailure
			if !errors.As(err, &failure) || failure.Code != test.code || result.UsageKnown != test.known || result.ServiceTier != test.tier ||
				strings.Contains(string(result.Response), "secret-token") {
				t.Fatalf("error frame lost tier/usage or leaked credential: result=%+v error=%v", result, err)
			}
			if test.known && (result.Usage.InputTokens != 3 || result.Usage.OutputTokens != 2 || result.Usage.CachedInputTokens != 1) {
				t.Fatalf("charged stream error lost usage: %+v", result)
			}
		})
	}
}
