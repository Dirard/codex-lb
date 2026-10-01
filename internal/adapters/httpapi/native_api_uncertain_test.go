package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type nativeLateAccountingErrorProvider struct{}

func TestNativeExplicitMissingUsageAllowanceKeepsPendingWithoutFalseOutcome(t *testing.T) {
	for _, limit := range []int64{0, 1} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chat_unknown","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}]}`))
			}))
			defer upstream.Close()
			store, _, handler := newNativeTestServer(t, upstream, &nativeTestProvider{}, limit)
			ctx := context.Background()
			source, err := store.GetModelSource(ctx, "src_native_embed")
			if err != nil {
				t.Fatal(err)
			}
			source.ProviderConfig.AllowMissingUsage = true
			if err := store.SaveModelSource(ctx, source, nil); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"public-embed","messages":[{"role":"user","content":"hello"}]}`))
			r.Header.Set("Authorization", "Bearer synthetic-native-key")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			want := 200
			if limit != 0 {
				want = 502
			}
			pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			if w.Code != want || err != nil || len(pending) != 1 {
				t.Fatalf("missing-usage opt-in accounting: status=%d pending=%+v err=%v body=%s", w.Code, pending, err, w.Body.String())
			}
			if version, err := store.CaptureQuotaOutcome(ctx, source.ID); err != nil || version != 0 {
				t.Fatalf("pending response became a finalized account success: %d %v", version, err)
			}
		})
	}
}

func (nativeLateAccountingErrorProvider) Respond(_ context.Context, _ application.ResponseTarget, _ json.RawMessage,
	emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
	for _, event := range []application.ResponseEvent{
		{Type: "response.created", Data: []byte(`{"type":"response.created","response":{"id":"resp_late","status":"in_progress","model":"gpt-5.5"}}`)},
		{Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","delta":"hi"}`)},
		{Type: "response.completed", Data: []byte(`{"type":"response.completed","response":{"id":"resp_late","status":"completed","usage":{"inputTokens":1,"outputTokens":1}}}`)},
	} {
		if err := emit(event); err != nil {
			return application.ResponseResult{}, err
		}
	}
	return application.ResponseResult{ResponseID: "resp_late", Usage: domain.UsageAmount{InputTokens: 1, CachedInputTokens: 2}, UsageKnown: true}, nil
}

func TestNativeMissingUsageRetainsReservationAndBlocksFurtherLimitedTraffic(t *testing.T) {
	for _, test := range []struct {
		name, path, request, response, contentType string
		stream                                     bool
	}{
		{"embeddings", "/v1/embeddings", `{"model":"public-embed","input":"hello"}`,
			`{"object":"list","data":[]}`, "application/json", false},
		{"chat", "/v1/chat/completions", `{"model":"public-embed","messages":[{"role":"user","content":"hello"}]}`,
			`{"id":"chat","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`, "application/json", false},
		{"chat stream", "/v1/chat/completions", `{"model":"public-embed","messages":[{"role":"user","content":"hello"}],"stream":true}`,
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n", "text/event-stream", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", test.contentType)
				_, _ = w.Write([]byte(test.response))
			}))
			defer upstream.Close()
			store, _, handler := newNativeTestServer(t, upstream, &nativeTestProvider{}, 1)
			post := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, "http://localhost"+test.path, strings.NewReader(test.request))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Authorization", "Bearer synthetic-native-key")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			first := post()
			if !test.stream && (first.Code != 502 || !strings.Contains(first.Body.String(), "usage_unavailable")) ||
				test.stream && (first.Code != 200 || !strings.Contains(first.Body.String(), `"content":"hi"`) ||
					!strings.Contains(first.Body.String(), "usage_unavailable") || strings.Contains(first.Body.String(), "data: [DONE]")) {
				t.Fatalf("missing usage error: %d %s", first.Code, first.Body.String())
			}
			ctx := context.Background()
			pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			if err != nil || len(pending) != 1 || pending[0].Status != "reserved" || !pending[0].NeedsReconciliation {
				t.Fatalf("unknown usage was zero-settled: %+v, %v", pending, err)
			}
			key, err := store.GetAPIKey(ctx, "key_native")
			if err != nil || len(key.Limits) != 1 || key.Limits[0].CurrentValue != 1 {
				t.Fatalf("reservation budget was forgiven: %+v, %v", key.Limits, err)
			}
			if released, err := store.ReleaseStaleReservations(ctx, time.Now().Add(24*time.Hour)); err != nil || released != 0 {
				t.Fatalf("unknown usage was stale-released: %d, %v", released, err)
			}
			if second := post(); second.Code != 429 || calls.Load() != 1 {
				t.Fatalf("limited key bypassed held budget: %d calls=%d body=%s", second.Code, calls.Load(), second.Body.String())
			}
			totals, err := store.UsageTotals(ctx, "key_native", "src_native_embed")
			if err != nil || totals.RequestCount != 0 || totals.Usage.CostMicrodollars != 0 {
				t.Fatalf("unknown usage recorded as known zero: %+v, %v", totals, err)
			}
			event := domain.UsageEvent{RequestID: "confirmed", AccountID: "src_native_embed",
				ModelSourceID: "src_native_embed", Model: "public-embed", RequestKind: "normal",
				Status: "success", RequestedAt: time.Now().UTC(),
				Usage: domain.UsageAmount{InputTokens: 2, CostMicrodollars: 2}}
			if applied, err := store.SettleUsage(ctx, pending[0].ID, domain.UsageSettlement{Status: "finalized", Event: event}); err != nil || !applied {
				t.Fatalf("confirmed offline usage not settled: %v, %v", applied, err)
			}
			totals, err = store.UsageTotals(ctx, "key_native", "src_native_embed")
			if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 2 {
				t.Fatalf("confirmed usage missing or duplicated: %+v, %v", totals, err)
			}
		})
	}
}

