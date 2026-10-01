package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func addAffinityAccount(t *testing.T, store *sqlite.Store, id string) {
	t.Helper()
	ctx := context.Background()
	account, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	account.ID, account.ChatGPTAccountID = id, id
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	credential, err := store.GetAccountCredential(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	credential.AccountID = id
	if err := store.SaveAccountCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
}

func TestAffinityRoutesNewHTTPAndWebSocketRequestsButNeverMovesHardOwner(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			ctx := context.Background()
			var calls atomic.Int64
			targets := make(chan application.ResponseTarget, 8)
			server, store := wireFixture(t, wireProvider(func(_ context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				if !strings.Contains(string(body), `"prompt_cache_key":"shared-cache"`) {
					t.Error("provider-facing cache key was rewritten")
				}
				targets <- target
				return wireComplete(fmt.Sprintf("affinity_%d", calls.Add(1)), emit)
			}))
			settings, err := store.LoadSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			settings.RoutingStrategy, settings.StickyThreadsEnabled = "round_robin", false
			if err := store.SaveSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			post := func(previous string) application.ResponseTarget {
				t.Helper()
				body := `{"model":"gpt-6-sol","input":"hello","prompt_cache_key":"shared-cache"`
				if previous != "" {
					body += `,"previous_response_id":` + fmt.Sprintf("%q", previous)
				}
				if transport == "websocket" {
					conn := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}})
					payload := capabilityFrame(t, conn, body+`,"type":"response.create"}`)
					conn.CloseNow()
					if !strings.Contains(payload, "response.completed") {
						t.Fatalf("WS locality: %s", payload)
					}
				} else if status, payload := liteHTTPPost(t, server.URL+"/v1/responses", body+`}`); status != 200 {
					t.Fatalf("HTTP locality: %d %s", status, payload)
				}
				return <-targets
			}
			first := post("")
			addAffinityAccount(t, store, "z-alternative")
			if repeated := post(""); repeated.Account.ID != first.Account.ID {
				t.Fatal("explicit cache locality was disabled with automatic sticky threads")
			}
			reset := time.Now().Add(time.Hour)
			if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: first.Account.ID, Window: "primary", UsedPercent: 96, ResetAt: &reset, ObservedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if moved := post(""); moved.Account.ID != "z-alternative" {
				t.Fatalf("soft pin did not reallocate under pressure: %s", moved.Account.ID)
			}
			list, err := store.ListAffinities(ctx, domain.AffinityFilter{Limit: 10}, time.Now(), time.Hour)
			if err != nil || list.Total != 1 || list.Entries[0].AccountID != "z-alternative" || strings.Contains(list.Entries[0].Key, "shared-cache") {
				t.Fatalf("locality storage shape/privacy: %+v %v", list, err)
			}
			binding, err := store.LookupAffinity(ctx, "wire-key", list.Entries[0].Kind, list.Entries[0].Key)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: first.Account.ID, Window: "primary", UsedPercent: 100, ResetAt: &reset, ObservedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if owned := post("affinity_1"); owned.Account.ID != first.Account.ID {
				t.Fatal("soft rebind stole a hard owner at zero quota")
			}
			after, err := store.LookupAffinity(ctx, "wire-key", binding.Kind, binding.Key)
			if err != nil || after.Version != binding.Version || after.AccountID != binding.AccountID {
				t.Fatalf("hard continuation modified the soft binding: %+v %v", after, err)
			}
		})
	}
}

func TestAutomaticClientAffinityIsOptionalAndCacheValidationPrecedesDispatch(t *testing.T) {
	var calls atomic.Int64
	targets := make(chan application.ResponseTarget, 8)
	server, store := wireFixture(t, wireProvider(func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		targets <- target
		return wireComplete(fmt.Sprintf("client_affinity_%d", calls.Add(1)), emit)
	}))
	ctx := context.Background()
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.RoutingStrategy, settings.StickyThreadsEnabled = "round_robin", true
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	for i, header := range []string{"X-Parent-Session-Id", "X-Opencode-Session", "X-Session-Id", "X-Session-Affinity"} {
		if i == 1 {
			addAffinityAccount(t, store, "z-alternative")
		}
		status, payload := liteHTTPPost(t, server.URL+"/v1/responses", `{"model":"gpt-6-sol","input":"hello"}`, http.Header{header: {"client-session"}})
		if status != 200 {
			t.Fatalf("client alias %s: %d %s", header, status, payload)
		}
		if target := <-targets; target.Account.ID != "wire-account" {
			t.Fatalf("client aliases did not share locality: %s", header)
		}
	}
	settings, err = store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.StickyThreadsEnabled = false
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	status, payload := liteHTTPPost(t, server.URL+"/v1/responses", `{"model":"gpt-6-sol","input":"hello"}`, http.Header{"X-Session-Affinity": {"client-session"}})
	if status != 200 {
		t.Fatalf("disabled automatic affinity: %d %s", status, payload)
	}
	if target := <-targets; target.Account.ID != "z-alternative" {
		t.Fatal("disabled automatic hint still overrode round-robin")
	}
	for _, hint := range []string{`42`, `"` + strings.Repeat("x", 4097) + `"`} {
		status, payload = liteHTTPPost(t, server.URL+"/v1/responses", `{"model":"gpt-6-sol","input":"hello","prompt_cache_key":`+hint+`}`)
		if status != 400 || calls.Load() != 5 {
			t.Fatalf("invalid hint dispatched: %d %s", status, payload)
		}
	}
}

