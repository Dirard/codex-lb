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

	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestQuotaRefusalAfterChargeOrOutputStillMarksOwnerWithoutReplay(t *testing.T) {
	for _, test := range []struct {
		name           string
		known, visible bool
		usage          string
		status         int
	}{
		{"charged", true, false, `{"input_tokens":2,"output_tokens":1}`, 200},
		{"visible_unknown", false, true, "", 200},
		{"partial_usage", false, false, `{"input_tokens":2}`, 502},
		{"invalid_usage", false, false, `{"input_tokens":-1,"output_tokens":0}`, 502},
		{"malformed_usage", false, false, `"invalid"`, 502},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if calls.Add(1) == 1 {
					fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"quota_owner","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ready"}]}],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`+"\n\n")
					return
				}
				if test.visible {
					fmt.Fprint(w, "data: "+`{"type":"response.output_text.delta","delta":"visible"}`+"\n\n")
				}
				usage := ""
				if test.usage != "" {
					usage = `,"usage":` + test.usage
				}
				fmt.Fprintf(w, "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"quota_failed\",\"status\":\"failed\",\"error\":{\"code\":\"usage_limit_reached\"}%s}}\n\n", usage)
			}))
			defer upstream.Close()
			var adapter *provider.Adapter
			server, store, _ := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				return adapter.Respond(ctx, target, body, emit)
			}))
			adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
			defer adapter.Close()
			ctx := context.Background()
			settings, _ := store.LoadSettings(ctx)
			settings.HTTPTransportPolicy = "always_http"
			if err := store.SaveSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			if status, body := liteHTTPPost(t, server.URL+"/v1/responses", `{"model":"gpt-6-sol","input":"initial"}`); status != 200 {
				t.Fatalf("initial: %d %s", status, body)
			}
			other, _ := store.GetAccount(ctx, "wire-account")
			other.ID, other.Email = "other-account", "other@example.invalid"
			if err := store.SaveAccount(ctx, other); err != nil {
				t.Fatal(err)
			}
			credential, _ := store.GetAccountCredential(ctx, "wire-account")
			credential.AccountID = other.ID
			if err := store.SaveAccountCredential(ctx, credential); err != nil {
				t.Fatal(err)
			}
			status, body := liteHTTPPost(t, server.URL+"/v1/responses", fmt.Sprintf(`{"model":"gpt-6-sol","previous_response_id":"quota_owner","input":"continue","stream":%t}`, test.visible))
			if status != test.status || calls.Load() != 2 || status == 200 && !strings.Contains(body, "response.failed") && !strings.Contains(body, `"status":"failed"`) {
				t.Fatalf("unsafe quota retry or error delivery: %d %s calls=%d", status, body, calls.Load())
			}
			account, err := store.GetAccount(ctx, "wire-account")
			owner, ownerErr := store.GetContinuation(ctx, "wire-key", "quota_owner", time.Now())
			if err != nil || ownerErr != nil || account.Status != domain.AccountQuotaExceeded || !owner.QuotaRefused {
				t.Fatalf("quota proof lost: %s %+v %v %v", account.Status, owner, err, ownerErr)
			}
			pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			totals, totalErr := store.UsageTotals(ctx, "wire-key", "")
			if err != nil || totalErr != nil {
				t.Fatalf("accounting read: %v %v", err, totalErr)
			}
			if test.known && (len(pending) != 0 || totals.Usage.InputTokens+totals.Usage.OutputTokens != 3) || !test.known && (len(pending) != 1 || totals.RequestCount != 1) {
				t.Fatalf("wrong quota billing decision: %+v pending=%d", totals, len(pending))
			}
		})
	}
}

func TestOwnedResponseCanSkipTwoQuotaRefusedAccounts(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		initial := calls.Add(1) == 1
		account := r.Header.Get("ChatGPT-Account-ID")
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
		}
		if initial && account != "a" {
			t.Error("unexpected initial owner")
		}
		if account != "a" && body["previous_response_id"] != nil {
			t.Error("owner anchor crossed accounts")
		}
		if !initial && account != "c" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"code":"usage_limit_reached"}}`)
			return
		}
		id := "chain_final"
		if initial {
			id = "chain_owner"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ready\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", id)
	}))
	defer upstream.Close()
	var adapter *provider.Adapter
	server, store, _ := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return adapter.Respond(ctx, target, body, emit)
	}))
	adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
	defer adapter.Close()
	ctx := context.Background()
	account, _ := store.GetAccount(ctx, "wire-account")
	account.ChatGPTAccountID = "a"
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	settings, _ := store.LoadSettings(ctx)
	settings.HTTPTransportPolicy, settings.RoutingStrategy, settings.PreferEarlierResetAccounts = "always_http", "round_robin", false
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if status, body := liteHTTPPost(t, server.URL+"/v1/responses", `{"model":"gpt-6-sol","input":"initial"}`); status != 200 {
		t.Fatalf("initial: %d %s", status, body)
	}
	credential, _ := store.GetAccountCredential(ctx, account.ID)
	for _, id := range []string{"b", "c"} {
		account.ID, account.ChatGPTAccountID, account.Email = id+"-account", id, id+"@example.invalid"
		credential.AccountID = account.ID
		if err := store.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveAccountCredential(ctx, credential); err != nil {
			t.Fatal(err)
		}
	}
	status, body := liteHTTPPost(t, server.URL+"/v1/responses", `{"model":"gpt-6-sol","previous_response_id":"chain_owner","input":"continue"}`)
	if status != 200 || !strings.Contains(body, "chain_final") || calls.Load() != 4 {
		t.Fatalf("chained quota failover: %d %s calls=%d", status, body, calls.Load())
	}
	owner, err := store.GetContinuation(ctx, "wire-key", "chain_final", time.Now())
	if err != nil || owner.AccountID != "c-account" {
		t.Fatalf("final owner: %+v %v", owner, err)
	}
	for _, id := range []string{"wire-account", "b-account"} {
		account, err := store.GetAccount(ctx, id)
		if err != nil || account.Status != domain.AccountQuotaExceeded {
			t.Fatalf("refusal not saved for %s: %+v %v", id, account, err)
		}
	}
}
