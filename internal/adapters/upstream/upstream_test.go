package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func testTarget(serverURL string, capabilities Capabilities) Target {
	return Target{
		ProviderID: "zai", AccountID: "account-1", KeyID: "key-1",
		BaseURL: serverURL + "/v1", Credential: "secret-token", Capabilities: capabilities,
	}
}

type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func collectEvents() (*eventLog, func(Event) error) {
	log := &eventLog{}
	return log, func(event Event) error {
		log.mu.Lock()
		defer log.mu.Unlock()
		log.events = append(log.events, event)
		return nil
	}
}

func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return value
}

func findOutput(t *testing.T, response []byte, kind string) map[string]any {
	t.Helper()
	var payload struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(response, &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, item := range payload.Output {
		if item["type"] == kind {
			return item
		}
	}
	t.Fatalf("output type %q not found in %s", kind, response)
	return nil
}

func TestPrepareResponsesReleasesParsedPayloadWithoutDroppingWireFields(t *testing.T) {
	body := mustJSON(map[string]any{"model": "m", "stream": true,
		"input":  []any{map[string]any{"role": "user", "content": strings.Repeat("x", 32<<10)}},
		"system": "system text", "instructions": "instruction text",
		"tools": []any{map[string]any{"type": "function", "name": "tool", "parameters": map[string]any{"type": "object"}}},
	})
	for _, transport := range []StreamTransport{TransportHTTP, TransportWebSocket} {
		capabilities := ResponsesCapabilities()
		capabilities.StreamTransport = transport
		req, items, _, err := prepareRequest(Request{Body: body}, testTarget("http://127.0.0.1", capabilities))
		if err != nil {
			t.Fatal(err)
		}
		if req.Input != nil || req.Tools != nil || req.System != "" || req.Instructions != "" || items != nil {
			t.Fatal("Responses retained a second parsed payload")
		}
		wire, err := passthroughBody(req, capabilities)
		if err != nil || string(wire) != string(body) || &wire[0] != &body[0] {
			t.Fatalf("required wire fields changed or were copied: %v", err)
		}
	}
}

func TestPrepareChatRequestReusesCallerWireAndParsedInput(t *testing.T) {
	body := json.RawMessage(`{"model":"m","stream":true,"unknown":true,"input":[{"role":"user","content":"Привет 🌊"}]}`)
	before := string(body)
	capabilities := ChatCompletionsCapabilities()
	target := testTarget("http://127.0.0.1", capabilities)
	req, items, catalog, err := prepareRequest(Request{Body: body}, target)
	if err != nil {
		t.Fatal(err)
	}
	if &req.Raw[0] != &body[0] {
		t.Fatal("Responses wire was copied before translation")
	}
	chat, messages, err := translateToChat(req, items, Continuation{}, catalog, capabilities, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || !strings.Contains(string(messages[0].Content), "Привет 🌊") ||
		!chat.Stream || string(body) != before {
		t.Fatalf("translation changed semantics: messages=%#v body=%s", messages, body)
	}
}

func TestExecuteChatTranslatesToolsReasoningImageAndFollowUp(t *testing.T) {
	requests := make(chan []byte, 2)
	responses := []string{
		`{"choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"thinking","tool_calls":[
			{"id":"call_custom","type":"function","function":{"name":"apply_patch","arguments":"{\"patch\":\"alpha\"}"}},
			{"id":"call_ns","type":"function","function":{"name":"mcp-git-get","arguments":"{\"ref\":\"main\"}"}}
		]}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":4}}}`,
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"done"}}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Error("credential was not sent as bearer token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responses[len(requests)-1]))
	}))
	defer server.Close()

	capabilities := ZAIChatCompletionsCapabilities()
	capabilities.ImageInput = true
	capabilities.ModelMappings = map[string]string{"codex-model": "glm-5.2"}
	capabilities.ExtraBody = map[string]json.RawMessage{"provider_option": json.RawMessage(`"safe"`)}
	adapter := New(server.Client(), NewContinuationStore())
	first := mustJSON(map[string]any{
		"model":        "codex-model",
		"instructions": "system prompt",
		"input": []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "look"},
				map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAA"},
			},
		}},
		"tools": []any{
			map[string]any{"type": "custom", "name": "apply_patch"},
			map[string]any{"type": "namespace", "name": "mcp-git", "tools": []any{
				map[string]any{"type": "function", "name": "get", "parameters": map[string]string{"type": "object"}},
			}},
		},
		"parallel_tool_calls": true,
		"reasoning":           map[string]string{"effort": "high"},
		"stream":              false,
	})
	result, err := adapter.Execute(context.Background(), testTarget(server.URL, capabilities), Request{Body: first})
	if err != nil {
		t.Fatal(err)
	}
	var requestBody map[string]any
	if err := json.Unmarshal(<-requests, &requestBody); err != nil {
		t.Fatal(err)
	}
	if requestBody["model"] != "glm-5.2" || !reflect.DeepEqual(requestBody["thinking"], map[string]any{"type": "enabled"}) ||
		requestBody["reasoning_effort"] != "high" || requestBody["provider_option"] != "safe" {
		t.Fatalf("request customization was not applied: %s", requestBody)
	}
	messages := requestBody["messages"].([]any)
	if messages[0].(map[string]any)["role"] != "system" || len(messages) != 2 {
		t.Fatalf("unexpected messages: %v", messages)
	}
	secondMessage := messages[1].(map[string]any)
	if secondMessage["role"] != "user" {
		t.Fatalf("second message role: %v", secondMessage)
	}
	parts := secondMessage["content"].([]any)
	if parts[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("image input was not translated: %v", parts)
	}

	custom := findOutput(t, result.Response, "custom_tool_call")
	namespaced := findOutput(t, result.Response, "function_call")
	if custom["input"] != "alpha" || namespaced["namespace"] != "mcp-git" || namespaced["name"] != "get" {
		t.Fatalf("tool outputs were not decoded: %v %v", custom, namespaced)
	}
	if result.Usage.CachedTokens != 4 || result.Usage.TotalTokens != 15 {
		t.Fatalf("usage was not preserved: %+v", result.Usage)
	}

	second := mustJSON(map[string]any{
		"model": "codex-model", "previous_response_id": result.ResponseID,
		"input": []any{
			map[string]any{"type": "custom_tool_call_output", "call_id": custom["call_id"], "output": "patch result"},
			map[string]any{"type": "function_call_output", "call_id": namespaced["call_id"], "output": map[string]any{"ok": true}},
		},
		"tools": []any{
			map[string]any{"type": "custom", "name": "apply_patch"},
			map[string]any{"type": "namespace", "name": "mcp-git", "tools": []any{
				map[string]any{"type": "function", "name": "get", "parameters": map[string]string{"type": "object"}},
			}},
		},
		"stream": false,
	})
	if _, err = adapter.Execute(context.Background(), testTarget(server.URL, capabilities), Request{Body: second}); err != nil {
		t.Fatal(err)
	}
	var followUp map[string]any
	if err := json.Unmarshal(<-requests, &followUp); err != nil {
		t.Fatal(err)
	}
	roles := make([]string, 0)
	for _, raw := range followUp["messages"].([]any) {
		message := raw.(map[string]any)
		roles = append(roles, message["role"].(string))
		if message["role"] == "assistant" && len(message["tool_calls"].(any).([]any)) != 2 {
			t.Fatalf("assistant parallel tool calls were not retained: %v", message)
		}
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool,tool" {
		t.Fatalf("follow-up roles: %v", roles)
	}
}

