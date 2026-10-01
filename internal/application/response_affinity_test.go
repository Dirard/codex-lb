package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

type affinityRoutingStub struct {
	binding         domain.AffinityBinding
	lookupErr       error
	lookupCount     int
	lookupKey       string
	lookupKind      domain.AffinityKind
	lookupKeyID     string
	saved           domain.AffinityBinding
	expectedVersion int64
	reservationID   string
	saveChanged     bool
	saveErr         error
}

func (stub *affinityRoutingStub) LookupAffinity(_ context.Context, keyID string, kind domain.AffinityKind, key string) (domain.AffinityBinding, error) {
	stub.lookupCount++
	stub.lookupKeyID, stub.lookupKind, stub.lookupKey = keyID, kind, key
	if stub.lookupErr != nil {
		return domain.AffinityBinding{}, stub.lookupErr
	}
	return stub.binding, nil
}

func (stub *affinityRoutingStub) SaveAffinity(_ context.Context, binding domain.AffinityBinding, expectedVersion int64, reservationID string) (bool, error) {
	stub.saved, stub.expectedVersion, stub.reservationID = binding, expectedVersion, reservationID
	return stub.saveChanged, stub.saveErr
}

func TestRequestAffinityExplicitCacheKeyIsScopedAndIndependentOfStickySetting(t *testing.T) {
	request := responseRequest{Object: map[string]json.RawMessage{
		"promptCacheKey": json.RawMessage(`"cache-hint"`),
	}}
	store := &affinityRoutingStub{lookupErr: domain.ErrNotFound}
	first, err := lookupRequestAffinity(context.Background(), store, "key-one", conversationIdentity{}, "", request, domain.RuntimeSettings{})
	if err != nil || first == nil || first.preferredAccountID() != "" {
		t.Fatalf("explicit hint unavailable: %v", err)
	}
	if store.lookupKind != domain.AffinityPromptCache || store.lookupKeyID != "key-one" || strings.Contains(store.lookupKey, "cache-hint") {
		t.Fatalf("wrong or raw cache identity: %+v", store)
	}
	firstKey := store.lookupKey
	_, err = lookupRequestAffinity(context.Background(), store, "key-two", conversationIdentity{}, "", request, domain.RuntimeSettings{})
	if err != nil || store.lookupKey == firstKey {
		t.Fatalf("different API keys shared a binding: %v", err)
	}
	request.Object = map[string]json.RawMessage{"prompt_cache_key": json.RawMessage(`"cache-hint"`)}
	_, err = lookupRequestAffinity(context.Background(), store, "key-one", conversationIdentity{}, "", request, domain.RuntimeSettings{})
	if err != nil || store.lookupKey != firstKey {
		t.Fatalf("canonical and camel aliases differed: %v", err)
	}
	store.lookupCount = 0
	request.Object = nil
	if affinity, err := lookupRequestAffinity(context.Background(), store, "key-one", conversationIdentity{}, "", request, domain.RuntimeSettings{}); err != nil || affinity != nil || store.lookupCount != 0 {
		t.Fatalf("disabled automatic affinity performed lookup: %v", err)
	}
}

func TestRequestAffinityThreadSessionClientAndFingerprintClassification(t *testing.T) {
	settings := domain.RuntimeSettings{StickyThreadsEnabled: true, OpenAICacheAffinityMaxAgeSeconds: 1800}
	request := responseRequest{Model: "gpt-6-sol", Object: map[string]json.RawMessage{"instructions": json.RawMessage(`"be brief"`)},
		Input: []json.RawMessage{json.RawMessage(`{"role":"assistant","content":"prior"}`), json.RawMessage(`{"role":"user","content":"sensitive prompt"}`)}}
	store := &affinityRoutingStub{lookupErr: domain.ErrNotFound}
	lookup := func(identity conversationIdentity, client string) (domain.AffinityKind, string) {
		t.Helper()
		if _, err := lookupRequestAffinity(context.Background(), store, "key", identity, client, request, settings); err != nil {
			t.Fatal(err)
		}
		return store.lookupKind, store.lookupKey
	}
	threadKind, threadKey := lookup(conversationIdentity{SessionID: "process-a", ThreadID: "thread-1"}, "client")
	if threadKind != domain.AffinityStickyThread || strings.Contains(threadKey, "thread-1") {
		t.Fatalf("thread affinity was not opaque: %s %s", threadKind, threadKey)
	}
	_, otherProcess := lookup(conversationIdentity{SessionID: "process-b", ThreadID: "thread-1"}, "client")
	_, otherThread := lookup(conversationIdentity{SessionID: "process-a", ThreadID: "thread-2"}, "client")
	if threadKey == otherProcess || threadKey == otherThread {
		t.Fatal("process/thread tuples collided")
	}
	sessionKind, sessionKey := lookup(conversationIdentity{SessionID: "process-a"}, "client")
	clientKind, clientKey := lookup(conversationIdentity{}, "process-a")
	if sessionKind != domain.AffinityCodexSession || clientKind != domain.AffinityStickyThread || sessionKey == clientKey {
		t.Fatal("process session and client hint were conflated")
	}
	fingerprintKind, fingerprintKey := lookup(conversationIdentity{}, "")
	if fingerprintKind != domain.AffinityPromptCache || strings.Contains(fingerprintKey, "sensitive prompt") {
		t.Fatal("fingerprint did not use opaque prompt-cache namespace")
	}
	request.Input[1] = json.RawMessage(`{"role":"user","content":"different prompt"}`)
	_, changedKey := lookup(conversationIdentity{}, "")
	if changedKey == fingerprintKey {
		t.Fatal("first user message did not affect fingerprint")
	}
}

