package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestPublicWarmupReservesKeyBudgetAndRetainsUnknownBilling(t *testing.T) {
	var calls atomic.Int64
	var fail atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"code":"private-secret","message":"private-secret"}}`)
			return
		}
		fmt.Fprint(w, `{"id":"resp_paid","object":"response.compact","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	server, store := publicWarmupFixture(t, upstream)
	ctx := context.Background()
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	key.Limits[0].MaxValue = 2500
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, body := publicWarmupPost(t, server, "/v1/warmup/force", "", "synthetic-key", nil)
	var summary application.PublicWarmupSummary
	if status != 200 || json.Unmarshal([]byte(body), &summary) != nil || len(summary.Submitted) != 1 || calls.Load() != 1 {
		t.Fatalf("known billed warmup = %d %s calls=%d", status, body, calls.Load())
	}
	key, err = store.GetAPIKey(ctx, "wire-key")
	if err != nil || key.Limits[0].CurrentValue != 3 {
		t.Fatalf("known warmup usage not charged to key: %+v %v", key, err)
	}
	logs, err := store.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 10})
	if err != nil || len(logs.Requests) != 1 || logs.Requests[0].RequestKind != "warmup" || logs.Requests[0].APIKeyID == nil || *logs.Requests[0].APIKeyID != "wire-key" {
		t.Fatalf("key warmup log identity = %+v %v", logs, err)
	}
	if _, err := store.GetContinuation(ctx, "wire-key", "resp_paid", time.Now()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("public warmup created a conversation owner: %v", err)
	}
	if bindings, err := store.ListAffinities(ctx, domain.AffinityFilter{Limit: 10}, time.Now(), time.Hour); err != nil || bindings.Total != 0 {
		t.Fatalf("synthetic warmup created soft locality: %+v %v", bindings, err)
	}
	key.Limits[0].MaxValue = 2
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, body = publicWarmupPost(t, server, "/v1/warmup/force", "", "synthetic-key", nil)
	if status != 200 || json.Unmarshal([]byte(body), &summary) != nil || len(summary.Failed) != 1 || summary.Failed[0].ErrorCode != "api_key_limit_exceeded" || calls.Load() != 1 {
		t.Fatalf("exhausted key dispatched warmup: %d %s calls=%d", status, body, calls.Load())
	}
	key.Limits[0].MaxValue = 2500
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	status, body = publicWarmupPost(t, server, "/v1/warmup/force", "", "synthetic-key", nil)
	if status != 200 || json.Unmarshal([]byte(body), &summary) != nil || len(summary.Failed) != 1 || calls.Load() != 2 || strings.Contains(body, "private-secret") {
		t.Fatalf("unknown billing leaked or lost: %d %s calls=%d", status, body, calls.Load())
	}
	pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
	if err != nil || len(pending) != 1 || !pending[0].NeedsReconciliation || pending[0].APIKeyID != "wire-key" || pending[0].AccountID != "wire-account" {
		t.Fatalf("unknown warmup billing not retained: %+v %v", pending, err)
	}
	key, err = store.GetAPIKey(ctx, "wire-key")
	if err != nil || key.Limits[0].CurrentValue <= 3 {
		t.Fatalf("unknown warmup did not hold key budget: %+v %v", key, err)
	}
	key.Limits[0].MaxValue = key.Limits[0].CurrentValue
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, body = publicWarmupPost(t, server, "/v1/warmup/force", "", "synthetic-key", nil)
	if status != 200 || json.Unmarshal([]byte(body), &summary) != nil || len(summary.Failed) != 1 || summary.Failed[0].ErrorCode != "api_key_limit_exceeded" || calls.Load() != 2 {
		t.Fatalf("held warmup budget bypassed: %d %s calls=%d", status, body, calls.Load())
	}
}

func TestPublicWarmupFanoutBoundAndSanitizedPartialFailure(t *testing.T) {
	var inFlight, maximum, calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active := inFlight.Add(1)
		for {
			previous := maximum.Load()
			if active <= previous || maximum.CompareAndSwap(previous, active) {
				break
			}
		}
		defer inFlight.Add(-1)
		calls.Add(1)
		time.Sleep(30 * time.Millisecond)
		if r.Header.Get("ChatGPT-Account-ID") == "account-3" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"code":"private-secret","message":"private-secret"}}`)
			return
		}
		fmt.Fprint(w, `{"id":"resp_warmup","object":"response.compact","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	server, store := publicWarmupFixture(t, upstream)
	for i := 1; i < 8; i++ {
		publicWarmupAddAccount(t, store, fmt.Sprintf("account-%d", i))
	}
	status, body := publicWarmupPost(t, server, "/v1/warmup/force", "", "synthetic-key", nil)
	var summary application.PublicWarmupSummary
	if status != 200 || json.Unmarshal([]byte(body), &summary) != nil || summary.TotalAccounts != 8 || len(summary.Submitted) != 7 || len(summary.Failed) != 1 || calls.Load() != 8 || strings.Contains(body, "private-secret") {
		t.Fatalf("partial warmup summary = %d %s calls=%d", status, body, calls.Load())
	}
	if maximum.Load() > 5 {
		t.Fatalf("fanout exceeded five: %d", maximum.Load())
	}
}

