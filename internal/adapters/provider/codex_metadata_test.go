package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codex-lb/internal/application"
)

func TestCodexMetadataDoesNotLeakToExternalSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		for _, name := range codexCompatibilityMetadataHeaders {
			if r.Header.Get(name) != "" || strings.Contains(string(body), name) {
				t.Errorf("subscription metadata forwarded to external provider: %s", name)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer server.Close()
	source, credential := zaiSource(server.URL)
	adapter := New(&sourceStore{source: source, credential: credential}, nil, testCipher{}, Config{HTTPClient: server.Client()})
	defer adapter.Close()
	metadata := make(map[string]string)
	for _, name := range codexCompatibilityMetadataHeaders {
		metadata[name] = "subscription-only"
	}
	_, err := adapter.Respond(context.Background(), application.ResponseTarget{
		Account: source.Account(), KeyID: "key", CompatibilityMetadata: metadata,
	}, json.RawMessage(`{"model":"gpt-5.6-sol","input":"hello","stream":false}`), nil)
	if err != nil {
		t.Fatal(err)
	}
}
