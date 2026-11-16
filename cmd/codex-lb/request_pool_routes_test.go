package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// Every model route of the runtime takes a slot of the key's request pool,
// and no other route does. The runtime has no accounts, so nothing reaches a
// provider: a first request fails after its admission, a second is refused by
// the pool before anything else.
func TestRuntimeRoutesConsumeTheRequestPool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := openRuntime(ctx, testConfig(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	store := r.data.store
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.APIKeyAuthEnabled = true
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	keys := 0
	pooledKey := func(t *testing.T) string {
		t.Helper()
		keys++
		secret := fmt.Sprintf("synthetic-pool-route-key-%d", keys)
		id := fmt.Sprintf("pool-route-%d", keys)
		if err := store.SaveAPIKey(ctx, domain.APIKey{ID: id, Name: id, KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte(secret))), KeyPrefix: "synthetic", IsActive: true,
			RequestPool: &domain.RequestPool{RequestsPerMinute: 1}}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := store.AllowVideoKey(ctx, id, time.Now()); err != nil {
			t.Fatal(err)
		}
		return secret
	}
	send := func(method, path, secret string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(`{"model":"gpt-6-sol","input":"hi","prompt":"x","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		r.server.Handler.ServeHTTP(w, req)
		return w
	}
	refused := func(w *httptest.ResponseRecorder) bool {
		return w.Code == http.StatusTooManyRequests && strings.Contains(w.Body.String(), "rate_limit_exceeded")
	}
	consuming := [][2]string{
		{"POST", "/v1/responses"}, {"POST", "/backend-api/codex/responses/"},
		{"POST", "/v1/responses/compact"}, {"POST", "/backend-api/codex/responses/compact"},
		{"POST", "/v1/images/generations"}, {"POST", "/backend-api/codex/images/edits"},
		{"POST", "/v1/chat/completions"}, {"POST", "/v1/embeddings"},
		{"POST", "/v1/audio/transcriptions"}, {"POST", "/backend-api/transcribe"},
		{"POST", "/backend-api/codex/realtime/calls"}, {"POST", "/v1/realtime/calls"}, {"POST", "/v1/live/calls"},
		{"GET", "/v1/realtime?model=gpt-realtime"}, {"POST", "/video/v1/generations"}, {"POST", "/v1/warmup"},
	}
	for _, route := range consuming {
		t.Run(route[0]+" "+route[1], func(t *testing.T) {
			secret := pooledKey(t)
			first := send(route[0], route[1], secret)
			if refused(first) || first.Header().Get("X-Ratelimit-Limit-Requests") != "1" || first.Header().Get("X-Ratelimit-Remaining-Requests") != "0" {
				t.Fatalf("first %d %v %s", first.Code, first.Header(), first.Body)
			}
			second := send(route[0], route[1], secret)
			if !refused(second) || second.Header().Get("Retry-After") != "60" {
				t.Fatalf("second %d %s", second.Code, second.Body)
			}
			// Browser pages read the pool on a call's answer, refusals included.
			if strings.HasSuffix(route[1], "/calls") && route[1] != "/backend-api/codex/realtime/calls" &&
				!strings.Contains(second.Header().Get("Access-Control-Expose-Headers"), "x-ratelimit-remaining-requests") {
				t.Fatalf("pool headers not exposed: %q", second.Header().Get("Access-Control-Expose-Headers"))
			}
		})
	}
	free := [][2]string{
		{"GET", "/v1/models"}, {"GET", "/v1/usage"}, {"POST", "/backend-api/files"},
		{"POST", "/backend-api/codex/analytics-events/events"}, {"POST", "/v1/realtime/client_secrets"},
		{"GET", "/video/v1/generations"}, {"GET", "/backend-api/codex/rtc_unknown"},
	}
	secret := pooledKey(t)
	send("POST", "/v1/responses", secret) // the pool is now full
	for _, route := range free {
		for range 2 {
			w := send(route[0], route[1], secret)
			if refused(w) || w.Header().Get("X-Ratelimit-Limit-Requests") != "" {
				t.Fatalf("%s %s took the pool: %d %s", route[0], route[1], w.Code, w.Body)
			}
		}
	}
	if w := send("POST", "/v1/chat/completions", secret); !refused(w) {
		t.Fatalf("full pool admitted %d", w.Code)
	}
}
