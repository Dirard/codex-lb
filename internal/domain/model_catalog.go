package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ModelCatalogSourceKind string

const (
	ModelCatalogSourceSubscription     ModelCatalogSourceKind = "subscription"
	ModelCatalogSourceOpenAICompatible ModelCatalogSourceKind = "openai_compatible"
)

type CatalogReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

type CatalogModel struct {
	Slug                       string                     `json:"slug"`
	DisplayName                string                     `json:"display_name"`
	Description                string                     `json:"description"`
	BaseInstructions           string                     `json:"base_instructions"`
	ContextWindow              int64                      `json:"context_window"`
	InputModalities            []string                   `json:"input_modalities"`
	SupportedReasoningLevels   []CatalogReasoningLevel    `json:"supported_reasoning_levels"`
	DefaultReasoningLevel      string                     `json:"default_reasoning_level,omitempty"`
	SupportsReasoningSummaries bool                       `json:"supports_reasoning_summaries"`
	SupportVerbosity           bool                       `json:"support_verbosity"`
	DefaultVerbosity           string                     `json:"default_verbosity,omitempty"`
	PreferWebsockets           bool                       `json:"prefer_websockets"`
	SupportsParallelToolCalls  bool                       `json:"supports_parallel_tool_calls"`
	SupportedInAPI             bool                       `json:"supported_in_api"`
	MinimalClientVersion       string                     `json:"minimal_client_version,omitempty"`
	Priority                   int                        `json:"priority"`
	AvailableInPlans           []string                   `json:"available_in_plans"`
	Aliases                    []string                   `json:"aliases,omitempty"`
	UpstreamModel              string                     `json:"upstream_model,omitempty"`
	SourceKind                 ModelCatalogSourceKind     `json:"source_kind"`
	SourceID                   string                     `json:"source_id,omitempty"`
	Raw                        map[string]json.RawMessage `json:"_catalog_raw,omitempty"`
}

type CatalogSnapshot struct {
	Models                  map[string]CatalogModel        `json:"models"`
	MetadataModels          map[string]CatalogModel        `json:"metadata_models"`
	ModelPlans              map[string][]string            `json:"model_plans"`
	ModelAccounts           map[string][]string            `json:"model_accounts"`
	ModelTierPlans          map[string]map[string][]string `json:"model_tier_plans"`
	ModelTierAccounts       map[string]map[string][]string `json:"model_tier_accounts"`
	AccountPlans            map[string]string              `json:"account_plans"`
	FetchedAt               time.Time                      `json:"fetched_at"`
	AccountCatalogsComplete bool                           `json:"account_catalogs_complete"`
	BootstrapFloorActive    bool                           `json:"bootstrap_floor_active"`
	SuppressedModels        []string                       `json:"suppressed_models"`
}

type ModelCatalogRecord struct {
	SchemaVersion int              `json:"schema_version"`
	RefreshedAt   time.Time        `json:"refreshed_at"`
	ContentHash   string           `json:"content_hash"`
	Snapshot      *CatalogSnapshot `json:"snapshot"`
}

type CatalogFetchError struct {
	Status int
	Err    error
}

func (e *CatalogFetchError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("catalog fetch failed with status %d", e.Status)
}

func (e *CatalogFetchError) Unwrap() error { return e.Err }

func (e *CatalogFetchError) AuthRejected() bool { return e.Status == 401 }

func ParseCatalogModel(raw map[string]json.RawMessage) (CatalogModel, error) {
	var model CatalogModel
	if err := decodeRaw(raw["slug"], &model.Slug); err != nil || strings.TrimSpace(model.Slug) == "" {
		return CatalogModel{}, ErrInvalid
	}
	_ = decodeRaw(raw["display_name"], &model.DisplayName)
	_ = decodeRaw(raw["description"], &model.Description)
	_ = decodeRaw(raw["base_instructions"], &model.BaseInstructions)
	_ = decodeRaw(raw["context_window"], &model.ContextWindow)
	_ = decodeRaw(raw["input_modalities"], &model.InputModalities)
	_ = decodeRaw(raw["supported_reasoning_levels"], &model.SupportedReasoningLevels)
	_ = decodeRaw(raw["default_reasoning_level"], &model.DefaultReasoningLevel)
	_ = decodeRaw(raw["supports_reasoning_summaries"], &model.SupportsReasoningSummaries)
	_ = decodeRaw(raw["support_verbosity"], &model.SupportVerbosity)
	_ = decodeRaw(raw["default_verbosity"], &model.DefaultVerbosity)
	_ = decodeRaw(raw["prefer_websockets"], &model.PreferWebsockets)
	_ = decodeRaw(raw["supports_parallel_tool_calls"], &model.SupportsParallelToolCalls)
	model.SupportedInAPI = true
	_ = decodeRaw(raw["supported_in_api"], &model.SupportedInAPI)
	_ = decodeRaw(raw["minimal_client_version"], &model.MinimalClientVersion)
	_ = decodeRaw(raw["priority"], &model.Priority)
	_ = decodeRaw(raw["available_in_plans"], &model.AvailableInPlans)
	model.SourceKind = ModelCatalogSourceSubscription
	model.Raw = make(map[string]json.RawMessage, len(raw))
	for key, value := range raw {
		model.Raw[key] = append(json.RawMessage(nil), value...)
	}
	if model.DisplayName == "" {
		model.DisplayName = model.Slug
	}
	if model.Description == "" {
		model.Description = model.DisplayName
	}
	if model.InputModalities == nil {
		model.InputModalities = []string{}
	}
	if model.SupportedReasoningLevels == nil {
		model.SupportedReasoningLevels = []CatalogReasoningLevel{}
	}
	if model.AvailableInPlans == nil {
		model.AvailableInPlans = []string{}
	}
	return model, nil
}

