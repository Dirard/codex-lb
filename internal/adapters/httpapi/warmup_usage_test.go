package httpapi

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

func TestManualProbePreservesWarmupAccounting(t *testing.T) {
	for _, test := range []struct {
		name, response                  string
		upstreamStatus, status, pending int
		tokens                          int64
	}{
		{"zero", `{"type":"response.completed","response":{"id":"probe","status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0}}}`, 200, 200, 0, 0},
		{"missing_content_type", `{"type":"response.completed","response":{"id":"probe","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1}}}`, 200, 200, 0, 3},
		{"missing", `{"type":"response.completed","response":{"id":"probe","status":"completed","output":[]}}`, 200, 502, 1, 0},
		{"partial", `{"type":"response.completed","response":{"id":"probe","status":"completed","output":[],"usage":{"input_tokens":2}}}`, 200, 502, 1, 0},
		{"charged_quota", `{"type":"response.failed","response":{"id":"probe","status":"failed","error":{"code":"usage_limit_reached"},"usage":{"input_tokens":2,"output_tokens":1}}}`, 200, 200, 0, 3},
		{"partial_quota", `{"error":{"code":"usage_limit_reached"},"usage":{"input_tokens":2}}`, 429, 200, 1, 0},
		{"free_quota", `{"error":{"code":"usage_limit_reached"}}`, 429, 200, 0, 0},
		{"unsupported_model", `{"detail":"The 'gpt-6-luna' model is not supported when using Codex with a ChatGPT account."}`, 400, 502, 1, 0},
		{"truncated", `{"type":"response.output_text.delta","delta":"partial"}`, 200, 502, 1, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			expectedVersion := "0.156.0"
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Version") != expectedVersion || r.Header.Get("User-Agent") != "codex_cli_rs/"+expectedVersion {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"detail":"The 'gpt-6-luna' model is not supported when using Codex with a ChatGPT account."}`)
					return
				}
				var request struct {
					Model           string            `json:"model"`
					MaxOutputTokens json.RawMessage   `json:"max_output_tokens"`
					Stream          bool              `json:"stream"`
					Store           bool              `json:"store"`
					Instructions    *string           `json:"instructions"`
					Input           []json.RawMessage `json:"input"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "gpt-6-luna" {
					t.Error("default probe did not select gpt-6-luna")
				}
				if request.MaxOutputTokens != nil {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"detail":"Unsupported parameter: max_output_tokens"}`)
					return
				}
				if !request.Stream || request.Store || request.Instructions == nil || len(request.Input) != 1 {
					t.Error("probe did not use the subscription request shape")
				}
				if test.upstreamStatus != 200 {
					w.WriteHeader(test.upstreamStatus)
					fmt.Fprint(w, test.response)
					return
				}
				if test.name == "missing_content_type" {
					w.Header()["Content-Type"] = nil // Suppress net/http's automatic MIME sniffing.
				} else {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				fmt.Fprint(w, "data: "+test.response+"\n\n")
			}))
			defer upstream.Close()
			server, store, _ := newAccountsTestServer(t, &httpStubOAuth{})
			ctx := context.Background()
			account := domain.Account{ID: "probe-account", Kind: domain.AccountChatGPT, Email: "probe@example.invalid", PlanType: "plus", Status: domain.AccountPaused, CreatedAt: time.Now()}
			if err := store.SaveAccount(ctx, account); err != nil {
				t.Fatal(err)
			}
			ciphertext, err := server.cipher.Encrypt([]byte("synthetic-probe-token"))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: account.ID, AccessTokenEncrypted: ciphertext, RefreshTokenEncrypted: ciphertext, IDTokenEncrypted: ciphertext}); err != nil {
				t.Fatal(err)
			}
			adapter := provider.New(store, nativeTokenSource{}, server.cipher, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL, ResolveClientVersion: store.LoadCodexClientVersion})
			defer adapter.Close()
			warmups := application.NewWarmupService(store, adapter, store, nativeAdmission{}, nil, time.Now)
			admin := http.NewServeMux()
			server.registerAdmin(admin)
			server.registerWarmupRoutes(admin, warmups)
			root := http.NewServeMux()
			server.registerAuth(root)
			root.Handle("/api/", server.requireAdmin(admin))
			unauth := httptest.NewRecorder()
			root.ServeHTTP(unauth, httptest.NewRequest(http.MethodPost, "/api/accounts/probe-account/probe", strings.NewReader(`{}`)))
			if unauth.Code != 401 || calls.Load() != 0 {
				t.Fatal("probe dispatched before admin authorization")
			}
			setup := httptest.NewRequest(http.MethodPost, "http://localhost/api/dashboard-auth/password/setup", strings.NewReader(`{"password":"synthetic-probe-password"}`))
			setup.RemoteAddr = "127.0.0.1:5678"
			setup.Header.Set("Content-Type", "application/json")
			setupRec := httptest.NewRecorder()
			root.ServeHTTP(setupRec, setup)
			if setupRec.Code != 200 {
				t.Fatalf("admin setup: %d", setupRec.Code)
			}
			if test.name == "zero" {
				settings, err := store.LoadSettings(ctx)
				if err != nil {
					t.Fatal(err)
				}
				expectedVersion = "0.157.0"
				update := httptest.NewRequest(http.MethodPut, "http://localhost/api/settings", strings.NewReader(fmt.Sprintf(`{"expectedVersion":%d,"codexClientVersion":%q}`, settings.Version, expectedVersion)))
				update.Header.Set("Content-Type", "application/json")
				update.AddCookie(setupRec.Result().Cookies()[0])
				saved := httptest.NewRecorder()
				root.ServeHTTP(saved, update)
				if saved.Code != 200 || calls.Load() != 0 {
					t.Fatalf("version save failed or generated traffic: %d calls=%d", saved.Code, calls.Load())
				}
			}
			request := httptest.NewRequest(http.MethodPost, "http://localhost/api/accounts/probe-account/probe", strings.NewReader(`{}`))
			request.Header.Set("Content-Type", "application/json")
			request.AddCookie(setupRec.Result().Cookies()[0])
			response := httptest.NewRecorder()
			root.ServeHTTP(response, request)
			if response.Code != test.status || calls.Load() != 1 {
				t.Fatalf("probe outcome: %d %s calls=%d", response.Code, response.Body.String(), calls.Load())
			}
			pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			totals, totalsErr := store.UsageTotals(ctx, "", account.ID)
			if err != nil || totalsErr != nil || len(pending) != test.pending || totals.RequestCount != int64(1-test.pending) || totals.Usage.InputTokens+totals.Usage.OutputTokens != test.tokens {
				t.Fatalf("probe accounting: totals=%+v pending=%d err=%v/%v", totals, len(pending), err, totalsErr)
			}
			logs, err := store.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 10})
			if err != nil || len(logs.Requests) != 1 || logs.Requests[0].RequestKind != "warmup" || logs.Requests[0].APIKeyID != nil {
				t.Fatalf("warmup identity: %+v %v", logs, err)
			}
			if test.pending != 0 && (logs.Requests[0].Tokens != nil || logs.Requests[0].CostUSD != nil) {
				t.Fatal("unknown warmup billing was shown as free")
			}
			updated, err := store.GetAccount(ctx, account.ID)
			if err != nil || updated.Status != domain.AccountPaused {
				t.Fatalf("probe changed operator state: %s %v", updated.Status, err)
			}
		})
	}
}