func TestOpenStreamChatEmitsReasoningTextParallelToolsAndUsage(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = decodeBody(t, raw)
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(value any) {
			encoded, _ := json.Marshal(value)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		}
		send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"reasoning_content": "think"}}}})
		send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "hello "}}}})
		send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{
			map[string]any{"index": 0, "id": "call_1", "function": map[string]any{"name": "shell", "arguments": `{"cmd":`}},
		}}}}})
		send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{
			map[string]any{"index": 1, "id": "call_2", "function": map[string]any{"name": "shell", "arguments": "{}"}},
		}}}}})
		send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}})
		send(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10}})
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	request := mustJSON(map[string]any{
		"model": "glm-5.2", "input": "hi", "stream": true,
		"tools": []any{map[string]any{"type": "function", "name": "shell", "parameters": map[string]string{"type": "object"}}},
	})
	log, emit := collectEvents()
	adapter := New(server.Client(), NewContinuationStore())
	result, err := adapter.OpenStream(context.Background(), testTarget(server.URL, ZAIChatCompletionsCapabilities()), Request{Body: request}, emit)
	if err != nil {
		t.Fatal(err)
	}
	log.mu.Lock()
	types := make([]string, len(log.events))
	for i, event := range log.events {
		types[i] = event.Type
	}
	log.mu.Unlock()
	for _, required := range []string{"response.created", "response.reasoning_text.delta", "response.output_text.delta", "response.function_call_arguments.delta", "response.completed"} {
		found := false
		for _, kind := range types {
			if kind == required {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing event %q in %v", required, types)
		}
	}
	if result.Usage.TotalTokens != 10 || body["stream"] != true || !reflect.DeepEqual(body["stream_options"], map[string]any{"include_usage": true}) {
		t.Fatalf("stream contract or usage was not preserved: %+v %+v", result, body)
	}
	terminal := findOutput(t, result.Response, "function_call")
	if terminal["call_id"] != "call_1" || terminal["arguments"] != `{"cmd":` {
		t.Fatalf("parallel tool call was not finalized correctly: %v", terminal)
	}
	log.mu.Lock()
	eventItemID := ""
	for _, event := range log.events {
		if event.Type != "response.output_item.done" {
			continue
		}
		var payload struct {
			Item struct {
				Type   string `json:"type"`
				ID     string `json:"id"`
				CallID string `json:"call_id"`
			} `json:"item"`
		}
		_ = json.Unmarshal(event.Data, &payload)
		if payload.Item.Type == "function_call" && payload.Item.CallID == "call_1" {
			eventItemID = payload.Item.ID
		}
	}
	log.mu.Unlock()
	if eventItemID == "" || eventItemID != terminal["id"] {
		t.Fatalf("terminal output item ID differs from emitted item: %q != %v", eventItemID, terminal["id"])
	}
	if adapter.store.Len() != 1 {
		t.Fatal("successful stream was not retained for continuation")
	}
}

