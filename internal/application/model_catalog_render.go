package application

import (
	"encoding/json"
	"sort"
	"strings"

	"codex-lb/internal/domain"
)

func sourceReasoningOptIn(raw map[string]json.RawMessage) bool {
	return rawBool(raw["supports_reasoning"])
}

func rawBool(raw json.RawMessage) bool {
	var value bool
	return json.Unmarshal(raw, &value) == nil && value
}

func sourceReasoningLevels(raw map[string]json.RawMessage) []domain.CatalogReasoningLevel {
	var declared []json.RawMessage
	if json.Unmarshal(raw["supported_reasoning_levels"], &declared) != nil || !sourceReasoningOptIn(raw) {
		return []domain.CatalogReasoningLevel{}
	}
	result := make([]domain.CatalogReasoningLevel, 0, len(declared))
	seen := map[string]bool{}
	for _, item := range declared {
		var effort, description string
		if json.Unmarshal(item, &effort) == nil {
			description = strings.ToLower(strings.TrimSpace(effort)) + " reasoning effort"
		} else {
			var object struct {
				Effort      string `json:"effort"`
				Description string `json:"description"`
			}
			if json.Unmarshal(item, &object) != nil || strings.TrimSpace(object.Effort) == "" {
				continue
			}
			effort = object.Effort
			if object.Description != "" {
				description = object.Description
			} else {
				description = strings.ToLower(strings.TrimSpace(effort)) + " reasoning effort"
			}
		}
		effort = strings.ToLower(strings.TrimSpace(effort))
		if effort == "" || seen[effort] {
			continue
		}
		seen[effort] = true
		result = append(result, domain.CatalogReasoningLevel{Effort: effort, Description: description})
	}
	return result
}

func catalogAllowedModels(key domain.APIKey) map[string]bool {
	if len(key.AllowedModels) == 0 && key.EnforcedModel == nil {
		return nil
	}
	values := map[string]bool{}
	for _, model := range key.AllowedModels {
		values[canonicalModel(model)] = true
	}
	if key.EnforcedModel != nil {
		forced := canonicalModel(*key.EnforcedModel)
		for model := range values {
			if model != forced {
				delete(values, model)
			}
		}
		values[forced] = true
	}
	return values
}

func catalogVisibilityModels(key domain.APIKey) map[string]bool {
	if !key.ApplyToCodexModel || len(key.AllowedModels) == 0 {
		return nil
	}
	return catalogAllowedModels(key)
}

func catalogModelAllowed(model domain.CatalogModel, allowed map[string]bool) bool {
	if allowed == nil {
		return true
	}
	if allowed[canonicalModel(model.Slug)] {
		return true
	}
	for _, alias := range model.Aliases {
		if allowed[canonicalModel(alias)] {
			return true
		}
	}
	return false
}

