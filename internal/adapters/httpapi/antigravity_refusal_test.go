package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"codex-lb/internal/application"
)

// Classified Cloud Code refusals reach the client with their own code, a
// clear message and the provider's retry hint, and never move the request.
func TestAntigravityRefusalsReachTheClientWithTheirHint(t *testing.T) {
	cases := []struct {
		failure *application.ProviderFailure
		status  int
		retry   string
		message string
	}{
		{&application.ProviderFailure{Code: "model_capacity_exhausted", Status: 503, Dispatched: true, LimitHeaders: map[string][]string{"Retry-After": {"30"}}},
			503, "30", "no capacity for this model"},
		{&application.ProviderFailure{Code: "model_capacity_exhausted", Status: 503, Dispatched: true}, 503, "2", "no capacity for this model"},
		{&application.ProviderFailure{Code: "rate_limit_exceeded", Status: 429, Dispatched: true, LimitHeaders: map[string][]string{"Retry-After": {"1"}}},
			429, "1", "limiting the rate"},
		{&application.ProviderFailure{Code: "upstream_content_refused", Status: 429, Dispatched: true}, 429, "", "refused this request for its content"},
		{&application.ProviderFailure{Code: "account_suspended", Status: 403, Dispatched: true}, 403, "", "suspended this account"},
		{&application.ProviderFailure{Code: "account_validation_required", Status: 403, Dispatched: true}, 403, "", "needs a new sign-in"},
		{&application.ProviderFailure{Code: "model_access_denied", Status: 403, Dispatched: true}, 403, "", "does not include this model"},
		{&application.ProviderFailure{Code: "antigravity_account_unavailable"}, 502, "", "needs a new sign-in or is suspended"},
	}
	for _, tc := range cases {
		t.Run(tc.failure.Code, func(t *testing.T) {
			var calls atomic.Int32
			server, _ := wireFixture(t, wireProvider(func(context.Context, application.ResponseTarget, json.RawMessage, func(application.ResponseEvent) error) (application.ResponseResult, error) {
				calls.Add(1)
				return application.ResponseResult{}, tc.failure
			}))
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hello"}`))
			request.Header.Set("Authorization", "Bearer synthetic-key")
			request.Header.Set("Content-Type", "application/json")
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != tc.status || !strings.Contains(string(body), `"code":"`+tc.failure.Code+`"`) || !strings.Contains(string(body), tc.message) {
				t.Fatalf("answer: %d %s", response.StatusCode, body)
			}
			if got := response.Header.Get("Retry-After"); got != tc.retry {
				t.Fatalf("Retry-After = %q, want %q", got, tc.retry)
			}
			if calls.Load() != 1 {
				t.Fatalf("a non-quota refusal was retried: %d calls", calls.Load())
			}
		})
	}
}
