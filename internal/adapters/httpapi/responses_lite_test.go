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

const liteWireInput = `[{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"read","parameters":{"type":"object","properties":{"file_id":{"type":"string"}}}}]},{"type":"message","role":"developer","content":"inline instructions"},{"role":"user","content":"hello"}]`

func liteHTTPPost(t *testing.T, url, body string, extra ...http.Header) (int, string) {
	t.Helper()
	r, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer synthetic-key")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(application.ResponsesLiteHeader, "untrusted-value")
	for _, headers := range extra {
		for name, values := range headers {
			r.Header[name] = values
		}
	}
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(payload)
}

func TestResponsesLiteHTTPAndCompactWire(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := fmt.Sprintf("resp_lite_http_%d", calls.Add(1))
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid upstream body")
			return
		}
		lite := strings.Contains(string(body["input"]), `"type":"additional_tools"`)
		if (r.Header.Get(application.ResponsesLiteHeader) == "true") != lite ||
			strings.Contains(strings.ToLower(string(body["client_metadata"])), application.ResponsesLiteMetadataKey) ||
			strings.Contains(string(body["reasoning"]), "all_turns") != lite ||
			string(body["instructions"]) != `"top-level instructions"` {
			t.Error("HTTP Lite signaling or instructions changed")
		}
		if lite && !strings.Contains(string(body["input"]), "inline instructions") {
			t.Error("Lite prefix was extracted from input")
		}
		if strings.Contains(string(body["input"]), `"type":"compaction_trigger"`) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"output\":[{\"type\":\"compaction\",\"encrypted_content\":\"opaque\"}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n", id)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n", id)
	}))
	defer upstream.Close()
	var adapter *provider.Adapter
	server, store, proxy := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return adapter.Respond(ctx, target, body, emit)
	}))
	adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	defer adapter.Close()
	settings, err := store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.UpstreamStreamTransport = "http"
	if err := store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	operations := application.NewCodexOperations(store, store, adapter, time.Hour)
	operations.ConfigureAdmission(proxy)
	operations.ConfigureAccountSelection(proxy)
	proxy.ConfigureCompaction(operations)
	mux := http.NewServeMux()
	httpapi.RegisterCodexOperationRoutes(mux, store, operations)
	compact := httptest.NewServer(mux)
	defer compact.Close()
	for _, route := range []struct {
		base, path string
		trigger    bool
	}{
		{server.URL, "/v1/responses", false},
		{server.URL, "/v1/responses/", false},
		{server.URL, "/backend-api/codex/responses", false},
		{server.URL, "/backend-api/codex/responses/", true},
		{compact.URL, "/v1/responses/compact", false},
		{compact.URL, "/v1/responses/compact/", false},
		{compact.URL, "/backend-api/codex/responses/compact", false},
		{compact.URL, "/backend-api/codex/responses/compact/", false},
	} {
		for _, lite := range []bool{false, true} {
			input := `[{"role":"user","content":"hello"}]`
			if lite {
				input = liteWireInput
			}
			if route.trigger {
				input = strings.TrimSuffix(input, "]") + `,{"type":"compaction_trigger"}]`
			}
			body := `{"model":"gpt-6-sol","stream":true,"instructions":"top-level instructions","input":` + input + `,"client_metadata":{"` + strings.ToUpper(application.ResponsesLiteMetadataKey) + `":"true","keep":"yes"}}`
			if status, result := liteHTTPPost(t, route.base+route.path, body); status != 200 {
				t.Fatalf("%s: %d %s", route.path, status, result)
			}
		}
	}
	before := calls.Load()
	for _, base := range []string{server.URL + "/v1/responses", compact.URL + "/v1/responses/compact"} {
		for _, invalid := range []string{`"client_metadata":[]`, `"reasoning":[]`} {
			if status, _ := liteHTTPPost(t, base, `{"model":"gpt-6-sol","input":`+liteWireInput+`,`+invalid+`}`); status != 400 || calls.Load() != before {
				t.Fatal("malformed Lite field dispatched or was not rejected")
			}
		}
	}
}

func liteSocketTurn(t *testing.T, connection *websocket.Conn, body string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := connection.Write(ctx, websocket.MessageText, []byte(body)); err != nil {
		t.Fatal(err)
	}
	for {
		_, data, err := connection.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var event struct {
			Type     string `json:"type"`
			Response struct {
				ID string `json:"id"`
			} `json:"response"`
		}
		if json.Unmarshal(data, &event) != nil || event.Type == "error" {
			t.Fatalf("WebSocket turn failed: %s", data)
		}
		if event.Type == "response.completed" {
			return event.Response.ID
		}
	}
}

