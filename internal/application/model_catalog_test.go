package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

type catalogStore struct {
	accounts    []domain.Account
	credentials map[string]domain.AccountCredential
	sources     []domain.ModelSource
	eligible    []domain.Account
	loaded      domain.ModelCatalogRecord
	saved       []domain.ModelCatalogRecord
}

func (s *catalogStore) ListAccounts(context.Context) ([]domain.Account, error) {
	return s.accounts, nil
}
func (s *catalogStore) GetAccountCredential(_ context.Context, id string) (domain.AccountCredential, error) {
	if credential, ok := s.credentials[id]; ok {
		return credential, nil
	}
	return domain.AccountCredential{}, domain.ErrNotFound
}
func (s *catalogStore) ListModelSources(context.Context) ([]domain.ModelSource, error) {
	return s.sources, nil
}
func (s *catalogStore) EligibleAccounts(context.Context, string) ([]domain.Account, error) {
	if len(s.eligible) == 0 {
		return nil, domain.ErrNoAccounts
	}
	return s.eligible, nil
}
func (s *catalogStore) LoadModelCatalogSnapshot(context.Context) (domain.ModelCatalogRecord, error) {
	return s.loaded, nil
}
func (s *catalogStore) SaveModelCatalogSnapshot(_ context.Context, record domain.ModelCatalogRecord, expected ...domain.Account) error {
	for _, account := range expected {
		current, exists := s.account(account.ID)
		if !exists || current.Generation != account.Generation || current.Status == domain.AccountDeactivated {
			return domain.ErrConflict
		}
	}
	s.saved = append(s.saved, record)
	return nil
}

func (s *catalogStore) account(id string) (domain.Account, bool) {
	for _, account := range s.accounts {
		if account.ID == id {
			return account, true
		}
	}
	return domain.Account{}, false
}

type catalogFetcher struct {
	models  map[string][]domain.CatalogModel
	fail    map[string]bool
	onFetch func(domain.Account)
}

func (f catalogFetcher) Fetch(_ context.Context, account domain.Account, _ string) ([]domain.CatalogModel, error) {
	if f.onFetch != nil {
		f.onFetch(account)
	}
	if f.fail[account.ID] {
		return nil, errors.New("offline")
	}
	return f.models[account.ID], nil
}

type catalogTokens struct{}

func (catalogTokens) AccessToken(context.Context, domain.Account, domain.AccountCredential) (string, error) {
	return "access", nil
}
func (catalogTokens) ForceRefresh(context.Context, domain.Account, string) (string, error) {
	return "refreshed", nil
}

func catalogModelFromJSON(t *testing.T, payload string) domain.CatalogModel {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		t.Fatal(err)
	}
	model, err := domain.ParseCatalogModel(raw)
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func catalogFixture(t *testing.T) (*ModelCatalogService, *catalogStore) {
	t.Helper()
	store := &catalogStore{
		accounts: []domain.Account{
			{ID: "a", Kind: domain.AccountChatGPT, PlanType: "pro", Status: domain.AccountActive},
			{ID: "b", Kind: domain.AccountChatGPT, PlanType: "pro", Status: domain.AccountActive},
		},
		credentials: map[string]domain.AccountCredential{
			"a": {AccountID: "a", AccessTokenEncrypted: []byte("a")},
			"b": {AccountID: "b", AccessTokenEncrypted: []byte("b")},
		},
	}
	fetcher := catalogFetcher{models: map[string][]domain.CatalogModel{
		"a": {catalogModelFromJSON(t, `{"slug":"gpt-x","display_name":"GPT X","context_window":100,"available_in_plans":["pro"],"model_messages":{"keep":true},"service_tiers":[{"id":"priority","name":"Fast"}],"additional_speed_tiers":["fast"],"experimental_supported_tools":["web_search",2],"truncation_policy":{"mode":"tokens","limit":10}}`)},
		"b": {catalogModelFromJSON(t, `{"slug":"gpt-x","display_name":"GPT X","context_window":100,"available_in_plans":["pro"]}`),
			catalogModelFromJSON(t, `{"slug":"private","display_name":"Private","context_window":100,"available_in_plans":["pro"]}`)},
	}}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	service := NewModelCatalogService(store, fetcher, catalogTokens{}, ModelCatalogConfig{Now: func() time.Time { return now }})
	return service, store
}

func TestModelCatalogRefreshMergesAccountsAndPersists(t *testing.T) {
	service, store := catalogFixture(t)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot := service.Snapshot()
	if !snapshot.AccountCatalogsComplete || snapshot.BootstrapFloorActive {
		t.Fatalf("snapshot was not authoritative: %+v", snapshot)
	}
	if !reflect.DeepEqual(snapshot.ModelAccounts["gpt-x"], []string{"a", "b"}) {
		t.Fatalf("model accounts = %#v", snapshot.ModelAccounts["gpt-x"])
	}
	if got := snapshot.ModelAccounts["private"]; !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("private accounts = %#v", got)
	}
	if got := snapshot.ModelTierAccounts["gpt-x"]["priority"]; !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("priority accounts = %#v", got)
	}
	var tiers []any
	if err := json.Unmarshal(snapshot.Models["gpt-x"].Raw["service_tiers"], &tiers); err != nil || len(tiers) != 1 {
		t.Fatalf("service tiers were not deduplicated: %s %v", snapshot.Models["gpt-x"].Raw["service_tiers"], err)
	}
	if len(store.saved) != 1 || !reflect.DeepEqual(store.saved[0].Snapshot, snapshot) {
		t.Fatal("refresh was not persisted")
	}
}

