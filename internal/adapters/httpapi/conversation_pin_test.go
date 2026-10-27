package httpapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"codex-lb/internal/application"
)

type conversationAttempts struct {
	mu       sync.Mutex
	accounts []string
	bodies   []string
}

func (a *conversationAttempts) record(target application.ResponseTarget, body json.RawMessage) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.accounts = append(a.accounts, target.Account.ID)
	a.bodies = append(a.bodies, string(body))
	return len(a.accounts)
}

func (a *conversationAttempts) snapshot() ([]string, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.accounts...), append([]string(nil), a.bodies...)
}

func quotaRefused() error {
	return &application.ProviderFailure{Code: "usage_limit_reached", Status: 429, QuotaRefused: true, Dispatched: true}
}

// Provider-side conversation state belongs to the account that holds it. A
// quota refusal must not resend the turn elsewhere without that state.
func TestQuotaRefusalNeverReplaysProviderConversationOnAnotherAccount(t *testing.T) {
	attempts := &conversationAttempts{}
	server, store := wireFixture(t, func(_ context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		if attempts.record(target, body) == 1 {
			return application.ResponseResult{}, quotaRefused()
		}
		return wireComplete("resp_without_context", emit)
	})
	addAffinityAccount(t, store, "wire-account-b")
	status, payload := liteHTTPPost(t, server.URL+"/v1/responses", `{"model":"gpt-6-sol","conversation":"conv_123","input":"only the newest user message","stream":true}`)
	accounts, bodies := attempts.snapshot()
	if status != 409 || !strings.Contains(payload, "quota_failover_requires_context") || len(accounts) != 1 {
		t.Fatalf("conversation-bound turn was replayed: status=%d body=%s attempts=%v sent=%q", status, payload, accounts, bodies)
	}
}

// A stored response produced inside a provider conversation stays pinned: its
// local history is not the whole conversation and cannot be replayed elsewhere.
func TestQuotaRefusalNeverReplaysConversationBoundContinuation(t *testing.T) {
	attempts := &conversationAttempts{}
	server, store := wireFixture(t, func(_ context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		if attempts.record(target, body) == 1 {
			return wireComplete("resp_in_conversation", emit)
		}
		return application.ResponseResult{}, quotaRefused()
	})
	addAffinityAccount(t, store, "wire-account-b")
	if status, payload := liteHTTPPost(t, server.URL+"/v1/responses", `{"model":"gpt-6-sol","conversation":"conv_123","input":"first","stream":true}`); status != 200 {
		t.Fatalf("first turn: %d %s", status, payload)
	}
	status, payload := liteHTTPPost(t, server.URL+"/v1/responses", `{"model":"gpt-6-sol","previous_response_id":"resp_in_conversation","input":"second","stream":true}`)
	accounts, _ := attempts.snapshot()
	if status != 409 || !strings.Contains(payload, "quota_failover_requires_context") || len(accounts) != 2 || accounts[0] != accounts[1] {
		t.Fatalf("conversation-bound continuation crossed accounts: status=%d body=%s attempts=%v", status, payload, accounts)
	}
}