func TestAffinityCannotOverrideChangedKeyAccountScope(t *testing.T) {
	var calls atomic.Int64
	targets := make(chan string, 2)
	server, store := wireFixture(t, wireProvider(func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		targets <- target.Account.ID
		return wireComplete(fmt.Sprintf("affinity_scope_%d", calls.Add(1)), emit)
	}))
	const body = `{"model":"gpt-6-sol","input":"hello","prompt_cache_key":"scope"}`
	if status, payload := liteHTTPPost(t, server.URL+"/v1/responses", body); status != 200 {
		t.Fatalf("seed: %d %s", status, payload)
	}
	if <-targets != "wire-account" {
		t.Fatal("unexpected initial owner")
	}
	addAffinityAccount(t, store, "z-alternative")
	key, err := store.GetAPIKey(context.Background(), "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	key.AccountAssignmentScopeEnabled, key.AssignedAccountIDs = true, []string{"z-alternative"}
	if err := store.SaveAPIKey(context.Background(), key, time.Now()); err != nil {
		t.Fatal(err)
	}
	if status, payload := liteHTTPPost(t, server.URL+"/v1/responses", body); status != 200 {
		t.Fatalf("new scope: %d %s", status, payload)
	}
	if <-targets != "z-alternative" {
		t.Fatal("soft mapping overrode current key account scope")
	}
}

type failingAffinityStore struct {
	*sqlite.Store
	reservations chan string
}

func (s failingAffinityStore) SaveAffinity(_ context.Context, _ domain.AffinityBinding, _ int64, reservationID string) (bool, error) {
	s.reservations <- reservationID
	return false, errors.New("synthetic affinity write failure")
}

func TestAffinityWriteFailureSettlesUnusedReservationBeforeHTTPDispatch(t *testing.T) {
	_, baseStore := wireFixture(t, nil)
	store := failingAffinityStore{Store: baseStore, reservations: make(chan string, 3)}
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
	defer adapter.Close()
	proxy := application.NewProxy(store, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, _ func(application.ResponseEvent) error) (application.ResponseResult, error) {
		calls.Add(1)
		return application.ResponseResult{}, errors.New("unexpected provider dispatch")
	}), nil, application.ProxyConfig{MaxStreams: 1, MaxQueued: 1})
	operations := application.NewCodexOperations(store, store, adapter, time.Hour)
	operations.ConfigureAdmission(proxy)
	operations.ConfigureAccountSelection(proxy)
	proxy.ConfigureCompaction(operations)
	mux := http.NewServeMux()
	httpapi.RegisterCodexOperationRoutes(mux, store, operations)
	mux.Handle("/", httpapi.NewProxyHandler(store, proxy, nil))
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, test := range []struct{ path, input string }{
		{"/v1/responses", `"hello"`},
		{"/v1/responses/compact", `"compact"`},
		{"/backend-api/codex/responses", `[{"role":"user","content":"compact"},{"type":"compaction_trigger"}]`},
	} {
		status, body := liteHTTPPost(t, server.URL+test.path, `{"model":"gpt-6-sol","stream":true,"prompt_cache_key":"failed-cache","input":`+test.input+`}`)
		if status != 503 || !strings.Contains(body, "affinity_persistence_failed") || calls.Load() != 0 {
			t.Fatalf("write failure dispatched or committed HTTP 200: %d %s calls=%d", status, body, calls.Load())
		}
		reservation, err := store.GetReservation(context.Background(), <-store.reservations)
		if err != nil || reservation.Status != "failed" || reservation.NeedsReconciliation {
			t.Fatalf("unused reservation retained: %+v %v", reservation, err)
		}
	}
	totals, err := store.UsageTotals(context.Background(), "wire-key", "")
	if err != nil || totals.FailedCount != 3 || totals.Usage != (domain.UsageAmount{}) {
		t.Fatalf("undispatched failures charged usage: %+v %v", totals, err)
	}
}