func sourceAssignmentAllowed(key domain.APIKey, sourceID string) bool {
	return !key.SourceAssignmentScopeEnabled || contains(key.AssignedSourceIDs, sourceID)
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func sourceVisibility(model domain.CatalogModel, key domain.APIKey, visibility, allowed map[string]bool) string {
	if model.Visibility() != "list" {
		return model.Visibility()
	}
	if visibility != nil && !catalogModelAllowed(model, allowed) {
		return "hide"
	}
	return "list"
}

func catalogBackendModel(model domain.CatalogModel) bool {
	return model.SupportedInAPI || string(model.Raw["shell_type"]) == `"shell_command"`
}

func setCatalogVisibility(model *domain.CatalogModel, visibility map[string]bool) {
	if visibility == nil {
		model.Raw["visibility"] = json.RawMessage(`"hide"`)
		return
	}
	if visibility[model.Slug] {
		model.Raw["visibility"] = json.RawMessage(`"list"`)
	} else {
		model.Raw["visibility"] = json.RawMessage(`"hide"`)
	}
}

func openAIModel(slug string, model domain.CatalogModel, created int64) OpenAIModel {
	maxOutput := rawInt64(model.Raw["max_output_tokens"])
	switch slug {
	case "gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex":
		if maxOutput == nil {
			value := int64(128000)
			maxOutput = &value
		}
	}
	defaultReasoning := optionalString(model.DefaultReasoningLevel)
	defaultVerbosity := optionalString(model.DefaultVerbosity)
	minimalVersion := optionalString(model.MinimalClientVersion)
	defaultTier := rawOptionalString(model.Raw["default_service_tier"])
	item := OpenAIModel{
		ID: slug, Object: "model", Created: created, OwnedBy: "codex-lb",
		Metadata: OpenAIModelMetadata{
			DisplayName: model.DisplayName, Description: model.Description, ContextWindow: model.ContextWindow,
			InputContextWindow: model.ContextWindow, MaxOutputTokens: maxOutput, InputModalities: model.InputModalities,
			SupportedReasoningLevels: reasoningMetadata(model.SupportedReasoningLevels), DefaultReasoningLevel: defaultReasoning,
			SupportsReasoningSummaries: model.SupportsReasoningSummaries, SupportVerbosity: model.SupportVerbosity,
			DefaultVerbosity: defaultVerbosity, PreferWebsockets: model.PreferWebsockets,
			SupportsParallelToolCalls: model.SupportsParallelToolCalls, SupportedInAPI: model.SupportedInAPI,
			MinimalClientVersion: minimalVersion, Priority: model.Priority,
			AdditionalSpeedTiers: rawStringList(model.Raw["additional_speed_tiers"]),
			ServiceTiers:         rawObjectList(model.Raw["service_tiers"]), DefaultServiceTier: defaultTier,
		},
		Capabilities: OpenAIModelCapabilities{
			ContextLength: model.ContextWindow, MaxOutputTokens: maxOutput,
			SupportsReasoning: len(model.SupportedReasoningLevels) > 0 || model.SupportsReasoningSummaries || rawBool(model.Raw["supports_reasoning"]),
			SupportsImages:    contains(model.InputModalities, "image"), SupportsVision: contains(model.InputModalities, "image"),
			SupportsToolUse: model.SupportsParallelToolCalls, SupportsStreaming: rawStreaming(model.Raw["supports_streaming"]),
			InputModalities: model.InputModalities, OutputModalities: []string{"text"},
		},
		APITypes: []string{"chat_completions"}, ContextLength: model.ContextWindow, ContextLengthCamel: model.ContextWindow,
		MaxOutputTokens: maxOutput, MaxOutputTokensCamel: maxOutput,
		SupportsReasoning: len(model.SupportedReasoningLevels) > 0 || model.SupportsReasoningSummaries || rawBool(model.Raw["supports_reasoning"]),
		SupportsImages:    contains(model.InputModalities, "image"), SupportsVision: contains(model.InputModalities, "image"),
	}
	item.SupportsReasoningCamel = item.SupportsReasoning
	item.SupportsImagesCamel = item.SupportsImages
	item.SupportsVisionCamel = item.SupportsVision
	return item
}

func rawStreaming(raw json.RawMessage) bool {
	var value *bool
	if json.Unmarshal(raw, &value) == nil && value != nil {
		return *value
	}
	return true
}

func dashboardModel(slug string, model domain.CatalogModel, sourceOnly bool) DashboardModel {
	efforts := make([]string, 0, len(model.SupportedReasoningLevels))
	for _, level := range model.SupportedReasoningLevels {
		efforts = append(efforts, level.Effort)
	}
	return DashboardModel{ID: slug, Name: model.DisplayName, SourceOnly: sourceOnly,
		SupportedReasoningEfforts: efforts, DefaultReasoningEffort: optionalString(model.DefaultReasoningLevel)}
}

func reasoningMetadata(levels []domain.CatalogReasoningLevel) []OpenAIReasoningLevel {
	result := make([]OpenAIReasoningLevel, 0, len(levels))
	for _, level := range levels {
		result = append(result, OpenAIReasoningLevel(level))
	}
	return result
}

func catalogCreated(model domain.CatalogModel) int64 {
	for _, key := range []string{"created", "created_at", "createdAt"} {
		if value := rawInt64(model.Raw[key]); value != nil {
			return *value
		}
	}
	return 0
}

func rawInt64(raw json.RawMessage) *int64 {
	var value int64
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func rawOptionalString(raw json.RawMessage) *string {
	var value string
	if json.Unmarshal(raw, &value) != nil || value == "" {
		return nil
	}
	return &value
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func rawStringList(raw json.RawMessage) []string {
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	return values
}

func rawObjectList(raw json.RawMessage) []map[string]json.RawMessage {
	var values []map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	return values
}

func sortModelsBySlug(models []OpenAIModel) {
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
}

func sortCatalogModels(models []domain.CatalogModel) {
	sort.Slice(models, func(i, j int) bool { return models[i].Slug < models[j].Slug })
}
