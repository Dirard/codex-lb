package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"codex-lb/internal/application"
)

func TestRejectedTerminalCannotBecomeClientSuccess(t *testing.T) {
	server, _ := wireFixture(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		if err := emit(application.ResponseEvent{Type: "response.completed", Data: json.RawMessage(`{"type":"response.completed","response":{"id":"invalid"}}`)}); err != nil {
			return application.ResponseResult{}, err
		}
		return application.ResponseResult{}, &application.ProviderFailure{Code: "invalid_upstream_usage", Status: 502, Dispatched: true}
	}))
	request, _ := http.NewRequest("POST", server.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-6-sol","input":"hello","stream":true}`))
	request.Header.Set("Authorization", "Bearer synthetic-key")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 502 || strings.Contains(string(body), "response.completed") {
		t.Fatalf("invalid response was successful: %d %v", response.StatusCode, err)
	}
}
