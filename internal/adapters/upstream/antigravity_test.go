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
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type antigravityCall struct {
	path, query string
	header      http.Header
	body        map[string]any
}

// antigravityServer is a loopback Cloud Code stub answering requests in order.
func antigravityServer(t *testing.T, answers ...func(http.ResponseWriter)) (*httptest.Server, <-chan antigravityCall) {
	t.Helper()
	calls := make(chan antigravityCall, len(answers)+1)
	var next atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		calls <- antigravityCall{path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone(), body: body}
		index := int(next.Add(1)) - 1
		if index >= len(answers) {
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		answers[index](w)
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func sseAnswer(frames ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range frames {
			_, _ = fmt.Fprintf(w, "%s\n\n", frame)
		}
	}
}

func jsonAnswer(status int, header http.Header, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		for name, values := range header {
			w.Header()[name] = values
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func frame(parts string, usage string) string {
	payload := `{"response":{"candidates":[{"content":{"role":"model","parts":[` + parts + `]}}]`
	if usage != "" {
		payload += `,"usageMetadata":` + usage
	}
	return "data: " + payload + `,"modelVersion":"gemini-3.8-flash","responseId":"r1"},"traceId":"trace"}`
}

func antigravityStreamRequest(input any, extra map[string]any) Request {
	body := map[string]any{"model": "gemini-3.8-flash-high", "input": input, "stream": true, "tools": []any{shellTool()}}
	for key, value := range extra {
		body[key] = value
	}
	return Request{Body: mustJSON(body)}
}

func eventTypes(events []Event) []string {
	types := make([]string, len(events))
	for i, event := range events {
		types[i] = event.Type
	}
	return types
}

func receiveCall(t *testing.T, calls <-chan antigravityCall) antigravityCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("no request reached the stub")
		return antigravityCall{}
	}
}

func TestAntigravityStreamTranslatesTextThoughtsAndSignedCall(t *testing.T) {
	server, calls := antigravityServer(t, sseAnswer(
		frame(`{"text":"Let me think","thought":true}`, `{"promptTokenCount":100,"totalTokenCount":100}`),
		frame(`{"text":"Listing files."}`, ""),
		// A frame split over two data lines is joined before decoding.
		"data: "+`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig-call",`+"\n"+
			`data: "functionCall":{"id":"call_9","name":"shell","args":{"cmd":["ls"]}}}]},"finishReason":"STOP"}],`+
			`"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":7,"cachedContentTokenCount":40,"totalTokenCount":127}},"traceId":"trace"}`,
	))
	adapter := New(server.Client(), NewContinuationStore())
	defer adapter.Close()
	target := antigravityTarget(server.URL)
	log, emit := collectEvents()
	result, err := adapter.OpenStream(context.Background(), target, antigravityStreamRequest("list files", nil), emit)
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{InputTokens: 100, OutputTokens: 27, TotalTokens: 127, CachedTokens: 40, ReasoningTokens: 7, ReasoningTokensKnown: true}
	if !result.UsageKnown || result.Usage != want || result.Failed {
		t.Fatalf("result = %+v", result)
	}
	if got := eventTypes(log.events); !reflect.DeepEqual(got, []string{
		"response.created",
		"response.output_item.added", "response.reasoning_text.delta",
		"response.output_item.added", "response.output_text.delta",
		"response.output_item.added", "response.function_call_arguments.delta",
		"response.output_item.done", "response.output_item.done", "response.output_item.done",
		"response.completed",
	}) {
		t.Fatalf("events = %v", got)
	}
	call := findOutput(t, result.Response, "function_call")
	if call["call_id"] != "call_9" || call["name"] != "shell" || call["arguments"] != `{"cmd":["ls"]}` {
		t.Fatalf("function call = %v", call)
	}
	if reasoning := findOutput(t, result.Response, "reasoning"); !strings.Contains(string(mustJSON(reasoning)), "Let me think") {
		t.Fatalf("reasoning = %v", reasoning)
	}

	request := receiveCall(t, calls)
	if request.path != "/v1internal:streamGenerateContent" || request.query != "alt=sse" {
		t.Fatalf("endpoint = %s?%s", request.path, request.query)
	}
	names := make([]string, 0, len(request.header))
	for name := range request.header {
		names = append(names, name)
	}
	sort.Strings(names)
	// Accept-Encoding and Content-Length come from Go's transport.
	if !reflect.DeepEqual(names, []string{"Accept-Encoding", "Authorization", "Content-Length", "Content-Type", "User-Agent"}) {
		t.Fatalf("headers = %v", names)
	}
	if request.header.Get("Authorization") != "Bearer access-token" || request.header.Get("Content-Type") != "application/json" ||
		request.header.Get("User-Agent") != target.AntigravityUserAgent {
		t.Fatalf("header values = %v", request.header)
	}
	if request.body["project"] != "aicode-consumers" || request.body["model"] != "gemini-3.8-flash-high" {
		t.Fatalf("envelope = %v", request.body)
	}

	stored, found, err := adapter.store.Load(ownerOf(target), result.ResponseID)
	if err != nil || !found || stored.Signatures[antigravityCallSignatureKey("call_9")] != "sig-call" {
		t.Fatalf("continuation signatures = %v found=%v err=%v", stored.Signatures, found, err)
	}
}

func TestAntigravityStreamCompletesWithoutFinishReasonAndWithLateUsage(t *testing.T) {
	server, _ := antigravityServer(t, sseAnswer(
		frame(`{"text":"Hi"}`, ""),
		frame(`{"text":""}`, `{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}`),
	))
	adapter := New(server.Client(), NewContinuationStore())
	defer adapter.Close()
	log, emit := collectEvents()
	result, err := adapter.OpenStream(context.Background(), antigravityTarget(server.URL), antigravityStreamRequest("hello", nil), emit)
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage != (Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7, ReasoningTokensKnown: true}) {
		t.Fatalf("usage = %+v", result.Usage)
	}
	message := findOutput(t, result.Response, "message")
	if !strings.Contains(string(mustJSON(message)), `"text":"Hi"`) {
		t.Fatalf("message = %v", message)
	}
	if types := eventTypes(log.events); types[len(types)-1] != "response.completed" {
		t.Fatalf("events = %v", types)
	}
}

