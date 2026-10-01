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

	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

func TestResponsesTransportPolicyOnHTTPAndWebSocketIngress(t *testing.T) {
	yes, no := true, false
	for _, test := range []struct {
		input                              string
		name, configured, policy, override string
		headers                            http.Header
		extra                              string
		wsIngress, wantWS, wantFallback    bool
		catalogPreference                  *bool
	}{
		{name: "smart single shot", policy: "smart"},
		{name: "smart explicit cache", policy: "smart", extra: `,"prompt_cache_key":"cache"`, wantWS: true, wantFallback: true},
		{name: "smart session", policy: "smart", headers: http.Header{"Session-Id": {"session"}}, wantWS: true, wantFallback: true},
		{name: "smart thread", policy: "smart", headers: http.Header{"Thread-Id": {"thread"}}, wantWS: true, wantFallback: true},
		{name: "pinned", policy: "pinned", extra: `,"prompt_cache_key":"cache"`},
		{name: "key pinned", policy: "always_websocket", override: "pinned"},
		{name: "key smart", policy: "always_websocket", override: "smart"},
		{name: "key websocket", policy: "always_http", override: "always_websocket", wantWS: true, wantFallback: true},
		{name: "explicit HTTP wins", configured: "http", policy: "always_websocket", override: "always_websocket"},
		{name: "explicit WS wins", configured: "websocket", policy: "pinned", override: "always_http", wantWS: true},
		{name: "native UA HTTP", policy: "always_websocket", headers: http.Header{"User-Agent": {"codex_cli_rs/0.1"}, "Session_id": {"session"}}},
		{name: "native Originator HTTP", policy: "always_websocket", headers: http.Header{"Originator": {"Codex Desktop"}, "Thread-Id": {"thread"}}},
		{name: "unrecognized Originator", policy: "always_websocket", headers: http.Header{"Originator": {"codex_custom"}}, wantWS: true, wantFallback: true},
		{name: "native explicit WS", configured: "websocket", policy: "pinned", headers: http.Header{"User-Agent": {"codex_cli_rs/0.1"}}, wantWS: true},
		{name: "image bypass", policy: "always_websocket", extra: `,"tools":[{"type":"image_generation"}]`},
		{name: "input image bypass", policy: "always_websocket", input: `[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]`},
		{name: "large bypass", policy: "always_websocket", extra: `,"instructions":"` + strings.Repeat("x", 14<<20) + `"`},
		{name: "catalog HTTP", policy: "always_websocket", catalogPreference: &no},
		{name: "catalog WS", policy: "always_websocket", catalogPreference: &yes, wantWS: true, wantFallback: true},
		{name: "native WS ignores HTTP policy", policy: "pinned", wsIngress: true, wantWS: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			targets := make(chan application.ResponseTarget, 1)
			server, store, proxy := wireFixtureWithProxy(t, wireProvider(func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				targets <- target
				return wireComplete("transport_response", emit)
			}))
			ctx := context.Background()
			settings, err := store.LoadSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			settings.HTTPTransportPolicy = test.policy
			if test.configured != "" {
				settings.UpstreamStreamTransport = test.configured
			}
			if err := store.SaveSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			if test.override != "" {
				key, err := store.GetAPIKey(ctx, "wire-key")
				if err != nil {
					t.Fatal(err)
				}
				key.TransportPolicyOverride = &test.override
				if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			if test.catalogPreference != nil {
				if err := store.SaveModelCatalogSnapshot(ctx, domain.ModelCatalogRecord{
					SchemaVersion: application.ModelCatalogSchemaVersion, RefreshedAt: time.Now(), ContentHash: "transport-catalog",
					Snapshot: &domain.CatalogSnapshot{
						Models:     map[string]domain.CatalogModel{"gpt-5.4": {Slug: "gpt-5.4", PreferWebsockets: *test.catalogPreference}},
						ModelPlans: map[string][]string{"gpt-5.4": {"plus"}}, ModelAccounts: map[string][]string{"gpt-5.4": {"wire-account"}},
						AccountPlans: map[string]string{"wire-account": "plus"}, AccountCatalogsComplete: true,
					},
				}); err != nil {
					t.Fatal(err)
				}
				catalog := application.NewModelCatalogService(store, nil, nil, application.ModelCatalogConfig{})
				if err := catalog.Load(ctx); err != nil {
					t.Fatal(err)
				}
				proxy.Catalog = catalog
			}
			input := test.input
			if input == "" {
				input = `"hello"`
			}
			body := `{"model":"gpt-5.4","input":` + input + `,"stream":true` + test.extra
			if test.wsIngress {
				conn := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}})
				if payload := capabilityFrame(t, conn, body+`,"type":"response.create"}`); !strings.Contains(payload, "response.completed") {
					t.Fatalf("WS request failed: %s", payload)
				}
			} else if status, payload := liteHTTPPost(t, server.URL+"/v1/responses", body+`}`, test.headers); status != 200 {
				t.Fatalf("HTTP request failed: %d %.200s", status, payload)
			}
			target := <-targets
			if target.UseWebSocket != test.wantWS || target.AllowHTTPFallback != test.wantFallback {
				t.Fatalf("transport: ws=%t fallback=%t, want %t %t", target.UseWebSocket, target.AllowHTTPFallback, test.wantWS, test.wantFallback)
			}
		})
	}
}

