package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type incompleteResponsesProvider struct{}

func (incompleteResponsesProvider) Respond(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
	for _, event := range []application.ResponseEvent{
		{Type: "response.created", Data: []byte(`{"type":"response.created","response":{"id":"resp_cut","status":"in_progress","model":"gpt-5.5","created_at":1700000000}}`)},
		{Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"delta":"partial"}`)},
		{Type: "response.incomplete", Data: []byte(`{"type":"response.incomplete","response":{"id":"resp_cut","status":"incomplete","model":"gpt-5.5","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":4,"output_tokens":3,"total_tokens":7}}}`)},
	} {
		if err := emit(event); err != nil {
			return application.ResponseResult{}, err
		}
	}
	return application.ResponseResult{ResponseID: "resp_cut", Response: []byte(`{"id":"resp_cut","status":"incomplete"}`), Usage: domain.UsageAmount{InputTokens: 4, OutputTokens: 3}, UsageKnown: true, Failed: true}, nil
}

// NET-13: an output-limit stop after partial content is a normal Chat end with
// finish_reason length, not an error chunk; every chunk carries created/model.
func TestNativeChatStreamRendersIncompleteAsLength(t *testing.T) {
	_, _, handler := newNativeTestServer(t, nil, incompleteResponsesProvider{}, 0)
	server := httptest.NewServer(handler)
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.5","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer synthetic-native-key")
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body strings.Builder
	buffer := make([]byte, 4096)
	for {
		n, readErr := response.Body.Read(buffer)
		body.Write(buffer[:n])
		if readErr != nil {
			break
		}
	}
	text := body.String()
	if response.StatusCode != http.StatusOK || !strings.Contains(text, `"finish_reason":"length"`) || !strings.HasSuffix(strings.TrimSpace(text), "data: [DONE]") || strings.Contains(text, `"error"`) {
		t.Fatalf("incomplete terminal rendered as %d:\n%s", response.StatusCode, text)
	}
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var chunk struct {
			Created int64  `json:"created"`
			Model   string `json:"model"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk) != nil || chunk.Created != 1700000000 || chunk.Model != "gpt-5.5" {
			t.Fatalf("chunk without created/model: %s", line)
		}
	}
}
