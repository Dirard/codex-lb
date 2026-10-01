package application_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type responseStub struct {
	mu       sync.Mutex
	accounts []string
	bodies   []json.RawMessage
	respond  func(context.Context, application.ResponseTarget, json.RawMessage, func(application.ResponseEvent) error) (application.ResponseResult, error)
}

func (s *responseStub) Respond(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
	s.mu.Lock()
	s.accounts = append(s.accounts, target.Account.ID)
	s.bodies = append(s.bodies, append(json.RawMessage(nil), body...))
	f := s.respond
	s.mu.Unlock()
	return f(ctx, target, body, emit)
}

func complete(id string, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
	body := json.RawMessage(fmt.Sprintf(`{"id":%q,"status":"completed","output":[{"type":"message","id":"msg_test","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":10,"output_tokens":10}}`, id))
	if emit != nil {
		if err := emit(application.ResponseEvent{Type: "response.created", Data: json.RawMessage(fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, id))}); err != nil {
			return application.ResponseResult{}, err
		}
		terminal, _ := json.Marshal(struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
		}{"response.completed", body})
		if err := emit(application.ResponseEvent{Type: "response.completed", Data: terminal}); err != nil {
			return application.ResponseResult{}, err
		}
	}
	return application.ResponseResult{ResponseID: id, Response: body, Usage: domain.UsageAmount{InputTokens: 10, OutputTokens: 10}, UsageKnown: true}, nil
}

func proxyFixture(t *testing.T) (*application.Proxy, *sqlite.Store, *responseStub) {
	proxy, store, stub, _ := proxyFixtureParts(t)
	return proxy, store, stub
}

func proxyFixtureParts(t *testing.T) (*application.Proxy, *sqlite.Store, *responseStub, *credentials.Vault) {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := vault.Encrypt([]byte("synthetic-test-credential"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.RoutingStrategy = "usage_weighted"
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"account-a", "account-b"} {
		if err := store.SaveAccount(ctx, domain.Account{ID: id, Kind: domain.AccountChatGPT, Provider: "openai", Email: id + "@example.invalid", PlanType: "plus", Status: domain.AccountActive, RoutingPolicy: "normal", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: id, AccessTokenEncrypted: encrypted, RefreshTokenEncrypted: encrypted, IDTokenEncrypted: encrypted}); err != nil {
			t.Fatal(err)
		}
	}
	key := domain.APIKey{ID: "key-test", Name: "test", KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic-key"))), KeyPrefix: "synthetic", IsActive: true, Limits: []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 1_000_000}}}
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	stub := &responseStub{}
	stub.respond = func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return complete("resp_"+target.Account.ID, emit)
	}
	proxy := application.NewProxy(store, stub, vault, application.ProxyConfig{MaxStreams: 1, MaxQueued: 1, QueueTimeout: 50 * time.Millisecond})
	return proxy, store, stub, vault
}

func TestExistingThreadAtZeroAndQuotaOnlyFailover(t *testing.T) {
	proxy, store, stub := proxyFixture(t)
	ctx := context.Background()
	options := application.ResponseOptions{KeyID: "key-test", Codex: true}
	emit := func(application.ResponseEvent) error { return nil }
	first, err := proxy.Respond(ctx, options, json.RawMessage(`{"model":"gpt-6-sol","input":"hello","stream":true}`), emit)
	if err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(time.Hour)
	if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "account-a", Window: "primary", UsedPercent: 100, ResetAt: &reset, ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	continuation := json.RawMessage(fmt.Sprintf(`{"model":"gpt-6-sol","previous_response_id":%q,"input":"continue","stream":true}`, first.ResponseID))
	if _, err := proxy.Respond(ctx, options, continuation, emit); err != nil {
		t.Fatal(err)
	}
	if stub.accounts[1] != "account-a" {
		t.Fatal("reported 0% moved active thread before upstream refusal")
	}
	if _, err := proxy.Respond(ctx, options, json.RawMessage(`{"model":"gpt-6-sol","input":"new thread","stream":true}`), emit); err != nil {
		t.Fatal(err)
	}
	if stub.accounts[2] != "account-b" {
		t.Fatal("new thread used exhausted account")
	}
	stub.respond = func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		if target.Account.ID == "account-a" {
			if err := emit(application.ResponseEvent{Type: "response.created", Data: json.RawMessage(`{"type":"response.created","response":{"id":"hidden-failed-attempt"}}`)}); err != nil {
				return application.ResponseResult{}, err
			}
			return application.ResponseResult{}, &application.ProviderFailure{Code: "usage_limit_reached", Status: 429, QuotaRefused: true, Dispatched: true}
		}
		return complete("resp_after_failover", emit)
	}
	var events []application.ResponseEvent
	result, err := proxy.Respond(ctx, options, continuation, func(event application.ResponseEvent) error { events = append(events, event); return nil })
	if err != nil || result.ResponseID != "resp_after_failover" {
		t.Fatalf("quota failover failed: %v", err)
	}
	for _, event := range events {
		if strings.Contains(string(event.Data), "hidden-failed-attempt") {
			t.Fatal("failed attempt response ID leaked to client")
		}
	}
	lastBody := stub.bodies[len(stub.bodies)-1]
	if strings.Contains(string(lastBody), "previous_response_id") || !strings.Contains(string(lastBody), "hello") || !strings.Contains(string(lastBody), "answer") {
		t.Fatal("failover transferred old owner ID or lost conversation")
	}
	owner, err := store.GetContinuation(ctx, "key-test", result.ResponseID, time.Now())
	if err != nil || owner.AccountID != "account-b" {
		t.Fatal("new response has wrong owner")
	}
	key, err := store.GetAPIKey(ctx, "key-test")
	if err != nil || key.Limits[0].CurrentValue != 80 {
		t.Fatalf("usage not settled once: current=%d error=%v", key.Limits[0].CurrentValue, err)
	}
}

