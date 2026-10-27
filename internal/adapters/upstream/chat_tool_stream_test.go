package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func chatToolServer(t *testing.T, deltas []map[string]any, beforeDone <-chan struct{}, streamedEarly *atomic.Bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(value any) {
			encoded, _ := json.Marshal(value)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		}
		for _, call := range deltas {
			send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{call}}}}})
		}
		w.(http.Flusher).Flush()
		if beforeDone != nil {
			select {
			case <-beforeDone:
				streamedEarly.Store(true)
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
		send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}})
		send(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10}})
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(server.Close)
	return server
}

func chatToolRequest() Request {
	return Request{Body: mustJSON(map[string]any{
		"model": "glm-5.2", "input": "hi", "stream": true,
		"tools": []any{
			map[string]any{"type": "function", "name": "shell", "parameters": map[string]string{"type": "object"}},
			map[string]any{"type": "custom", "name": "apply_patch"},
		},
	})}
}

// NET-12: a declared function call streams before [DONE]; a custom tool keeps
// terminal-time emission; the done item keeps the streamed item ID.
func TestOpenStreamChatStreamsDeclaredFunctionCallBeforeDone(t *testing.T) {
	seen := make(chan struct{})
	var streamedEarly atomic.Bool
	server := chatToolServer(t, []map[string]any{
		{"index": 0, "id": "call_1", "function": map[string]any{"name": "shell", "arguments": ""}},
		{"index": 0, "function": map[string]any{"arguments": `{"cmd":`}},
		{"index": 0, "function": map[string]any{"arguments": `"ls"}`}},
		{"index": 1, "id": "call_2", "function": map[string]any{"name": "apply_patch", "arguments": `{"patch":"x"}`}},
	}, seen, &streamedEarly)
	adapter := New(server.Client(), NewContinuationStore())
	defer adapter.Close()
	var events []Event
	once := false
	result, err := adapter.OpenStream(context.Background(), testTarget(server.URL, ZAIChatCompletionsCapabilities()), chatToolRequest(), func(e Event) error {
		events = append(events, e)
		if e.Type == "response.function_call_arguments.delta" && !once {
			once = true
			close(seen)
		}
		return nil
	})
	if err != nil || !streamedEarly.Load() {
		t.Fatalf("function call was not streamed before [DONE]: early=%v err=%v", streamedEarly.Load(), err)
	}
	var addedID, doneID string
	var arguments strings.Builder
	customBeforeDone := false
	for i, e := range events {
		var payload struct {
			Delta string `json:"delta"`
			Item  struct {
				ID   string `json:"id"`
				Type string `json:"type"`
				Name string `json:"name"`
				Args string `json:"arguments"`
			} `json:"item"`
		}
		_ = json.Unmarshal(e.Data, &payload)
		switch {
		case e.Type == "response.output_item.added" && payload.Item.Name == "shell":
			addedID = payload.Item.ID
		case e.Type == "response.function_call_arguments.delta" && addedID != "" && doneID == "":
			arguments.WriteString(payload.Delta)
		case e.Type == "response.output_item.done" && payload.Item.Name == "shell":
			doneID = payload.Item.ID
			if payload.Item.Args != `{"cmd":"ls"}` {
				t.Fatalf("done arguments: %q", payload.Item.Args)
			}
		case e.Type == "response.output_item.added" && payload.Item.Type == "custom_tool_call":
			customBeforeDone = i < len(events)-1 && doneID != ""
		}
	}
	if addedID == "" || addedID != doneID || arguments.String() != `{"cmd":"ls"}` || !customBeforeDone {
		t.Fatalf("streamed item mismatch: added=%q done=%q args=%q custom=%v", addedID, doneID, arguments.String(), customBeforeDone)
	}
	if output := findOutput(t, result.Response, "function_call"); output["id"] != addedID || output["arguments"] != `{"cmd":"ls"}` {
		t.Fatalf("terminal output differs from streamed item: %v", output)
	}
}

// NET-12: a streamed call cannot change identity afterwards.
func TestOpenStreamChatRejectsToolCallChangedAfterStreaming(t *testing.T) {
	server := chatToolServer(t, []map[string]any{
		{"index": 0, "id": "call_1", "function": map[string]any{"name": "shell", "arguments": `{"cmd":`}},
		{"index": 0, "function": map[string]any{"name": "_other", "arguments": `"ls"}`}},
	}, nil, nil)
	adapter := New(server.Client(), NewContinuationStore())
	defer adapter.Close()
	_, err := adapter.OpenStream(context.Background(), testTarget(server.URL, ZAIChatCompletionsCapabilities()), chatToolRequest(), func(Event) error { return nil })
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != ErrorCodeInvalidToolCall {
		t.Fatalf("changed tool call accepted: %v", err)
	}
}

// NET-11: many small argument deltas accumulate in linear time and memory.
func TestOpenStreamChatAccumulatesManyArgumentDeltasLinearly(t *testing.T) {
	const pieces = 20000
	deltas := []map[string]any{{"index": 0, "id": "call_1", "function": map[string]any{"name": "apply_patch", "arguments": `{"patch":"`}}}
	for range pieces {
		deltas = append(deltas, map[string]any{"index": 0, "function": map[string]any{"arguments": "abcde"}})
	}
	deltas = append(deltas, map[string]any{"index": 0, "function": map[string]any{"arguments": `"}`}})
	server := chatToolServer(t, deltas, nil, nil)
	adapter := New(server.Client(), NewContinuationStore())
	defer adapter.Close()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	result, err := adapter.OpenStream(context.Background(), testTarget(server.URL, ZAIChatCompletionsCapabilities()), chatToolRequest(), func(Event) error { return nil })
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if output := findOutput(t, result.Response, "custom_tool_call"); len(output["input"].(string)) != pieces*5 {
		t.Fatalf("arguments lost: %d bytes", len(output["input"].(string)))
	}
	// Quadratic concatenation allocated about 1 GB for this stream.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 256<<20 {
		t.Fatalf("argument accumulation allocated %d MiB", allocated>>20)
	}
}