func (m CatalogModel) Clone() CatalogModel {
	m.InputModalities = append([]string(nil), m.InputModalities...)
	m.SupportedReasoningLevels = append([]CatalogReasoningLevel(nil), m.SupportedReasoningLevels...)
	m.AvailableInPlans = append([]string(nil), m.AvailableInPlans...)
	m.Aliases = append([]string(nil), m.Aliases...)
	raw := make(map[string]json.RawMessage, len(m.Raw))
	for key, value := range m.Raw {
		raw[key] = append(json.RawMessage(nil), value...)
	}
	m.Raw = raw
	return m
}

func CanonicalServiceTier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "fast" {
		return "priority"
	}
	return value
}

func OmitEquivalentServiceTier(value string) bool {
	switch CanonicalServiceTier(value) {
	case "", "auto", "default":
		return true
	default:
		return false
	}
}

func (m CatalogModel) ServiceTiers() map[string]bool {
	result := make(map[string]bool)
	add := func(value string) {
		if canonical := CanonicalServiceTier(value); canonical != "" {
			result[canonical] = true
		}
	}
	var values []string
	_ = decodeRaw(m.Raw["additional_speed_tiers"], &values)
	for _, value := range values {
		add(value)
	}
	var entries []map[string]json.RawMessage
	_ = decodeRaw(m.Raw["service_tiers"], &entries)
	for _, entry := range entries {
		for _, key := range []string{"slug", "name", "id", "tier"} {
			var value string
			if decodeRaw(entry[key], &value) == nil && value != "" {
				add(value)
			}
		}
	}
	var value string
	if decodeRaw(m.Raw["default_service_tier"], &value) == nil {
		add(value)
	}
	return result
}

func (m CatalogModel) Visibility() string {
	var visibility string
	if decodeRaw(m.Raw["visibility"], &visibility) != nil || visibility == "" {
		return "list"
	}
	return visibility
}

func (s *CatalogSnapshot) IsSuppressed(model string) bool {
	if s == nil {
		return false
	}
	model = strings.ToLower(strings.TrimSpace(model))
	for _, slug := range s.SuppressedModels {
		if slug == model {
			return true
		}
	}
	return false
}

func (s *CatalogSnapshot) Clone() *CatalogSnapshot {
	if s == nil {
		return nil
	}
	copied := *s
	copied.Models = cloneCatalogModels(s.Models)
	copied.MetadataModels = cloneCatalogModels(s.MetadataModels)
	copied.ModelPlans = cloneStringLists(s.ModelPlans)
	copied.ModelAccounts = cloneStringLists(s.ModelAccounts)
	copied.ModelTierPlans = cloneTierLists(s.ModelTierPlans)
	copied.ModelTierAccounts = cloneTierLists(s.ModelTierAccounts)
	copied.AccountPlans = make(map[string]string, len(s.AccountPlans))
	for key, value := range s.AccountPlans {
		copied.AccountPlans[key] = value
	}
	copied.SuppressedModels = append([]string(nil), s.SuppressedModels...)
	return &copied
}

func cloneCatalogModels(models map[string]CatalogModel) map[string]CatalogModel {
	result := make(map[string]CatalogModel, len(models))
	for key, value := range models {
		result[key] = value.Clone()
	}
	return result
}

func cloneStringLists(values map[string][]string) map[string][]string {
	result := make(map[string][]string, len(values))
	for key, value := range values {
		result[key] = append([]string(nil), value...)
	}
	return result
}

func cloneTierLists(values map[string]map[string][]string) map[string]map[string][]string {
	result := make(map[string]map[string][]string, len(values))
	for key, value := range values {
		result[key] = cloneStringLists(value)
	}
	return result
}

func (s *CatalogSnapshot) AccountSupportsModel(accountID, model string) (known bool, supported bool) {
	if s == nil {
		return false, false
	}
	if ids, ok := lookupCatalogList(s.ModelAccounts, model); ok {
		return true, containsString(ids, accountID)
	}
	return false, false
}

func (s *CatalogSnapshot) AccountSupportsTier(accountID, model, tier string) (known bool, supported bool) {
	if s == nil {
		return false, false
	}
	if _, knownModel := lookupCatalogList(s.ModelAccounts, model); !knownModel {
		return false, false
	}
	tier = CanonicalServiceTier(tier)
	if tier == "" {
		return true, true
	}
	if tiers, ok := s.ModelTierAccounts[model]; ok {
		if ids, ok := tiers[tier]; ok {
			return true, containsString(ids, accountID)
		}
	}
	return true, false
}

func (s *CatalogSnapshot) PlanSupportsModel(plan, model string) (known bool, supported bool) {
	if s == nil {
		return false, false
	}
	if plans, ok := lookupCatalogList(s.ModelPlans, model); ok {
		return true, containsString(plans, plan)
	}
	return false, false
}

func lookupCatalogList(values map[string][]string, key string) ([]string, bool) {
	if value, ok := values[key]; ok {
		return value, true
	}
	value, ok := values[strings.ToLower(strings.TrimSpace(key))]
	return value, ok
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func decodeRaw(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return ErrNotFound
	}
	return json.Unmarshal(raw, dst)
}
