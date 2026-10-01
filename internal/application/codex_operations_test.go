package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

type operationStore struct {
	Accounts
	APIKeys
	Settings
	Continuations
	ModelSourceRepository
	UsageLedger
	AffinityRoutingStore

	accounts map[string]domain.Account
	keys     map[string]domain.APIKey
	eligible []domain.Account
	sources  []domain.ModelSource
	owner    *domain.Continuation
	events   []domain.UsageEvent
	settled  []string
}

func (s *operationStore) GetAccount(_ context.Context, id string) (domain.Account, error) {
	account, ok := s.accounts[id]
	if !ok {
		return domain.Account{}, domain.ErrNotFound
	}
	return account, nil
}
func (s *operationStore) GetAPIKey(_ context.Context, id string) (domain.APIKey, error) {
	key, ok := s.keys[id]
	if !ok {
		return domain.APIKey{}, domain.ErrNotFound
	}
	return key, nil
}
func (s *operationStore) EligibleAccounts(context.Context, string) ([]domain.Account, error) {
	return s.eligible, nil
}
func (s *operationStore) LoadSettings(context.Context) (domain.RuntimeSettings, error) {
	return domain.RuntimeSettings{}, nil
}
func (s *operationStore) GetContinuation(_ context.Context, keyID, id string, _ time.Time) (domain.Continuation, error) {
	if s.owner == nil || s.owner.KeyID != keyID || s.owner.ResponseID != id {
		return domain.Continuation{}, domain.ErrNotFound
	}
	return *s.owner, nil
}
func (s *operationStore) ListModelSources(context.Context) ([]domain.ModelSource, error) {
	return s.sources, nil
}
func (s *operationStore) GetModelSource(_ context.Context, id string) (domain.ModelSource, error) {
	for _, source := range s.sources {
		if source.ID == id {
			return source, nil
		}
	}
	return domain.ModelSource{}, domain.ErrNotFound
}
func (s *operationStore) ScopedAccountForOwner(_ context.Context, _, accountID string, _ time.Time) (domain.Account, error) {
	return s.GetAccount(context.Background(), accountID)
}
func (s *operationStore) ReserveUsage(_ context.Context, request domain.ReservationRequest) (domain.Reservation, error) {
	return domain.Reservation{ID: request.ID, APIKeyID: request.APIKeyID, AccountID: request.AccountID, Model: request.Model}, nil
}
func (s *operationStore) SettleUsage(_ context.Context, id string, settlement domain.UsageSettlement) (bool, error) {
	s.settled = append(s.settled, id)
	s.events = append(s.events, settlement.Event)
	return true, nil
}
func (s *operationStore) RecordCodexContentFreeUsage(_ context.Context, event domain.UsageEvent) error {
	s.events = append(s.events, event)
	return nil
}
func (s *operationStore) RecordAccountOutcome(context.Context, string, string, bool, bool) error {
	return nil
}

type operationOwners struct {
	items map[string]CodexResourceOwner
}

func (o *operationOwners) SaveCodexResourceOwner(_ context.Context, owner CodexResourceOwner) error {
	o.items[owner.ResourceType+"\x00"+owner.KeyID+"\x00"+owner.ResourceID] = owner
	return nil
}
func (o *operationOwners) GetCodexResourceOwner(_ context.Context, resourceType, resourceID, keyID string, _ time.Time) (CodexResourceOwner, error) {
	owner, ok := o.items[resourceType+"\x00"+keyID+"\x00"+resourceID]
	if !ok {
		return CodexResourceOwner{}, domain.ErrNotFound
	}
	return owner, nil
}

type operationProvider struct {
	targets []CodexOperationTarget
	bodies  []json.RawMessage
	results map[string]CodexOperationResult
}