func TestNativeAllowMissingUsageCannotBypassKeyLimit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	store, _, handler := newNativeTestServer(t, upstream, &nativeTestProvider{}, 1)
	ctx := context.Background()
	source, err := store.GetModelSource(ctx, "src_native_embed")
	if err != nil {
		t.Fatal(err)
	}
	source.ProviderConfig.AllowMissingUsage = true
	if err := store.SaveModelSource(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions",
		strings.NewReader(`{"model":"public-embed","messages":[{"role":"user","content":"hello"}]}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic-native-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "usage_unavailable") {
		t.Fatalf("allowMissingUsage bypassed limited key: %d %s", w.Code, w.Body.String())
	}
	pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
	if err != nil || len(pending) != 1 || !pending[0].NeedsReconciliation {
		t.Fatalf("allowMissingUsage forgave unknown spend: %+v, %v", pending, err)
	}
}

func TestNativeStreamAllowMissingUsageGatesTerminalUntilSettlement(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	store, _, handler := newNativeTestServer(t, upstream, &nativeTestProvider{}, 1)
	ctx := context.Background()
	source, err := store.GetModelSource(ctx, "src_native_embed")
	if err != nil {
		t.Fatal(err)
	}
	source.ProviderConfig.AllowMissingUsage = true
	if err := store.SaveModelSource(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions",
		strings.NewReader(`{"model":"public-embed","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic-native-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `"content":"hi"`) || !strings.Contains(body, "usage_unavailable") ||
		strings.Contains(body, `"finish_reason":"stop"`) || strings.Contains(body, "data: [DONE]") {
		t.Fatalf("success terminal leaked before usage settlement: %d %s", w.Code, body)
	}
	pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("late usage failure did not retain reservation: %+v, %v", pending, err)
	}
}

func TestTranslatedChatStreamDropsTerminalOnLateAccountingError(t *testing.T) {
	_, _, handler := newNativeTestServer(t, nil, nativeLateAccountingErrorProvider{}, 0)
	r := httptest.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"stream":true,"stream_options":{"include_usage":true}}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic-native-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `"content":"hi"`) ||
		!strings.Contains(body, "invalid_upstream_usage") || strings.Contains(body, `"finish_reason":"stop"`) ||
		strings.Contains(body, "data: [DONE]") {
		t.Fatalf("translated terminal leaked before settlement: %d %s", w.Code, body)
	}
}

func TestNativeStreamFailureOmitsUpstreamMessage(t *testing.T) {
	frame := nativeFailedError(application.NativeAPIResult{Response: []byte(`{"error":{"code":"provider_failed","message":"secret provider detail"}}`)})
	if strings.Contains(string(frame), "secret provider detail") || !strings.Contains(string(frame), "provider_failed") {
		t.Fatalf("unsafe native stream error frame: %s", frame)
	}
}