func TestOpenStreamDisconnectDoesNotCreateSuccessOrContinuation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"))
	}))
	defer server.Close()
	adapter := New(server.Client(), NewContinuationStore())
	log, emit := collectEvents()
	_, err := adapter.OpenStream(context.Background(), testTarget(server.URL, ChatCompletionsCapabilities()), Request{
		Body: mustJSON(map[string]any{"model": "m", "input": "hi", "stream": true}),
	}, emit)
	var upstreamErr *Error
	if !errors.As(err, &upstreamErr) || upstreamErr.Code != ErrorCodeStreamIncomplete {
		t.Fatalf("expected stream_incomplete, got %#v", err)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	for _, event := range log.events {
		if event.Type == "response.completed" {
			t.Fatal("disconnect was reported as completed")
		}
	}
	if adapter.store.Len() != 0 {
		t.Fatal("partial stream was retained as continuation state")
	}
}

func TestOpenStreamCancellationReturnsContextError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	adapter := New(server.Client(), NewContinuationStore())
	_, err := adapter.OpenStream(ctx, testTarget(server.URL, ChatCompletionsCapabilities()), Request{
		Body: mustJSON(map[string]any{"model": "m", "input": "hi", "stream": true}),
	}, func(Event) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline, got %#v", err)
	}
}

