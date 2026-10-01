package application

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"codex-lb/internal/domain"
)

type OpenAIReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

type OpenAIModelMetadata struct {
	DisplayName                string                       `json:"display_name"`
	Description                string                       `json:"description"`
	ContextWindow              int64                        `json:"context_window"`
	InputContextWindow         int64                        `json:"input_context_window"`
	MaxOutputTokens            *int64                       `json:"max_output_tokens"`
	InputModalities            []string                     `json:"input_modalities"`
	SupportedReasoningLevels   []OpenAIReasoningLevel       `json:"supported_reasoning_levels"`
	DefaultReasoningLevel      *string                      `json:"default_reasoning_level"`
	SupportsReasoningSummaries bool                         `json:"supports_reasoning_summaries"`
	SupportVerbosity           bool                         `json:"support_verbosity"`
	DefaultVerbosity           *string                      `json:"default_verbosity"`
	PreferWebsockets           bool                         `json:"prefer_websockets"`
	SupportsParallelToolCalls  bool                         `json:"supports_parallel_tool_calls"`
	SupportedInAPI             bool                         `json:"supported_in_api"`
	MinimalClientVersion       *string                      `json:"minimal_client_version"`
	Priority                   int                          `json:"priority"`
	AdditionalSpeedTiers       []string                     `json:"additional_speed_tiers,omitempty"`
	ServiceTiers               []map[string]json.RawMessage `json:"service_tiers,omitempty"`
	DefaultServiceTier         *string                      `json:"default_service_tier,omitempty"`
}

type OpenAIModel struct {
	ID                     string                  `json:"id"`
	Object                 string                  `json:"object"`
	Created                int64                   `json:"created"`
	OwnedBy                string                  `json:"owned_by"`
	Metadata               OpenAIModelMetadata     `json:"metadata"`
	Capabilities           OpenAIModelCapabilities `json:"capabilities"`
	APITypes               []string                `json:"api_types"`
	ContextLength          int64                   `json:"context_length"`
	ContextLengthCamel     int64                   `json:"contextLength"`
	MaxOutputTokens        *int64                  `json:"max_output_tokens"`
	MaxOutputTokensCamel   *int64                  `json:"maxOutputTokens"`
	SupportsReasoning      bool                    `json:"supports_reasoning"`
	SupportsReasoningCamel bool                    `json:"supportsReasoning"`
	SupportsImages         bool                    `json:"supports_images"`
	SupportsImagesCamel    bool                    `json:"supportsImages"`
	SupportsVision         bool                    `json:"supports_vision"`
	SupportsVisionCamel    bool                    `json:"supportsVision"`
}

type OpenAIModelCapabilities struct {
	ContextLength       int64    `json:"context_length"`
	MaxOutputTokens     *int64   `json:"max_output_tokens"`
	SupportsReasoning   bool     `json:"supports_reasoning"`
	SupportsImages      bool     `json:"supports_images"`
	SupportsImagesCamel bool     `json:"supportsImages"`
	SupportsVision      bool     `json:"supports_vision"`
	SupportsVisionCamel bool     `json:"supportsVision"`
	SupportsToolUse     bool     `json:"supports_tool_use"`
	SupportsStreaming   bool     `json:"supports_streaming"`
	InputModalities     []string `json:"input_modalities"`
	OutputModalities    []string `json:"output_modalities"`
}

