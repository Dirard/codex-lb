package httpapi_test

import (
	"context"
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

func TestRealtimeStreamFailureAndCancellationReleaseCapacity(t *testing.T) {
	for _, mode := range []string{"abnormal upstream", "abrupt upstream", "oversized upstream", "oversized client", "cancel request"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connection, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer connection.CloseNow()
				connection.SetReadLimit(application.MaxRealtimeMessageBytes + 1024)
				close(entered)
				switch mode {
				case "abnormal upstream":
					_ = connection.Close(websocket.StatusInternalError, "synthetic-private-SDP")
				case "abrupt upstream":
					_ = connection.CloseNow()
				case "oversized upstream":
					_ = connection.Write(r.Context(), websocket.MessageBinary, []byte(strings.Repeat("x", application.MaxRealtimeMessageBytes+64)))
				default:
					_, _, _ = connection.Read(r.Context())
				}
			}))
			defer upstream.Close()
			_, store, proxy := wireFixtureWithProxy(t, nil)
			account, err := store.GetAccount(context.Background(), "wire-account")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveCodexResourceOwner(context.Background(), application.CodexResourceOwner{ResourceType: application.CodexResourceRealtime, ResourceID: "rtc_contract", KeyID: "wire-key", AccountID: account.ID, AccountGeneration: account.Generation, RouteRevision: account.RouteRevision, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
			defer adapter.Close()
			ops := application.NewCodexOperations(store, store, adapter, time.Hour)
			ops.ConfigureAdmission(proxy)
			var archives atomic.Int32
			ops.ConfigureDiagnostics(func(context.Context, application.ErrorDiagnostic) { archives.Add(1) })
			handler := httpapi.NewCodexOperationsHandler(store, ops)
			done := make(chan struct{})
			cancels := make(chan context.CancelFunc, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				cancels <- cancel
				handler.ServeHTTP(w, r.WithContext(ctx))
				close(done)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/live/rtc_contract", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-key"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.CloseNow()
			connection.SetReadLimit(application.MaxRealtimeMessageBytes + 1024)
			cancelRequest := <-cancels
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("upstream sideband did not open")
			}
			if mode == "cancel request" {
				cancelRequest()
			}
			if mode == "oversized client" {
				_ = connection.Write(ctx, websocket.MessageBinary, []byte(strings.Repeat("x", application.MaxRealtimeMessageBytes+64)))
			}
			_, data, readErr := connection.Read(ctx)
			if readErr == nil || len(data) != 0 || strings.Contains(readErr.Error(), "synthetic-private") {
				t.Fatal("private or oversized frame escaped")
			}
			if mode != "cancel request" && websocket.CloseStatus(readErr) == websocket.StatusNormalClosure {
				t.Fatal("abnormal/oversized stream became normal close")
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("sideband tasks did not join")
			}
			logs, err := store.ListRequestLogs(context.Background(), domain.RequestLogFilter{Limit: 10})
			wantStatus := "error"
			if mode == "cancel request" {
				wantStatus = "cancelled"
			}
			if err != nil || len(logs.Requests) != 1 || logs.Requests[0].Status != wantStatus || archives.Load() != 0 {
				t.Fatalf("terminal outcome missing/wrong: logs=%+v archives=%d err=%v", logs, archives.Load(), err)
			}
			capacityCtx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			release, err := proxy.Acquire(capacityCtx)
			if err != nil {
				t.Fatalf("capacity leaked: %v", err)
			}
			release()
		})
	}
}
