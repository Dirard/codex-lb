package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

// A model priced at zero (prod prices codex-auto-review so through an
// override) must still serve a key whose group caps cost: its request costs
// nothing, so the cost limit only holds it while it runs.
func TestFreeModelServesKeyWithCostLimit(t *testing.T) {
	var calls atomic.Int64
	provider := wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		calls.Add(1)
		return wireComplete("free_1", emit)
	})
	server, store, proxy := wireFixtureWithProxy(t, provider)
	proxy.ResolvePrice = func(_ context.Context, _ domain.Account, model string) (pricing.Price, error) {
		if model == "codex-auto-review" {
			return pricing.Price{}, nil
		}
		_, price, err := pricing.LookupCodex(model)
		return price, err
	}
	ctx := context.Background()
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	key.Limits = append(key.Limits, domain.LimitRule{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: 800_000_000})
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"true", "false"} {
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/backend-api/codex/responses",
			strings.NewReader(`{"model":"codex-auto-review","input":"review this command","stream":`+stream+`}`))
		req.Header.Set("Authorization", "Bearer synthetic-key")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("stream=%s: %d %s", stream, res.StatusCode, body)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls %d, want 2", calls.Load())
	}
	key, err = store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range key.Limits {
		want := int64(0) // free requests leave nothing on the cost limit
		if limit.Type == domain.LimitTotalTokens {
			want = 40 // two requests of 10 input and 10 output tokens
		}
		if limit.CurrentValue != want {
			t.Fatalf("%s limit at %d, want %d", limit.Type, limit.CurrentValue, want)
		}
	}
}
