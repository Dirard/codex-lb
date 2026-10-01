package httpapi_test

import (
	"context"
	"crypto/sha256"
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
	"github.com/coder/websocket"
)

func TestLogicalThreadsKeepIndependentOwnersUntilQuotaRefusal(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/backend-api/codex/responses/"} {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			var calls atomic.Int64
			var refuse atomic.Bool
			targets := make(chan application.ResponseTarget, 10)
			server, store := wireFixture(t, wireProvider(func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				targets <- target
				id := fmt.Sprintf("identity_%d", calls.Add(1))
				if refuse.Load() && target.Account.ID == "wire-account" {
					return application.ResponseResult{}, &application.ProviderFailure{Code: "usage_limit_reached", Status: 429, QuotaRefused: true, Dispatched: true}
				}
				return wireComplete(id, emit)
			}))
			post := func(thread, turn, body string) application.ResponseTarget {
				t.Helper()
				headers := http.Header{"Session_id": {"shared-process"}, "Thread-Id": {thread}}
				if turn != "" {
					headers.Set("X-Codex-Turn-State", turn)
				}
				status, payload := liteHTTPPost(t, server.URL+path, body, headers)
				if status != 200 {
					t.Fatalf("thread %s: %d %s", thread, status, payload)
				}
				return <-targets
			}
			first := post("one", "", `{"model":"gpt-6-sol","input":"original"}`)
			account, err := store.GetAccount(ctx, "wire-account")
			if err != nil {
				t.Fatal(err)
			}
			account.ID = "z-alternative"
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
			reset := time.Now().Add(time.Hour)
			if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "wire-account", Window: "primary", UsedPercent: 100, ResetAt: &reset, ObservedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			second := post("two", "", `{"model":"gpt-6-sol","input":"new sibling"}`)
			continued := post("one", "provider-opaque", `{"model":"gpt-6-sol","previous_response_id":"identity_1","input":"continue"}`)
			if first.Account.ID != "wire-account" || second.Account.ID != "z-alternative" || continued.Account.ID != first.Account.ID ||
				first.SessionID == second.SessionID || first.SessionID != continued.SessionID || continued.TurnState != "provider-opaque" {
				t.Fatalf("logical threads lost ownership: first=%+v sibling=%+v continued=%+v", first, second, continued)
			}
			refuse.Store(true)
			refused := post("one", "provider-opaque", `{"model":"gpt-6-sol","previous_response_id":"identity_3","input":"next"}`)
			moved := <-targets
			if refused.Account.ID != "wire-account" || moved.Account.ID != "z-alternative" || moved.TurnState != "" {
				t.Fatalf("quota-only failover leaked owner state: %+v %+v", refused, moved)
			}
			turnID := fmt.Sprintf("session:turn:%x", sha256.Sum256([]byte("provider-opaque")))
			alias, err := store.GetContinuation(ctx, "wire-key", turnID, time.Now())
			if err != nil || alias.AccountID != "z-alternative" || alias.TurnStateForwardable {
				t.Fatalf("replay did not convert the token into a proxy-only anchor: %+v %v", alias, err)
			}
			status, payload := liteHTTPPost(t, server.URL+path, `{"model":"gpt-6-sol","input":"turn-only reconnect"}`, http.Header{"X-Codex-Turn-State": {"provider-opaque"}})
			if status != 200 {
				t.Fatalf("turn-only reconnect: %d %s", status, payload)
			}
			if target := <-targets; target.Account.ID != moved.Account.ID || target.TurnState != "" {
				t.Fatalf("reconnected token crossed accounts: %+v", target)
			}
			before := calls.Load()
			status, payload = liteHTTPPost(t, server.URL+path, `{"model":"gpt-6-sol","previous_response_id":"identity_1","input":"conflict"}`, http.Header{"X-Codex-Turn-State": {"provider-opaque"}})
			if status != 409 || !strings.Contains(payload, "conversation_owner_conflict") || calls.Load() != before {
				t.Fatalf("conflicting previous/turn owners dispatched: %d %s", status, payload)
			}
			if err := store.SaveCodexResourceOwner(ctx, application.CodexResourceOwner{ResourceType: application.CodexResourceFile, ResourceID: "old-file", KeyID: "wire-key", AccountID: "wire-account", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			status, payload = liteHTTPPost(t, server.URL+path, `{"model":"gpt-6-sol","input":[{"type":"input_file","file_id":"old-file"}]}`, http.Header{"X-Codex-Turn-State": {"provider-opaque"}})
			if status != 409 || !strings.Contains(payload, "file_owner_conflict") || calls.Load() != before {
				t.Fatalf("conflicting file/turn owners dispatched: %d %s", status, payload)
			}
		})
	}
}

