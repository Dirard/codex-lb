package httpapi_test

import (
	"context"
	"encoding/json"
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

func TestCodexResponsesCompactionTriggerUsesCompactOwnerAndOneLedgerEntry(t *testing.T) {
	var compactCalls, responseCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		compactCalls.Add(1)
		if r.URL.Path != "/codex/responses" || r.Header.Get("Session_id") != "compact-session" || r.Header.Get("ChatGPT-Account-ID") != "wire-account" {
			t.Errorf("wrong compact target: path=%s session=%s account=%s", r.URL.Path, r.Header.Get("Session_id"), r.Header.Get("ChatGPT-Account-ID"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var sent map[string]json.RawMessage
		if json.Unmarshal(body, &sent) != nil || sent["include"] != nil || string(sent["stream"]) != "true" || sent["tools"] != nil || string(sent["store"]) != "false" || string(sent["prompt_cache_key"]) != `"cache-key"` || string(sent["previous_response_id"]) != `"resp-anchor"` || string(sent["conversation"]) != `"conv-anchor"` {
			t.Errorf("wrong compact wire payload: %s", body)
		}
		var input []map[string]json.RawMessage
		if json.Unmarshal(sent["input"], &input) != nil || len(input) != 4 || string(input[3]["type"]) != `"compaction_trigger"` || len(input[3]) != 1 || !strings.Contains(string(sent["input"]), "Omitted inline image bytes") || !strings.Contains(string(sent["input"]), "9007199254740993") || strings.Contains(string(sent["input"]), "data:image/png;base64") {
			t.Errorf("trigger was not canonical and terminal: %s", sent["input"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+`{"type":"response.completed","response":{"object":"response","output":[{"type":"message","content":"old text"},{"type":"compaction_summary","id":"cmp_summary","encrypted_content":"opaque"}],"usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}}`+"\n\n")
	}))
	defer upstream.Close()
	server, store, proxy := wireFixtureWithProxy(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, _ func(application.ResponseEvent) error) (application.ResponseResult, error) {
		responseCalls.Add(1)
		return application.ResponseResult{}, nil
	}))
	account, err := store.GetAccount(context.Background(), "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	account.ChatGPTAccountID = "wire-account"
	if err := store.SaveAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContinuation(context.Background(), domain.Continuation{ResponseID: "resp-anchor", KeyID: "wire-key", AccountID: account.ID, ProviderID: account.Provider, Model: "gpt-6-sol", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, domain.ContinuationBounds{MaxRecords: 10, MaxContextBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	defer adapter.Close()
	operations := application.NewCodexOperations(store, store, adapter, time.Hour)
	operations.ConfigureAdmission(proxy)
	operations.ConfigureAccountSelection(proxy)
	proxy.ConfigureCompaction(operations)
	requestBody := `{"model":"gpt-6-sol","instructions":"summarize","input":[{"role":"user","content":"hello"},{"type":"custom_tool_call","name":"view_image","call_id":"call_image","input":"{}"},{"type":"custom_tool_call_output","call_id":"call_image","counter":9007199254740993,"output":[{"type":"input_text","text":"Image Size: 1512x982."},{"type":"input_image","image_url":"data:image/png;base64,` + strings.Repeat("A", 500_000) + `"}]},{"type":"compaction_trigger","ignored":true}],"previous_response_id":"resp-anchor","conversation":"conv-anchor","promptCacheKey":"cache-key","tools":[],"include":[],"stream":true}`
	req, _ := http.NewRequest("POST", server.URL+"/backend-api/codex/responses", strings.NewReader(requestBody))
	req.Header.Set("Authorization", "Bearer synthetic-key")
	req.Header.Set("Session_id", "compact-session")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("compact response status=%d err=%v body=%s", res.StatusCode, err, body)
	}
	type compactionEvent struct {
		Type           string         `json:"type"`
		SequenceNumber int            `json:"sequence_number"`
		Item           map[string]any `json:"item"`
		Response       struct {
			ID     string           `json:"id"`
			Output []map[string]any `json:"output"`
			Usage  map[string]int   `json:"usage"`
		} `json:"response"`
	}
	var events []compactionEvent
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var event compactionEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	want := []string{"response.created", "response.output_item.added", "response.output_item.done", "response.completed"}
	if len(events) != len(want) || compactCalls.Load() != 1 || responseCalls.Load() != 0 {
		t.Fatalf("wrong dispatch/events: compact=%d responses=%d events=%d body=%s", compactCalls.Load(), responseCalls.Load(), len(events), body)
	}
	for i, event := range events {
		if event.Type != want[i] || event.SequenceNumber != i {
			t.Fatalf("wrong event %d: %+v", i, event)
		}
	}
	responseID := events[0].Response.ID
	if !strings.HasPrefix(responseID, "resp_") || events[1].Item["status"] != "in_progress" || events[2].Item["status"] != "completed" || events[3].Response.ID != responseID || len(events[3].Response.Output) != 1 || events[3].Response.Output[0]["id"] != "cmp_summary" || events[3].Response.Output[0]["encrypted_content"] != "opaque" || events[3].Response.Usage["total_tokens"] != 15 || !strings.Contains(string(body), "data: [DONE]") {
		t.Fatalf("wrong compact SSE result: %s", body)
	}
	owner, err := store.GetContinuation(context.Background(), "wire-key", responseID, time.Now())
	if err != nil || owner.AccountID != "wire-account" {
		t.Fatalf("compact owner not saved: %+v %v", owner, err)
	}
	totals, err := store.UsageTotals(context.Background(), "wire-key", "wire-account")
	if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 12 || totals.Usage.OutputTokens != 3 {
		t.Fatalf("compact usage settled more than once or incorrectly: %+v %v", totals, err)
	}
	tooLarge := `{"model":"gpt-6-sol","input":[{"role":"user","content":"` + strings.Repeat("x", 500_000) + `"},{"role":"user","content":"` + strings.Repeat("y", 500_000) + `"},{"type":"compaction_trigger"}],"stream":true}`
	req, _ = http.NewRequest("POST", server.URL+"/backend-api/codex/responses", strings.NewReader(tooLarge))
	req.Header.Set("Authorization", "Bearer synthetic-key")
	res, err = server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	invalidBody, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 400 || !strings.Contains(string(invalidBody), "responses_compact_input_too_large") || compactCalls.Load() != 1 {
		t.Fatalf("untrimmable input was admitted: status=%d calls=%d body=%s", res.StatusCode, compactCalls.Load(), invalidBody)
	}
	totals, err = store.UsageTotals(context.Background(), "wire-key", "wire-account")
	if err != nil || totals.RequestCount != 1 {
		t.Fatalf("untrimmable input reserved usage: %+v %v", totals, err)
	}
}

func TestCompactionTriggerValidationAndWebSocketForward(t *testing.T) {
	var responseCalls atomic.Int64
	server, store := wireFixture(t, wireProvider(func(_ context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		responseCalls.Add(1)
		if target.Account.Kind != domain.AccountChatGPT || !strings.Contains(string(body), `"compaction_trigger"`) {
			t.Errorf("terminal trigger left subscription Responses flow: %s", body)
		}
		return wireComplete("resp-forwarded", emit)
	}))
	for _, path := range []string{"/backend-api/codex/responses", "/v1/responses"} {
		for _, input := range []string{`[{"type":"compaction_trigger"},{"role":"user","content":"later"}]`, `[{"role":"user","content":"hi"},{"type":"compaction_trigger"},{"type":"compaction_trigger"}]`} {
			req, _ := http.NewRequest("POST", server.URL+path, strings.NewReader(`{"model":"gpt-6-sol","stream":true,"input":`+input+`}`))
			req.Header.Set("Authorization", "Bearer synthetic-key")
			res, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != 400 || !strings.Contains(string(body), `"param":"input"`) {
				t.Fatalf("malformed trigger dispatched: path=%s status=%d body=%s", path, res.StatusCode, body)
			}
		}
	}
	if responseCalls.Load() != 0 {
		t.Fatal("malformed trigger reached upstream")
	}
	valid := `{"model":"gpt-6-sol","stream":true,"input":[{"role":"user","content":"hi"},{"type":"compaction_trigger"}]}`
	req, _ := http.NewRequest("POST", server.URL+"/backend-api/codex/responses", strings.NewReader(valid))
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 || responseCalls.Load() != 0 {
		t.Fatal("compact trigger bypassed key authentication")
	}
	req, _ = http.NewRequest("POST", server.URL+"/backend-api/codex/responses", strings.NewReader(valid))
	req.Header.Set("Authorization", "Bearer synthetic-key")
	req.Header.Set(domain.RequiredCapabilityHeader, domain.TrustedCyberCapability)
	res, err = server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 400 || responseCalls.Load() != 0 {
		t.Fatal("compact trigger bypassed HTTP capability transport policy")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/backend-api/codex/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	for _, input := range []string{`[{"type":"compaction_trigger"},{"role":"user","content":"later"}]`, `[{"role":"user","content":"hi"},{"type":"compaction_trigger"}]`} {
		if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-6-sol","input":`+input+`}`)); err != nil {
			t.Fatal(err)
		}
		_, body, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(input, `"later"`) {
			if !strings.Contains(string(body), `"param":"input"`) || responseCalls.Load() != 0 {
				t.Fatalf("malformed WS trigger reached upstream: %s", body)
			}
		} else if !strings.Contains(string(body), "response.completed") || responseCalls.Load() != 1 {
			t.Fatalf("valid WS trigger did not forward once: %s", body)
		}
	}
	req, _ = http.NewRequest("POST", server.URL+"/v1/responses", strings.NewReader(valid))
	req.Header.Set("Authorization", "Bearer synthetic-key")
	res, err = server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	v1Body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(v1Body), "response.completed") || responseCalls.Load() != 2 {
		t.Fatalf("v1 trigger did not remain a normal Responses request: status=%d body=%s", res.StatusCode, v1Body)
	}
	totals, err := store.UsageTotals(context.Background(), "wire-key", "wire-account")
	if err != nil || totals.RequestCount != 2 {
		t.Fatalf("malformed requests consumed key usage: %+v %v", totals, err)
	}
}
