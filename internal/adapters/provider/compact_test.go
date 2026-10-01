package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codex-lb/internal/application"
)

func TestCompactStreamCollection(t *testing.T) {
	const usage = `"usage":{"input_tokens":4,"output_tokens":2},"service_tier":"priority"`
	const item = `{"type":"compaction","id":"cmp_kept","encrypted_content":"opaque","status":"completed"}`
	const message = `{"type":"message","content":[{"type":"output_text","text":"opaque"}]}`
	for _, tc := range []struct {
		name, before, output, contentType string
	}{
		{"terminal", "", `[` + item + `]`, "text/event-stream"},
		{"missing content type", "", `[` + item + `]`, ""},
		{"message", "", `[{"type":"message","text":"older"},` + message + `]`, "text/event-stream"},
		{"indexed", `{"type":"response.output_item.added","output_index":0,"item":{"type":"compaction","encrypted_content":""}}` + "\n" + `{"type":"response.output_item.done","output_index":0,"item":` + item + `}`, `[]`, "text/event-stream"},
		{"unindexed", `{"type":"response.output_item.done","item":` + message + `}`, `null`, "text/event-stream"},
		{"explicit preferred", "", `[` + item + `,{"type":"message","text":"not-summary"}]`, "text/event-stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Input []struct {
						Type string `json:"type"`
					} `json:"input"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Input) != 1 || request.Input[0].Type != "compaction_trigger" {
					t.Error("existing final trigger was duplicated")
				}
				w.Header()["Content-Type"] = []string{tc.contentType}
				fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{}}\n\n")
				for _, event := range strings.Split(tc.before, "\n") {
					if event != "" {
						fmt.Fprintf(w, "data: %s\n\n", event)
					}
				}
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"output\":%s,%s}}\n\n", tc.output, usage)
			}))
			defer server.Close()
			adapter, _ := operationAdapter(t, server)
			defer adapter.Close()
			first := 0
			result, err := adapter.Compact(context.Background(), application.CodexOperationTarget{Account: operationAccount(), KeyID: "key", OnFirstUpstreamEvent: func() { first++ }}, []byte(`{"model":"gpt-6-luna","input":[{"type":"compaction_trigger"}]}`))
			var compact struct {
				Object string `json:"object"`
				Output []struct {
					Encrypted string `json:"encrypted_content"`
				} `json:"output"`
			}
			if err != nil || json.Unmarshal(result.Body, &compact) != nil || compact.Object != "response.compaction" || len(compact.Output) != 1 || compact.Output[0].Encrypted != "opaque" || first != 1 || !result.UsageKnown || result.Usage.InputTokens != 4 || result.ServiceTier != "priority" || !result.OutputObserved {
				t.Fatalf("bad compact collection: result=%+v first=%d err=%v body=%s", result, first, err, result.Body)
			}
		})
	}
}

func TestCompactStreamFailureKeepsBillingAndDoesNotRefreshAfterOutput(t *testing.T) {
	for _, tc := range []struct {
		name, terminal string
		known          bool
		input          int64
	}{
		{"disconnect", "", false, 0},
		{"quota after output", `{"type":"error","status":429,"error":{"code":"insufficient_quota"}}`, false, 0},
		{"auth zero after output", `{"type":"error","status":401,"error":{"code":"invalid_token"},"usage":{"input_tokens":0,"output_tokens":0}}`, true, 0},
		{"billed failure", `{"type":"response.failed","response":{"status":"failed","error":{"code":"insufficient_quota"},"usage":{"input_tokens":4,"output_tokens":2},"service_tier":"priority"}}`, true, 4},
		{"partial billing", `{"type":"response.failed","response":{"error":{"code":"insufficient_quota"},"usage":{"input_tokens":4}}}`, false, 4},
		{"missing output", `{"type":"response.completed","response":{"output":[],"usage":{"input_tokens":4,"output_tokens":2}}}`, true, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
				if tc.terminal != "" {
					fmt.Fprintf(w, "data: %s\n\n", tc.terminal)
				}
			}))
			defer server.Close()
			adapter, _ := operationAdapter(t, server)
			defer adapter.Close()
			tokens := &refreshTokens{}
			adapter.chatgpt = tokens
			result, err := adapter.Compact(context.Background(), application.CodexOperationTarget{Account: operationAccount(), KeyID: "key"}, []byte(`{"model":"gpt-6-luna","input":[]}`))
			if err == nil && !result.Failed || result.UsageKnown != tc.known || !result.OutputObserved || tc.known && result.Usage.InputTokens != tc.input || calls != 1 || tokens.calls.Load() != 0 {
				t.Fatalf("unsafe stream failure: %+v err=%v calls=%d refresh=%d", result, err, calls, tokens.calls.Load())
			}
			if tc.name == "billed failure" && result.ServiceTier != "priority" {
				t.Fatal("error tier lost")
			}
		})
	}
}
