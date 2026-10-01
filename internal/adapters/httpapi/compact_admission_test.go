package httpapi_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestCompactCapacitySelectsFreeAccountButNeverMovesOwner(t *testing.T) {
	ctx := context.Background()
	_, store := wireFixture(t, nil)
	account, _ := store.GetAccount(ctx, "wire-account")
	account.ChatGPTAccountID, account.RoutingPolicy = account.ID, "burn_first"
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	account.ID, account.ChatGPTAccountID, account.RoutingPolicy = "alternative", "alternative", "normal"
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	credential, _ := store.GetAccountCredential(ctx, "wire-account")
	credential.AccountID = account.ID
	if err := store.SaveAccountCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	settings, _ := store.LoadSettings(ctx)
	createCap := 1
	settings.ProxyAccountResponseCreateLimitOverride = &createCap
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContinuation(ctx, domain.Continuation{ResponseID: "pinned", KeyID: "wire-key", AccountID: "wire-account", ProviderID: "openai", Model: "gpt-6-sol", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, domain.ContinuationBounds{MaxRecords: 10, MaxContextBytes: 10000}); err != nil {
		t.Fatal(err)
	}
	entered, unblock := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var oldCalls, alternateCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("ChatGPT-Account-ID")
		if id == "wire-account" {
			if oldCalls.Add(1) == 1 {
				close(entered)
				select {
				case <-unblock:
				case <-r.Context().Done():
					return
				}
			}
		} else {
			alternateCalls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%q,"object":"response.compact","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":3,"output_tokens":2}}`, "compact_"+id)
	}))
	t.Cleanup(upstream.Close)
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	t.Cleanup(func() { adapter.Close() })
	proxy := application.NewProxy(store, nil, nil, application.ProxyConfig{MaxStreams: 4, MaxQueued: 4, QueueTimeout: 100 * time.Millisecond})
	operations := application.NewCodexOperations(store, store, adapter, time.Hour)
	operations.ConfigureAdmission(proxy)
	operations.ConfigureAccountSelection(proxy)
	mux := http.NewServeMux()
	httpapi.RegisterCodexOperationRoutes(mux, store, operations)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Cleanup(func() { once.Do(func() { close(unblock) }) })
	post := func(body string) (int, string, error) {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses/compact", strings.NewReader(body))
		if err != nil {
			return 0, "", err
		}
		request.Header.Set("Authorization", "Bearer synthetic-key")
		request.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(request)
		if err != nil {
			return 0, "", err
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		return response.StatusCode, string(payload), err
	}
	const body = `{"model":"gpt-6-sol","input":"compact","prompt_cache_key":"compact-locality"}`
	finished := make(chan error, 1)
	go func() {
		status, payload, err := post(body)
		if err == nil && status != 200 {
			err = fmt.Errorf("held compact: status=%d body=%s", status, payload)
		}
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first compact was not dispatched")
	}
	bindings, err := store.ListAffinities(ctx, domain.AffinityFilter{Limit: 10}, time.Now(), time.Hour)
	if err != nil || bindings.Total != 1 || bindings.Entries[0].AccountID != "wire-account" {
		t.Fatalf("compact did not persist locality before dispatch: %+v %v", bindings, err)
	}
	if status, payload, err := post(body); err != nil || status != 200 || alternateCalls.Load() != 1 {
		t.Fatalf("new compact waited on busy account instead of free alternative: %d %s %v", status, payload, err)
	}
	if status, payload, err := post(`{"model":"gpt-6-sol","previous_response_id":"pinned","input":"tail"}`); err != nil || status != 429 || !strings.Contains(payload, "account_response_create_cap") || oldCalls.Load() != 1 || alternateCalls.Load() != 1 {
		t.Fatalf("hard owner moved or bypassed capacity: %d %s %v", status, payload, err)
	}
	once.Do(func() { close(unblock) })
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	bindings, err = store.ListAffinities(ctx, domain.AffinityFilter{Limit: 10}, time.Now(), time.Hour)
	if err != nil || bindings.Total != 1 || bindings.Entries[0].AccountID != "alternative" {
		t.Fatalf("late compact completion overwrote the capacity rebind: %+v %v", bindings, err)
	}
	if status, payload, err := post(body); err != nil || status != 200 || oldCalls.Load() != 2 {
		t.Fatalf("compact lease not released: %d %s %v", status, payload, err)
	}
}
