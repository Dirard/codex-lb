package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTranslatedChatStopsAtDoneBeforeHTTPCloses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`+"\n\ndata: [DONE]\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	adapter := New(server.Client(), NewContinuationStore())
	defer adapter.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	terminal := false
	result, err := adapter.OpenStream(ctx, testTarget(server.URL, ZAIChatCompletionsCapabilities()), Request{Body: []byte(`{"model":"glm-test","input":"hello","stream":true}`)}, func(event Event) error {
		terminal = terminal || event.Type == "response.completed"
		return nil
	})
	if err != nil || !terminal || result.Usage.InputTokens != 2 {
		t.Fatalf("translated stream waited for HTTP EOF: %+v %v terminal=%v", result, err, terminal)
	}
}
