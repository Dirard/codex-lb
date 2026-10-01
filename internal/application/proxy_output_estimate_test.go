package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestSubscriptionOutputEstimateUsesUncappedReservation(t *testing.T) {
	for _, test := range []struct {
		name                 string
		limit, cap, reserved int64
	}{
		{"remaining budget clamp", 1024, 1, 1024},
		{"uncapped estimate actual usage", 4096, 1, 2048},
		{"larger conservative hint", 4096, 5000, 4096},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy, store, stub := proxyFixture(t)
			ctx := context.Background()
			key, err := store.GetAPIKey(ctx, "key-test")
			if err != nil {
				t.Fatal(err)
			}
			key.Limits = []domain.LimitRule{{Type: domain.LimitOutputTokens, Window: domain.WindowWeekly, MaxValue: test.limit}}
			if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
				t.Fatal(err)
			}
			stub.respond = func(ctx context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				reserved, err := store.GetAPIKey(ctx, key.ID)
				if err != nil || len(reserved.Limits) != 1 || reserved.Limits[0].CurrentValue != test.reserved {
					t.Fatalf("unsupported cap reduced the reservation: %+v %v", reserved.Limits, err)
				}
				return complete("resp_estimate", emit)
			}
			body := json.RawMessage(fmt.Sprintf(`{"model":"gpt-6-luna","input":"ping","max_output_tokens":%d}`, test.cap))
			_, err = proxy.Respond(ctx, application.ResponseOptions{KeyID: key.ID, Codex: true}, body, nil)
			if err != nil || len(stub.accounts) != 1 {
				t.Fatalf("sufficient budget failed: %v calls=%d", err, len(stub.accounts))
			}
			key, err = store.GetAPIKey(ctx, key.ID)
			if err != nil || len(key.Limits) != 1 || key.Limits[0].CurrentValue != 10 {
				t.Fatalf("settlement used the hint rather than actual output: %+v %v", key.Limits, err)
			}
		})
	}
}