func (p *operationProvider) Compact(_ context.Context, target CodexOperationTarget, body json.RawMessage) (CodexOperationResult, error) {
	p.targets, p.bodies = append(p.targets, target), append(p.bodies, body)
	return p.results["compact"], nil
}
func (p *operationProvider) Control(_ context.Context, target CodexOperationTarget, _ CodexControlRequest) (CodexOperationResult, error) {
	p.targets = append(p.targets, target)
	return p.results["control"], nil
}
func (p *operationProvider) CreateFile(_ context.Context, target CodexOperationTarget, body json.RawMessage) (CodexOperationResult, error) {
	p.targets, p.bodies = append(p.targets, target), append(p.bodies, body)
	return p.results["create"], nil
}
func (p *operationProvider) FinalizeFile(_ context.Context, target CodexOperationTarget, _ string) (CodexOperationResult, error) {
	p.targets = append(p.targets, target)
	return p.results["finalize"], nil
}
func (p *operationProvider) Transcribe(_ context.Context, target CodexOperationTarget, _ CodexTranscriptionRequest) (CodexOperationResult, error) {
	p.targets = append(p.targets, target)
	return p.results["transcribe"], nil
}
func (p *operationProvider) Realtime(_ context.Context, target CodexOperationTarget, _ CodexRealtimeRequest, _ CodexRealtimeConnection) error {
	p.targets = append(p.targets, target)
	return nil
}

func operationFixture() (*CodexOperations, *operationProvider, *operationOwners, *operationStore) {
	now := time.Now().UTC()
	first := domain.Account{ID: "acct-a", Kind: domain.AccountChatGPT, Provider: "openai", Status: domain.AccountActive, CreatedAt: now}
	second := first
	second.ID = "acct-b"
	store := &operationStore{
		accounts: map[string]domain.Account{"acct-a": first, "acct-b": second},
		keys: map[string]domain.APIKey{"key": {
			ID: "key", IsActive: true, AllowedModels: []string{"gpt-6-sol"}, CreatedAt: now,
		}},
		eligible: []domain.Account{first, second},
	}
	provider := &operationProvider{results: map[string]CodexOperationResult{
		"compact":    {Status: 200, UsageKnown: true, Body: []byte(`{"object":"response.compact"}`), ContentType: "application/json"},
		"create":     {Status: 200, Body: []byte(`{"file_id":"file-1","upload_url":"https://storage"}`), ContentType: "application/json"},
		"finalize":   {Status: 200, Body: []byte(`{"status":"success"}`), ContentType: "application/json"},
		"transcribe": {Status: 200, UsageKnown: true, Body: []byte(`{"text":"ok"}`), ContentType: "application/json"},
	}}
	owners := &operationOwners{items: map[string]CodexResourceOwner{}}
	service := NewCodexOperations(store, owners, provider, time.Hour)
	service.ConfigureAdmission(immediateAdmission{})
	service.selectCompact = func(context.Context, string, *responseRequest, domain.RuntimeSettings, domain.Account, map[string]bool, ...string) (domain.Account, *accountLease, error) {
		return store.eligible[0], nil, nil
	}
	return service, provider, owners, store
}

type immediateAdmission struct{}

func (immediateAdmission) Acquire(context.Context) (func(), error) { return func() {}, nil }