type publicWarmupTestProvider struct {
	application.CodexOperationProvider
	compact func(context.Context, application.CodexOperationTarget, json.RawMessage) (application.CodexOperationResult, error)
}

func (p publicWarmupTestProvider) Compact(ctx context.Context, target application.CodexOperationTarget, body json.RawMessage) (application.CodexOperationResult, error) {
	return p.compact(ctx, target, body)
}

type publicWarmupCountingAdmission struct{ acquired, released atomic.Int64 }

func (a *publicWarmupCountingAdmission) Acquire(context.Context) (func(), error) {
	a.acquired.Add(1)
	return func() { a.released.Add(1) }, nil
}

func TestPublicWarmupCancellationWaitsForAccountingAndReleasesAdmission(t *testing.T) {
	_, store, _ := wireFixtureWithProxy(t, nil)
	entered := make(chan struct{})
	provider := publicWarmupTestProvider{compact: func(ctx context.Context, _ application.CodexOperationTarget, _ json.RawMessage) (application.CodexOperationResult, error) {
		close(entered)
		<-ctx.Done()
		return application.CodexOperationResult{}, ctx.Err()
	}}
	operations := application.NewCodexOperations(store, store, provider, time.Hour)
	admission := &publicWarmupCountingAdmission{}
	operations.ConfigureAdmission(admission)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := operations.PublicWarmup(ctx, application.CodexOperationOptions{KeyID: "wire-key"}, "force")
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("warmup was not dispatched")
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("cancelled warmup error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled warmup did not finish accounting")
	}
	if admission.acquired.Load() != 1 || admission.released.Load() != 1 {
		t.Fatalf("admission leaked: %d/%d", admission.acquired.Load(), admission.released.Load())
	}
	pending, err := store.ListReservationsNeedingReconciliation(context.Background(), "", 10)
	if err != nil || len(pending) != 1 || !pending[0].NeedsReconciliation {
		t.Fatalf("cancelled warmup lost accounting: %+v %v", pending, err)
	}
}

type publicWarmupBlockingStore struct {
	*sqlite.Store
	reserved chan struct{}
	resume   chan struct{}
}

func (s *publicWarmupBlockingStore) ReserveUsage(ctx context.Context, request domain.ReservationRequest) (domain.Reservation, error) {
	reservation, err := s.Store.ReserveUsage(ctx, request)
	if err == nil {
		close(s.reserved)
		<-s.resume
	}
	return reservation, err
}

func TestPublicWarmupRechecksCurrentKeyScopeAfterReservation(t *testing.T) {
	_, store, _ := wireFixtureWithProxy(t, nil)
	ctx := context.Background()
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	key.AccountAssignmentScopeEnabled = true
	key.AssignedAccountIDs = []string{"wire-account"}
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	blocked := &publicWarmupBlockingStore{Store: store, reserved: make(chan struct{}), resume: make(chan struct{})}
	var calls atomic.Int64
	provider := publicWarmupTestProvider{compact: func(context.Context, application.CodexOperationTarget, json.RawMessage) (application.CodexOperationResult, error) {
		calls.Add(1)
		return application.CodexOperationResult{Status: 200, UsageKnown: true}, nil
	}}
	operations := application.NewCodexOperations(blocked, blocked, provider, time.Hour)
	operations.ConfigureAdmission(publicWarmupAdmission{})
	type outcome struct {
		summary application.PublicWarmupSummary
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		summary, err := operations.PublicWarmup(ctx, application.CodexOperationOptions{KeyID: key.ID}, "force")
		done <- outcome{summary, err}
	}()
	select {
	case <-blocked.reserved:
	case <-time.After(5 * time.Second):
		t.Fatal("warmup did not reserve key budget")
	}
	key.AssignedAccountIDs = nil
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	close(blocked.resume)
	select {
	case result := <-done:
		if result.err != nil || len(result.summary.Failed) != 1 || result.summary.Failed[0].ErrorCode != "warmup_scope_changed" || calls.Load() != 0 {
			t.Fatalf("stale key scope dispatched: %+v calls=%d", result, calls.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("warmup did not settle after scope revocation")
	}
	pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("undispatched warmup retained uncertain billing: %+v %v", pending, err)
	}
}