func TestModelCatalogSettingsRefreshUsesOwnedPoller(t *testing.T) {
	service, store := catalogFixture(t)
	service.config.RefreshEvery = time.Hour
	fetched := make(chan struct{}, 2)
	fetcher := service.fetcher.(catalogFetcher)
	fetcher.onFetch = func(account domain.Account) {
		if account.ID == "a" {
			fetched <- struct{}{}
		}
	}
	service.fetcher = fetcher
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runErr error
	go func() { runErr = service.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitForFetch := func() {
		t.Helper()
		select {
		case <-fetched:
		case <-time.After(2 * time.Second):
			t.Fatal("catalog did not refresh")
		}
	}
	waitForFetch()
	service.RequestRefresh()
	waitForFetch()
	// Read persisted state only after the owned poller has joined.
	cancel()
	<-done
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("catalog poller stop: %v", runErr)
	}
	if len(store.saved) != 2 {
		t.Fatalf("settings refresh count = %d", len(store.saved))
	}
}

func TestModelCatalogStaleRefreshCannotPublishAfterReimport(t *testing.T) {
	service, store := catalogFixture(t)
	fetcher := service.fetcher.(catalogFetcher)
	fetcher.onFetch = func(account domain.Account) {
		if account.ID != "a" {
			return
		}
		store.accounts[0].Generation = 1
		store.credentials["a"] = domain.AccountCredential{AccountID: "a", Generation: 1, AccessTokenEncrypted: []byte("new")}
	}
	service.fetcher = fetcher
	if err := service.Refresh(context.Background()); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale refresh published = %v", err)
	}
	if service.Snapshot() != nil || len(store.saved) != 0 {
		t.Fatal("reimported account received stale catalog cache")
	}
}

func TestModelCatalogSnapshotAndNilPolicyAreDefensive(t *testing.T) {
	service, _ := catalogFixture(t)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	visible := service.Snapshot()
	visible.Models["gpt-x"].Raw["service_tiers"] = json.RawMessage(`[]`)
	visible.ModelAccounts["gpt-x"] = []string{"changed"}
	if string(service.Snapshot().Models["gpt-x"].Raw["service_tiers"]) == `[]` ||
		reflect.DeepEqual(service.Snapshot().ModelAccounts["gpt-x"], []string{"changed"}) {
		t.Fatal("Snapshot exposed mutable registry maps")
	}
	empty := NewModelCatalogService(&catalogStore{}, nil, nil, ModelCatalogConfig{})
	result := empty.FilterAccounts(ModelCatalogSelection{Model: "gpt-x", Candidates: []domain.Account{{ID: "a", Kind: domain.AccountChatGPT, PlanType: "pro"}}})
	if len(result.Candidates) != 1 {
		t.Fatalf("nil snapshot policy failed: %+v", result)
	}
}

func TestModelCatalogPartialFailureRetainsSamePlanCoverage(t *testing.T) {
	service, _ := catalogFixture(t)
	service.fetcher = catalogFetcher{fail: map[string]bool{"b": true}, models: service.fetcher.(catalogFetcher).models}
	if err := service.Refresh(context.Background()); err == nil {
		t.Fatal("partial failure was hidden")
	}
	snapshot := service.Snapshot()
	if snapshot.AccountCatalogsComplete || !snapshot.BootstrapFloorActive {
		t.Fatalf("partial snapshot became authoritative: %+v", snapshot)
	}
	service.fetcher = catalogFetcher{fail: map[string]bool{"a": true}, models: service.fetcher.(catalogFetcher).models}
	if err := service.Refresh(context.Background()); err == nil {
		t.Fatal("second partial failure was hidden")
	}
	snapshot = service.Snapshot()
	if !snapshot.AccountCatalogsComplete {
		t.Fatalf("retained same-plan catalog did not complete coverage: %+v", snapshot)
	}
	if !reflect.DeepEqual(snapshot.ModelAccounts["gpt-x"], []string{"a", "b"}) {
		t.Fatalf("retained model accounts = %#v", snapshot.ModelAccounts["gpt-x"])
	}
}

