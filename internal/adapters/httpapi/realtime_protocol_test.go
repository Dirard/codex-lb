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
	"github.com/coder/websocket"
)

func TestRealtimeProtocolRoutesAndNegotiation(t *testing.T) {
	for _, path := range []string{"/v1/live/rtc_owned", "/backend-api/codex/rtc_owned", "/v1/realtime?call_id=rtc_owned", "/v1/realtime/?call_id=rtc_owned"} {
		t.Run(path, func(t *testing.T) {
			legacy := strings.Contains(path, "/realtime")
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				wantPath := "/v1/live/rtc_owned"
				if legacy {
					wantPath = "/v1/realtime"
				}
				if r.URL.Path != wantPath || r.URL.Query().Get("intent") != "quicksilver" || r.Header.Get("OpenAI-Alpha") != "quicksilver=v2" || r.Header.Get("OpenAI-Beta") != "realtime=v1" || r.Header.Get("Authorization") != "Bearer synthetic-test-token" || r.Header.Get("Cookie") != "" {
					t.Error("realtime protocol headers/path changed")
				}
				if legacy && (len(r.URL.Query()["call_id"]) != 1 || r.URL.Query().Get("call_id") != "rtc_owned") || !legacy && len(r.URL.Query()["call_id"]) != 0 {
					t.Error("wrong call selector upstream")
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				kind, data, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				_ = conn.Write(r.Context(), kind, data)
				_ = conn.Close(websocket.StatusNormalClosure, "")
			}))
			defer upstream.Close()
			_, store, proxy := wireFixtureWithProxy(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			account, err := store.GetAccount(ctx, "wire-account")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveCodexResourceOwner(ctx, application.CodexResourceOwner{ResourceType: application.CodexResourceRealtime, ResourceID: "rtc_owned", KeyID: "wire-key", AccountID: account.ID, AccountGeneration: account.Generation, RouteRevision: account.RouteRevision, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
			defer adapter.Close()
			ops := application.NewCodexOperations(store, store, adapter, time.Hour)
			ops.ConfigureAdmission(proxy)
			handler := httpapi.NewCodexOperationsHandler(store, ops)
			server := httptest.NewServer(handler)
			defer server.Close()
			separator := "?"
			if legacy {
				separator = "&"
			}
			headers := http.Header{"Authorization": {"Bearer synthetic-key"}, "OpenAI-Alpha": {"quicksilver=v2"}, "OpenAI-Beta": {"RESPONSES = experimental, realtime=v1, responses_websockets = old"}, "Cookie": {"synthetic-private"}}
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+path+separator+"intent=quicksilver", &websocket.DialOptions{HTTPHeader: headers})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"test"}`)); err != nil {
				t.Fatal(err)
			}
			_, body, err := conn.Read(ctx)
			if err != nil || string(body) != `{"type":"test"}` {
				t.Fatal("protocol round trip failed")
			}
			_ = conn.Close(websocket.StatusNormalClosure, "")
			before := calls.Load()
			for _, invalid := range []string{"/v1/realtime", "/v1/realtime?call_id=rtc_owned&call_id=rtc_owned", "/v1/realtime?call_id=", "/v1/live/rtc_owned?call_id=other"} {
				r := httptest.NewRequest("GET", invalid, nil)
				r.Header.Set("Authorization", "Bearer synthetic-key")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != 400 {
					t.Fatalf("bad selector not rejected before upgrade: %s status=%d", invalid, w.Code)
				}
			}
			for _, name := range []string{"OpenAI-Alpha", "OpenAI-Beta"} {
				r := httptest.NewRequest("GET", "/v1/live/rtc_owned", nil)
				r.Header.Set("Authorization", "Bearer synthetic-key")
				r.Header.Set(name, strings.Repeat("x", 1025))
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != 400 {
					t.Fatal("oversized negotiation accepted")
				}
			}
			if calls.Load() != before {
				t.Fatal("invalid request dispatched")
			}
		})
	}
}
