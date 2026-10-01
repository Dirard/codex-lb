package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
)

func TestResponsesPreservePolicyWireNormalization(t *testing.T) {
	for _, body := range []string{
		`{"model":"blocked-model","model":"gpt-6-sol","input":"hello","stream":true}`,
		`{"model":"gpt-6-sol","Model":"blocked-model","input":"hello","stream":true}`,
		`{"model":"blocked-model","m\u006fdel":"gpt-6-sol","input":"hello","stream":true}`,
	} {
		t.Run(body, func(t *testing.T) {
			var object map[string]json.RawMessage
			_ = json.Unmarshal([]byte(body), &object)
			canonical, _ := json.Marshal(object)
			server, store := wireFixture(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, wire json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				var actual struct{ Model string }
				if json.Unmarshal(wire, &actual) != nil || actual.Model != "gpt-6-sol" || !bytes.Equal(wire, canonical) {
					t.Errorf("policy and wire normalization diverged: %s", wire)
				}
				return wireComplete("normalized-response", emit)
			}))
			key, err := store.GetAPIKey(context.Background(), "wire-key")
			if err != nil {
				t.Fatal(err)
			}
			key.AllowedModels = []string{"gpt-6-sol"}
			if err := store.SaveAPIKey(context.Background(), key, time.Now()); err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer synthetic-key")
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			result, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != http.StatusOK || !bytes.Contains(result, []byte("response.completed")) {
				t.Fatalf("response failed: status=%d err=%v", response.StatusCode, err)
			}
		})
	}
}
