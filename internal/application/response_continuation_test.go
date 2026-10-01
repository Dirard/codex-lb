package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type continuationMarkProbe struct {
	*sqlite.Store
	marked chan domain.Continuation
}

type heldIncarnationStore struct {
	*sqlite.Store
	owner      domain.Continuation
	file       application.CodexResourceOwner
	generation int64
	revision   int64
}

func (s *heldIncarnationStore) GetContinuation(ctx context.Context, keyID, responseID string, now time.Time) (domain.Continuation, error) {
	if responseID == s.owner.ResponseID {
		return s.owner, nil
	}
	return s.Store.GetContinuation(ctx, keyID, responseID, now)
}

func (s *heldIncarnationStore) GetAccount(ctx context.Context, id string) (domain.Account, error) {
	account, err := s.Store.GetAccount(ctx, id)
	account.Generation, account.RouteRevision = s.generation, s.revision
	return account, err
}

func (s *heldIncarnationStore) ScopedAccountForOwner(ctx context.Context, keyID, accountID string, now time.Time) (domain.Account, error) {
	account, err := s.Store.ScopedAccountForOwner(ctx, keyID, accountID, now)
	account.Generation, account.RouteRevision = s.generation, s.revision
	return account, err
}

func (s *heldIncarnationStore) GetCodexResourceOwner(ctx context.Context, resourceType, resourceID, keyID string, now time.Time) (application.CodexResourceOwner, error) {
	if resourceID == s.file.ResourceID {
		return s.file, nil
	}
	return s.Store.GetCodexResourceOwner(ctx, resourceType, resourceID, keyID, now)
}

func TestHeldResponseAndFileOwnersCannotUseChangedAccount(t *testing.T) {
	for _, test := range []struct {
		name                 string
		generation, revision int64
	}{{"reimport", 1, 0}, {"route edit", 0, 1}} {
		t.Run(test.name, func(t *testing.T) {
			_, store, provider, cipher := proxyFixtureParts(t)
			stale := &heldIncarnationStore{Store: store, generation: test.generation, revision: test.revision,
				owner: domain.Continuation{ResponseID: "response-old", KeyID: "key-test", AccountID: "account-a",
					ProviderID: "openai", Model: "gpt-6-sol", ExpiresAt: time.Now().Add(time.Hour)},
				file: application.CodexResourceOwner{ResourceType: application.CodexResourceFile,
					ResourceID: "file-old", KeyID: "key-test", AccountID: "account-a", ExpiresAt: time.Now().Add(time.Hour)},
			}
			proxy := application.NewProxy(stale, provider, cipher, application.ProxyConfig{})
			ctx := context.Background()
			if _, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"},
				[]byte(`{"model":"gpt-6-sol","previous_response_id":"response-old","input":"continue"}`), nil); !staleOwnerError(err) {
				t.Fatalf("held response owner used changed account: %v", err)
			}
			if _, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"},
				[]byte(`{"model":"gpt-6-sol","input":[{"role":"user","content":[{"type":"input_file","file_id":"file-old"}]}]}`), nil); !staleOwnerError(err) {
				t.Fatalf("held file owner used changed account: %v", err)
			}
			if len(provider.accounts) != 0 {
				t.Fatalf("provider was called for stale owners: %v", provider.accounts)
			}
		})
	}
}

func staleOwnerError(err error) bool {
	var failure *application.ProxyError
	return errors.As(err, &failure) && failure.Code == "previous_response_owner_unavailable"
}

func (s *continuationMarkProbe) MarkContinuationQuotaRefused(ctx context.Context, keyID, responseID, accountID, reservationID string) error {
	if err := s.Store.MarkContinuationQuotaRefused(ctx, keyID, responseID, accountID, reservationID); err != nil {
		return err
	}
	marked, err := s.Store.GetContinuation(ctx, keyID, responseID, time.Now())
	if err != nil {
		return err
	}
	s.marked <- marked
	return nil
}

