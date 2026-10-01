package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

const ModelCatalogSchemaVersion = 1

type ModelCatalogStore interface {
	ListAccounts(context.Context) ([]domain.Account, error)
	GetAccountCredential(context.Context, string) (domain.AccountCredential, error)
	ListModelSources(context.Context) ([]domain.ModelSource, error)
	EligibleAccounts(context.Context, string) ([]domain.Account, error)
	LoadModelCatalogSnapshot(context.Context) (domain.ModelCatalogRecord, error)
	SaveModelCatalogSnapshot(context.Context, domain.ModelCatalogRecord, ...domain.Account) error
}

type ModelCatalogFetcher interface {
	Fetch(ctx context.Context, account domain.Account, accessToken string) ([]domain.CatalogModel, error)
}

type ModelCatalogTokenSource interface {
	AccessToken(context.Context, domain.Account, domain.AccountCredential) (string, error)
	ForceRefresh(context.Context, domain.Account, string) (string, error)
}

type ModelCatalogConfig struct {
	SnapshotTTL  time.Duration
	RefreshEvery time.Duration
	Now          func() time.Time
}

type ModelCatalogService struct {
	store    ModelCatalogStore
	fetcher  ModelCatalogFetcher
	tokens   ModelCatalogTokenSource
	config   ModelCatalogConfig
	mu       sync.RWMutex
	snapshot *domain.CatalogSnapshot
	refresh  chan struct{}
}

func NewModelCatalogService(store ModelCatalogStore, fetcher ModelCatalogFetcher, tokens ModelCatalogTokenSource, config ModelCatalogConfig) *ModelCatalogService {
	if config.SnapshotTTL <= 0 {
		config.SnapshotTTL = 24 * time.Hour
	}
	if config.RefreshEvery <= 0 {
		config.RefreshEvery = 5 * time.Minute
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &ModelCatalogService{store: store, fetcher: fetcher, tokens: tokens, config: config, refresh: make(chan struct{}, 1)}
}

// Load applies a fresh persisted snapshot without waiting for network I/O.
func (s *ModelCatalogService) Load(ctx context.Context) error {
	record, err := s.store.LoadModelCatalogSnapshot(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if record.SchemaVersion == ModelCatalogSchemaVersion && !record.RefreshedAt.IsZero() &&
		s.config.Now().Sub(record.RefreshedAt) <= s.config.SnapshotTTL {
		s.snapshot = record.Snapshot
	} else {
		s.snapshot = nil
	}
	s.mu.Unlock()
	return nil
}

func (s *ModelCatalogService) Snapshot() *domain.CatalogSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snapshot == nil {
		return nil
	}
	return s.snapshot.Clone()
}

// RequestRefresh coalesces settings changes into the owned catalog poller.
func (s *ModelCatalogService) RequestRefresh() {
	select {
	case s.refresh <- struct{}{}:
	default:
	}
}

func (s *ModelCatalogService) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.config.RefreshEvery)
	defer ticker.Stop()
	for {
		_ = s.Refresh(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-s.refresh:
		}
	}
}

func (s *ModelCatalogService) Refresh(ctx context.Context) error {
	now := s.config.Now().UTC()
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return err
	}
	active := make([]domain.Account, 0, len(accounts))
	credentials := make(map[string]domain.AccountCredential, len(accounts))
	for _, account := range accounts {
		if account.Kind != domain.AccountChatGPT || account.Status != domain.AccountActive || account.RequiresEgressDecision {
			continue
		}
		credential, err := s.store.GetAccountCredential(ctx, account.ID)
		if err != nil {
			continue
		}
		active = append(active, account)
		credentials[account.ID] = credential
	}
	if len(active) == 0 {
		record := domain.ModelCatalogRecord{SchemaVersion: ModelCatalogSchemaVersion, RefreshedAt: now, ContentHash: catalogHash(nil), Snapshot: nil}
		if err := s.store.SaveModelCatalogSnapshot(ctx, record); err != nil {
			return err
		}
		s.mu.Lock()
		s.snapshot = nil
		s.mu.Unlock()
		return nil
	}

	results := make(map[string]catalogAccountResult, len(active))
	var failures []error
	for _, account := range active {
		models, err := s.fetch(ctx, account, credentials[account.ID])
		if err != nil {
			failures = append(failures, fmt.Errorf("account %s: %w", account.ID, err))
			continue
		}
		results[account.ID] = catalogAccountResult{plan: account.PlanType, models: models}
	}
	if len(results) == 0 {
		return errors.Join(failures...)
	}

	s.mu.RLock()
	previous := s.snapshot
	s.mu.RUnlock()
	snapshot := buildCatalogSnapshot(previous, active, results, now)
	record := domain.ModelCatalogRecord{SchemaVersion: ModelCatalogSchemaVersion, RefreshedAt: now, ContentHash: catalogHash(snapshot), Snapshot: snapshot}
	if err := s.store.SaveModelCatalogSnapshot(ctx, record, active...); err != nil {
		return err
	}
	s.mu.Lock()
	s.snapshot = snapshot
	s.mu.Unlock()
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	return nil
}

