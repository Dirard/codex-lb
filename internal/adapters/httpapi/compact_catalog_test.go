package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestCompactRoutesApplyCatalogBeforeDispatchWithoutReplacingOwner(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := calls.Add(1)
		if r.Header.Get("ChatGPT-Account-ID") != "supported" {
			t.Error("compact chose an account without catalog support")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["service_tier"] != nil {
			t.Errorf("effective default tier not propagated: %s %v", body["service_tier"], err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"resp_compact_%d","object":"response.compact","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`, count)
	}))
	defer upstream.Close()
	responses, store, proxy := wireFixtureWithProxy(t, wireProvider(func(context.Context, application.ResponseTarget, json.RawMessage, func(application.ResponseEvent) error) (application.ResponseResult, error) {
		t.Error("trigger used ordinary Responses instead of compact")
		return application.ResponseResult{}, nil
	}))
	account, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	account.ID, account.ChatGPTAccountID = "z-supported", "supported"
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	credential, err := store.GetAccountCredential(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	credential.AccountID = account.ID
	if err := store.SaveAccountCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	tier := "priority"
	key.EnforcedServiceTier = &tier
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveModelCatalogSnapshot(ctx, domain.ModelCatalogRecord{
		SchemaVersion: application.ModelCatalogSchemaVersion, RefreshedAt: time.Now(), ContentHash: "synthetic-catalog",
		Snapshot: &domain.CatalogSnapshot{
			Models:                  map[string]domain.CatalogModel{"gpt-6-sol": {Slug: "gpt-6-sol"}},
			ModelPlans:              map[string][]string{"gpt-6-sol": {"plus"}},
			ModelAccounts:           map[string][]string{"gpt-6-sol": {account.ID}},
			AccountPlans:            map[string]string{"wire-account": "plus", account.ID: "plus"},
			AccountCatalogsComplete: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	catalog := application.NewModelCatalogService(store, nil, nil, application.ModelCatalogConfig{})
	if err := catalog.Load(ctx); err != nil {
		t.Fatal(err)
	}
	proxy.Catalog = catalog
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	defer adapter.Close()
	operations := application.NewCodexOperations(store, store, adapter, time.Hour)
	operations.Catalog = catalog
	operations.ConfigureAdmission(proxy)
	operations.ConfigureAccountSelection(proxy)
	proxy.ConfigureCompaction(operations)
	mux := http.NewServeMux()
	httpapi.RegisterCodexOperationRoutes(mux, store, operations)
	standalone := httptest.NewServer(mux)
	defer standalone.Close()
	if err := store.SaveContinuation(ctx, domain.Continuation{ResponseID: "resp_unsupported_owner", KeyID: key.ID, AccountID: "wire-account", ProviderID: "openai", Model: "gpt-6-sol", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, domain.ContinuationBounds{MaxRecords: 10, MaxContextBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/backend-api/codex/responses/compact", "/v1/responses/compact", "/backend-api/codex/responses"} {
		base, input := standalone.URL, `[ {"role":"user","content":"compact me"} ]`
		if !strings.HasSuffix(path, "/compact") {
			base, input = responses.URL, `[{"role":"user","content":"compact me"},{"type":"compaction_trigger"}]`
		}
		for _, anchored := range []bool{false, true} {
			anchor := ""
			if anchored {
				anchor = `,"previous_response_id":"resp_unsupported_owner"`
			}
			before := calls.Load()
			r, _ := http.NewRequest("POST", base+path, strings.NewReader(`{"model":"gpt-6-sol","input":`+input+`,"stream":true`+anchor+`}`))
			r.Header.Set("Authorization", "Bearer synthetic-key")
			r.Header.Set("Content-Type", "application/json")
			res, err := standalone.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if anchored {
				if res.StatusCode < 400 || calls.Load() != before {
					t.Fatalf("unsupported owner replaced: %s %d %s", path, res.StatusCode, body)
				}
			} else if res.StatusCode != 200 || calls.Load() != before+1 {
				t.Fatalf("supported alternative not selected: %s %d %s", path, res.StatusCode, body)
			}
		}
	}
	totals, err := store.UsageTotals(ctx, key.ID, "")
	if err != nil || totals.RequestCount != 3 || totals.Usage.InputTokens != 6 || totals.Usage.OutputTokens != 3 {
		t.Fatalf("rejected owner was billed: %+v %v", totals, err)
	}
}
