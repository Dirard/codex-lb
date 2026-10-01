package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type refreshTokens struct {
	tokenSource
	calls atomic.Int64
}

func (s *refreshTokens) ForceRefresh(_ context.Context, account domain.Account, rejected string) (string, error) {
	if account.ID != "chatgpt" || rejected != "chatgpt-access-token" {
		return "", errors.New("wrong refresh scope")
	}
	s.calls.Add(1)
	return "chatgpt-refreshed-token", nil
}

func TestChatGPTRefreshRetriesOnlyAuthenticationRejection(t *testing.T) {
	for _, code := range []int{401, 429, 503} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls atomic.Int64
			tokens := &refreshTokens{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(code)
					_, _ = w.Write([]byte(`{"error":{"code":"rejected"}}`))
					return
				}
				if r.Header.Get("Authorization") != "Bearer chatgpt-refreshed-token" || r.Header.Get("ChatGPT-Account-ID") != "workspace" {
					t.Error("refresh crossed credentials/owner")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"refreshed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
			}))
			defer server.Close()
			adapter := New(&sourceStore{credential: domain.AccountCredential{AccountID: "chatgpt"}}, tokens, testCipher{}, Config{HTTPClient: server.Client(), ChatGPTBaseURL: server.URL})
			defer adapter.Close()
			result, err := adapter.Respond(context.Background(), application.ResponseTarget{KeyID: "key", Account: domain.Account{ID: "chatgpt", Kind: domain.AccountChatGPT, ChatGPTAccountID: "workspace"}}, []byte(`{"model":"gpt-6-sol","input":"hello"}`), nil)
			if code == 401 {
				if err != nil || result.ResponseID != "refreshed" || calls.Load() != 2 || tokens.calls.Load() != 1 {
					t.Fatalf("refresh outcome: %v calls=%d refresh=%d", err, calls.Load(), tokens.calls.Load())
				}
			} else if err == nil || calls.Load() != 1 || tokens.calls.Load() != 0 {
				t.Fatal("non-auth failure was replayed")
			}
		})
	}
}

func TestChatGPTRefreshNeverReplaysVisibleOutput(t *testing.T) {
	tokens := &refreshTokens{}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: " + `{"type":"response.output_text.delta","delta":"visible"}` + "\n\ndata: " + `{"type":"error","status":401,"error":{"code":"invalid_token"}}` + "\n\n"))
	}))
	defer server.Close()
	adapter := New(&sourceStore{credential: domain.AccountCredential{AccountID: "chatgpt"}}, tokens, testCipher{}, Config{HTTPClient: server.Client(), ChatGPTBaseURL: server.URL})
	defer adapter.Close()
	result, err := adapter.Respond(context.Background(), application.ResponseTarget{KeyID: "key", Account: domain.Account{ID: "chatgpt", Kind: domain.AccountChatGPT}}, []byte(`{"model":"gpt-6-sol","input":"hello","stream":true}`), func(application.ResponseEvent) error { return nil })
	if err == nil || !result.OutputObserved || calls.Load() != 1 || tokens.calls.Load() != 0 || strings.Contains(err.Error(), "visible") {
		t.Fatal("visible response was replayed or leaked")
	}
}

func TestChatGPTRefreshRetriesConfirmedZeroButNotChargedAuthFailure(t *testing.T) {
	for _, test := range []struct {
		name, usage  string
		known, retry bool
		input        int64
	}{
		{"zero", `{"input_tokens":0,"output_tokens":0}`, true, true, 0},
		{"charged", `{"input_tokens":3,"output_tokens":0}`, true, false, 3},
		{"partial", `{"input_tokens":3}`, false, false, 0},
		{"invalid", `{"input_tokens":-1,"output_tokens":0}`, false, false, 0},
		{"malformed", `"invalid"`, false, false, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			tokens := &refreshTokens{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprintf(w, `{"error":{"code":"invalid_token"},"usage":%s}`, test.usage)
					return
				}
				if r.Header.Get("Authorization") != "Bearer chatgpt-refreshed-token" {
					t.Error("wrong refreshed owner credential")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"refreshed","status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0}}}`+"\n\n")
			}))
			defer server.Close()
			adapter := New(&sourceStore{credential: domain.AccountCredential{AccountID: "chatgpt"}}, tokens, testCipher{}, Config{HTTPClient: server.Client(), ChatGPTBaseURL: server.URL})
			defer adapter.Close()
			result, err := adapter.Respond(context.Background(), application.ResponseTarget{KeyID: "key", Account: domain.Account{ID: "chatgpt", Kind: domain.AccountChatGPT}}, []byte(`{"model":"gpt-6-sol","input":"hello"}`), nil)
			if !test.retry {
				if err == nil || !result.UsageReported || result.UsageKnown != test.known || result.Usage.InputTokens != test.input || calls.Load() != 1 || tokens.calls.Load() != 0 {
					t.Fatal("charged auth rejection lost usage or repeated")
				}
			} else if err != nil || result.ResponseID != "refreshed" || calls.Load() != 2 || tokens.calls.Load() != 1 {
				t.Fatalf("zero auth rejection did not recover: %+v %v", result, err)
			}
		})
	}
}