func (s *ModelCatalogService) fetch(ctx context.Context, account domain.Account, credential domain.AccountCredential) ([]domain.CatalogModel, error) {
	token, err := s.tokens.AccessToken(ctx, account, credential)
	if err != nil {
		return nil, err
	}
	models, err := s.fetcher.Fetch(ctx, account, token)
	var fetchErr *domain.CatalogFetchError
	if errors.As(err, &fetchErr) && fetchErr.AuthRejected() {
		token, err = s.tokens.ForceRefresh(ctx, account, token)
		if err != nil {
			return nil, err
		}
		models, err = s.fetcher.Fetch(ctx, account, token)
	}
	return models, err
}

type catalogAccountResult struct {
	plan   string
	models []domain.CatalogModel
}

func buildCatalogSnapshot(previous *domain.CatalogSnapshot, active []domain.Account, current map[string]catalogAccountResult, now time.Time) *domain.CatalogSnapshot {
	effective := make(map[string]catalogAccountResult, len(active))
	for id, result := range current {
		effective[id] = result
	}
	for _, account := range active {
		if _, ok := effective[account.ID]; ok || previous == nil {
			continue
		}
		oldPlan, covered := previous.AccountPlans[account.ID]
		if !covered || oldPlan != account.PlanType {
			continue
		}
		models := make([]domain.CatalogModel, 0)
		for slug, accountIDs := range previous.ModelAccounts {
			if slices.Contains(accountIDs, account.ID) {
				if model, ok := previous.Models[slug]; ok {
					models = append(models, model)
				}
			}
		}
		sort.Slice(models, func(i, j int) bool { return models[i].Slug < models[j].Slug })
		effective[account.ID] = catalogAccountResult{plan: account.PlanType, models: models}
	}

	snapshot := &domain.CatalogSnapshot{
		Models: map[string]domain.CatalogModel{}, MetadataModels: map[string]domain.CatalogModel{},
		ModelPlans: map[string][]string{}, ModelAccounts: map[string][]string{},
		ModelTierPlans: map[string]map[string][]string{}, ModelTierAccounts: map[string]map[string][]string{},
		AccountPlans: map[string]string{}, FetchedAt: now,
	}
	accountIDs := make([]string, 0, len(effective))
	for id, result := range effective {
		snapshot.AccountPlans[id] = result.plan
		accountIDs = append(accountIDs, id)
	}
	sort.Strings(accountIDs)
	for _, id := range accountIDs {
		result := effective[id]
		for _, incoming := range result.models {
			model := incoming.Clone()
			incomingTiers := incoming.ServiceTiers()
			if existing, ok := snapshot.Models[model.Slug]; ok {
				model = mergeCatalogTierMetadata(existing, model)
			}
			snapshot.Models[model.Slug] = model
			snapshot.ModelAccounts[model.Slug] = appendUnique(snapshot.ModelAccounts[model.Slug], id)
			snapshot.ModelPlans[model.Slug] = appendUnique(snapshot.ModelPlans[model.Slug], result.plan)
			for tier := range incomingTiers {
				if snapshot.ModelTierPlans[model.Slug] == nil {
					snapshot.ModelTierPlans[model.Slug] = map[string][]string{}
				}
				if snapshot.ModelTierAccounts[model.Slug] == nil {
					snapshot.ModelTierAccounts[model.Slug] = map[string][]string{}
				}
				snapshot.ModelTierPlans[model.Slug][tier] = appendUnique(snapshot.ModelTierPlans[model.Slug][tier], result.plan)
				snapshot.ModelTierAccounts[model.Slug][tier] = appendUnique(snapshot.ModelTierAccounts[model.Slug][tier], id)
			}
		}
	}

	suppressed := make([]string, 0)
	if previous != nil {
		for slug, advertisers := range previous.ModelAccounts {
			if len(advertisers) == 0 {
				continue
			}
			if _, live := snapshot.Models[slug]; !live {
				suppressed = appendUnique(suppressed, strings.ToLower(slug))
			}
		}
		for _, slug := range previous.SuppressedModels {
			if _, live := snapshot.Models[slug]; !live {
				suppressed = appendUnique(suppressed, slug)
			}
		}
	}
	complete := len(effective) == len(active)
	if complete {
		for slug := range domain.BootstrapCatalog() {
			if _, live := snapshot.Models[slug]; !live {
				suppressed = appendUnique(suppressed, slug)
			}
		}
	}
	snapshot.AccountCatalogsComplete = complete
	snapshot.BootstrapFloorActive = !complete
	snapshot.SuppressedModels = suppressed

	bootstrap := domain.BootstrapCatalog()
	metadata := make(map[string]domain.CatalogModel, len(bootstrap))
	for slug, model := range bootstrap {
		metadata[slug] = model
	}
	if previous != nil {
		for slug, model := range previous.MetadataModels {
			if _, bundled := bootstrap[slug]; bundled {
				metadata[slug] = model
			}
		}
	}
	for _, id := range accountIDs {
		for _, model := range effective[id].models {
			if _, bundled := bootstrap[model.Slug]; bundled {
				metadata[model.Slug] = model.Clone()
			}
		}
	}
	for slug, model := range snapshot.Models {
		metadata[slug] = model
	}
	snapshot.MetadataModels = metadata
	return snapshot
}