func TestGeneric429AndVisibleToolNeverReplay(t *testing.T) {
	for _, toolVisible := range []bool{false, true} {
		t.Run(fmt.Sprint(toolVisible), func(t *testing.T) {
			proxy, _, stub := proxyFixture(t)
			stub.respond = func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				if toolVisible {
					if err := emit(application.ResponseEvent{Type: "response.output_item.added", Data: json.RawMessage(`{"type":"response.output_item.added","item":{"type":"function_call","name":"side_effect"}}`)}); err != nil {
						return application.ResponseResult{}, err
					}
				}
				return application.ResponseResult{}, &application.ProviderFailure{Code: "rate_limit_exceeded", Status: 429, QuotaRefused: toolVisible, Dispatched: true}
			}
			_, err := proxy.Respond(context.Background(), application.ResponseOptions{KeyID: "key-test"}, json.RawMessage(`{"model":"gpt-6-sol","input":"hello","stream":true}`), func(application.ResponseEvent) error { return nil })
			if err == nil || len(stub.accounts) != 1 {
				t.Fatal("unsafe/generic failure replayed on another account")
			}
		})
	}
}

func TestCancellationReleasesAdmissionAndRetainsReservation(t *testing.T) {
	proxy, store, stub := proxyFixture(t)
	started := make(chan struct{})
	stub.respond = func(ctx context.Context, _ application.ResponseTarget, _ json.RawMessage, _ func(application.ResponseEvent) error) (application.ResponseResult, error) {
		close(started)
		<-ctx.Done()
		return application.ResponseResult{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, json.RawMessage(`{"model":"gpt-6-sol","input":"hello"}`), nil)
		finished <- err
	}()
	select {
	case <-started:
	case err := <-finished:
		t.Fatalf("request failed before dispatch: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("request did not dispatch")
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	key, err := store.GetAPIKey(context.Background(), "key-test")
	if err != nil || key.Limits[0].CurrentValue == 0 {
		t.Fatal("cancelled in-flight reservation was released")
	}
	pending, err := store.ListReservationsNeedingReconciliation(context.Background(), "", 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("cancelled usage was not retained: %+v %v", pending, err)
	}
	stub.respond = func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, _ func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return complete("after-cancel", nil)
	}
	if _, err := proxy.Respond(context.Background(), application.ResponseOptions{KeyID: "key-test"}, json.RawMessage(`{"model":"gpt-6-sol","input":"next"}`), nil); err != nil {
		t.Fatal("stream capacity leaked after cancellation")
	}
}

func TestKnownUsageSurvivesStreamDeliveryFailure(t *testing.T) {
	proxy, store, stub := proxyFixture(t)
	deliveryErr := errors.New("synthetic delivery failure")
	stub.respond = func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		err := emit(application.ResponseEvent{Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","delta":"visible"}`)})
		return application.ResponseResult{Usage: domain.UsageAmount{InputTokens: 2, OutputTokens: 1}, UsageKnown: true}, err
	}
	_, err := proxy.Respond(context.Background(), application.ResponseOptions{KeyID: "key-test"}, []byte(`{"model":"gpt-6-sol","input":"hello","stream":true}`), func(application.ResponseEvent) error {
		return deliveryErr
	})
	if !errors.Is(err, deliveryErr) {
		t.Fatalf("delivery failure was lost: %v", err)
	}
	totals, err := store.UsageTotals(context.Background(), "key-test", "")
	pending, pendingErr := store.ListReservationsNeedingReconciliation(context.Background(), "", 10)
	if err != nil || pendingErr != nil || totals.RequestCount != 1 || totals.FailedCount != 1 ||
		totals.Usage.InputTokens != 2 || totals.Usage.OutputTokens != 1 || len(pending) != 0 {
		t.Fatalf("confirmed usage was not settled once: totals=%+v pending=%+v errors=%v/%v", totals, pending, err, pendingErr)
	}
}

func TestProviderPanicRetainsReservation(t *testing.T) {
	proxy, store, stub := proxyFixture(t)
	settings, err := store.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, reserve := 1, 0
	settings.ProxyAccountStreamLimitOverride, settings.ProxyAccountStreamRecoveryReserveOverride = &stream, &reserve
	settings.RoutingStrategy, settings.SingleAccountID = "single_account", "account-a"
	if err := store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	stub.respond = func(context.Context, application.ResponseTarget, json.RawMessage, func(application.ResponseEvent) error) (application.ResponseResult, error) {
		panic("synthetic provider panic")
	}
	_, err = proxy.Respond(context.Background(), application.ResponseOptions{KeyID: "key-test"}, []byte(`{"model":"gpt-6-sol","input":"hello"}`), nil)
	var failure *application.ProviderFailure
	if !errors.As(err, &failure) || failure.Code != "upstream_adapter_failure" {
		t.Fatalf("provider panic was not sanitized: %v", err)
	}
	pending, err := store.ListReservationsNeedingReconciliation(context.Background(), "", 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("panicked attempt lost reconciliation ownership: %+v %v", pending, err)
	}
	stub.respond = func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return complete("after-panic-"+target.Account.ID, emit)
	}
	if _, err := proxy.Respond(context.Background(), application.ResponseOptions{KeyID: "key-test"}, []byte(`{"model":"gpt-6-sol","input":"next"}`), nil); err != nil {
		t.Fatalf("panicked account lease was not released: %v", err)
	}
}

func TestConfirmedOwnerUsesRecoverySlotWithoutMigrating(t *testing.T) {
	_, store, stub, vault := proxyFixtureParts(t)
	proxy := application.NewProxy(store, stub, vault, application.ProxyConfig{MaxStreams: 3, MaxQueued: 3, QueueTimeout: 50 * time.Millisecond})
	ctx := context.Background()
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream, reserve := 2, 1
	settings.ProxyAccountStreamLimitOverride = &stream
	settings.ProxyAccountStreamRecoveryReserveOverride = &reserve
	settings.RoutingStrategy, settings.SingleAccountID = "single_account", "account-a"
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	owner, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, []byte(`{"model":"gpt-6-sol","input":"initial"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var dispatched atomic.Int32
	stub.respond = func(_ context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		id := dispatched.Add(1)
		if strings.Contains(string(body), `"hold"`) {
			close(entered)
			<-release
		}
		return complete(fmt.Sprintf("resp_%s_next_%d", target.Account.ID, id), emit)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, []byte(`{"model":"gpt-6-sol","input":"hold"}`), nil)
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("ordinary request did not start")
	}
	_, err = proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test", SessionID: "forged"}, []byte(`{"model":"gpt-6-sol","input":"new"}`), nil)
	var capacity *application.ProxyError
	if !errors.As(err, &capacity) || capacity.Code != "account_stream_cap" || capacity.Status != 429 {
		close(release)
		t.Fatalf("forged/new work consumed recovery reserve: %v", err)
	}
	follow := json.RawMessage(fmt.Sprintf(`{"model":"gpt-6-sol","previous_response_id":%q,"input":"follow"}`, owner.ResponseID))
	_, err = proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, follow, nil)
	close(release)
	if err != nil || <-finished != nil {
		t.Fatalf("confirmed owner could not use reserved slot: %v", err)
	}
	if len(stub.accounts) != 3 || stub.accounts[0] != "account-a" || stub.accounts[1] != "account-a" || stub.accounts[2] != "account-a" {
		t.Fatalf("capacity moved hard owner or dispatched forged request: %v", stub.accounts)
	}
}