func TestResponsesPassthroughForwardAndTerminalUsage(t *testing.T) {
	var body []byte
	var sawHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		sawHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_up\",\"status\":\"in_progress\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_up\",\"status\":\"completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n"))
	}))
	defer server.Close()
	request := mustJSON(map[string]any{
		"model": "native", "input": "hi", "stream": true,
		"text":    map[string]any{"format": map[string]any{"type": "text"}},
		"include": []string{"reasoning.encrypted_content"},
		"store":   false, "service_tier": "auto",
		"metadata":              map[string]string{"user_id": "u"},
		"future_response_field": map[string]bool{"enabled": true},
	})
	log, emit := collectEvents()
	adapter := New(server.Client(), NewContinuationStore())
	target := testTarget(server.URL, ResponsesCapabilities())
	target.Headers = http.Header{
		"Chatgpt-Account-Id": []string{"acct"},
		"X-Codex-Turn-State": []string{"turn"},
		"Session_id":         []string{"scoped-session"},
		"Authorization":      []string{"must-not-copy"},
		"Cookie":             []string{"must-not-copy"},
	}
	result, err := adapter.OpenStream(context.Background(), target, Request{Body: request}, emit)
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponseID != "resp_up" || result.Usage.TotalTokens != 3 || result.Failed {
		t.Fatalf("passthrough terminal result: %+v", result)
	}
	if string(body) != string(request) {
		t.Fatalf("Responses body was changed: %s", body)
	}
	if sawHeaders.Get("ChatGPT-Account-ID") != "acct" || sawHeaders.Get("X-Codex-Turn-State") != "turn" ||
		sawHeaders.Get("Session_id") != "scoped-session" ||
		sawHeaders.Get("Authorization") != "Bearer secret-token" || sawHeaders.Get("Cookie") != "" {
		t.Fatalf("explicit upstream header allowlist was not enforced: %+v", sawHeaders)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.events) != 2 || log.events[0].Type != "response.created" || log.events[1].Type != "response.completed" {
		t.Fatalf("unexpected passthrough events: %+v", log.events)
	}
}

func TestResponsesWebSocketUsesResponseCreateAndStopsAtTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer connection.Close(websocket.StatusNormalClosure, "")
		_, create, err := connection.Read(context.Background())
		if err != nil {
			t.Errorf("read response.create: %v", err)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(create, &payload); err != nil || payload["type"] != "response.create" ||
			payload["stream"] != nil || payload["background"] != nil || payload["custom"] != "kept" {
			t.Fatalf("invalid response.create payload: %s", create)
		}
		if r.Header.Get("ChatGPT-Account-ID") != "acct" {
			t.Fatalf("allowed header was not sent: %+v", r.Header)
		}
		_ = connection.Write(context.Background(), websocket.MessageText, mustJSON(map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp_ws", "status": "in_progress"},
		}))
		_ = connection.Write(context.Background(), websocket.MessageText, mustJSON(map[string]any{
			"type": "response.completed", "response": map[string]any{
				"id": "resp_ws", "status": "completed",
				"usage": map[string]uint{"input_tokens": 1, "output_tokens": 2, "total_tokens": 3},
			},
		}))
	}))
	defer server.Close()
	capabilities := ResponsesCapabilities()
	capabilities.StreamTransport = TransportWebSocket
	target := testTarget(server.URL, capabilities)
	target.Headers = http.Header{"Chatgpt-Account-Id": []string{"acct"}}
	log, emit := collectEvents()
	result, err := New(server.Client(), NewContinuationStore()).OpenStream(
		context.Background(), target,
		Request{Body: mustJSON(map[string]any{"model": "native", "input": "hi", "stream": true, "custom": "kept"})},
		emit,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponseID != "resp_ws" || result.Usage.TotalTokens != 3 || result.Failed {
		t.Fatalf("WebSocket terminal result: %+v", result)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.events) != 2 || log.events[1].Type != "response.completed" {
		t.Fatalf("unexpected WebSocket events: %+v", log.events)
	}
}

func TestQuotaHTTPErrorCodeIsNotCollapsedToRateLimit(t *testing.T) {
	for _, code := range []string{"1308", "insufficient_quota", "usage_limit_reached"} {
		body := fmt.Sprintf(`{"error":{"code":%q,"message":"quota"}}`, code)
		err := upstreamHTTPError(http.StatusTooManyRequests, []byte(body))
		if err.Code != ErrorCodeInsufficientQuota {
			t.Fatalf("quota code %q became %q", code, err.Code)
		}
	}
	err := upstreamHTTPError(http.StatusTooManyRequests, []byte(`{"error":{"code":"temporarily_busy"}}`))
	if err.Code != ErrorCodeRateLimited {
		t.Fatalf("temporary 429 became %q", err.Code)
	}
}

