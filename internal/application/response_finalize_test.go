package application_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// writeCounter counts every durable write port the proxy can use per attempt.
type writeCounter struct {
	*sqlite.Store
	mu     sync.Mutex
	writes map[string]int
}

func (s *writeCounter) count(name string) {
	s.mu.Lock()
	s.writes[name]++
	s.mu.Unlock()
}

func (s *writeCounter) ReserveUsage(ctx context.Context, r domain.ReservationRequest) (domain.Reservation, error) {
	s.count("ReserveUsage")
	return s.Store.ReserveUsage(ctx, r)
}
func (s *writeCounter) SettleUsage(ctx context.Context, id string, st domain.UsageSettlement) (bool, error) {
	s.count("SettleUsage")
	return s.Store.SettleUsage(ctx, id, st)
}
func (s *writeCounter) MarkReservationUncertain(ctx context.Context, id string) (bool, error) {
	s.count("MarkReservationUncertain")
	return s.Store.MarkReservationUncertain(ctx, id)
}
func (s *writeCounter) RecordAccountOutcome(ctx context.Context, a, r string, q, ok bool) error {
	s.count("RecordAccountOutcome")
	return s.Store.RecordAccountOutcome(ctx, a, r, q, ok)
}
func (s *writeCounter) SaveContinuation(ctx context.Context, c domain.Continuation, b domain.ContinuationBounds) error {
	s.count("SaveContinuation")
	return s.Store.SaveContinuation(ctx, c, b)
}
func (s *writeCounter) SaveSessionContinuation(ctx context.Context, c domain.Continuation, r string, b domain.ContinuationBounds) error {
	s.count("SaveSessionContinuation")
	return s.Store.SaveSessionContinuation(ctx, c, r, b)
}
func (s *writeCounter) MarkContinuationQuotaRefused(ctx context.Context, k, r, a, res string) error {
	s.count("MarkContinuationQuotaRefused")
	return s.Store.MarkContinuationQuotaRefused(ctx, k, r, a, res)
}
func (s *writeCounter) FinalizeResponse(ctx context.Context, f domain.ResponseFinalization) (domain.ResponseFinalizationResult, error) {
	s.count("FinalizeResponse")
	return s.Store.FinalizeResponse(ctx, f)
}

// A fresh streamed turn with session, turn-state and prompt-cache identities
// commits exactly once before dispatch and once before its terminal.
func TestResponseCommitsOnceBeforeDispatchAndOnceAfterTerminal(t *testing.T) {
	_, store, stub, vault := proxyFixtureParts(t)
	counter := &writeCounter{Store: store, writes: map[string]int{}}
	proxy := application.NewProxy(counter, stub, vault, application.ProxyConfig{MaxStreams: 1, MaxQueued: 1})
	var events []string
	result, err := proxy.Respond(context.Background(), application.ResponseOptions{KeyID: "key-test", SessionID: "session", TurnState: "turn", SynthesizedTurnState: true},
		json.RawMessage(`{"model":"gpt-6-sol","prompt_cache_key":"cache","input":"hello","stream":true}`),
		func(event application.ResponseEvent) error { events = append(events, event.Type); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"ReserveUsage": 1, "FinalizeResponse": 1}; len(counter.writes) != len(want) ||
		counter.writes["ReserveUsage"] != 1 || counter.writes["FinalizeResponse"] != 1 {
		t.Fatalf("durable writes per turn: %v", counter.writes)
	}
	ctx := context.Background()
	for _, id := range []string{result.ResponseID} {
		if _, err := store.GetContinuation(ctx, "key-test", id, time.Now()); err != nil {
			t.Fatalf("owner %s: %v", id, err)
		}
	}
	if len(events) == 0 || events[len(events)-1] != "response.completed" {
		t.Fatalf("terminal not delivered after finalization: %v", events)
	}
}

// When ownership cannot be persisted the client must not see the terminal,
// because a following previous_response_id would be unknown; the bill stays.
func TestOwnershipFailureWithholdsTerminalButKeepsSettlement(t *testing.T) {
	proxy, store, stub := proxyFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	taken := domain.Continuation{ResponseID: "resp_taken", KeyID: "key-test", AccountID: "account-a", ProviderID: "openai",
		Model: "another-model", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := store.SaveContinuation(ctx, taken, domain.ContinuationBounds{MaxRecords: 100, MaxContextBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	stub.respond = func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return complete("resp_taken", emit)
	}
	var events []string
	_, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, json.RawMessage(`{"model":"gpt-6-sol","input":"hello","stream":true}`),
		func(event application.ResponseEvent) error { events = append(events, event.Type); return nil })
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("ownership failure not reported: %v", err)
	}
	for _, event := range events {
		if event == "response.completed" {
			t.Fatalf("terminal delivered without durable ownership: %v", events)
		}
	}
	totals, err := store.UsageTotals(ctx, "key-test", "")
	if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 10 {
		t.Fatalf("settlement lost with ownership: %+v %v", totals, err)
	}
}

// A remembered session alias points at its response's stored context instead
// of a second encrypted copy.
func TestSessionAliasSharesResponseContext(t *testing.T) {
	proxy, store, _ := proxyFixture(t)
	ctx := context.Background()
	body := json.RawMessage(`{"model":"gpt-6-sol","input":[{"type":"message","role":"user","content":"` + strings.Repeat("x", 64<<10) + `"}]}`)
	result, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test", SessionID: "s1"}, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetContinuation(ctx, "key-test", result.ResponseID, time.Now())
	if err != nil || len(owner.ContextEncrypted) == 0 {
		t.Fatalf("owner context missing: %v", err)
	}
	alias, err := store.GetContinuation(ctx, "key-test", fmt.Sprintf("session:%x", sha256.Sum256([]byte("s1"))), time.Now())
	if err != nil || alias.ContextResponseID != result.ResponseID || !bytes.Equal(alias.ContextEncrypted, owner.ContextEncrypted) {
		t.Fatalf("alias does not share the owner's context: ref=%q %v", alias.ContextResponseID, err)
	}
}
