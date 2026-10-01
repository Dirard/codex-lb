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

	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

type fixedTokenSource struct{}

func (fixedTokenSource) AccessToken(context.Context, domain.Account, domain.AccountCredential) (string, error) {
	return "synthetic-test-token", nil
}

func (fixedTokenSource) ForceRefresh(context.Context, domain.Account, string) (string, error) {
	return "synthetic-refreshed-token", nil
}

func TestCodexHTTPBridgeRepairsInvalidPreviousOnSameAccount(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, body, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var request struct {
				Previous string                     `json:"previous_response_id"`
				Input    json.RawMessage            `json:"input"`
				Metadata map[string]json.RawMessage `json:"client_metadata"`
			}
			if json.Unmarshal(body, &request) != nil {
				t.Error("invalid request")
				return
			}
			attempt := calls.Add(1)
			if attempt == 2 {
				if request.Previous != "wire-first" {
					t.Error("continuation did not use original response ID")
				}
				_ = conn.Write(r.Context(), websocket.MessageText, []byte("{\"type\":\"error\",\"status\":400,\"error\":{\"type\":\"invalid_request_error\",\"message\":\"Invalid `previous_response_id`.\",\"param\":\"previous_response_id\"}}"))
				continue
			}
			id, text := "wire-first", "first-answer"
			if attempt == 3 {
				id, text = "wire-repaired", "second-answer"
				if _, inherited := request.Metadata["x-codex-turn-metadata"]; inherited {
					t.Error("fresh replay restored stripped metadata from compatibility headers")
				}
				if request.Previous != "" || !strings.Contains(string(request.Input), "hello") || !strings.Contains(string(request.Input), "first-answer") || !strings.Contains(string(request.Input), "follow-up") {
					t.Error("repair lost history or retained previous_response_id")
					return
				}
			}
			event := map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": text}}}}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 10, "total_tokens": 20}}}
			encoded, _ := json.Marshal(event)
			if err := conn.Write(r.Context(), websocket.MessageText, encoded); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	var adapter *provider.Adapter
	server, store := wireFixture(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return adapter.Respond(ctx, target, body, emit)
	}))
	settings, err := store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.UpstreamStreamTransport = "websocket" // This regression exercises WS recovery, not auto selection.
	if err := store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	defer adapter.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	request := func(previous, input string) string {
		t.Helper()
		payload := fmt.Sprintf(`{"model":"gpt-6-sol","input":%q,"stream":true}`, input)
		if previous != "" {
			payload = fmt.Sprintf(`{"model":"gpt-6-sol","input":%q,"previous_response_id":%q,"stream":true}`, input, previous)
		}
		req, _ := http.NewRequest("POST", server.URL+"/backend-api/codex/responses", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer synthetic-key")
		req.Header.Set("X-Codex-Turn-Metadata", "old-turn-metadata")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("HTTP bridge status=%d error=%v", res.StatusCode, err)
		}
		return string(body)
	}
	if body := request("", "hello"); !strings.Contains(body, "wire-first") {
		t.Fatal("initial response failed")
	}
	if body := request("wire-first", "follow-up"); !strings.Contains(body, "wire-repaired") || strings.Contains(body, "invalid_request_error") {
		t.Fatal("previous-response error was exposed instead of repaired")
	}
	if calls.Load() != 3 {
		t.Fatalf("unexpected attempts: %d", calls.Load())
	}
	owner, err := store.GetContinuation(context.Background(), "wire-key", "wire-repaired", time.Now())
	if err != nil || owner.AccountID != "wire-account" {
		t.Fatal("lost response switched accounts without quota refusal")
	}
}