func mergeCatalogTierMetadata(existing, incoming domain.CatalogModel) domain.CatalogModel {
	merged := incoming.Clone()
	merged.Raw = mergeRawObjects(existing.Raw, incoming.Raw, "additional_speed_tiers")
	merged.Raw = mergeRawObjects(existing.Raw, merged.Raw, "service_tiers")
	if len(merged.Raw["default_service_tier"]) == 0 || string(merged.Raw["default_service_tier"]) == "null" {
		if value := existing.Raw["default_service_tier"]; len(value) > 0 && string(value) != "null" {
			merged.Raw["default_service_tier"] = value
		}
	}
	return merged
}

func mergeRawObjects(primary, secondary map[string]json.RawMessage, key string) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(secondary)+len(primary))
	for k, v := range secondary {
		result[k] = v
	}
	var primaryValues, secondaryValues []any
	_ = json.Unmarshal(primary[key], &primaryValues)
	_ = json.Unmarshal(secondary[key], &secondaryValues)
	seen := make(map[string]bool, len(primaryValues)+len(secondaryValues))
	merged := make([]any, 0, len(primaryValues)+len(secondaryValues))
	for _, value := range append(primaryValues, secondaryValues...) {
		encoded, _ := json.Marshal(value)
		identity := string(encoded)
		if object, ok := value.(map[string]any); ok {
			for _, field := range []string{"slug", "name", "id", "tier"} {
				if text, ok := object[field].(string); ok && text != "" {
					identity = "tier:" + domain.CanonicalServiceTier(text)
					break
				}
			}
		}
		if !seen[identity] {
			seen[identity] = true
			merged = append(merged, value)
		}
	}
	if len(merged) != 0 {
		encoded, _ := json.Marshal(merged)
		result[key] = encoded
	}
	return result
}

func appendUnique(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func catalogHash(snapshot *domain.CatalogSnapshot) string {
	var encoded []byte
	if snapshot == nil {
		encoded = []byte("null")
	} else {
		encoded, _ = json.Marshal(snapshot)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
