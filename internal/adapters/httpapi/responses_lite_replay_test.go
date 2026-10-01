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
	"github.com/coder/websocket"
)

func TestResponsesLiteQuotaFailoverRebuildsPrefixAndAnsweredTools(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(application.ResponsesLiteHeader) != "" {
			t.Error("Lite header leaked into handshake")
		}
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		for {
			_, raw, err := connection.Read(r.Context())
			if err != nil {
				return
			}
			call := calls.Add(1)
			var body map[string]json.RawMessage
			_ = json.Unmarshal(raw, &body)
			if call == 2 {
				if string(body["previous_response_id"]) != `"lite-first"` || r.Header.Get("ChatGPT-Account-ID") != "initial" {
					t.Error("active continuation lost its account")
				}
				_ = connection.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"hidden-refusal","status":"in_progress"}}`))
				_ = connection.Write(r.Context(), websocket.MessageText, []byte(`{"type":"error","status":429,"error":{"code":"usage_limit_reached","message":"quota exhausted"}}`))
				continue
			}
			id, output := "lite-first", `[{"type":"custom_tool_call","id":"old-item","call_id":"tool-1","name":"apply_patch","input":"patch"}]`
			if call == 3 {
				id, output = "lite-moved", `[]`
				var metadata map[string]string
				_ = json.Unmarshal(body["client_metadata"], &metadata)
				if r.Header.Get("ChatGPT-Account-ID") != "alternative" || body["previous_response_id"] != nil || r.Header.Get("X-Codex-Turn-State") != "" ||
					metadata[application.ResponsesLiteMetadataKey] != "true" || metadata["keep"] != "yes" {
					t.Error("fresh replay retained old owner state or lost canonical Lite signal")
				}
				for _, required := range []string{"additional_tools", "inline instructions", "custom_tool_call", "custom_tool_call_output", "tool-1", "done"} {
					if !strings.Contains(string(body["input"]), required) {
						t.Errorf("fresh replay lost %s", required)
					}
				}
				if strings.Contains(string(body["input"]), "old-item") {
					t.Error("fresh replay retained account-owned item id")
				}
			}
			created := fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"status":"in_progress"}}`, id)
			completed := fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":%s,"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`, id, output)
			if connection.Write(r.Context(), websocket.MessageText, []byte(created)) != nil || connection.Write(r.Context(), websocket.MessageText, []byte(completed)) != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	var adapter *provider.Adapter
	server, store := wireFixture(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return adapter.Respond(ctx, target, body, emit)
	}))
	adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	defer adapter.Close()
	ctx := context.Background()
	account, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	account.ChatGPTAccountID = "initial"
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	connection := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}})
	defer connection.CloseNow()
	if id := liteSocketTurn(t, connection, `{"type":"response.create","model":"gpt-6-sol","input":`+liteWireInput+`}`); id != "lite-first" {
		t.Fatalf("first Lite turn failed: %s", id)
	}
	account.ID, account.ChatGPTAccountID = "alternative-account", "alternative"
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
	frame := `{"type":"response.create","model":"gpt-6-sol","previous_response_id":"lite-first","input":[{"type":"custom_tool_call_output","call_id":"tool-1","output":"done"}],"client_metadata":{"` + application.ResponsesLiteMetadataKey + `":"true","keep":"yes"}}`
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := connection.Write(callCtx, websocket.MessageText, []byte(frame)); err != nil {
		t.Fatal(err)
	}
	for {
		_, event, err := connection.Read(callCtx)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(event), "hidden-refusal") || strings.Contains(string(event), "usage_limit_reached") || strings.Contains(string(event), `"type":"error"`) {
			t.Fatalf("internal refusal exposed downstream: %s", event)
		}
		if strings.Contains(string(event), `"type":"response.completed"`) {
			break
		}
	}
	owner, err := store.GetContinuation(ctx, "wire-key", "lite-moved", time.Now())
	if err != nil || owner.AccountID != account.ID || calls.Load() != 3 {
		t.Fatalf("Lite failover did not complete on new owner: %+v calls=%d error=%v", owner, calls.Load(), err)
	}
	totals, err := store.UsageTotals(ctx, "wire-key", "")
	if err != nil || totals.Usage.InputTokens != 4 || totals.Usage.OutputTokens != 2 {
		t.Fatalf("Lite replay accounting changed: %+v %v", totals, err)
	}
	if owner.FilePinned {
		t.Fatal("a function schema property was misclassified as a file pin")
	}
	initial, err := store.GetAccount(ctx, "wire-account")
	if err != nil || initial.Status != domain.AccountQuotaExceeded {
		t.Fatalf("explicit quota refusal was not retained: status=%s error=%v", initial.Status, err)
	}
}