func TestSessionAliasMovesOnlyAfterQuotaFailover(t *testing.T) {
	proxy, store, stub := proxyFixture(t)
	ctx := context.Background()
	options := application.ResponseOptions{KeyID: "key-test", SessionID: "logical-session"}
	first, err := proxy.Respond(ctx, options, []byte(`{"model":"gpt-6-sol","input":"original"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetContinuation(ctx, options.KeyID, first.ResponseID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	stub.respond = func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		calls++
		if target.Account.ID == owner.AccountID {
			return application.ResponseResult{}, &application.ProviderFailure{Code: "usage_limit_reached", Status: 429, QuotaRefused: true, Dispatched: true}
		}
		return complete(fmt.Sprintf("after_switch_%d", calls), emit)
	}
	body := []byte(fmt.Sprintf(`{"model":"gpt-6-sol","previous_response_id":%q,"input":"continue"}`, first.ResponseID))
	second, err := proxy.Respond(ctx, options, body, nil)
	if err != nil || calls != 2 {
		t.Fatalf("quota switch failed: %v calls=%d", err, calls)
	}
	nextOwner, err := store.GetContinuation(ctx, options.KeyID, second.ResponseID, time.Now())
	if err != nil || nextOwner.AccountID == owner.AccountID {
		t.Fatal("response did not switch")
	}
	// Make the former owner eligible: the next full-context turn must still stay
	// on the new session owner, not be freshly balanced or stuck on the old one.
	account, err := store.GetAccount(ctx, owner.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	account.Status = domain.AccountActive
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	calls = 0
	if _, err := proxy.Respond(ctx, options, []byte(`{"model":"gpt-6-sol","input":"full context after switch"}`), nil); err != nil || calls != 1 {
		t.Fatalf("session alias did not follow new owner: %v calls=%d", err, calls)
	}
}

func TestStaleQuotaRefusalDoesNotMarkNewerSameAccountSession(t *testing.T) {
	_, store, oldStub, cipher := proxyFixtureParts(t)
	newerProvider := &responseStub{}
	newerProvider.respond = func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return complete("newer_same_account", emit)
	}
	newerProxy := application.NewProxy(store, newerProvider, cipher, application.ProxyConfig{MaxStreams: 1, MaxQueued: 1})
	probe := &continuationMarkProbe{Store: store, marked: make(chan domain.Continuation, 1)}
	oldProxy := application.NewProxy(probe, oldStub, cipher, application.ProxyConfig{MaxStreams: 1, MaxQueued: 1})
	ctx := context.Background()
	options := application.ResponseOptions{KeyID: "key-test", SessionID: "logical-session"}
	if _, err := oldProxy.Respond(ctx, options, []byte(`{"model":"gpt-6-sol","input":"initial"}`), nil); err != nil {
		t.Fatal(err)
	}
	oldStub.respond = func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, _ func(application.ResponseEvent) error) (application.ResponseResult, error) {
		if target.Account.ID != "account-a" {
			return complete("stale_refusal_failover", nil)
		}
		if _, err := newerProxy.Respond(ctx, options, []byte(`{"model":"gpt-6-sol","input":"newer generation"}`), nil); err != nil {
			return application.ResponseResult{}, err
		}
		return application.ResponseResult{}, &application.ProviderFailure{Code: "usage_limit_reached", Status: 429, QuotaRefused: true, Dispatched: true}
	}
	result, err := oldProxy.Respond(ctx, options, []byte(`{"model":"gpt-6-sol","input":"stale generation"}`), nil)
	if err != nil || result.ResponseID != "stale_refusal_failover" {
		t.Fatalf("stale refusal blocked failover: %+v, %v", result, err)
	}
	select {
	case marked := <-probe.marked:
		if marked.AccountID != "account-a" || marked.QuotaRefused {
			t.Fatalf("stale refusal marked newer session: %+v", marked)
		}
	default:
		t.Fatal("quota refusal was not fenced")
	}
}

func TestEvictedHistoryNeverBecomesReplayableTail(t *testing.T) {
	proxy, store, _ := proxyFixture(t)
	ctx := context.Background()
	owner := domain.Continuation{ResponseID: "missing-local-history", KeyID: "key-test", AccountID: "account-a", ProviderID: "openai", Model: "gpt-6-sol", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), FilePinned: true}
	if err := store.SaveContinuation(ctx, owner, domain.ContinuationBounds{MaxRecords: 10, MaxContextBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	result, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, []byte(`{"model":"gpt-6-sol","previous_response_id":"missing-local-history","input":"small tail"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetContinuation(ctx, "key-test", result.ResponseID, time.Now())
	if err != nil || len(saved.ContextEncrypted) != 0 || !saved.FilePinned {
		t.Fatalf("evicted history or file owner was forgotten: %v", err)
	}
}
