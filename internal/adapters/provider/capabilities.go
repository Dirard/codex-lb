package provider

import (
	"encoding/json"
	"strings"

	"codex-lb/internal/adapters/upstream"
	"codex-lb/internal/domain"
)

type modelMetadata struct {
	SupportsReasoning        bool                       `json:"supports_reasoning"`
	SupportedReasoningLevels []json.RawMessage          `json:"supported_reasoning_levels"`
	DefaultReasoningLevel    string                     `json:"default_reasoning_level"`
	SourceRequestOverrides   map[string]json.RawMessage `json:"source_request_overrides"`
	ExperimentalTools        []string                   `json:"experimental_supported_tools"`
}

func (metadata modelMetadata) reasoningEfforts() []string {
	values := make([]string, 0, len(metadata.SupportedReasoningLevels))
	for _, raw := range metadata.SupportedReasoningLevels {
		var value string
		if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
			values = append(values, strings.TrimSpace(value))
			continue
		}
		var object struct {
			Effort string `json:"effort"`
		}
		if json.Unmarshal(raw, &object) == nil && strings.TrimSpace(object.Effort) != "" {
			values = append(values, strings.TrimSpace(object.Effort))
		}
	}
	return values
}

func sourceCapabilities(source domain.ModelSource, model domain.ModelSourceModel, protocol upstream.Protocol, transport upstream.StreamTransport) (upstream.Capabilities, error) {
	capabilities := upstream.Capabilities{
		Protocol: protocol, StreamTransport: transport,
		Tools: model.Tools, ParallelToolCalls: model.Tools, ImageInput: model.Vision,
		ModelMappings:      modelMappings(model),
		ExtraBody:          source.ProviderConfig.ExtraBody,
		DropBodyFields:     source.ProviderConfig.DropBodyFields,
		AllowedHostedTools: append([]string(nil), source.ProviderConfig.AllowedHostedTools...),
		AllowNoSSEDone:     source.ProviderConfig.AllowNoSSEDone,
		AllowMissingUsage:  source.ProviderConfig.AllowMissingUsage,
	}
	if protocol == upstream.ProtocolChatCompletions {
		capabilities.AllowedHostedTools = nil
	}
	var metadata modelMetadata
	if strings.TrimSpace(model.RawMetadataJSON) != "" {
		if err := json.Unmarshal([]byte(model.RawMetadataJSON), &metadata); err != nil {
			return capabilities, providerFailure("invalid_model_metadata", 0, false)
		}
	}
	capabilities.Reasoning = metadata.SupportsReasoning
	if source.Kind == domain.ModelSourceZAI && protocol == upstream.ProtocolChatCompletions {
		capabilities.EnableGLMThinking = true
		capabilities.AllowedHostedTools = nil
	}
	capabilities.AllowedHostedTools = append(capabilities.AllowedHostedTools, metadata.ExperimentalTools...)
	for field, value := range metadata.SourceRequestOverrides {
		if capabilities.ExtraBody == nil {
			capabilities.ExtraBody = make(map[string]json.RawMessage)
		}
		capabilities.ExtraBody[field] = value
	}
	normalized, err := capabilities.Normalized()
	return normalized, err
}

func modelMappings(model domain.ModelSourceModel) map[string]string {
	target := strings.TrimSpace(model.UpstreamModel)
	if target == "" {
		target = model.Model
	}
	mappings := make(map[string]string)
	mappings[model.Model] = target
	for _, alias := range model.Aliases {
		alias = strings.TrimSpace(alias)
		if alias != "" {
			mappings[alias] = target
		}
	}
	return mappings
}
