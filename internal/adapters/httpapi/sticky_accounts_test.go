package httpapi_test

import (
	"context"
	"encoding/json"
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
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// stickyFixture serves Responses through the real provider adapter and SQLite
// ownership with two ChatGPT accounts. The upstream stub refuses for quota
// whichever account is named in refuse.
type stickyFixture struct {
	server   *httptest.Server
	store    *sqlite.Store
	refuse   atomic.Value
	mu       sync.Mutex
	accounts []string
}

func newStickyFixture(t *testing.T) *stickyFixture {
	t.Helper()
	f := &stickyFixture{}
	f.refuse.Store("")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account := r.Header.Get("ChatGPT-Account-ID")
		_, _ = io.Copy(io.Discard, r.Body)
		f.mu.Lock()
		f.accounts = append(f.accounts, account)
		n := len(f.accounts)
		f.mu.Unlock()
		if account == f.refuse.Load().(string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","status":429,"error":{"type":"usage_limit_reached","code":"usage_limit_reached","message":"Synthetic quota refusal"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_%d","status":"completed","output":[{"type":"message","id":"msg_%d","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":10}}}`, n, n))
	}))
	t.Cleanup(upstream.Close)
	var adapter *provider.Adapter
	f.server, f.store, _ = wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return adapter.Respond(ctx, target, body, emit)
	}))
	adapter = provider.New(f.store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
	t.Cleanup(func() { adapter.Close() })
	ctx := context.Background()
	account, err := f.store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	account.ChatGPTAccountID = account.ID
	account.RoutingPolicy = "burn_first" // new conversations start here
	if err := f.store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	credential, err := f.store.GetAccountCredential(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	backup := domain.Account{ID: "backup-account", ChatGPTAccountID: "backup-account", Kind: domain.AccountChatGPT, Provider: "openai", PlanType: "plus", RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: time.Now()}
	if err := f.store.SaveAccount(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: backup.ID, AccessTokenEncrypted: credential.AccessTokenEncrypted, RefreshTokenEncrypted: credential.RefreshTokenEncrypted, IDTokenEncrypted: credential.IDTokenEncrypted}); err != nil {
		t.Fatal(err)
	}
	return f
}

// post sends one turn of a thread and returns the status, the body and the
// completed response id when there is one.
func (f *stickyFixture) post(t *testing.T, thread, body string) (int, string, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, f.server.URL+"/backend-api/codex/responses", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer synthetic-key")
	request.Header.Set("Session_id", "sticky-client")
	request.Header.Set("Thread-Id", thread)
	response, err := f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	id := ""
	for _, line := range strings.Split(string(data), "\n") {
		var event struct {
			Type     string `json:"type"`
			Response struct {
				ID string `json:"id"`
			} `json:"response"`
		}
		if strings.HasPrefix(line, "data:") && json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) == nil && event.Type == "response.completed" {
			id = event.Response.ID
		}
	}
	return response.StatusCode, string(data), id
}

func (f *stickyFixture) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.accounts...)
}

func TestStickyOnlyAccountKeepsItsConversationOnQuotaRefusal(t *testing.T) {
	for _, sticky := range []bool{true, false} {
		t.Run(fmt.Sprintf("sticky=%v", sticky), func(t *testing.T) {
			f := newStickyFixture(t)
			ctx := context.Background()
			if err := f.store.UpdateAccountStickyOnly(ctx, "wire-account", sticky); err != nil {
				t.Fatal(err)
			}
			_, _, first := f.post(t, "thread", `{"model":"gpt-6-luna","input":"start","stream":true}`)
			owner, err := f.store.GetContinuation(ctx, "wire-key", first, time.Now())
			if err != nil || owner.AccountID != "wire-account" {
				t.Fatalf("conversation did not start on the first account: %+v %v", owner, err)
			}
			f.refuse.Store("wire-account")
			status, body, second := f.post(t, "thread", fmt.Sprintf(`{"model":"gpt-6-luna","previous_response_id":%q,"input":"next","stream":true}`, first))
			calls := f.calls()
			if !sticky {
				if second == "" || len(calls) != 3 || calls[2] != "backup-account" {
					t.Fatalf("normal account did not fail over: status=%d calls=%v body=%s", status, calls, body)
				}
				return
			}
			if second != "" || status != http.StatusTooManyRequests || !strings.Contains(body, `"type":"usage_limit_reached"`) || len(calls) != 2 || calls[1] != "wire-account" {
				t.Fatalf("sticky-only owner's conversation moved or the refusal was hidden: status=%d calls=%v body=%s", status, calls, body)
			}
			marked, err := f.store.GetContinuation(ctx, "wire-key", first, time.Now())
			if err != nil || marked.QuotaRefused {
				t.Fatalf("sticky-only conversation was marked quota-refused: %+v %v", marked, err)
			}
			f.refuse.Store("")
			_, body, third := f.post(t, "thread", fmt.Sprintf(`{"model":"gpt-6-luna","previous_response_id":%q,"input":"after reset","stream":true}`, first))
			calls = f.calls()
			if third == "" || len(calls) != 3 || calls[2] != "wire-account" {
				t.Fatalf("next turn did not return to the sticky-only owner: calls=%v body=%s", calls, body)
			}
			key, err := f.store.GetAPIKey(ctx, "wire-key")
			if err != nil || len(key.Limits) != 1 || key.Limits[0].CurrentValue != 40 {
				t.Fatalf("refusal was not settled uncharged: %+v %v", key.Limits, err)
			}
		})
	}
}

func TestStickyOnlyAccountStillHandsOffANewConversation(t *testing.T) {
	f := newStickyFixture(t)
	ctx := context.Background()
	if err := f.store.UpdateAccountStickyOnly(ctx, "wire-account", true); err != nil {
		t.Fatal(err)
	}
	f.refuse.Store("wire-account")
	_, body, first := f.post(t, "fresh", `{"model":"gpt-6-luna","input":"start","stream":true}`)
	calls := f.calls()
	if first == "" || len(calls) != 2 || calls[0] != "wire-account" || calls[1] != "backup-account" {
		t.Fatalf("a new conversation refused by a sticky-only account did not move: calls=%v body=%s", calls, body)
	}
}

func TestStickyOnlyAccountIgnoresAnEarlierQuotaMark(t *testing.T) {
	f := newStickyFixture(t)
	ctx := context.Background()
	_, _, first := f.post(t, "thread", `{"model":"gpt-6-luna","input":"start","stream":true}`)
	reservation := domain.ReservationRequest{ID: "earlier-refusal", APIKeyID: "wire-key", AccountID: "wire-account", Continuation: true,
		Model: "gpt-6-luna", Budget: domain.UsageAmount{InputTokens: 1}, Now: time.Now()}
	if _, err := f.store.ReserveUsage(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SettleUsage(ctx, reservation.ID, domain.UsageSettlement{Status: "failed", Event: domain.UsageEvent{RequestID: reservation.ID,
		AccountID: reservation.AccountID, Model: reservation.Model, Status: "error", ErrorCode: "usage_limit_reached", RequestedAt: reservation.Now}}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.MarkContinuationQuotaRefused(ctx, "wire-key", first, "wire-account", reservation.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpdateAccountStickyOnly(ctx, "wire-account", true); err != nil {
		t.Fatal(err)
	}
	_, body, second := f.post(t, "thread", fmt.Sprintf(`{"model":"gpt-6-luna","previous_response_id":%q,"input":"next","stream":true}`, first))
	calls := f.calls()
	if second == "" || len(calls) != 2 || calls[1] != "wire-account" {
		t.Fatalf("a marked conversation of a sticky-only owner was moved: calls=%v body=%s", calls, body)
	}
}

func TestStickyOnlyCompactionStaysOnOwner(t *testing.T) {
	for _, marked := range []bool{false, true} {
		t.Run(fmt.Sprintf("marked=%v", marked), func(t *testing.T) {
			ctx := context.Background()
			var oldCalls, newCalls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("ChatGPT-Account-ID") == "wire-account" {
					oldCalls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(429)
					fmt.Fprint(w, `{"error":{"code":"usage_limit_reached","message":"old quota"}}`)
					return
				}
				newCalls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"compact_moved","output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`+"\n\n")
			}))
			defer upstream.Close()
			_, store, proxy := wireFixtureWithProxy(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, _ func(application.ResponseEvent) error) (application.ResponseResult, error) {
				return application.ResponseResult{ResponseID: "old-response", Response: []byte(`{"id":"old-response","output":[]}`), Usage: domain.UsageAmount{InputTokens: 1, OutputTokens: 1}, UsageKnown: true}, nil
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
			if _, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "wire-key", SessionID: "logical-session"}, []byte(`{"model":"gpt-6-sol","input":[{"role":"user","content":"original"}]}`), nil); err != nil {
				t.Fatal(err)
			}
			if err := store.UpdateAccountStickyOnly(ctx, "wire-account", true); err != nil {
				t.Fatal(err)
			}
			account.ID, account.ChatGPTAccountID, account.RoutingPolicy = "z-alternative", "z-alternative", "normal"
			if err := store.SaveAccount(ctx, account); err != nil {
				t.Fatal(err)
			}
			credential, _ := store.GetAccountCredential(ctx, "wire-account")
			credential.AccountID = account.ID
			if err := store.SaveAccountCredential(ctx, credential); err != nil {
				t.Fatal(err)
			}
			if marked {
				reservation := domain.ReservationRequest{ID: "earlier-refusal", APIKeyID: "wire-key", AccountID: "wire-account", Continuation: true,
					Model: "gpt-6-sol", Budget: domain.UsageAmount{InputTokens: 1}, Now: time.Now()}
				if _, err := store.ReserveUsage(ctx, reservation); err != nil {
					t.Fatal(err)
				}
				if _, err := store.SettleUsage(ctx, reservation.ID, domain.UsageSettlement{Status: "failed", Event: domain.UsageEvent{RequestID: reservation.ID,
					AccountID: reservation.AccountID, Model: reservation.Model, Status: "error", ErrorCode: "usage_limit_reached", RequestedAt: reservation.Now}}); err != nil {
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
			httpapi.RegisterCodexOperationRoutes(mux, store, operations, nil)
			compact := httptest.NewServer(mux)
			defer compact.Close()
			headers := http.Header{}
			headers.Set("Session_id", "logical-session")
			status, body := liteHTTPPost(t, compact.URL+"/v1/responses/compact", `{"model":"gpt-6-sol","stream":true,"input":[{"role":"user","content":"original"}],"previous_response_id":"old-response"}`, headers)
			if status != http.StatusTooManyRequests || !strings.Contains(body, "usage_limit_reached") || oldCalls.Load() != 1 || newCalls.Load() != 0 {
				t.Fatalf("sticky-only compaction moved: status=%d old=%d new=%d body=%s", status, oldCalls.Load(), newCalls.Load(), body)
			}
			if !marked {
				old, err := store.GetContinuation(ctx, "wire-key", "old-response", time.Now())
				if err != nil || old.QuotaRefused {
					t.Fatalf("sticky-only compaction marked the conversation: %+v %v", old, err)
				}
			}
		})
	}
}
