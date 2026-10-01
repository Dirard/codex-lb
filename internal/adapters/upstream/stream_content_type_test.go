package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMissingStreamContentTypeStillRequiresValidProtocol(t *testing.T) {
	const completed = "event: response.completed\ndata: " + `{"type":"response.completed","response":{"id":"r","status":"completed","usage":{"input_tokens":9,"output_tokens":6}}}` + "\n\n"
	for _, test := range []struct {
		name, contentType, body string
		allowMissing, success   bool
	}{
		{"subscription missing header", "", completed, true, true},
		{"external missing header", "", completed, false, false},
		{"explicit SSE", "text/event-stream; charset=utf-8", completed, false, true},
		{"explicit JSON", "application/json", completed, true, false},
		{"explicit HTML", "text/html", completed, true, false},
		{"headerless HTML", "", "<html>not a model response</html>", true, false},
		{"invalid JSON event", "", "data: not JSON\n\n", true, false},
		{"missing terminal", "", "data: {\"type\":\"response.created\"}\n\n", true, false},
		{"missing usage", "", "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\"}}\n\n", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header()["Content-Type"] = nil
				if test.contentType != "" {
					w.Header().Set("Content-Type", test.contentType)
				}
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			adapter := New(server.Client(), nil)
			defer adapter.Close()
			capabilities := ResponsesCapabilities()
			capabilities.AllowMissingSSEContentType = test.allowMissing
			completedEvents := 0
			result, err := adapter.OpenStream(context.Background(), testTarget(server.URL, capabilities), Request{Body: json.RawMessage(`{"model":"gpt-6-luna","input":"Say OK.","stream":true}`)}, func(event Event) error {
				if event.Type == "response.completed" {
					completedEvents++
				}
				return nil
			})
			if test.success {
				if err != nil || !result.UsageKnown || result.Usage.InputTokens != 9 || result.Usage.OutputTokens != 6 || completedEvents != 1 {
					t.Fatalf("valid stream lost: result=%+v completed=%d err=%v", result, completedEvents, err)
				}
			} else if err == nil || completedEvents != 0 {
				t.Fatalf("invalid stream was accepted: completed=%d err=%v", completedEvents, err)
			}
		})
	}
}