func TestTurnStateRequiresProofAndSynthesizedWebSocketTokenNeverGoesUpstream(t *testing.T) {
	var calls atomic.Int64
	targets := make(chan application.ResponseTarget, 8)
	server, store := wireFixture(t, wireProvider(func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		targets <- target
		return wireComplete(fmt.Sprintf("turn_%d", calls.Add(1)), emit)
	}))
	for _, token := range []string{"unknown-opaque", "turn_forged-proxy-token"} {
		status, body := liteHTTPPost(t, server.URL+"/v1/responses", `{"model":"gpt-6-sol","input":"hi"}`, http.Header{"X-Codex-Turn-State": {token}})
		if status != 409 || !strings.Contains(body, "conversation_owner_required") {
			t.Fatalf("unproven token accepted: %d %s", status, body)
		}
		conn := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}, "X-Codex-Turn-State": {token}})
		if body := capabilityFrame(t, conn, `{"type":"response.create","model":"gpt-6-sol","input":"hi"}`); !strings.Contains(body, "conversation_owner_required") {
			t.Fatalf("WS accepted unproven token: %s", body)
		}
		conn.CloseNow()
	}
	if calls.Load() != 0 {
		t.Fatal("unproven token reached upstream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	token := response.Header.Get("X-Codex-Turn-State")
	for range 2 {
		if body := capabilityFrame(t, conn, `{"type":"response.create","model":"gpt-6-sol","input":"hi"}`); !strings.Contains(body, "response.completed") {
			t.Fatalf("synthesized turn failed: %s", body)
		}
		if target := <-targets; target.TurnState != "" {
			t.Fatal("proxy-generated token was forwarded upstream")
		}
	}
	conn.CloseNow()
	alias, err := store.GetContinuation(ctx, "wire-key", fmt.Sprintf("session:turn:%x", sha256.Sum256([]byte(token))), time.Now())
	if err != nil || alias.TurnStateForwardable || alias.AccountID != "wire-account" {
		t.Fatalf("synthesized owner not retained: %+v %v", alias, err)
	}
	reconnected := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}, "X-Codex-Turn-State": {token}})
	if body := capabilityFrame(t, reconnected, `{"type":"response.create","model":"gpt-6-sol","input":"reconnect"}`); !strings.Contains(body, "response.completed") {
		t.Fatalf("turn-only WS reconnect: %s", body)
	}
	if target := <-targets; target.TurnState != "" || target.Account.ID != alias.AccountID {
		t.Fatalf("turn-only reconnect did not restore proxy-only owner: %+v", target)
	}
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	key.ID, key.Name, key.KeyHash = "other-key", "other", fmt.Sprintf("%x", sha256.Sum256([]byte("other-synthetic")))
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, body := capabilityPost(t, server.URL, "other-synthetic", `{"model":"gpt-6-sol","input":"cross-key"}`, http.Header{"X-Codex-Turn-State": {token}})
	if status != 409 || calls.Load() != 3 {
		t.Fatalf("turn-state crossed key scope: %d %s", status, body)
	}
}

func TestFailedResponseDoesNotEstablishConversationAliases(t *testing.T) {
	server, store := wireFixture(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, _ func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return application.ResponseResult{ResponseID: "failed-response", Failed: true, ErrorCode: "server_error", UsageKnown: true,
			Response: []byte(`{"id":"failed-response","status":"failed","output":[]}`)}, nil
	}))
	liteHTTPPost(t, server.URL+"/v1/responses", `{"model":"gpt-6-sol","input":"hi"}`, http.Header{"Session_id": {"failed-session"}, "Thread-Id": {"failed-thread"}})
	framed, _ := json.Marshal([]string{"failed-session", "failed-thread"})
	if _, err := store.GetContinuation(context.Background(), "wire-key", fmt.Sprintf("session:thread:%x", sha256.Sum256(framed)), time.Now()); err != domain.ErrNotFound {
		t.Fatalf("failed response established an owner alias: %v", err)
	}
}

func TestCompactSharesThreadIdentityAndHeaderAliases(t *testing.T) {
	ctx := context.Background()
	responseTargets := make(chan application.ResponseTarget, 2)
	responses, store, proxy := wireFixtureWithProxy(t, wireProvider(func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		responseTargets <- target
		return wireComplete("compact-thread-seed", emit)
	}))
	account, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	account.ChatGPTAccountID = account.ID
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	headers := http.Header{"Session-Id": {"shared-process"}, "Thread-Id": {"compact-thread"}}
	if status, body := liteHTTPPost(t, responses.URL+"/v1/responses", `{"model":"gpt-6-sol","input":"seed"}`, headers); status != 200 {
		t.Fatalf("seed: %d %s", status, body)
	}
	seed := <-responseTargets
	reset := time.Now().Add(time.Hour)
	if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: account.ID, Window: "primary", UsedPercent: 100, ResetAt: &reset, ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	targets := make(chan http.Header, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targets <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"compact_thread_%d","object":"response.compact","output":[{"type":"compaction","encrypted_content":"summary"}],"usage":{"input_tokens":1,"output_tokens":1}}`, calls.Add(1))
	}))
	defer upstream.Close()
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
	headers = http.Header{"X-Codex-Session-Id": {" shared-process "}, "Thread-Id": {" compact-thread "}, "X-Codex-Turn-State": {"provider-token"}}
	for _, test := range []struct{ path, input string }{
		{compact.URL + "/v1/responses/compact/", `"compact"`},
		{responses.URL + "/backend-api/codex/responses", `[{"role":"user","content":"compact"},{"type":"compaction_trigger"}]`},
	} {
		status, body := liteHTTPPost(t, test.path, `{"model":"gpt-6-sol","stream":true,"input":`+test.input+`}`, headers)
		if status != 200 {
			t.Fatalf("compact continuation: %d %s", status, body)
		}
		sent := <-targets
		if sent.Get("Session_id") != seed.SessionID || sent.Get("X-Codex-Turn-State") != "provider-token" || sent.Get("ChatGPT-Account-ID") != account.ID {
			t.Fatalf("compact diverged from Responses identity: session=%q turn=%q account=%q", sent.Get("Session_id"), sent.Get("X-Codex-Turn-State"), sent.Get("ChatGPT-Account-ID"))
		}
	}
	status, body := liteHTTPPost(t, compact.URL+"/v1/responses/compact", `{"model":"gpt-6-sol","input":"compact"}`, http.Header{"X-Codex-Turn-State": {"unknown-token"}})
	if status != 409 || !strings.Contains(body, "conversation_owner_required") || calls.Load() != 2 {
		t.Fatalf("unknown compact token reached upstream: %d %s", status, body)
	}
}
