package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestFillFirstRouteUsesPrimaryThenSecondaryAndStableID(t *testing.T) {
	selected := make(chan string, 1)
	var count atomic.Int64
	server, store := wireFixture(t, wireProvider(func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, _ func(application.ResponseEvent) error) (application.ResponseResult, error) {
		selected <- target.Account.ID
		id := fmt.Sprintf("fill_%d", count.Add(1))
		return application.ResponseResult{ResponseID: id, Response: json.RawMessage(fmt.Sprintf(`{"id":%q,"status":"completed","output":[]}`, id)), UsageKnown: true}, nil
	}))
	ctx := context.Background()
	other, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	other.ID, other.Email = "a-account", "other@example.invalid"
	if err := store.SaveAccount(ctx, other); err != nil {
		t.Fatal(err)
	}
	credential, err := store.GetAccountCredential(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	credential.AccountID = other.ID
	if err := store.SaveAccountCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	settings, _ := store.LoadSettings(ctx)
	settings.RoutingStrategy, settings.PreferEarlierResetAccounts = "fill_first", false
	settings.StickyThreadsEnabled = false // This test isolates the unpinned selection strategy.
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	setUsage := func(id string, primary, secondary float64) {
		t.Helper()
		reset, now := time.Now().Add(time.Hour), time.Now()
		for window, value := range map[string]float64{"primary": primary, "secondary": secondary} {
			if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: id, Window: window, UsedPercent: value, ResetAt: &reset, ObservedAt: now}); err != nil {
				t.Fatal(err)
			}
		}
	}
	respond := func(want string) {
		t.Helper()
		if status, body := liteHTTPPost(t, server.URL+"/v1/responses", `{"model":"gpt-6-sol","input":"new conversation"}`); status != 200 {
			t.Fatalf("fill-first request: %d %s", status, body)
		}
		if got := <-selected; got != want {
			t.Fatalf("fill_first chose %s, want %s", got, want)
		}
	}
	setUsage("wire-account", 80, 10)
	setUsage(other.ID, 20, 90)
	respond("wire-account")
	setUsage(other.ID, 80, 90)
	respond(other.ID)
	setUsage("wire-account", 80, 90)
	respond(other.ID)
	respond(other.ID) // Last-selected history must not turn a tie into round-robin.
}
