package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
	"github.com/coder/websocket"
)

func TestExternalSourceWebSocketFallbackBeforeReservation(t *testing.T) {
	var calls, nativeCalls atomic.Int64
	server, store, proxy := wireFixtureWithProxy(t, wireProvider(func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		calls.Add(1)
		if target.Account.ID != "external" {
			t.Error("external-only key reached a subscription account")
		}
		if target.UseWebSocket {
			nativeCalls.Add(1)
		}
		return wireComplete("source_response", emit)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proxy.ResolvePrice = func(context.Context, domain.Account, string) (pricing.Price, error) { return pricing.Price{}, nil }
	if err := store.SaveAccount(ctx, domain.Account{ID: "external", Kind: domain.AccountExternal, Provider: "zai", Status: domain.AccountActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	credential, err := store.GetAccountCredential(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: "external", ExternalKeyEncrypted: credential.AccessTokenEncrypted}); err != nil {
		t.Fatal(err)
	}
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	key.AllowedModels = []string{"glm-5.3"}
	key.ApplyToCodexModel = true
	key.AccountAssignmentScopeEnabled = true
	key.AssignedAccountIDs = []string{"external"}
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/responses", "/v1/responses/", "/backend-api/codex/responses", "/backend-api/codex/responses/"} {
		t.Run(path, func(t *testing.T) {
			connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+path, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-key"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.CloseNow()
			for _, body := range []string{
				`{"type":"response.create","model":"glm-5.3","input":[],"generate":false,"store":false}`,
				`{"type":"response.create","model":"glm-5.3","input":"hello","store":false}`,
			} {
				if err := connection.Write(ctx, websocket.MessageText, []byte(body)); err != nil {
					t.Fatal(err)
				}
				_, frame, err := connection.Read(ctx)
				var event struct {
					Type   string `json:"type"`
					Status int    `json:"status"`
					Error  struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err != nil || json.Unmarshal(frame, &event) != nil || event.Type != "error" || event.Status != 503 || event.Error.Code != "model_source_requires_http_transport" {
					t.Fatalf("missing retryable transport envelope: %s %v", frame, err)
				}
			}
			if err := connection.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-6-luna","input":"hello"}`)); err != nil {
				t.Fatal(err)
			}
			_, frame, err := connection.Read(ctx)
			if err != nil || !strings.Contains(string(frame), "model_not_allowed") {
				t.Fatalf("transport fallback bypassed model policy: %s %v", frame, err)
			}
		})
	}
	key, err = store.GetAPIKey(ctx, "wire-key")
	if err != nil || len(key.Limits) != 1 || key.Limits[0].CurrentValue != 0 || calls.Load() != 0 {
		t.Fatalf("unsupported frames reserved budget or reached upstream: %+v calls=%d %v", key.Limits, calls.Load(), err)
	}
	pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("transport fallback retained usage: %+v %v", pending, err)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"glm-5.3","input":"hello","stream":true,"store":false}`))
	request.Header.Set("Authorization", "Bearer synthetic-key")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !strings.Contains(string(body), "response.completed") || calls.Load() != 1 || nativeCalls.Load() != 0 {
		t.Fatalf("HTTP retry failed: status=%d calls=%d native=%d body=%s err=%v", response.StatusCode, calls.Load(), nativeCalls.Load(), body, err)
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.UpstreamStreamTransport = "websocket"
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if err := connection.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"glm-5.3","input":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	_, frame, err := connection.Read(ctx)
	if err != nil || !strings.Contains(string(frame), "response.completed") || calls.Load() != 2 || nativeCalls.Load() != 1 {
		t.Fatalf("explicit external native WS regressed: %s %v", frame, err)
	}
}
