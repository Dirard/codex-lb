package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestOperationAuthenticationRetryDoesNotRepeatReportedBilling(t *testing.T) {
	for _, test := range []struct {
		name, billing string
		retry         bool
	}{
		{"absent", "", true},
		{"zero_tokens", `,"usage":{"input_tokens":0,"output_tokens":0}`, true},
		{"charged_tokens", `,"usage":{"input_tokens":2,"output_tokens":1}`, false},
		{"partial_tokens", `,"usage":{"input_tokens":2}`, false},
		{"invalid_tokens", `,"usage":{"input_tokens":-1,"output_tokens":0}`, false},
		{"audio_seconds", `,"usage":{"seconds":3}`, false},
		{"duration", `,"duration":3`, false},
		{"invalid_duration", `,"duration":"unknown"`, false},
		{"zero_tokens_with_duration", `,"usage":{"input_tokens":0,"output_tokens":0},"duration":3`, false},
		{"output_without_usage", `,"output":[{"type":"message","text":"already generated"}]`, false},
		{"output_with_zero_usage", `,"output":[{"type":"message","text":"already generated"}],"usage":{"input_tokens":0,"output_tokens":0}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			tokens := &refreshTokens{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if calls.Add(1) == 1 {
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprintf(w, `{"error":{"code":"invalid_token"}%s}`, test.billing)
					return
				}
				fmt.Fprint(w, `{"id":"compact","object":"response.compaction","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":0,"output_tokens":0}}`)
			}))
			defer server.Close()
			adapter := New(&sourceStore{credential: domain.AccountCredential{AccountID: "chatgpt"}}, tokens, testCipher{}, Config{HTTPClient: server.Client(), ChatGPTBaseURL: server.URL})
			defer adapter.Close()
			result, err := adapter.Compact(context.Background(), application.CodexOperationTarget{
				Account: domain.Account{ID: "chatgpt", Kind: domain.AccountChatGPT}, KeyID: "key",
			}, []byte(`{"model":"gpt-6-sol","input":[]}`))
			if test.retry {
				if err != nil || result.Failed || calls.Load() != 2 || tokens.calls.Load() != 1 {
					t.Fatalf("free auth rejection did not recover: %+v %v calls=%d refresh=%d", result, err, calls.Load(), tokens.calls.Load())
				}
			} else if calls.Load() != 1 || tokens.calls.Load() != 0 || !result.Failed {
				t.Fatalf("reported billing was repeated: %+v %v calls=%d refresh=%d", result, err, calls.Load(), tokens.calls.Load())
			}
		})
	}
}
