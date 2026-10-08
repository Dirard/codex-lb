package httpapi_test

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

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestNamedThreadsShareCacheWithoutSharingAccountPin(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		for _, strategy := range []string{"round_robin", "capacity_weighted"} {
			t.Run(transport+"/"+strategy, func(t *testing.T) {
				ctx := context.Background()
				var calls atomic.Int64
				targets := make(chan application.ResponseTarget, 1)
				server, store := wireFixture(t, wireProvider(func(_ context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
					var request struct {
						Cache string `json:"prompt_cache_key"`
					}
					if json.Unmarshal(body, &request) != nil || request.Cache != "shared-cache" && request.Cache != "changed-cache" {
						t.Error("provider-facing cache hint was changed")
					}
					targets <- target
					return wireComplete(fmt.Sprintf("named_%d", calls.Add(1)), emit)
				}))
				settings, err := store.LoadSettings(ctx)
				if err != nil {
					t.Fatal(err)
				}
				settings.RoutingStrategy, settings.StickyThreadsEnabled = strategy, false
				settings.PreferEarlierResetAccounts = false
				// Keep the old cache pin healthy even after quota updates below:
				// this test must not pass because soft pressure moved a shared pin.
				settings.StickyReallocationPrimaryBudgetThresholdPct = 100
				settings.StickyReallocationSecondaryBudgetThresholdPct = 100
				if err := store.SaveSettings(ctx, settings); err != nil {
					t.Fatal(err)
				}
				post := func(thread, cache, previous string) (application.ResponseTarget, string) {
					t.Helper()
					payload := map[string]any{"model": "gpt-6-sol", "input": "full history", "prompt_cache_key": cache}
					headers := http.Header{"Authorization": {"Bearer synthetic-key"}, "Session_id": {"shared-process"}}
					if thread != "" {
						headers.Set("Thread-Id", thread)
						headers.Set("X-Openai-Subagent", "worker")
						headers.Set("X-Codex-Parent-Thread-Id", "parent")
					}
					if previous != "" {
						payload["previous_response_id"] = previous
					}
					if transport == "websocket" {
						payload["type"] = "response.create"
						encoded, _ := json.Marshal(payload)
						connection := capabilitySocket(t, server.URL, headers)
						body := capabilityFrame(t, connection, string(encoded))
						connection.CloseNow()
						if !strings.Contains(body, "response.completed") {
							t.Fatalf("branch failed: %s", body)
						}
					} else {
						encoded, _ := json.Marshal(payload)
						if status, body := liteHTTPPost(t, server.URL+"/backend-api/codex/responses", string(encoded), headers); status != 200 {
							t.Fatalf("branch failed: %d %s", status, body)
						}
					}
					return <-targets, fmt.Sprintf("named_%d", calls.Load())
				}
				// Seed the pre-upgrade key-wide pin with a client without Thread-Id.
				post("", "shared-cache", "")
				for i := 1; i < 6; i++ {
					addAffinityAccount(t, store, fmt.Sprintf("alternative-%d", i))
				}
				owners, responses := make([]string, 6), make([]string, 6)
				seen := map[string]bool{}
				for i := range 6 {
					target, response := post(fmt.Sprintf("child-%d", i), "shared-cache", "")
					if seen[target.Account.ID] {
						t.Fatalf("independent child %d inherited sibling/legacy pin %s", i, target.Account.ID)
					}
					seen[target.Account.ID] = true
					owners[i], responses[i] = target.Account.ID, response
					if strategy == "capacity_weighted" {
						// Zero subscription weight but still eligible via purchased credits.
						// The next weighted draw deterministically chooses a nonzero peer;
						// the old healthy key-wide pin would incorrectly force this account.
						now, reset, credit := time.Now(), time.Now().Add(time.Hour), 100.0
						if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{
							AccountID: target.Account.ID, ObservedAt: now,
							Quotas:  []domain.AccountQuota{{AccountID: target.Account.ID, Window: "secondary", UsedPercent: 100, ResetAt: &reset, ObservedAt: now}},
							Credits: &domain.AccountCreditStatus{AccountID: target.Account.ID, Balance: &credit, ObservedAt: now},
						}); err != nil {
							t.Fatal(err)
						}
					}
				}
				// Remove credit eligibility; hard owners still work at telemetry zero
				// without a provider quota refusal, even after changing the cache key.
				for i, account := range owners {
					now, reset, credit := time.Now(), time.Now().Add(time.Hour), 0.0
					if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{
						AccountID: account, ObservedAt: now,
						Quotas:  []domain.AccountQuota{{AccountID: account, Window: "secondary", UsedPercent: 100, ResetAt: &reset, ObservedAt: now}},
						Credits: &domain.AccountCreditStatus{AccountID: account, Balance: &credit, ObservedAt: now},
					}); err != nil {
						t.Fatal(err)
					}
					for _, previous := range []string{"", responses[i]} {
						if target, _ := post(fmt.Sprintf("child-%d", i), "changed-cache", previous); target.Account.ID != account {
							t.Fatal("cache change or quota telemetry moved an established thread")
						}
					}
				}
				if calls.Load() != 19 {
					t.Fatalf("unexpected retry: %d requests", calls.Load())
				}
				key, err := store.GetAPIKey(ctx, "wire-key")
				if err != nil || key.Limits[0].CurrentValue != 19*20 {
					t.Fatalf("accounting mismatch: %+v %v", key.Limits, err)
				}
				pins, err := store.ListAffinities(ctx, domain.AffinityFilter{Limit: 20}, time.Now(), time.Hour)
				if err != nil || pins.Total != 7 {
					t.Fatalf("named and anonymous cache bindings were not separate: total=%d %v", pins.Total, err)
				}
			})
		}
	}
}

func TestCompactCacheAffinitySeparatesNamedThreads(t *testing.T) {
	ctx := context.Background()
	selected := make(chan string, 1)
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selected <- r.Header.Get("ChatGPT-Account-ID")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"named_compact_%d","object":"response.compact","output":[{"type":"compaction","encrypted_content":"synthetic"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`, calls.Add(1))
	}))
	defer upstream.Close()
	_, store, proxy := wireFixtureWithProxy(t, nil)
	addAffinityAccount(t, store, "wire-account")
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.RoutingStrategy, settings.StickyThreadsEnabled = "round_robin", false
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
	defer adapter.Close()
	operations := application.NewCodexOperations(store, store, adapter, time.Hour)
	operations.ConfigureAdmission(proxy)
	operations.ConfigureAccountSelection(proxy)
	mux := http.NewServeMux()
	httpapi.RegisterCodexOperationRoutes(mux, store, operations)
	server := httptest.NewServer(mux)
	defer server.Close()
	for i, want := range []string{"wire-account", "alternative", "wire-account"} {
		if i == 1 {
			addAffinityAccount(t, store, "alternative")
		}
		headers := http.Header{"Session_id": {"shared-process"}, "Thread-Id": {fmt.Sprintf("compact-child-%d", i)}}
		status, body := liteHTTPPost(t, server.URL+"/v1/responses/compact/", `{"model":"gpt-6-sol","input":"history","prompt_cache_key":"shared-cache"}`, headers)
		if status != 200 {
			t.Fatalf("compact failed: %d %s", status, body)
		}
		if got := <-selected; got != want {
			t.Fatalf("compact inherited another thread's cache pin: %s want %s", got, want)
		}
	}
}