func TestSubscriptionAutoHandshakeFallbackIsSameOwnerAndBillingSafe(t *testing.T) {
	for _, test := range []struct {
		disconnectHandshake           bool
		afterCreateStatus             int
		name, body                    string
		status                        int
		headers                       http.Header
		explicit, disconnect, refresh bool
		wantHTTP                      int64
	}{
		{name: "upgrade", status: 426, body: `{"error":{"code":"upgrade_required"}}`, wantHTTP: 1},
		{name: "challenge header", status: 403, headers: http.Header{"Cf-Mitigated": {"challenge"}}, wantHTTP: 1},
		{name: "challenge HTML", status: 403, headers: http.Header{"Server": {"cloudflare"}, "Content-Type": {"text/html"}}, body: "<html>Just a moment</html>", wantHTTP: 1},
		{name: "generic forbidden", status: 403, body: `{"error":{"code":"forbidden"}}`},
		{name: "permission denial with edge header", status: 403, headers: http.Header{"Cf-Mitigated": {"challenge"}}, body: `{"error":{"code":"forbidden"}}`},
		{name: "HTML without provenance", status: 403, body: "<html>Just a moment</html>"},
		{name: "quota", status: 429, body: `{"error":{"code":"usage_limit_reached"}}`},
		{name: "auth refusal disguised as upgrade", status: 426, body: `{"error":{"code":"invalid_api_key"}}`},
		{name: "charged upgrade", status: 426, body: `{"usage":{"input_tokens":3,"output_tokens":2}}`},
		{name: "invalid usage", status: 426, body: `{"usage":{"input_tokens":-1,"output_tokens":2}}`},
		{name: "explicit WS", status: 426, explicit: true},
		{name: "disconnect after create", disconnect: true},
		{name: "network failure before create", disconnectHandshake: true},
		{name: "upgrade error after create", afterCreateStatus: 426},
		{name: "HTTP auth refresh keeps fallback mode", status: 426, refresh: true, wantHTTP: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var handshakes, httpCalls, creates atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("ChatGPT-Account-ID") != "wire-account" {
					t.Error("transport fallback changed account")
				}
				if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
					handshakes.Add(1)
					if test.disconnectHandshake {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						conn.Close()
						return
					}
					if test.disconnect || test.afterCreateStatus != 0 {
						conn, err := websocket.Accept(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer conn.CloseNow()
						if _, _, err := conn.Read(r.Context()); err == nil {
							creates.Add(1)
						}
						if test.afterCreateStatus != 0 {
							conn.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`{"type":"error","status":%d,"error":{"code":"upgrade_required"}}`, test.afterCreateStatus)))
						}
						return
					}
					for name, values := range test.headers {
						w.Header()[name] = values
					}
					w.WriteHeader(test.status)
					io.WriteString(w, test.body)
					return
				}
				attempt := httpCalls.Add(1)
				var body map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["client_metadata"] != nil ||
					r.Header.Get(application.ResponsesLiteHeader) != "true" || !strings.Contains(string(body["reasoning"]), "all_turns") {
					t.Error("fallback did not rebuild HTTP Responses Lite wire")
				}
				if test.refresh && attempt == 1 {
					w.WriteHeader(401)
					io.WriteString(w, `{"error":{"code":"invalid_api_key"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"fallback_response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n")
			}))
			defer upstream.Close()
			var adapter *provider.Adapter
			server, store := wireFixture(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				return adapter.Respond(ctx, target, body, emit)
			}))
			ctx := context.Background()
			account, err := store.GetAccount(ctx, "wire-account")
			if err != nil {
				t.Fatal(err)
			}
			account.ChatGPTAccountID = account.ID
			if err := store.SaveAccount(ctx, account); err != nil {
				t.Fatal(err)
			}
			settings, err := store.LoadSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			settings.HTTPTransportPolicy = "always_websocket"
			if test.explicit {
				settings.UpstreamStreamTransport = "websocket"
			}
			if err := store.SaveSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
			defer adapter.Close()
			status, payload := liteHTTPPost(t, server.URL+"/v1/responses", `{"model":"gpt-5.4","stream":true,"input":`+liteWireInput+`}`)
			if handshakes.Load() != 1 || httpCalls.Load() != test.wantHTTP || ((test.disconnect || test.afterCreateStatus != 0) && creates.Load() != 1) {
				t.Fatalf("unexpected attempts: handshake=%d HTTP=%d create=%d status=%d body=%.200s", handshakes.Load(), httpCalls.Load(), creates.Load(), status, payload)
			}
			if test.wantHTTP > 0 {
				if status != 200 || !strings.Contains(payload, "fallback_response") {
					t.Fatalf("fallback failed: %d %.200s", status, payload)
				}
				totals, err := store.UsageTotals(ctx, "wire-key", "")
				if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 3 || totals.Usage.OutputTokens != 2 {
					t.Fatalf("fallback accounted more than one model request: %+v %v", totals, err)
				}
			}
		})
	}
}