func TestAntigravityStreamWithoutUsageFails(t *testing.T) {
	server, _ := antigravityServer(t, sseAnswer(frame(`{"text":"Hi"}`, "")))
	adapter := New(server.Client(), NewContinuationStore())
	defer adapter.Close()
	log, emit := collectEvents()
	_, err := adapter.OpenStream(context.Background(), antigravityTarget(server.URL), antigravityStreamRequest("hello", nil), emit)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != ErrorCodeMissingUsage {
		t.Fatalf("err = %v", err)
	}
	if types := eventTypes(log.events); types[len(types)-1] != "response.failed" || adapter.store.Len() != 0 {
		t.Fatalf("events = %v, stored = %d", types, adapter.store.Len())
	}
}

func TestAntigravityStreamClassifiesErrorFrame(t *testing.T) {
	server, _ := antigravityServer(t, sseAnswer(
		frame(`{"text":"partial"}`, ""),
		`data: {"error":{"code":429,"message":"Quota exhausted","status":"RESOURCE_EXHAUSTED","details":[`+
			`{"@type":"`+errorInfoType+`","reason":"QUOTA_EXHAUSTED"},{"@type":"`+retryInfoType+`","retryDelay":"60s"}]}}`,
	))
	adapter := New(server.Client(), NewContinuationStore())
	defer adapter.Close()
	log, emit := collectEvents()
	result, err := adapter.OpenStream(context.Background(), antigravityTarget(server.URL), antigravityStreamRequest("hello", nil), emit)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != ErrorCodeInsufficientQuota || failure.Status != 429 ||
		failure.RetryAfter != time.Minute || failure.RejectedBeforeExecution {
		t.Fatalf("err = %#v", err)
	}
	if !result.Failed || result.ErrorCode != ErrorCodeInsufficientQuota {
		t.Fatalf("result = %+v", result)
	}
	if types := eventTypes(log.events); types[len(types)-1] != "response.failed" || adapter.store.Len() != 0 {
		t.Fatalf("events = %v, stored = %d", types, adapter.store.Len())
	}
}

