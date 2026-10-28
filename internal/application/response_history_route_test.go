package application_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// unreadableContext is a well-formed token this vault cannot decrypt.
func unreadableContext() []byte {
	raw := make([]byte, 96)
	raw[0] = 0x80
	return []byte(base64.URLEncoding.EncodeToString(raw))
}

// Stored history is read after dispatch. An unreadable history no longer fails
// a same-owner turn the provider can serve from its own state; the next
// continuation is stored without context and cross-account replay stays closed.
func TestUnreadableStoredHistoryKeepsOwnerTurnAndClosesReplay(t *testing.T) {
	proxy, store, stub := proxyFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	owner := domain.Continuation{ResponseID: "resp_unreadable", KeyID: "key-test", AccountID: "account-a", ProviderID: "openai",
		Model: "gpt-6-sol", CreatedAt: now, ExpiresAt: now.Add(time.Hour), ContextEncrypted: unreadableContext()}
	if err := store.SaveContinuation(ctx, owner, domain.ContinuationBounds{MaxRecords: 100, MaxContextBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	body := json.RawMessage(`{"model":"gpt-6-sol","previous_response_id":"resp_unreadable","input":"next"}`)
	result, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, body, nil)
	if err != nil || len(stub.accounts) != 1 || stub.accounts[0] != "account-a" {
		t.Fatalf("owner turn failed on unreadable history: %v attempts=%v", err, stub.accounts)
	}
	next, err := store.GetContinuation(ctx, "key-test", result.ResponseID, time.Now())
	if err != nil || len(next.ContextEncrypted) != 0 {
		t.Fatalf("incomplete tail stored as full context: %d bytes %v", len(next.ContextEncrypted), err)
	}
	stub.respond = func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		if target.Account.ID == "account-a" {
			return application.ResponseResult{}, &application.ProviderFailure{Code: "usage_limit_reached", Status: 429, QuotaRefused: true, Dispatched: true}
		}
		return complete("resp_replayed", emit)
	}
	_, err = proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, body, nil)
	var failure *application.ProxyError
	if !errors.As(err, &failure) || failure.Code != "quota_failover_requires_context" || len(stub.accounts) != 2 {
		t.Fatalf("unreadable history was replayed elsewhere: %v attempts=%v", err, stub.accounts)
	}
}