func TestCodexCompactUsesConversationAndFileOwner(t *testing.T) {
	service, provider, owners, store := operationFixture()
	store.owner = &domain.Continuation{ResponseID: sessionLookupID("session"), KeyID: "key", AccountID: "acct-a", ProviderID: "openai", Model: "gpt-6-sol", ExpiresAt: time.Now().Add(time.Hour)}
	owners.items["file\x00key\x00file-1"] = CodexResourceOwner{AccountID: "acct-a"}
	body := mustOperationJSON(map[string]any{
		"model": "gpt-6-sol", "instructions": "summarize", "input": []any{map[string]any{"type": "input_file", "file_id": "file-1"}},
		"max_output_tokens": 100, "service_tier": "priority",
	})
	result, err := service.Compact(context.Background(), "key", "session", "turn-state", body)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != 200 || provider.targets[0].Account.ID != "acct-a" || provider.targets[0].TurnState != "turn-state" {
		t.Fatalf("compact owner/result mismatch: %+v %+v", result, provider.targets)
	}
	var sent map[string]json.RawMessage
	if json.Unmarshal(provider.bodies[0], &sent) != nil || string(sent["store"]) != "false" ||
		sent["max_output_tokens"] != nil || sent["service_tier"] == nil {
		t.Fatalf("compact wire shape changed: %s", provider.bodies[0])
	}
	owners.items["file\x00key\x00file-1"] = CodexResourceOwner{AccountID: "acct-b"}
	if _, err = service.Compact(context.Background(), "key", "session", "", body); err == nil || !strings.Contains(err.Error(), "differ") {
		t.Fatalf("conversation/file owner conflict was accepted: %v", err)
	}
	unknownFileBody := mustOperationJSON(map[string]any{
		"model": "gpt-6-sol", "instructions": "summarize", "input": []any{map[string]any{"type": "input_file", "file_id": "file-unknown"}},
	})
	if _, err = service.Compact(context.Background(), "key", "", "", unknownFileBody); err == nil {
		t.Fatal("unknown file owner was silently accepted for a new conversation")
	}
}

func TestCodexTriggerCompactRejectsMissingSummaryAfterOneSettlement(t *testing.T) {
	service, provider, _, store := operationFixture()
	provider.results["compact"] = CodexOperationResult{Status: 200, UsageKnown: true, Body: []byte(`{"object":"response.compact","output":[{"type":"message","content":"old text"}]}`)}
	_, err := service.CompactWithOptions(context.Background(), CodexOperationOptions{KeyID: "key", CodexResponsesTrigger: true}, []byte(`{"model":"gpt-6-sol","input":[{"role":"user","content":"hi"},{"type":"compaction_trigger"}]}`))
	if err == nil || !strings.Contains(err.Error(), "invalid_upstream_response") || len(provider.targets) != 1 || len(store.settled) != 1 || len(store.events) != 1 || store.events[0].Status != "error" {
		t.Fatalf("invalid compact output was not settled as one failure: err=%v calls=%d settlements=%d events=%+v", err, len(provider.targets), len(store.settled), store.events)
	}
}

func TestCodexFileCreateAndFinalizeKeepOwner(t *testing.T) {
	service, provider, owners, _ := operationFixture()
	_, err := service.CreateFile(context.Background(), "key", "", mustOperationJSON(map[string]any{"file_name": "shot.png", "file_size": 4, "use_case": "codex"}))
	if err != nil {
		t.Fatal(err)
	}
	owner := owners.items["file\x00key\x00file-1"]
	if owner.AccountID != provider.targets[0].Account.ID || owner.AccountID == "" {
		t.Fatalf("file owner was not persisted: %+v", owner)
	}
	if _, err = service.FinalizeFile(context.Background(), "key", "file-1"); err != nil {
		t.Fatal(err)
	}
	if provider.targets[1].Account.ID != owner.AccountID {
		t.Fatalf("finalize changed owner: %+v", provider.targets)
	}
	if _, err = service.FinalizeFile(context.Background(), "key", "missing"); err == nil {
		t.Fatal("unknown file owner was accepted")
	}
}