func TestAntigravityHTTPRefusalsAreClassifiedBeforeExecution(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		header     http.Header
		body       string
		code       string
		retryAfter time.Duration
		actionURL  string
	}{
		{"quota", 429, nil, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"` + errorInfoType + `","reason":"QUOTA_EXHAUSTED","metadata":{"quotaResetDelay":"90s"}}]}}`,
			ErrorCodeInsufficientQuota, 90 * time.Second, ""},
		{"rate limit", 429, http.Header{"Retry-After": {"3"}}, `{"error":{"code":429,"details":[{"@type":"` + errorInfoType + `","reason":"RATE_LIMIT_EXCEEDED"}]}}`,
			ErrorCodeRateLimited, 3 * time.Second, ""},
		{"content", 429, nil, `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`,
			ErrorCodeContentRefused, 0, ""},
		{"capacity", 503, nil, `{"error":{"code":503,"status":"UNAVAILABLE","details":[{"@type":"` + errorInfoType + `","reason":"MODEL_CAPACITY_EXHAUSTED"},{"@type":"` + retryInfoType + `","retryDelay":"2s"}]}}`,
			ErrorCodeCapacityExhausted, 2 * time.Second, ""},
		{"suspended", 403, nil, `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"` + errorInfoType + `","reason":"TOS_VIOLATION","metadata":{"appeal_url":"https://forms.gle/appeal"}}]}}`,
			ErrorCodeAccountSuspended, 0, "https://forms.gle/appeal"},
		{"validation", 403, nil, `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"` + errorInfoType + `","reason":"VALIDATION_REQUIRED","metadata":{"validation_url":"https://accounts.google.com/v"}}]}}`,
			ErrorCodeAccountValidationRequired, 0, "https://accounts.google.com/v"},
		{"model access", 403, nil, `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"` + errorInfoType + `","reason":"MODEL_ACCESS_DENIED"}]}}`,
			ErrorCodeModelAccessDenied, 0, ""},
		{"unauthenticated", 401, nil, `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`, "http_401", 0, ""},
	}
	for _, tc := range cases {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s stream=%v", tc.name, stream), func(t *testing.T) {
				server, calls := antigravityServer(t, jsonAnswer(tc.status, tc.header, tc.body))
				adapter := New(server.Client(), NewContinuationStore())
				defer adapter.Close()
				request := antigravityStreamRequest("hello", map[string]any{"stream": stream})
				log, emit := collectEvents()
				var result Result
				var err error
				if stream {
					result, err = adapter.OpenStream(context.Background(), antigravityTarget(server.URL), request, emit)
				} else {
					result, err = adapter.Execute(context.Background(), antigravityTarget(server.URL), request)
				}
				var failure *Error
				if !errors.As(err, &failure) || failure.Code != tc.code || failure.Status != tc.status ||
					failure.RetryAfter != tc.retryAfter || failure.ActionURL != tc.actionURL || !failure.RejectedBeforeExecution {
					t.Fatalf("err = %#v", err)
				}
				if !result.Failed || result.ErrorCode != tc.code || result.UsageReported || len(result.Response) != 0 {
					t.Fatalf("result = %+v", result)
				}
				for _, event := range log.events {
					if event.Type != "response.failed" {
						t.Fatalf("a refusal emitted %s", event.Type)
					}
				}
				wantPath := "/v1internal:generateContent"
				if stream {
					wantPath = "/v1internal:streamGenerateContent"
				}
				if call := receiveCall(t, calls); call.path != wantPath {
					t.Fatalf("path = %s", call.path)
				}
			})
		}
	}
}

func TestAntigravityExecuteTranslatesGenerateContent(t *testing.T) {
	server, calls := antigravityServer(t, jsonAnswer(http.StatusOK, nil, `{"response":{"candidates":[{"content":{"role":"model","parts":[
		{"text":"pondering","thought":true},{"text":"Answer","thoughtSignature":"sig-text"}]},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":3,"thoughtsTokenCount":4,"totalTokenCount":16}},"traceId":"t"}`))
	adapter := New(server.Client(), NewContinuationStore())
	defer adapter.Close()
	target := antigravityTarget(server.URL)
	result, err := adapter.Execute(context.Background(), target, antigravityStreamRequest("question", map[string]any{"stream": false}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage != (Usage{InputTokens: 9, OutputTokens: 7, TotalTokens: 16, ReasoningTokens: 4, ReasoningTokensKnown: true}) {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if message := findOutput(t, result.Response, "message"); !strings.Contains(string(mustJSON(message)), `"text":"Answer"`) {
		t.Fatalf("message = %v", message)
	}
	findOutput(t, result.Response, "reasoning")
	if call := receiveCall(t, calls); call.path != "/v1internal:generateContent" || call.query != "" {
		t.Fatalf("endpoint = %s?%s", call.path, call.query)
	}
	stored, _, _ := adapter.store.Load(ownerOf(target), result.ResponseID)
	if len(stored.Signatures) != 1 {
		t.Fatalf("turn signature was not stored: %v", stored.Signatures)
	}
}

// R6: signatures come back on the same part through previous_response_id and
// through a stateless full replay; unknown Gemini calls get the placeholder
// and Claude calls never get an invented signature.
func TestAntigravitySignaturesReplayAcrossRequests(t *testing.T) {
	usage := `{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12}`
	server, calls := antigravityServer(t,
		sseAnswer(frame(`{"functionCall":{"id":"call_9","name":"shell","args":{"cmd":["ls"]}},"thoughtSignature":"sig-call"}`, usage)),
		sseAnswer(frame(`{"text":"done"}`, ""), frame(`{"text":"","thoughtSignature":"sig-text"}`, usage)),
		sseAnswer(frame(`{"text":"ok"}`, usage)),
		sseAnswer(frame(`{"text":"ok"}`, usage)),
	)
	store := NewContinuationStore()
	adapter := New(server.Client(), store)
	defer adapter.Close()
	target := antigravityTarget(server.URL)
	_, emit := collectEvents()

	first, err := adapter.OpenStream(context.Background(), target, antigravityStreamRequest("list files", nil), emit)
	if err != nil {
		t.Fatal(err)
	}
	receiveCall(t, calls)
	if _, err := adapter.OpenStream(context.Background(), target, antigravityStreamRequest([]any{
		map[string]any{"type": "function_call_output", "call_id": "call_9", "output": "a.txt"},
	}, map[string]any{"previous_response_id": first.ResponseID}), emit); err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, "continued contents", receiveCall(t, calls).body["request"].(map[string]any)["contents"], `[
		{"role":"user","parts":[{"text":"list files"}]},
		{"role":"model","parts":[{"functionCall":{"id":"call_9","name":"shell","args":{"cmd":["ls"]}},"thoughtSignature":"sig-call"}]},
		{"role":"user","parts":[{"functionResponse":{"id":"call_9","name":"shell","response":{"output":"a.txt"}}}]}
	]`)

	if _, err := adapter.OpenStream(context.Background(), target, antigravityStreamRequest([]any{
		map[string]any{"role": "user", "content": "list files"},
		map[string]any{"type": "function_call", "call_id": "call_9", "name": "shell", "arguments": `{"cmd":["ls"]}`},
		map[string]any{"type": "function_call_output", "call_id": "call_9", "output": "a.txt"},
		map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "done"}}},
		map[string]any{"role": "user", "content": "next"},
		map[string]any{"type": "function_call", "call_id": "call_x", "name": "shell", "arguments": `{}`},
		map[string]any{"type": "function_call_output", "call_id": "call_x", "output": "b"},
	}, nil), emit); err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, "replayed contents", receiveCall(t, calls).body["request"].(map[string]any)["contents"], `[
		{"role":"user","parts":[{"text":"list files"}]},
		{"role":"model","parts":[{"functionCall":{"id":"call_9","name":"shell","args":{"cmd":["ls"]}},"thoughtSignature":"sig-call"}]},
		{"role":"user","parts":[{"functionResponse":{"id":"call_9","name":"shell","response":{"output":"a.txt"}}}]},
		{"role":"model","parts":[{"text":"done","thoughtSignature":"sig-text"}]},
		{"role":"user","parts":[{"text":"next"}]},
		{"role":"model","parts":[{"functionCall":{"id":"call_x","name":"shell","args":{}},"thoughtSignature":"skip_thought_signature_validator"}]},
		{"role":"user","parts":[{"functionResponse":{"id":"call_x","name":"shell","response":{"output":"b"}}}]}
	]`)

	if _, err := adapter.OpenStream(context.Background(), target, antigravityStreamRequest([]any{
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"type": "function_call", "call_id": "call_y", "name": "shell", "arguments": `{}`},
		map[string]any{"type": "function_call_output", "call_id": "call_y", "output": "c"},
	}, map[string]any{"model": "claude-sonnet-5-5-high"}), emit); err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, "claude contents", receiveCall(t, calls).body["request"].(map[string]any)["contents"], `[
		{"role":"user","parts":[{"text":"hi"}]},
		{"role":"model","parts":[{"functionCall":{"id":"call_y","name":"shell","args":{}}}]},
		{"role":"user","parts":[{"functionResponse":{"id":"call_y","name":"shell","response":{"output":"c"}}}]}
	]`)

	other := target
	other.KeyID = "key-2"
	if found := store.Signatures(ownerOf(other), []string{antigravityCallSignatureKey("call_9")}); len(found) != 0 {
		t.Fatalf("signatures crossed owners: %v", found)
	}
}

