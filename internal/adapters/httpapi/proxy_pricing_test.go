package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestProxyUnpricedModelAndLiveTariff(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			var calls atomic.Int32
			server, store, proxy := wireFixtureWithProxy(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				return wireComplete(fmt.Sprintf("priced_%d", calls.Add(1)), emit)
			}))
			prices := application.NewModelPricingService(store, nil)
			proxy.ResolvePrice = func(ctx context.Context, _ domain.Account, model string) (pricing.Price, error) {
				return prices.ResolveCodex(ctx, model)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			path := "/backend-api/codex/responses"
			var conn *websocket.Conn
			if transport == "websocket" {
				var err error
				conn, _, err = websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+path, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-key"}}})
				if err != nil {
					t.Fatal(err)
				}
				defer conn.CloseNow()
			}
			request := func(previous string) (int, string) {
				t.Helper()
				object := map[string]any{"model": "codex-auto-review", "input": "synthetic", "stream": true}
				if previous != "" {
					object["previous_response_id"] = previous
				}
				if conn != nil {
					object["type"] = "response.create"
				}
				body, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				if conn != nil {
					if err := conn.Write(ctx, websocket.MessageText, body); err != nil {
						t.Fatal(err)
					}
					_, data, err := conn.Read(ctx)
					if err != nil {
						t.Fatal(err)
					}
					var event struct {
						Status int `json:"status"`
					}
					if err := json.Unmarshal(data, &event); err != nil {
						t.Fatal(err)
					}
					if event.Status == 0 {
						event.Status = http.StatusOK
					}
					return event.Status, string(data)
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, strings.NewReader(string(body)))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer synthetic-key")
				res, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				data, err := io.ReadAll(res.Body)
				if err != nil {
					t.Fatal(err)
				}
				return res.StatusCode, string(data)
			}
			checkUsage := func(want int64) {
				t.Helper()
				key, err := store.GetAPIKey(ctx, "wire-key")
				if err != nil || key.Limits[0].CurrentValue != want {
					t.Fatalf("key usage: %+v, error: %v", key.Limits, err)
				}
				pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
				if err != nil || len(pending) != 0 {
					t.Fatalf("unexpected reconciliation: %d, error: %v", len(pending), err)
				}
			}
			if status, body := request(""); status != 400 || !strings.Contains(body, `"model_not_priced"`) || calls.Load() != 0 {
				t.Fatalf("unpriced request: status=%d calls=%d body=%s", status, calls.Load(), body)
			}
			checkUsage(0)

			// An explicit synthetic tariff, not an assumed official review price.
			if _, err := prices.Save(ctx, "codex-auto-review", pricing.Price{Standard: pricing.Rates{Input: 1000000, Cached: 100000, Output: 4000000}}); err != nil {
				t.Fatal(err)
			}
			if status, body := request(""); status != 200 || !strings.Contains(body, `"response.completed"`) || calls.Load() != 1 {
				t.Fatalf("live tariff did not restore request: status=%d calls=%d body=%s", status, calls.Load(), body)
			}
			checkUsage(20)
			if _, err := prices.Delete(ctx, "codex-auto-review"); err != nil {
				t.Fatal(err)
			}
			for _, previous := range []string{"", "priced_1"} {
				if status, body := request(previous); status != 400 || !strings.Contains(body, `"model_not_priced"`) || calls.Load() != 1 {
					t.Fatalf("deleted tariff became free or unavailable: status=%d calls=%d body=%s", status, calls.Load(), body)
				}
			}
			checkUsage(20)

			account, err := store.GetAccount(ctx, "wire-account")
			if err != nil {
				t.Fatal(err)
			}
			account.Status = domain.AccountPaused
			if err := store.SaveAccount(ctx, account); err != nil {
				t.Fatal(err)
			}
			wantStatus, wantCode := 503, "no_available_accounts"
			if conn != nil {
				// The same WebSocket retains its established conversation owner.
				wantStatus, wantCode = 409, "previous_response_owner_unavailable"
			}
			if status, body := request(""); status != wantStatus || !strings.Contains(body, wantCode) || calls.Load() != 1 {
				t.Fatalf("account availability error changed: status=%d calls=%d body=%s", status, calls.Load(), body)
			}
			checkUsage(20)
		})
	}
}
