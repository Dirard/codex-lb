package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"codex-lb/internal/adapters/streambuffer"
	"codex-lb/internal/application"
)

type trackedPrelude struct {
	application.ResponsePrelude
	closed *int
}

func (p *trackedPrelude) Close() error { *p.closed++; return p.ResponsePrelude.Close() }

func TestLargePreludePreservesQuotaOnlyFailoverAndCleanup(t *testing.T) {
	for _, outcome := range []string{"quota", "generic429", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			proxy, store, stub, vault := proxyFixtureParts(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			options := application.ResponseOptions{KeyID: "key-test"}
			initial, err := proxy.Respond(ctx, options, []byte(`{"model":"gpt-6-luna","input":"hello"}`), nil)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			opened, closed, calls := 0, 0, 0
			proxy.OpenResponsePrelude = func() (application.ResponsePrelude, error) {
				buffer, err := streambuffer.New(dir, vault)
				if err != nil {
					return nil, err
				}
				opened++
				return &trackedPrelude{buffer, &closed}, nil
			}
			stub.respond = func(ctx context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				calls++
				id := "large-" + target.Account.ID
				for _, kind := range []string{"response.created", "response.in_progress"} {
					data, _ := json.Marshal(map[string]any{"type": kind, "response": map[string]string{"id": id, "instructions": strings.Repeat("context", 15000)}})
					if err := emit(application.ResponseEvent{Type: kind, Data: data}); err != nil {
						return application.ResponseResult{}, err
					}
				}
				if calls == 1 {
					if outcome == "cancelled" {
						cancel()
						return application.ResponseResult{}, ctx.Err()
					}
					code := "rate_limit_exceeded"
					if outcome == "quota" {
						code = "insufficient_quota"
					}
					return application.ResponseResult{}, &application.ProviderFailure{Code: code, Status: 429, QuotaRefused: outcome == "quota", Dispatched: true}
				}
				return complete(id, emit)
			}
			body, _ := json.Marshal(map[string]any{"model": "gpt-6-luna", "input": "next turn", "stream": true, "previous_response_id": initial.ResponseID})
			var visible []application.ResponseEvent
			result, err := proxy.Respond(ctx, options, body, func(event application.ResponseEvent) error { visible = append(visible, event); return nil })
			pending, pendingErr := store.ListReservationsNeedingReconciliation(context.Background(), "", 10)
			if pendingErr != nil {
				t.Fatal(pendingErr)
			}
			if opened != closed || opened != calls {
				t.Fatalf("request-owned buffers leaked: open=%d close=%d attempts=%d", opened, closed, calls)
			}
			entries, errDir := os.ReadDir(dir)
			if errDir != nil || len(entries) != 0 {
				t.Fatal("temporary request files remain")
			}
			if outcome != "quota" {
				if err == nil || calls != 1 || len(pending) != 1 || len(visible) != 0 {
					t.Fatalf("uncertain attempt replayed or lost reservation: err=%v attempts=%d pending=%d events=%d", err, calls, len(pending), len(visible))
				}
				if outcome == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation lost")
				}
				return
			}
			if err != nil || calls != 2 || len(pending) != 0 || result.ResponseID == "" {
				t.Fatalf("safe quota failover failed: %v calls=%d pending=%d", err, calls, len(pending))
			}
			if len(stub.accounts) != 3 || stub.accounts[1] == stub.accounts[2] {
				t.Fatal("quota refusal did not move away from the exhausted owner")
			}
			for _, event := range visible {
				if strings.Contains(string(event.Data), "large-"+stub.accounts[1]) {
					t.Fatal("failed owner prelude leaked to the client")
				}
			}
			key, err := store.GetAPIKey(context.Background(), "key-test")
			if err != nil || key.Limits[0].CurrentValue != 40 {
				t.Fatal("quota failure charged or successful turn settled twice")
			}
		})
	}
}

func TestPreludeStorageFailureSurvivesAdapterWrapping(t *testing.T) {
	proxy, store, stub := proxyFixture(t)
	proxy.OpenResponsePrelude = func() (application.ResponsePrelude, error) {
		return nil, errors.New("synthetic private storage failure")
	}
	var diagnostic application.ErrorDiagnostic
	proxy.Diagnostics = func(_ context.Context, value application.ErrorDiagnostic) { diagnostic = value }
	stub.respond = func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		data, _ := json.Marshal(map[string]any{"type": "response.created", "response": map[string]string{"instructions": strings.Repeat("context", 15000)}})
		if err := emit(application.ResponseEvent{Type: "response.created", Data: data}); err == nil {
			t.Fatal("expected storage failure")
		}
		// Reproduce the adapter's lossy wrapping; the caller owns the original local error.
		return application.ResponseResult{}, &application.ProviderFailure{Code: "upstream_error", Status: 502, Dispatched: true}
	}
	_, err := proxy.Respond(context.Background(), application.ResponseOptions{KeyID: "key-test"}, []byte(`{"model":"gpt-6-luna","input":"test","stream":true}`), func(application.ResponseEvent) error {
		t.Fatal("failed prelude reached the client")
		return nil
	})
	if !errors.Is(err, application.ErrResponsePreludeStorage) || diagnostic.ErrorCode != "response_prelude_storage_failed" || len(diagnostic.Events) != 1 || !diagnostic.EventsTruncated {
		t.Fatalf("local cause or event summary lost: err=%v code=%s summaries=%d", err, diagnostic.ErrorCode, len(diagnostic.Events))
	}
	pending, err := store.ListReservationsNeedingReconciliation(context.Background(), "", 10)
	if err != nil || len(pending) != 1 || len(stub.accounts) != 1 {
		t.Fatal("storage failure replayed the request or invented usage")
	}
}