func TestModelCatalogLoadHonorsFreshnessAndSchema(t *testing.T) {
	service, _ := catalogFixture(t)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	fresh := service.Snapshot()
	store := &catalogStore{loaded: domain.ModelCatalogRecord{SchemaVersion: ModelCatalogSchemaVersion, RefreshedAt: service.config.Now(), Snapshot: fresh}}
	loaded := NewModelCatalogService(store, nil, nil, ModelCatalogConfig{Now: service.config.Now})
	if err := loaded.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Snapshot(), fresh) {
		t.Fatal("fresh snapshot was not applied")
	}
	store.loaded.RefreshedAt = service.config.Now().Add(-25 * time.Hour)
	if err := loaded.Load(context.Background()); err != nil || loaded.Snapshot() != nil {
		t.Fatalf("expired snapshot was applied: %+v %v", loaded.Snapshot(), err)
	}
	store.loaded.RefreshedAt = service.config.Now()
	store.loaded.SchemaVersion = ModelCatalogSchemaVersion + 1
	if err := loaded.Load(context.Background()); err != nil || loaded.Snapshot() != nil {
		t.Fatalf("future snapshot was applied: %+v %v", loaded.Snapshot(), err)
	}
}

func TestModelCatalogPolicyDistinguishesTierOriginAndOwners(t *testing.T) {
	service, _ := catalogFixture(t)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	candidates := []domain.Account{
		{ID: "a", Kind: domain.AccountChatGPT, PlanType: "pro"},
		{ID: "b", Kind: domain.AccountChatGPT, PlanType: "pro"},
	}
	explicit := service.FilterAccounts(ModelCatalogSelection{Model: "gpt-x", ServiceTier: "fast", Candidates: candidates})
	if len(explicit.Candidates) != 1 || explicit.Candidates[0].ID != "a" || explicit.EffectiveTier != "fast" {
		t.Fatalf("explicit fast selection = %+v", explicit)
	}
	unknownTier := catalogModelFromJSON(t, `{"slug":"no-tier","context_window":1,"available_in_plans":["pro"]}`)
	service.snapshot.Models["no-tier"] = unknownTier
	service.snapshot.ModelAccounts["no-tier"] = []string{"a", "b"}
	enforced := service.FilterAccounts(ModelCatalogSelection{Model: "no-tier", ServiceTier: "priority", TierEnforced: true, Candidates: candidates})
	if len(enforced.Candidates) != 2 || enforced.EffectiveTier != "" {
		t.Fatalf("unadvertised enforced tier was not removed: %+v", enforced)
	}
	client := service.FilterAccounts(ModelCatalogSelection{Model: "no-tier", ServiceTier: "priority", Candidates: candidates})
	if len(client.Candidates) != 0 || client.EffectiveTier != "priority" {
		t.Fatalf("unadvertised explicit tier was not rejected: %+v", client)
	}
	owner := service.FilterAccounts(ModelCatalogSelection{Model: "gpt-x", Candidates: []domain.Account{{ID: "c", Kind: domain.AccountChatGPT, PlanType: "pro"}}, EstablishedOwnerID: "c"})
	if len(owner.Candidates) != 0 {
		t.Fatalf("catalog mismatch rebalanced an owner: %+v", owner)
	}
	service.snapshot.ModelTierAccounts["gpt-x"]["priority"] = []string{"b"}
	owner = service.FilterAccounts(ModelCatalogSelection{Model: "gpt-x", ServiceTier: "priority", Candidates: []domain.Account{{ID: "a", Kind: domain.AccountChatGPT, PlanType: "pro"}}, EstablishedOwnerID: "a"})
	if len(owner.Candidates) != 0 || owner.EffectiveTier != "priority" {
		t.Fatalf("owner tier mismatch was not refused: %+v", owner)
	}
}

func TestModelCatalogSuppressionDoesNotTreatUnknownMappingAsSuppressed(t *testing.T) {
	service, _ := catalogFixture(t)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	service.snapshot.SuppressedModels = append(service.snapshot.SuppressedModels, "gpt-x")
	result := service.FilterAccounts(ModelCatalogSelection{Model: "operator-model", Candidates: []domain.Account{{ID: "a", Kind: domain.AccountChatGPT, PlanType: "pro"}}})
	if len(result.Candidates) != 1 {
		t.Fatalf("unknown operator mapping was suppressed: %+v", result)
	}
	external := domain.Account{ID: "source", Kind: domain.AccountExternal, Provider: "openai_compatible"}
	result = service.FilterAccounts(ModelCatalogSelection{Model: "gpt-x", ServiceTier: "priority", Candidates: []domain.Account{external}})
	if len(result.Candidates) != 1 || result.Candidates[0].ID != external.ID || result.EffectiveTier != "priority" {
		t.Fatalf("subscription suppression affected an external source: %+v", result)
	}
}

func TestModelCatalogNilSnapshotOwnerKeepsUnknownTier(t *testing.T) {
	service := NewModelCatalogService(&catalogStore{}, nil, nil, ModelCatalogConfig{})
	result := service.FilterAccounts(ModelCatalogSelection{Model: "operator-model", ServiceTier: "priority",
		Candidates:         []domain.Account{{ID: "owner", Kind: domain.AccountChatGPT, PlanType: "pro"}},
		EstablishedOwnerID: "owner"})
	if len(result.Candidates) != 1 || result.EffectiveTier != "priority" {
		t.Fatalf("nil snapshot changed owner fallback: %+v", result)
	}
}