func TestCodexTranscriptionUsesConfiguredSourceAndKeyPolicy(t *testing.T) {
	service, provider, _, store := operationFixture()
	external := domain.Account{ID: "src-audio", Kind: domain.AccountExternal, Provider: "openai_compatible", Status: domain.AccountActive}
	store.eligible = []domain.Account{external}
	key := store.keys["key"]
	key.AllowedModels = []string{"whisper-x"}
	store.keys["key"] = key
	store.sources = []domain.ModelSource{{
		ID: "src-audio", Kind: domain.ModelSourceOpenAICompatible, BaseURL: "http://127.0.0.1:1/v1",
		Enabled: true, Chat: true, Audio: true, Models: []domain.ModelSourceModel{{Model: "whisper-x", Enabled: true, Streaming: true}},
	}}
	result, err := service.Transcribe(context.Background(), "key", "", CodexTranscriptionRequest{Model: "whisper-x", Audio: []byte("audio")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != 200 || provider.targets[0].Account.ID != "src-audio" {
		t.Fatalf("transcription source mismatch: %+v %+v", result, provider.targets)
	}
	if _, err = service.Transcribe(context.Background(), "key", "", CodexTranscriptionRequest{Model: "other-model", Audio: []byte("audio")}); err == nil {
		t.Fatal("key model policy was bypassed")
	}
}

func TestCodexControlUsesSessionOwner(t *testing.T) {
	service, provider, _, store := operationFixture()
	store.owner = &domain.Continuation{ResponseID: sessionLookupID("session"), KeyID: "key", AccountID: "acct-b", ProviderID: "openai", ExpiresAt: time.Now().Add(time.Hour)}
	_, err := service.Control(context.Background(), "key", "session", "", CodexControlRequest{Method: "POST", Path: "thread/goal/set", Body: []byte(`{"goal":"x"}`), ContentType: "application/json"})
	if err != nil {
		t.Fatal(err)
	}
	if provider.targets[0].Account.ID != "acct-b" {
		t.Fatalf("control changed owner: %+v", provider.targets)
	}
	if _, err = service.Control(context.Background(), "key", "", "opaque", CodexControlRequest{Method: "POST", Path: "thread/goal/set"}); err == nil {
		t.Fatal("unowned turn-state was accepted")
	}
}

func TestCodexOperationsRejectHeldOwnersFromOldIncarnation(t *testing.T) {
	service, provider, owners, store := operationFixture()
	fresh := store.accounts["acct-a"]
	fresh.Generation = 1
	store.accounts[fresh.ID] = fresh
	store.eligible[0] = fresh
	store.owner = &domain.Continuation{ResponseID: sessionLookupID("session"), KeyID: "key",
		AccountID: fresh.ID, AccountGeneration: 0, ProviderID: fresh.Provider, ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := service.Control(context.Background(), "key", "session", "", CodexControlRequest{
		Method: "POST", Path: "thread/goal/set",
	}); err == nil || len(provider.targets) != 0 {
		t.Fatalf("old session used fresh account: err=%v calls=%d", err, len(provider.targets))
	}
	store.owner = nil
	owners.items["file\x00key\x00file-old"] = CodexResourceOwner{AccountID: fresh.ID, AccountGeneration: 0}
	if _, err := service.FinalizeFile(context.Background(), "key", "file-old"); err == nil || len(provider.targets) != 0 {
		t.Fatalf("old file used fresh account: err=%v calls=%d", err, len(provider.targets))
	}
	owners.items["realtime_call\x00key\x00call-old"] = CodexResourceOwner{AccountID: fresh.ID, AccountGeneration: 0}
	if _, err := service.AuthorizeRealtime(context.Background(), CodexOperationOptions{KeyID: "key"}, "call-old"); err == nil || len(provider.targets) != 0 {
		t.Fatalf("old realtime call used fresh account: err=%v calls=%d", err, len(provider.targets))
	}
	if _, err := service.CreateFile(context.Background(), "key", "", mustOperationJSON(map[string]any{
		"file_name": "new.txt", "file_size": 4, "use_case": "codex",
	})); err != nil {
		t.Fatal(err)
	}
	if owner := owners.items["file\x00key\x00file-1"]; owner.AccountGeneration != 1 || store.events[0].AccountGeneration != 1 {
		t.Fatalf("new account generation was not captured: owner=%+v event=%+v", owner, store.events[0])
	}
}

func mustOperationJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