func TestAntigravityStreamReplacesMissingAndRepeatedCallIDs(t *testing.T) {
	usage := `{"promptTokenCount":1,"candidatesTokenCount":1}`
	server, _ := antigravityServer(t, sseAnswer(frame(
		`{"functionCall":{"name":"shell","args":{}}},{"functionCall":{"id":"call_old","name":"shell"}},{"functionCall":{"id":"call_new","name":"shell"}},{"functionCall":{"id":"call_new","name":"shell"}}`,
		usage)))
	adapter := New(server.Client(), NewContinuationStore())
	defer adapter.Close()
	_, emit := collectEvents()
	result, err := adapter.OpenStream(context.Background(), antigravityTarget(server.URL), antigravityStreamRequest([]any{
		map[string]any{"role": "user", "content": "go"},
		map[string]any{"type": "function_call", "call_id": "call_old", "name": "shell", "arguments": `{}`},
		map[string]any{"type": "function_call_output", "call_id": "call_old", "output": "x"},
	}, nil), emit)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Output []map[string]any `json:"output"`
	}
	_ = json.Unmarshal(result.Response, &payload)
	seen := map[string]bool{"call_old": true}
	newSeen := false
	for _, item := range payload.Output {
		id, _ := item["call_id"].(string)
		if id == "" || seen[id] {
			t.Fatalf("call id %q is empty or repeated in %s", id, result.Response)
		}
		seen[id] = true
		newSeen = newSeen || id == "call_new"
	}
	if len(payload.Output) != 4 || !newSeen {
		t.Fatalf("output = %s", result.Response)
	}
}