func TestRequestAffinityTTLAndCompareAndSetSave(t *testing.T) {
	request := responseRequest{Object: map[string]json.RawMessage{"prompt_cache_key": json.RawMessage(`"test"`)}}
	store := &affinityRoutingStub{binding: domain.AffinityBinding{AccountID: "old", Version: 7, UpdatedAt: time.Now().Add(-time.Hour)}}
	affinity, err := lookupRequestAffinity(context.Background(), store, "key", conversationIdentity{}, "", request, domain.RuntimeSettings{OpenAICacheAffinityMaxAgeSeconds: 60})
	if err != nil || affinity.preferredAccountID() != "" {
		t.Fatalf("expired prompt-cache pin was used: %v", err)
	}
	if err := affinity.save(context.Background(), store, "new", "reservation"); err != nil {
		t.Fatal(err)
	}
	if store.expectedVersion != 7 || store.reservationID != "reservation" || store.saved.AccountID != "new" || store.saved.APIKeyID != "key" || store.saved.Kind != domain.AffinityPromptCache || store.saved.Key == "" {
		t.Fatalf("save did not retain observed CAS version and reservation: %+v", store)
	}
	store.binding.UpdatedAt = time.Now()
	affinity, err = lookupRequestAffinity(context.Background(), store, "key", conversationIdentity{}, "", request, domain.RuntimeSettings{OpenAICacheAffinityMaxAgeSeconds: 60})
	if err != nil || affinity.preferredAccountID() != "old" {
		t.Fatalf("fresh cache pin ignored: %v", err)
	}
	store.binding.UpdatedAt = time.Now().Add(-24 * time.Hour)
	request.Object = nil
	affinity, err = lookupRequestAffinity(context.Background(), store, "key", conversationIdentity{SessionID: "session"}, "", request, domain.RuntimeSettings{StickyThreadsEnabled: true, OpenAICacheAffinityMaxAgeSeconds: 60})
	if err != nil || affinity.preferredAccountID() != "old" {
		t.Fatalf("durable session pin incorrectly expired: %v", err)
	}
	store.saveErr = errors.New("write failed")
	if err := affinity.save(context.Background(), store, "new", "reservation"); !errors.Is(err, store.saveErr) {
		t.Fatalf("save failure was hidden: %v", err)
	}
	if err := (*requestAffinity)(nil).save(context.Background(), store, "new", "reservation"); err != nil {
		t.Fatal(err)
	}
}

func TestRequestAffinityRejectsMalformedHints(t *testing.T) {
	store := &affinityRoutingStub{lookupErr: domain.ErrNotFound}
	for _, object := range []map[string]json.RawMessage{
		{"prompt_cache_key": json.RawMessage(`42`)},
		{"prompt_cache_key": json.RawMessage(`"first"`), "promptCacheKey": json.RawMessage(`"second"`)},
		{"prompt_cache_key": json.RawMessage(`"` + strings.Repeat("x", maxAffinityHintBytes+1) + `"`)},
	} {
		_, err := lookupRequestAffinity(context.Background(), store, "key", conversationIdentity{}, "", responseRequest{Object: object}, domain.RuntimeSettings{})
		var proxyErr *ProxyError
		if !errors.As(err, &proxyErr) || proxyErr.Status != 400 {
			t.Fatalf("malformed hint accepted: %v", err)
		}
	}
	if store.lookupCount != 0 {
		t.Fatal("malformed hint reached storage")
	}
}
