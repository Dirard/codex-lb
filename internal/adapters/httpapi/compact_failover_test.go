package httpapi_test

import (
	"context"
	"crypto/sha256"
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
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type compactSettlementFailure struct {
	application.CodexOperationStore
}

func (s compactSettlementFailure) SettleUsage(context.Context, string, domain.UsageSettlement) (bool, error) {
	return false, errors.New("synthetic settlement failure")
}

type compactOutcomeFailure struct {
	application.CodexOperationStore
}

func (s compactOutcomeFailure) RecordAccountOutcome(context.Context, string, string, bool, bool) error {
	return errors.New("synthetic outcome persistence failure")
}

func TestCompactQuotaFailoverOnAuthenticatedRoutes(t *testing.T) {
	for _, test := range []struct {
		name, path, input string
		seed, forceFlag   bool
	}{
		{"new backend", "/backend-api/codex/responses/compact", `[{"role":"user","content":"new compact"}]`, false, false},
		{"full resend v1", "/v1/responses/compact/", `[{"role":"user","content":"original"},{"type":"custom_tool_call","call_id":"tool-1","name":"apply_patch","input":"patch"},{"type":"custom_tool_call_output","call_id":"tool-1","output":"done"}]`, true, false},
		{"delta backend", "/backend-api/codex/responses/compact/", `[{"type":"custom_tool_call_output","call_id":"tool-1","output":"done"}]`, true, false},
		{"trigger", "/backend-api/codex/responses", `[{"type":"custom_tool_call_output","call_id":"tool-1","output":"done"},{"type":"compaction_trigger"}]`, true, false},
		{"confirmed owner no reprobe", "/v1/responses/compact", `[{"type":"custom_tool_call_output","call_id":"tool-1","output":"done"}]`, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			var oldCalls, newCalls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/codex/responses" || r.Header.Get("Accept") != "text/event-stream" {
					t.Error("compact used the retired private endpoint")
				}
				if r.Header.Get("ChatGPT-Account-ID") == "wire-account" {
					oldCalls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(429)
					fmt.Fprint(w, `{"error":{"code":"usage_limit_reached","message":"old quota"}}`)
					return
				}
				newCalls.Add(1)
				var body map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&body) != nil || r.Header.Get("ChatGPT-Account-ID") != "z-alternative" ||
					body["previous_response_id"] != nil || body["turn_state"] != nil || r.Header.Get("X-Codex-Turn-State") != "" || r.Header.Get("Session_id") != "" {
					t.Error("compact replay leaked old owner anchors")
				}
				if test.seed {
					input := string(body["input"])
					if strings.Count(input, "original") != 1 || !strings.Contains(input, "custom_tool_call") || !strings.Contains(input, "done") || strings.Contains(input, "old-provider-item") {
						t.Errorf("compact replay lost or duplicated history: %s", input)
					}
					triggerCount := strings.Count(input, "compaction_trigger")
					if triggerCount != 1 {
						t.Error("compact replay changed the terminal trigger")
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: "+`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"type":"output_text","text":"summary"}]}}`+"\n\ndata: "+`{"type":"response.completed","response":{"id":"compact_moved","output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`+"\n\n")
			}))
			defer upstream.Close()
			responses, store, proxy := wireFixtureWithProxy(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, _ func(application.ResponseEvent) error) (application.ResponseResult, error) {
				return application.ResponseResult{ResponseID: "old-response", Response: []byte(`{"id":"old-response","output":[{"type":"custom_tool_call","id":"old-provider-item","call_id":"tool-1","name":"apply_patch","input":"patch"}]}`), Usage: domain.UsageAmount{InputTokens: 1, OutputTokens: 1}, UsageKnown: true}, nil
			}))
			account, err := store.GetAccount(ctx, "wire-account")
			if err != nil {
				t.Fatal(err)
			}
			account.ChatGPTAccountID = account.ID
			account.RoutingPolicy = "burn_first"
			if err := store.SaveAccount(ctx, account); err != nil {
				t.Fatal(err)
			}
			if test.seed {
				if _, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "wire-key", SessionID: "logical-session"}, []byte(`{"model":"gpt-6-sol","input":[{"role":"user","content":"original"}]}`), nil); err != nil {
					t.Fatal(err)
				}
			}
			account.ID, account.ChatGPTAccountID = "z-alternative", "z-alternative"
			account.RoutingPolicy = "normal"
			if err := store.SaveAccount(ctx, account); err != nil {
				t.Fatal(err)
			}
			credential, _ := store.GetAccountCredential(ctx, "wire-account")
			credential.AccountID = account.ID
			if err := store.SaveAccountCredential(ctx, credential); err != nil {
				t.Fatal(err)
			}
			if test.forceFlag {
				reservation := domain.ReservationRequest{ID: "confirmed-refusal", APIKeyID: "wire-key",
					AccountID: "wire-account", Continuation: true, Model: "gpt-6-sol",
					Budget: domain.UsageAmount{InputTokens: 1}, Now: time.Now()}
				if _, err := store.ReserveUsage(ctx, reservation); err != nil {
					t.Fatal(err)
				}
				if _, err := store.SettleUsage(ctx, reservation.ID, domain.UsageSettlement{Status: "failed",
					Event: domain.UsageEvent{RequestID: reservation.ID, AccountID: reservation.AccountID,
						Model: reservation.Model, Status: "error", ErrorCode: "usage_limit_reached",
						RequestedAt: reservation.Now}}); err != nil {
					t.Fatal(err)
				}
				if err := store.MarkContinuationQuotaRefused(ctx, "wire-key", "old-response", "wire-account", reservation.ID); err != nil {
					t.Fatal(err)
				}
			}
			adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
			defer adapter.Close()
			operations := application.NewCodexOperations(store, store, adapter, time.Hour)
			operations.ConfigureAdmission(proxy)
			operations.ConfigureAccountSelection(proxy)
			proxy.ConfigureCompaction(operations)
			mux := http.NewServeMux()
			httpapi.RegisterCodexOperationRoutes(mux, store, operations)
			compact := httptest.NewServer(mux)
			defer compact.Close()
			anchor := ""
			if test.seed {
				anchor = `,"previous_response_id":"old-response"`
			}
			base := compact.URL
			if test.path == "/backend-api/codex/responses" {
				base = responses.URL
			}
			headers := http.Header{}
			if test.seed {
				headers.Set("Session_id", "logical-session")
				headers.Set("X-Codex-Turn-State", "old-turn-state")
			}
			status, body := liteHTTPPost(t, base+test.path, `{"model":"gpt-6-sol","stream":true,"input":`+test.input+anchor+`}`, headers)
			wantOld := int64(1)
			if test.forceFlag {
				wantOld = 0
			}
			if status != 200 || strings.Contains(body, "old quota") || oldCalls.Load() != wantOld || newCalls.Load() != 1 {
				t.Fatalf("compact quota failover failed: status=%d old=%d new=%d body=%s", status, oldCalls.Load(), newCalls.Load(), body)
			}
			owner, err := store.GetContinuation(ctx, "wire-key", "compact_moved", time.Now())
			if err != nil || owner.AccountID != account.ID {
				t.Fatalf("compact owner not moved: %+v %v", owner, err)
			}
			if test.seed {
				turnID := fmt.Sprintf("session:turn:%x", sha256.Sum256([]byte("old-turn-state")))
				turn, err := store.GetContinuation(ctx, "wire-key", turnID, time.Now())
				if err != nil || turn.AccountID != account.ID || turn.TurnStateForwardable {
					t.Fatalf("compact replay retained a foreign upstream turn token: %+v %v", turn, err)
				}
				sessionID := fmt.Sprintf("session:%x", sha256.Sum256([]byte("logical-session")))
				session, err := store.GetContinuation(ctx, "wire-key", sessionID, time.Now())
				if err != nil || session.AccountID != account.ID {
					t.Fatalf("logical compact session did not move: %+v %v", session, err)
				}
				old, err := store.GetContinuation(ctx, "wire-key", "old-response", time.Now())
				if err != nil || !old.QuotaRefused {
					t.Fatal("quota refusal did not mark the original continuation")
				}
			}
			totals, err := store.UsageTotals(ctx, "wire-key", "")
			seedCount, seededUsage := int64(0), int64(0)
			if test.seed {
				seedCount, seededUsage = 1, 1
			}
			if test.forceFlag {
				seedCount++
			}
			if err != nil || totals.RequestCount != seedCount+wantOld+1 || totals.Usage.InputTokens != 3+seededUsage || totals.Usage.OutputTokens != 2+seededUsage {
				t.Fatalf("compact attempt accounting: %+v %v", totals, err)
			}
		})
	}
}

func TestCompactDoesNotRetryNonQuotaUnsafeHistoryOrAccountingFailure(t *testing.T) {
	for _, test := range []struct {
		name, code, input string
		failSettlement    bool
		owner, pause      bool
		wantCalls         int64
	}{
		{"generic429", "temporarily_busy", `[{"role":"user","content":"new"}]`, false, false, false, 1},
		{"charged quota", "usage_limit_reached", `[{"role":"user","content":"new"}]`, false, false, false, 1},
		{"charged owner quota", "usage_limit_reached", `[{"role":"user","content":"new"}]`, false, true, false, 1},
		{"partial billing quota", "usage_limit_reached", `[{"role":"user","content":"new"}]`, false, false, false, 1},
		{"output then quota", "usage_limit_reached", `[{"role":"user","content":"new"}]`, false, false, false, 1},
		{"output then zero quota", "usage_limit_reached", `[{"role":"user","content":"new"}]`, false, false, false, 1},
		{"invalid billing owner quota", "usage_limit_reached", `[{"role":"user","content":"new"}]`, false, true, false, 1},
		{"encrypted context", "usage_limit_reached", `[{"type":"compaction","encrypted_content":"opaque"},{"role":"user","content":"new"}]`, false, false, false, 1},
		{"settlement", "usage_limit_reached", `[{"role":"user","content":"new"}]`, true, false, false, 1},
		{"outcome persistence", "usage_limit_reached", `[{"role":"user","content":"new"}]`, false, false, false, 1},
		{"missing prior context", "usage_limit_reached", `[{"role":"user","content":"only new tail"}]`, false, true, false, 1},
		{"operator paused owner", "usage_limit_reached", `[{"role":"user","content":"original"},{"role":"assistant","content":"answer"},{"role":"user","content":"new"}]`, false, true, true, 1},
		{"transport", "connection_failure", `[{"role":"user","content":"new"}]`, false, false, false, 1},
		{"all candidates exhausted", "usage_limit_reached", `[{"role":"user","content":"new"}]`, false, false, false, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			charged := strings.HasPrefix(test.name, "charged")
			uncertain := strings.Contains(test.name, "billing")
			var calls atomic.Int64
			var beforeRefusal func()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if beforeRefusal != nil {
					beforeRefusal()
				}
				if strings.HasPrefix(test.name, "output then") {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: "+`{"type":"response.output_text.delta","delta":"partial"}`+"\n\n")
					billing := ""
					if test.name == "output then zero quota" {
						billing = `,"usage":{"input_tokens":0,"output_tokens":0}`
					}
					fmt.Fprintf(w, "data: {\"type\":\"error\",\"status\":429,\"error\":{\"code\":%q}%s}\n\n", test.code, billing)
					return
				}
				if test.code == "connection_failure" {
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					connection.Close()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(429)
				if uncertain {
					usage := `{"input_tokens":3}`
					if test.owner {
						usage = `{"input_tokens":-3,"output_tokens":2}`
					}
					fmt.Fprintf(w, `{"error":{"code":%q},"usage":%s}`, test.code, usage)
					return
				}
				if charged {
					fmt.Fprintf(w, `{"error":{"code":%q},"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`, test.code)
					return
				}
				fmt.Fprintf(w, `{"error":{"code":%q}}`, test.code)
			}))
			defer upstream.Close()
			_, store, proxy := wireFixtureWithProxy(t, nil)
			ctx := context.Background()
			account, _ := store.GetAccount(ctx, "wire-account")
			account.RoutingPolicy = "burn_first"
			if err := store.SaveAccount(ctx, account); err != nil {
				t.Fatal(err)
			}
			if test.owner {
				if err := store.SaveContinuation(ctx, domain.Continuation{ResponseID: "owned-compact", KeyID: "wire-key", AccountID: account.ID, ProviderID: account.Provider, Model: "gpt-6-sol", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, domain.ContinuationBounds{MaxRecords: 10, MaxContextBytes: 10000}); err != nil {
					t.Fatal(err)
				}
			}
			if test.pause {
				beforeRefusal = func() {
					initial, err := store.GetAccount(ctx, "wire-account")
					if err != nil {
						t.Error(err)
						return
					}
					initial.Status = domain.AccountPaused
					if err := store.SaveAccount(ctx, initial); err != nil {
						t.Error(err)
					}
				}
			}
			account.ID = "z-alternative"
			account.RoutingPolicy = "normal"
			if err := store.SaveAccount(ctx, account); err != nil {
				t.Fatal(err)
			}
			credential, _ := store.GetAccountCredential(ctx, "wire-account")
			credential.AccountID = account.ID
			if err := store.SaveAccountCredential(ctx, credential); err != nil {
				t.Fatal(err)
			}
			adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
			defer adapter.Close()
			var accounting application.CodexOperationStore = store
			if test.failSettlement {
				accounting = compactSettlementFailure{store}
			} else if test.name == "outcome persistence" {
				accounting = compactOutcomeFailure{store}
			}
			operations := application.NewCodexOperations(accounting, store, adapter, time.Hour)
			operations.ConfigureAdmission(proxy)
			operations.ConfigureAccountSelection(proxy)
			mux := http.NewServeMux()
			httpapi.RegisterCodexOperationRoutes(mux, store, operations)
			server := httptest.NewServer(mux)
			defer server.Close()
			anchor := ""
			if test.owner {
				anchor = `,"previous_response_id":"owned-compact"`
			}
			status, body := liteHTTPPost(t, server.URL+"/v1/responses/compact", `{"model":"gpt-6-sol","input":`+test.input+anchor+`}`)
			if status < 400 || calls.Load() != test.wantCalls || test.failSettlement && !strings.Contains(body, "usage_settlement_failed") {
				t.Fatalf("unsafe retry occurred: %d calls=%d body=%s", status, calls.Load(), body)
			}
			initial, _ := store.GetAccount(ctx, "wire-account")
			if strings.HasPrefix(test.name, "output then") {
				pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
				wantPending := 1
				if test.name == "output then zero quota" {
					wantPending = 0
				}
				if err != nil || len(pending) != wantPending {
					t.Fatalf("partial compact accounting lost: pending=%d want=%d err=%v", len(pending), wantPending, err)
				}
			}
			if uncertain {
				totals, err := store.UsageTotals(ctx, "wire-key", "")
				if err != nil || totals.RequestCount != 0 || status != 502 || initial.Status != domain.AccountQuotaExceeded {
					t.Fatalf("unknown bill became settled/free or lost quota proof: status=%d account=%s totals=%+v err=%v", status, initial.Status, totals, err)
				}
				pending, err := store.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 10, Statuses: []string{"reconciliation_required"}})
				if err != nil || pending.Total != 1 || len(pending.Requests) != 1 || pending.Requests[0].CostUSD != nil || pending.Requests[0].Tokens != nil {
					t.Fatalf("unknown compact bill not visible as pending: %+v %v", pending, err)
				}
			}
			if charged {
				totals, err := store.UsageTotals(ctx, "wire-key", "")
				if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 3 || totals.Usage.OutputTokens != 2 || status != 429 || initial.Status != domain.AccountQuotaExceeded {
					t.Fatalf("charged refusal was lost or replayed: status=%d account=%s totals=%+v err=%v", status, initial.Status, totals, err)
				}
			}
			if (charged || uncertain) && test.owner {
				owner, err := store.GetContinuation(ctx, "wire-key", "owned-compact", time.Now())
				if err != nil || !owner.QuotaRefused || owner.AccountID != initial.ID {
					t.Fatalf("billed refusal lost owner proof: %+v %v", owner, err)
				}
			}
			if (test.failSettlement || test.code == "temporarily_busy" || test.code == "connection_failure") && initial.Status != domain.AccountActive {
				t.Fatal("local failure or generic 429 penalized account health")
			}
			if test.pause && initial.Status != domain.AccountPaused {
				t.Fatal("quota refusal overwrote operator pause")
			}
		})
	}
}