func TestAntigravityRequiresTokenUserAgentAndProject(t *testing.T) {
	for name, change := range map[string]func(*Target){
		"token":      func(target *Target) { target.Credential = "" },
		"user agent": func(target *Target) { target.AntigravityUserAgent = "" },
		"project":    func(target *Target) { target.AntigravityProject = "" },
	} {
		t.Run(name, func(t *testing.T) {
			target := antigravityTarget("http://127.0.0.1:1")
			change(&target)
			_, err := New(nil, nil).Execute(context.Background(), target, antigravityStreamRequest("hi", map[string]any{"stream": false}))
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != ErrorCodeInvalidConfiguration {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestContinuationStoreSignatureIndexFollowsEviction(t *testing.T) {
	store := NewContinuationStoreWithLimits(1, 1<<20, time.Hour)
	owner := Owner{ProviderID: "antigravity", AccountID: "a", KeyID: "k"}
	if err := store.Save(owner, "resp_1", Continuation{Signatures: map[string]string{"call:x": "s1"}}); err != nil {
		t.Fatal(err)
	}
	if found := store.Signatures(owner, []string{"call:x", "call:y"}); !reflect.DeepEqual(found, map[string]string{"call:x": "s1"}) {
		t.Fatalf("found = %v", found)
	}
	if err := store.Save(owner, "resp_2", Continuation{}); err != nil {
		t.Fatal(err)
	}
	if found := store.Signatures(owner, []string{"call:x"}); len(found) != 0 || len(store.signatureOwners) != 0 {
		t.Fatalf("evicted signature still indexed: %v %v", found, store.signatureOwners)
	}
}