func TestResponsesLiteWebSocketAcceptedPrewarmAndConnectionIsolation(t *testing.T) {
	var sequence atomic.Int64
	received := make(chan bool, 16)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(application.ResponsesLiteHeader) != "" {
			t.Error("Lite header reached WebSocket handshake")
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
			var body map[string]json.RawMessage
			_ = json.Unmarshal(raw, &body)
			var metadata map[string]string
			_ = json.Unmarshal(body["client_metadata"], &metadata)
			lite := metadata[application.ResponsesLiteMetadataKey] == "true"
			if string(body["type"]) != `"response.create"` || metadata["keep"] != "yes" || strings.Contains(string(body["reasoning"]), "all_turns") != lite {
				t.Error("invalid Lite WebSocket frame")
			}
			received <- lite
			id := fmt.Sprintf("resp_lite_ws_%d", sequence.Add(1))
			created := fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"status":"in_progress"}}`, id)
			completed := fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`, id)
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
	connection := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}, application.ResponsesLiteHeader: {"untrusted"}})
	defer connection.CloseNow()
	turn := func(c *websocket.Conn, model, previous, input string, prewarm, want bool) string {
		t.Helper()
		body := fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":%q,"input":%s,"generate":%v,"client_metadata":{"%s":"true","keep":"yes"}}`, model, previous, input, !prewarm, strings.ToUpper(application.ResponsesLiteMetadataKey))
		id := liteSocketTurn(t, c, body)
		if lite := <-received; lite != want {
			t.Fatalf("model=%s previous=%s lite=%v want=%v", model, previous, lite, want)
		}
		return id
	}
	accepted := turn(connection, "gpt-5.6", "", liteWireInput, true, true)
	turn(connection, "gpt-6-sol", accepted, `[]`, false, false)
	ordinary := turn(connection, "gpt-5.6-sol", "", `[]`, false, false)
	turn(connection, "gpt-5.6-sol", ordinary, `[]`, false, false)
	older := turn(connection, "gpt-5.6-sol", accepted, `[]`, false, true)
	// The stale anchor is reconstructed from retained additional_tools, so
	// this attempt is body-derived Lite, not trust in a stale client marker.
	latest := turn(connection, "gpt-5.6-sol", accepted, `[]`, false, true)
	turn(connection, "gpt-5.6-sol", older, `[]`, false, false)
	latest = turn(connection, "gpt-5.6-sol", latest, `[{"role":"user","content":"follow-up"}]`, false, true)
	other := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}})
	defer other.CloseNow()
	turn(other, "gpt-5.6-sol", latest, `[]`, false, false)
}

func TestResponsesLiteEnforcedModelGuardCoversResponsesAndCompact(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int64
	server, store, proxy := wireFixtureWithProxy(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return wireComplete(fmt.Sprintf("unexpected_%d", calls.Add(1)), emit)
	}))
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	model := "gpt-6-sol"
	key.EnforcedModel, key.ApplyToCodexModel = &model, true
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveModelCatalogSnapshot(ctx, domain.ModelCatalogRecord{
		SchemaVersion: application.ModelCatalogSchemaVersion, RefreshedAt: time.Now(), ContentHash: "synthetic-lite-catalog",
		Snapshot: &domain.CatalogSnapshot{
			Models: map[string]domain.CatalogModel{model: {Slug: model, Raw: map[string]json.RawMessage{"use_responses_lite": []byte("false")}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	catalog := application.NewModelCatalogService(store, nil, nil, application.ModelCatalogConfig{})
	if err := catalog.Load(ctx); err != nil {
		t.Fatal(err)
	}
	proxy.Catalog = catalog
	operations := application.NewCodexOperations(store, store, nil, time.Hour)
	operations.Catalog = catalog
	operations.ConfigureAdmission(proxy)
	operations.ConfigureAccountSelection(proxy)
	proxy.ConfigureCompaction(operations)
	mux := http.NewServeMux()
	httpapi.RegisterCodexOperationRoutes(mux, store, operations)
	compact := httptest.NewServer(mux)
	defer compact.Close()
	for _, path := range []string{"/v1/responses", "/backend-api/codex/responses", "/v1/responses/compact/", "/backend-api/codex/responses/compact"} {
		base := server.URL
		if strings.Contains(path, "compact") {
			base = compact.URL
		}
		status, result := liteHTTPPost(t, base+path, `{"model":"gpt-5.6","stream":true,"input":`+liteWireInput+`}`)
		if status != 403 || !strings.Contains(result, "responses_lite_model_mismatch") || calls.Load() != 0 {
			t.Fatalf("%s did not enforce Lite model policy: %d %s", path, status, result)
		}
	}
	status, result := liteHTTPPost(t, server.URL+"/backend-api/codex/responses", `{"model":"gpt-5.6","stream":true,"input":`+strings.TrimSuffix(liteWireInput, "]")+`,{"type":"compaction_trigger"}]}`)
	if status != 403 || !strings.Contains(result, "responses_lite_model_mismatch") {
		t.Fatalf("trigger did not enforce Lite model policy: %d %s", status, result)
	}
	totals, err := store.UsageTotals(ctx, key.ID, "")
	if err != nil || totals.RequestCount != 0 {
		t.Fatalf("Lite model refusal was billed: %+v %v", totals, err)
	}
}