type DashboardModel struct {
	ID                        string   `json:"id"`
	Name                      string   `json:"name"`
	SourceOnly                bool     `json:"sourceOnly"`
	SupportedReasoningEfforts []string `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort    *string  `json:"defaultReasoningEffort"`
}

func (s *ModelCatalogService) discoveryModels() map[string]domain.CatalogModel {
	snapshot := s.Snapshot()
	bootstrap := domain.BootstrapCatalog()
	result := make(map[string]domain.CatalogModel, len(bootstrap))
	if snapshot == nil || snapshot.BootstrapFloorActive {
		for slug, model := range bootstrap {
			if snapshot == nil || !snapshot.IsSuppressed(slug) {
				result[slug] = model.Clone()
			}
		}
	}
	if snapshot != nil {
		for slug, model := range snapshot.Models {
			result[slug] = model.Clone()
		}
	}
	return result
}

func (s *ModelCatalogService) metadataModels() map[string]domain.CatalogModel {
	snapshot := s.Snapshot()
	if snapshot == nil {
		return domain.BootstrapCatalog()
	}
	if snapshot.MetadataModels != nil {
		return snapshot.MetadataModels
	}
	return snapshot.Models
}

func (s *ModelCatalogService) OpenAIModels(ctx context.Context, key domain.APIKey) ([]OpenAIModel, error) {
	models := s.discoveryModels()
	sources, err := s.sourceModels(ctx, key, false)
	if err != nil {
		return nil, err
	}
	allowed := catalogAllowedModels(key)
	now := s.config.Now().Unix()
	result := make([]OpenAIModel, 0, len(models)+len(sources))
	seen := map[string]bool{}
	for slug, model := range models {
		if !model.SupportedInAPI || !catalogModelAllowed(model, allowed) || seen[slug] {
			continue
		}
		result = append(result, openAIModel(slug, model, now))
		seen[slug] = true
	}
	for _, model := range sources {
		if seen[model.Slug] || !model.SupportedInAPI || !catalogModelAllowed(model, allowed) {
			continue
		}
		result = append(result, openAIModel(model.Slug, model, now))
		seen[model.Slug] = true
	}
	sortModelsBySlug(result)
	return result, nil
}

func (s *ModelCatalogService) CodexModels(ctx context.Context, key domain.APIKey) ([]domain.CatalogModel, []OpenAIModel, error) {
	discovery := s.discoveryModels()
	metadata := s.metadataModels()
	sources, err := s.sourceModels(ctx, key, true)
	if err != nil {
		return nil, nil, err
	}
	allowed := catalogAllowedModels(key)
	visibilityAllowed := catalogVisibilityModels(key)
	entries := make([]domain.CatalogModel, 0, len(discovery)+len(metadata)+len(sources))
	data := make([]OpenAIModel, 0, len(discovery))
	seen := map[string]bool{}
	sourceSlugs := map[string]bool{}
	for _, model := range sources {
		if sourceVisibility(model, key, visibilityAllowed, allowed) == "list" {
			sourceSlugs[model.Slug] = true
		}
	}
	effectiveSources := make([]domain.CatalogModel, 0, len(sources))
	sourceBySlug := map[string]domain.CatalogModel{}
	for _, model := range sources {
		existing, exists := sourceBySlug[model.Slug]
		if !exists || existing.Visibility() != "list" && model.Visibility() == "list" {
			if exists {
				for i := range effectiveSources {
					if effectiveSources[i].Slug == model.Slug {
						effectiveSources[i] = model
						break
					}
				}
			} else {
				effectiveSources = append(effectiveSources, model)
			}
			sourceBySlug[model.Slug] = model
		}
	}
	sources = effectiveSources
	for slug, model := range discovery {
		if !catalogBackendModel(model) || !catalogModelAllowed(model, allowed) {
			continue
		}
		entry := model
		if visibilityAllowed != nil {
			setCatalogVisibility(&entry, map[string]bool{slug: visibilityAllowed[slug]})
		}
		entries = append(entries, entry)
		seen[slug] = true
		if entry.SupportedInAPI && entry.Visibility() == "list" {
			data = append(data, openAIModel(slug, model, catalogCreated(model)))
		}
	}
	for slug, model := range metadata {
		if seen[slug] || sourceSlugs[slug] || !catalogBackendModel(model) || !catalogModelAllowed(model, allowed) {
			continue
		}
		setCatalogVisibility(&model, nil)
		entries = append(entries, model)
		seen[slug] = true
	}
	for _, model := range sources {
		if seen[model.Slug] {
			continue
		}
		visibility := sourceVisibility(model, key, visibilityAllowed, allowed)
		entry := model
		setCatalogVisibility(&entry, map[string]bool{model.Slug: visibility == "list"})
		if !catalogModelAllowed(entry, allowed) && visibilityAllowed == nil {
			continue
		}
		entries = append(entries, entry)
		if entry.SupportedInAPI && entry.Visibility() == "list" {
			data = append(data, openAIModel(model.Slug, model, catalogCreated(model)))
		}
	}
	sortCatalogModels(entries)
	sortModelsBySlug(data)
	return entries, data, nil
}

func (s *ModelCatalogService) DashboardModels(ctx context.Context) ([]DashboardModel, error) {
	models := s.discoveryModels()
	result := make([]DashboardModel, 0, len(models))
	for slug, model := range models {
		if !model.SupportedInAPI || model.Visibility() != "list" {
			continue
		}
		result = append(result, dashboardModel(slug, model, false))
	}
	key := domain.APIKey{ID: domain.LocalProxyKeyID}
	sources, err := s.sourceModels(ctx, key, false)
	if err != nil && !errors.Is(err, domain.ErrNoAccounts) {
		return nil, err
	}
	for _, model := range sources {
		result = append(result, dashboardModel(model.Slug, model, true))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (s *ModelCatalogService) sourceModels(ctx context.Context, key domain.APIKey, codex bool) ([]domain.CatalogModel, error) {
	sources, err := s.store.ListModelSources(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := s.store.EligibleAccounts(ctx, key.ID)
	if err != nil && !errors.Is(err, domain.ErrNoAccounts) {
		return nil, err
	}
	available := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		if account.Kind == domain.AccountExternal {
			available[account.ID] = true
		}
	}
	result := make([]domain.CatalogModel, 0)
	for _, source := range sources {
		if !source.Enabled || !available[source.ID] {
			continue
		}
		if key.SourceAssignmentScopeEnabled && !sourceAssignmentAllowed(key, source.ID) {
			continue
		}
		for _, model := range source.Models {
			if !model.Enabled || codex && !model.Streaming {
				continue
			}
			result = append(result, sourceCatalogModel(source, model))
		}
	}
	sortCatalogModels(result)
	return result, nil
}

func sourceCatalogModel(source domain.ModelSource, sourceModel domain.ModelSourceModel) domain.CatalogModel {
	raw := map[string]json.RawMessage{}
	if sourceModel.RawMetadataJSON != "" {
		_ = json.Unmarshal([]byte(sourceModel.RawMetadataJSON), &raw)
	}
	delete(raw, "source_request_overrides")
	contextWindow := sourceModel.ContextWindow
	if contextWindow == 0 {
		contextWindow = 128000
	}
	defaultRaw := map[string]json.RawMessage{
		"visibility": json.RawMessage(`"list"`), "shell_type": json.RawMessage(`"shell_command"`),
		"max_context_window":                json.RawMessage(strconv.FormatInt(contextWindow, 10)),
		"truncation_policy":                 json.RawMessage(`{"mode":"tokens","limit":10000}`),
		"include_skills_usage_instructions": json.RawMessage(`false`),
		"supports_image_detail_original":    json.RawMessage(`false`),
		"supports_search_tool":              json.RawMessage(`false`), "use_responses_lite": json.RawMessage(`false`),
		"experimental_supported_tools": json.RawMessage(`[]`), "supports_streaming": json.RawMessage(strconv.FormatBool(sourceModel.Streaming)),
		"model_provider": json.RawMessage(`"codex-lb"`),
	}
	for key, value := range raw {
		defaultRaw[key] = value
	}
	displayName := sourceModel.DisplayName
	if displayName == "" {
		displayName = sourceModel.Model
	}
	modality := []string{"text"}
	if sourceModel.Vision {
		modality = append(modality, "image")
	}
	reasoningLevels := sourceReasoningLevels(defaultRaw)
	defaultReasoning := sourceDefaultReasoningLevel(defaultRaw, reasoningLevels)
	model := domain.CatalogModel{
		Slug: sourceModel.Model, DisplayName: displayName, Description: displayName,
		ContextWindow: contextWindow, InputModalities: modality, SupportedReasoningLevels: reasoningLevels,
		DefaultReasoningLevel:     defaultReasoning,
		SupportsParallelToolCalls: sourceModel.Tools, SupportedInAPI: true,
		AvailableInPlans: []string{}, Aliases: sourceModel.Aliases, UpstreamModel: sourceModel.UpstreamModel,
		SourceKind: domain.ModelCatalogSourceOpenAICompatible, SourceID: source.ID, Raw: defaultRaw,
	}
	model.SupportsReasoningSummaries = sourceReasoningOptIn(defaultRaw) && rawBool(defaultRaw["supports_reasoning_summaries"])
	if sourceModel.MaxOutputTokens > 0 {
		defaultRaw["max_output_tokens"] = json.RawMessage(strconv.FormatInt(sourceModel.MaxOutputTokens, 10))
	}
	return model
}

func sourceDefaultReasoningLevel(raw map[string]json.RawMessage, levels []domain.CatalogReasoningLevel) string {
	value := rawOptionalString(raw["default_reasoning_level"])
	if value == nil {
		return ""
	}
	for _, level := range levels {
		if level.Effort == strings.ToLower(strings.TrimSpace(*value)) {
			return level.Effort
		}
	}
	return ""
}