func TestNativeReportedZeroUsageSettlesWithoutFalseUncertainty(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`))
	}))
	defer upstream.Close()
	store, _, handler := newNativeTestServer(t, upstream, &nativeTestProvider{}, 1)
	r := httptest.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions",
		strings.NewReader(`{"model":"public-embed","messages":[{"role":"user","content":"hello"}]}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic-native-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("reported zero usage was treated as unknown: %d %s", w.Code, w.Body.String())
	}
	ctx := context.Background()
	pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("reported zero usage left uncertain reservation: %+v, %v", pending, err)
	}
	key, err := store.GetAPIKey(ctx, "key_native")
	if err != nil || key.Limits[0].CurrentValue != 0 {
		t.Fatalf("reported zero usage kept the reserved budget: %+v, %v", key.Limits, err)
	}
	totals, err := store.UsageTotals(ctx, "key_native", "src_native_embed")
	if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 0 || totals.Usage.CostMicrodollars != 0 {
		t.Fatalf("reported zero usage was not recorded once: %+v, %v", totals, err)
	}
}

func TestNativeHTTPFailureAccountsOnlyConsistentReportedUsage(t *testing.T) {
	for _, test := range []struct {
		name, usage  string
		wantStatus   int
		wantPending  int
		wantRequests int64
	}{
		{"charged aliases", `{"prompt_tokens":3,"input_tokens":3,"completion_tokens":2,"output_tokens":2,"total_tokens":5,"prompt_cache_hit_tokens":1,"prompt_cache_miss_tokens":2}`, 503, 0, 1},
		{"conflicting explicit zero", `{"prompt_tokens":0,"input_tokens":3,"completion_tokens":2,"total_tokens":5}`, 502, 1, 0},
		{"conflicting cache details", `{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"prompt_cache_hit_tokens":1,"prompt_tokens_details":{"cached_tokens":2}}`, 502, 1, 0},
		{"unknown service tier", `{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}`, 502, 1, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			tier := `"priority"`
			if test.name == "unknown service tier" {
				tier = `"unpriced-tier"`
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":{"code":"synthetic_failure"},"usage":` + test.usage + `,"service_tier":` + tier + `}`))
			}))
			defer upstream.Close()
			store, _, handler := newNativeTestServer(t, upstream, &nativeTestProvider{}, 1)
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"public-embed","messages":[{"role":"user","content":"hello"}]}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer synthetic-native-key")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			ctx := context.Background()
			pending, pendingErr := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			totals, totalsErr := store.UsageTotals(ctx, "key_native", "src_native_embed")
			if w.Code != test.wantStatus || pendingErr != nil || totalsErr != nil || len(pending) != test.wantPending ||
				totals.RequestCount != test.wantRequests {
				t.Fatalf("native failure accounting: status=%d body=%s pending=%+v totals=%+v errors=%v/%v", w.Code, w.Body.String(), pending, totals, pendingErr, totalsErr)
			}
			if test.wantRequests == 1 && (totals.FailedCount != 1 || totals.Usage.InputTokens != 3 || totals.Usage.OutputTokens != 2 || totals.Usage.CachedInputTokens != 1) {
				t.Fatalf("charged failure lost counters: %+v", totals)
			}
		})
	}
}

func TestNativeSSEErrorFrameSettlesReportedUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: error\n" + `data: {"error":{"code":"synthetic_failure"},"service_tier":"priority","usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"prompt_cache_hit_tokens":1,"prompt_cache_miss_tokens":2}}` + "\n\n"))
	}))
	defer upstream.Close()
	store, _, handler := newNativeTestServer(t, upstream, &nativeTestProvider{}, 1)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"public-embed","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic-native-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	ctx := context.Background()
	pending, pendingErr := store.ListReservationsNeedingReconciliation(ctx, "", 10)
	totals, totalsErr := store.UsageTotals(ctx, "key_native", "src_native_embed")
	if w.Code != 502 || !strings.Contains(w.Body.String(), "synthetic_failure") || strings.Contains(w.Body.String(), "[DONE]") ||
		pendingErr != nil || totalsErr != nil || len(pending) != 0 || totals.RequestCount != 1 || totals.FailedCount != 1 ||
		totals.Usage.InputTokens != 3 || totals.Usage.OutputTokens != 2 || totals.Usage.CachedInputTokens != 1 {
		t.Fatalf("SSE error usage was lost: status=%d body=%s totals=%+v pending=%+v errors=%v/%v", w.Code, w.Body.String(), totals, pending, pendingErr, totalsErr)
	}
}