func TestUnsupportedCapabilitiesAndContinuationNamespace(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	adapter := New(server.Client(), NewContinuationStoreWithLimits(2, 1024*1024, time.Minute))

	_, err := adapter.Execute(context.Background(), testTarget(server.URL, ChatCompletionsCapabilities()), Request{
		Body: mustJSON(map[string]any{"model": "m", "input": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAA"}}}}, "stream": false}),
	})
	var upstreamErr *Error
	if !errors.As(err, &upstreamErr) || upstreamErr.Code != ErrorCodeUnsupportedCapability || calls != 0 {
		t.Fatalf("image capability was not rejected before dispatch: %#v calls=%d", err, calls)
	}
	_, err = adapter.Execute(context.Background(), testTarget(server.URL, ChatCompletionsCapabilities()), Request{
		Body: mustJSON(map[string]any{"model": "m", "input": "hi", "tools": []any{map[string]string{"type": "web_search"}}, "stream": false}),
	})
	if !errors.As(err, &upstreamErr) || upstreamErr.Code != ErrorCodeUnsupportedCapability || calls != 0 {
		t.Fatalf("hosted tool was not rejected before dispatch: %#v calls=%d", err, calls)
	}
	_, err = adapter.Execute(context.Background(), testTarget(server.URL, ChatCompletionsCapabilities()), Request{
		Body: mustJSON(map[string]any{"model": "m", "previous_response_id": "missing", "input": "hi", "stream": false}),
	})
	if !errors.As(err, &upstreamErr) || upstreamErr.Code != ErrorCodeContinuationNotFound || calls != 0 {
		t.Fatalf("missing continuation was silently recreated: %#v calls=%d", err, calls)
	}

	first := Owner{ProviderID: "p", AccountID: "a", KeyID: "k"}
	second := first
	second.AccountID = "other"
	if err := adapter.store.Save(first, "resp_same", Continuation{Messages: []ChatMessage{{Role: "user", Content: mustJSON("one")}}}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.store.Save(second, "resp_same", Continuation{Messages: []ChatMessage{{Role: "user", Content: mustJSON("two")}}}); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := adapter.store.Load(first, "resp_same")
	if err != nil || !ok || chatText(loaded.Messages[0].Content) != "one" {
		t.Fatalf("continuation namespace leaked: %+v %v %+v", loaded, ok, err)
	}
}

func TestSafeCustomizationCannotOverrideProtocolFields(t *testing.T) {
	capabilities := ChatCompletionsCapabilities()
	capabilities.ExtraBody = map[string]json.RawMessage{"messages": json.RawMessage(`[]`)}
	_, err := New(nil, nil).Execute(context.Background(), Target{
		ProviderID: "p", AccountID: "a", KeyID: "k", BaseURL: "http://127.0.0.1:1", Capabilities: capabilities,
	}, Request{Body: mustJSON(map[string]any{"model": "m", "input": "hi"})})
	var upstreamErr *Error
	if !errors.As(err, &upstreamErr) || upstreamErr.Code != ErrorCodeInvalidConfiguration {
		t.Fatalf("unsafe customization was accepted: %#v", err)
	}
}

func TestContinuationBoundedLRU(t *testing.T) {
	store := NewContinuationStoreWithLimits(2, 1024, time.Minute)
	owner := Owner{ProviderID: "p", AccountID: "a", KeyID: "k"}
	for _, id := range []string{"one", "two", "three"} {
		message := ChatMessage{Role: "user", Content: mustJSON(id)}
		if err := store.Save(owner, id, Continuation{Messages: []ChatMessage{message}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, _ := store.Load(owner, "one"); ok || store.Len() != 2 {
		t.Fatalf("bounded LRU failed: ok=%v len=%d", ok, store.Len())
	}
	if _, ok, _ := store.Load(owner, "three"); !ok {
		t.Fatal("newest continuation was evicted")
	}
}
