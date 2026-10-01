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

	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

// Exercise the public route, real provider error classification and SQLite
// ownership/ledger, injecting quota only at the upstream transport boundary.
func TestQuotaFailoverPreservesActiveThreadAndAccounting(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) { testQuotaContinuity(t, transport) })
	}
}

func testQuotaContinuity(t *testing.T, transport string) {
	var callsMu sync.Mutex
	var accounts []string
	var bodies []string
	var quotaOwner atomic.Value
	quotaOwner.Store("")
	upstreamReply := func(account string, body []byte) (int, []byte) {
		callsMu.Lock()
		accounts = append(accounts, account)
		bodies = append(bodies, string(body))
		n := len(accounts)
		callsMu.Unlock()
		if account == quotaOwner.Load().(string) {
			return http.StatusTooManyRequests, []byte(`{"type":"error","status":429,"error":{"type":"usage_limit_reached","code":"usage_limit_reached","message":"Synthetic quota refusal"}}`)
		}
		output := `[ {"type":"message","id":"msg_ok","role":"assistant","content":[{"type":"output_text","text":"50"}]} ]`
		if n == 2 {
			output = `[{"type":"function_call","id":"fc_calc","call_id":"call_calc","name":"calculate","arguments":"{\"numbers\":[17,25,8]}"}]`
		}
		return 200, []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_%d","status":"completed","output":%s,"usage":{"input_tokens":10,"output_tokens":10}}}`, n, output))
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account := r.Header.Get("ChatGPT-Account-ID")
		if transport == "websocket" {
			connection, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer connection.CloseNow()
			for {
				_, frame, err := connection.Read(r.Context())
				if err != nil {
					return
				}
				_, event := upstreamReply(account, frame)
				if err := connection.Write(r.Context(), websocket.MessageText, event); err != nil {
					return
				}
			}
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		status, event := upstreamReply(account, body)
		if status != 200 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(event)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
	}))
	defer upstream.Close()
	var adapter *provider.Adapter
	server, store, _ := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return adapter.Respond(ctx, target, body, emit)
	}))
	adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
	defer adapter.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connections := map[string]*websocket.Conn{}
	defer func() {
		for _, connection := range connections {
			connection.CloseNow()
		}
	}()
	account, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	account.ChatGPTAccountID = account.ID
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	credential, err := store.GetAccountCredential(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	backup := domain.Account{ID: "backup-account", ChatGPTAccountID: "backup-account", Kind: domain.AccountChatGPT, Provider: "openai", PlanType: "plus", Status: domain.AccountActive, CreatedAt: time.Now()}
	if err := store.SaveAccount(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: backup.ID, AccessTokenEncrypted: credential.AccessTokenEncrypted, RefreshTokenEncrypted: credential.RefreshTokenEncrypted, IDTokenEncrypted: credential.IDTokenEncrypted}); err != nil {
		t.Fatal(err)
	}
	post := func(thread, body string) string {
		t.Helper()
		terminalID := func(data []byte) string {
			var event struct {
				Type     string `json:"type"`
				Response struct {
					ID string `json:"id"`
				} `json:"response"`
			}
			if json.Unmarshal(data, &event) != nil || event.Type == "error" || strings.Contains(string(data), "usage_limit_reached") {
				t.Fatalf("client observed invalid or failed event: %s", data)
			}
			if event.Type == "response.completed" {
				return event.Response.ID
			}
			return ""
		}
		if transport == "websocket" {
			connection := connections[thread]
			if connection == nil {
				var err error
				connection, _, err = websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/backend-api/codex/responses", &websocket.DialOptions{HTTPHeader: http.Header{
					"Authorization": {"Bearer synthetic-key"}, "Session_id": {"shared-client-process"}, "Thread-Id": {thread},
				}})
				if err != nil {
					t.Fatal(err)
				}
				connections[thread] = connection
			}
			var frame map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body), &frame); err != nil {
				t.Fatal(err)
			}
			frame["type"] = json.RawMessage(`"response.create"`)
			encoded, _ := json.Marshal(frame)
			if err := connection.Write(ctx, websocket.MessageText, encoded); err != nil {
				t.Fatal(err)
			}
			for {
				_, data, err := connection.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if id := terminalID(data); id != "" {
					return id
				}
			}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/backend-api/codex/responses", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer synthetic-key")
		request.Header.Set("Session_id", "shared-client-process")
		request.Header.Set("Thread-Id", thread)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || strings.Contains(string(data), "usage_limit_reached") {
			t.Fatalf("client observed quota failure: status=%d body=%s err=%v", response.StatusCode, data, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "data:") {
				if id := terminalID([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))); id != "" {
					return id
				}
			}
		}
		t.Fatalf("missing successful terminal: %s", data)
		return ""
	}
	first := post("active-thread", `{"model":"gpt-6-luna","input":"Remember cedar-731; add 17,25,8 later","stream":true}`)
	owner, err := store.GetContinuation(ctx, "wire-key", first, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(time.Hour)
	if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: owner.AccountID, Window: "primary", UsedPercent: 100, ResetAt: &reset, ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	second := post("active-thread", fmt.Sprintf(`{"model":"gpt-6-luna","previous_response_id":%q,"input":"Use calculate now","stream":true}`, first))
	post("new-thread", `{"model":"gpt-6-luna","input":"Independent new thread","stream":true}`)
	quotaOwner.Store(owner.AccountID)
	third := post("active-thread", fmt.Sprintf(`{"model":"gpt-6-luna","previous_response_id":%q,"input":[{"type":"function_call_output","call_id":"call_calc","output":"50"}],"stream":true}`, second))
	nextOwner, err := store.GetContinuation(ctx, "wire-key", third, time.Now())
	if err != nil || nextOwner.AccountID == owner.AccountID {
		t.Fatalf("quota did not move ownership: %+v %v", nextOwner, err)
	}
	post("active-thread", fmt.Sprintf(`{"model":"gpt-6-luna","previous_response_id":%q,"input":"Continue after switch","stream":true}`, third))
	callsMu.Lock()
	defer callsMu.Unlock()
	if len(accounts) != 6 || accounts[0] != owner.AccountID || accounts[1] != owner.AccountID || accounts[2] == owner.AccountID || accounts[3] != owner.AccountID || accounts[4] != nextOwner.AccountID || accounts[5] != nextOwner.AccountID {
		t.Fatalf("zero-quota continuation/new thread/quota failover sequence: %v", accounts)
	}
	if strings.Contains(bodies[4], "previous_response_id") || !strings.Contains(bodies[4], "cedar-731") || !strings.Contains(bodies[4], "function_call_output") || !strings.Contains(bodies[4], `\"numbers\"`) || !strings.Contains(bodies[4], "call_calc") {
		t.Fatalf("replacement received stale anchor or incomplete tool history: %s", bodies[4])
	}
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil || len(key.Limits) != 1 || key.Limits[0].CurrentValue != 100 {
		t.Fatalf("five successful requests and a quota refusal settled incorrectly: %+v %v", key.Limits, err)
	}
	pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("definitive quota refusal retained unknown billing: %+v %v", pending, err)
	}
}
