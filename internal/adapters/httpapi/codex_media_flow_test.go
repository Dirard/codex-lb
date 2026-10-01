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
	"github.com/coder/websocket"
)

func TestCodexImageGenerationResponsesRoundTrip(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Tools []struct {
				Type string `json:"type"`
			} `json:"tools"`
			Input json.RawMessage `json:"input"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/codex/responses" ||
			json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Tools) != 1 ||
			body.Tools[0].Type != "image_generation" || !strings.Contains(string(body.Input), `"type":"input_image"`) {
			t.Error("image tool or screenshot was not forwarded to subscription Responses")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_image\",\"status\":\"completed\",\"output\":[{\"id\":\"image_1\",\"type\":\"image_generation_call\",\"status\":\"completed\",\"result\":\"aGVsbG8=\"}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}))
	defer upstream.Close()
	var adapter *provider.Adapter
	server, store, _ := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return adapter.Respond(ctx, target, body, emit)
	}))
	adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	defer adapter.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-6-sol","input":[{"role":"user","content":[{"type":"input_text","text":"Draw a square"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}],"tools":[{"type":"image_generation"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer synthetic-key")
	request.Header.Set("Content-Type", "application/json")
	client := server.Client()
	client.Timeout = 5 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || calls.Load() != 1 ||
		!strings.Contains(string(payload), `"type":"image_generation_call"`) || !strings.Contains(string(payload), "aGVsbG8=") {
		t.Fatalf("image roundtrip: status=%d calls=%d body=%s err=%v", response.StatusCode, calls.Load(), payload, err)
	}
	totals, err := store.UsageTotals(context.Background(), "wire-key", "")
	if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 1 {
		t.Fatalf("image usage lost: %+v %v", totals, err)
	}
}

func TestCodexRealtimeCallLiveWebSocketKeepsOwner(t *testing.T) {
	var created, live atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/codex/realtime/calls":
			created.Add(1)
			if r.Method != http.MethodPost || r.Header.Get("ChatGPT-Account-ID") != "original-owner" {
				t.Error("realtime call used the wrong account")
			}
			w.Header().Set("Location", "/backend-api/codex/realtime/calls/call-owned")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"call_id":"call-owned"}`)
		case "/v1/live/call-owned":
			live.Add(1)
			if r.URL.RawQuery != "version=2" || r.Header.Get("ChatGPT-Account-ID") != "original-owner" {
				t.Error("live sideband switched account or lost query")
			}
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.CloseNow()
			conn.SetReadLimit(application.MaxRealtimeMessageBytes)
			_, message, err := conn.Read(r.Context())
			if err != nil || string(message) != `{"type":"ping"}` {
				t.Errorf("live upstream frame: %s %v", message, err)
				return
			}
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"pong"}`)); err != nil {
				t.Error(err)
			}
			for range 2 {
				kind, payload, err := conn.Read(r.Context())
				if err != nil || len(payload) != 128<<10 {
					t.Error("large downstream frame failed")
					return
				}
				if err := conn.Write(r.Context(), kind, payload); err != nil {
					t.Error(err)
					return
				}
			}
			_ = conn.Close(websocket.StatusNormalClosure, "done")
		default:
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	_, store, proxy := wireFixtureWithProxy(t, nil)
	ctx := context.Background()
	owner, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	owner.ChatGPTAccountID = "original-owner"
	if err := store.SaveAccount(ctx, owner); err != nil {
		t.Fatal(err)
	}
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	defer adapter.Close()
	operations := application.NewCodexOperations(store, store, adapter, time.Hour)
	operations.ConfigureAdmission(proxy)
	mux := http.NewServeMux()
	httpapi.RegisterCodexOperationRoutes(mux, store, operations)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/backend-api/codex/realtime/calls", strings.NewReader(`{"model":"gpt-6-sol"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer synthetic-key")
	request.Header.Set("Content-Type", "application/json")
	client := server.Client()
	client.Timeout = 5 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated || created.Load() != 1 {
		t.Fatalf("create realtime call: status=%d upstream=%d", response.StatusCode, created.Load())
	}
	credential, err := store.GetAccountCredential(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	other := domain.Account{ID: "other-account", Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "other-owner", Email: "other@example.invalid", PlanType: "plus", Status: domain.AccountActive, CreatedAt: time.Now()}
	if err := store.SaveAccount(ctx, other); err != nil {
		t.Fatal(err)
	}
	credential.AccountID = other.ID
	if err := store.SaveAccountCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.RoutingStrategy, settings.SingleAccountID = "single_account", other.ID
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/live/call-owned?version=2"
	wsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(wsCtx, wsURL, &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Authorization": []string{"Bearer synthetic-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	connection.SetReadLimit(application.MaxRealtimeMessageBytes)
	if err := connection.Write(wsCtx, websocket.MessageText, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatal(err)
	}
	_, message, err := connection.Read(wsCtx)
	if err != nil || string(message) != `{"type":"pong"}` || live.Load() != 1 {
		t.Fatalf("live sideband: %s calls=%d err=%v", message, live.Load(), err)
	}
	for _, kind := range []websocket.MessageType{websocket.MessageText, websocket.MessageBinary} {
		payload := []byte(strings.Repeat("x", 128<<10))
		if err := connection.Write(wsCtx, kind, payload); err != nil {
			t.Fatal(err)
		}
		gotKind, got, err := connection.Read(wsCtx)
		if err != nil || gotKind != kind || string(got) != string(payload) {
			t.Fatalf("large frame not relayed: kind=%v bytes=%d err=%v", gotKind, len(got), err)
		}
	}
	_ = connection.Close(websocket.StatusNormalClosure, "done")
	recorded, err := store.GetCodexResourceOwner(ctx, application.CodexResourceRealtime, "call-owned", "wire-key", time.Now())
	if err != nil || recorded.AccountID != owner.ID {
		t.Fatalf("live call owner changed: %+v %v", recorded, err)
	}
}
