package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNativeRefusalsPreserveReportedBilling(t *testing.T) {
	for _, mode := range []string{"chat_json", "chat_stream_http", "chat_stream_error", "chat_json_error", "embeddings"} {
		for _, report := range []string{"charged", "zero", "partial", "invalid", "overflow", "inconsistent_total"} {
			t.Run(mode+"/"+report, func(t *testing.T) {
				usage := `{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}`
				wantTokens, wantPending := int64(3), 0
				if mode == "embeddings" {
					usage, wantTokens = `{"prompt_tokens":3,"total_tokens":3}`, 3
				}
				switch report {
				case "zero":
					usage, wantTokens = `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, 0
				case "partial":
					usage, wantTokens, wantPending = `{"prompt_tokens":2}`, 0, 1
				case "invalid":
					usage, wantTokens, wantPending = `{"prompt_tokens":-1,"completion_tokens":0,"total_tokens":0}`, 0, 1
				case "overflow":
					usage, wantTokens, wantPending = `{"prompt_tokens":9223372036854775808,"completion_tokens":0,"total_tokens":9223372036854775808}`, 0, 1
				case "inconsistent_total":
					usage, wantTokens, wantPending = `{"prompt_tokens":2,"completion_tokens":1,"total_tokens":8}`, 0, 1
				}
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					payload := fmt.Sprintf(`{"error":{"code":"usage_limit_reached"},"usage":%s}`, usage)
					if mode == "chat_stream_error" {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: "+payload+"\n\n")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if mode != "chat_json_error" {
						w.WriteHeader(429)
					}
					fmt.Fprint(w, payload)
				}))
				defer upstream.Close()
				store, _, handler := newNativeTestServer(t, upstream, &nativeTestProvider{}, 0)
				path := "/v1/chat/completions"
				body := fmt.Sprintf(`{"model":"public-embed","messages":[{"role":"user","content":"hello"}],"stream":%t}`, strings.HasPrefix(mode, "chat_stream"))
				if mode == "embeddings" {
					path, body = "/v1/embeddings", `{"model":"public-embed","input":"hello"}`
				}
				request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				request.Header.Set("Authorization", "Bearer synthetic-native-key")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if calls.Load() != 1 || strings.Contains(response.Body.String(), "[DONE]") || !strings.Contains(response.Body.String(), "error") {
					t.Fatalf("rejection was repeated or became success: %d %s calls=%d", response.Code, response.Body.String(), calls.Load())
				}
				ctx := context.Background()
				pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
				totals, totalErr := store.UsageTotals(ctx, "key_native", "src_native_embed")
				if err != nil || totalErr != nil || len(pending) != wantPending || totals.RequestCount != int64(1-wantPending) ||
					totals.FailedCount != int64(1-wantPending) || totals.Usage.InputTokens+totals.Usage.OutputTokens != wantTokens {
					t.Fatalf("wrong rejection accounting: totals=%+v pending=%d err=%v/%v", totals, len(pending), err, totalErr)
				}
			})
		}
	}
}
