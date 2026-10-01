package domain

import "encoding/json"

var bootstrapCatalogPlans = []string{
	"business", "education", "edu", "enterprise", "enterprise_cbp_usage_based", "finserv",
	"free", "free_workspace", "go", "hc", "k12", "plus", "pro", "prolite", "quorum",
	"self_serve_business_usage_based", "team",
}

var bootstrapCoreCatalogPlans = []string{
	"business", "education", "edu", "enterprise", "enterprise_cbp_usage_based", "finserv",
	"go", "hc", "plus", "pro", "prolite", "quorum", "self_serve_business_usage_based", "team",
}

var bootstrapGPT56CatalogPlans = []string{
	"business", "education", "edu", "edu_plus", "edu_pro", "enterprise",
	"enterprise_cbp_automation", "enterprise_cbp_usage_based", "finserv", "free",
	"free_workspace", "go", "hc", "k12", "plus", "pro", "prolite", "quorum", "sci",
	"self_serve_business_usage_based", "team",
}

func BootstrapCatalog() map[string]CatalogModel {
	standard := []CatalogReasoningLevel{
		{Effort: "low", Description: "Low reasoning effort"},
		{Effort: "medium", Description: "Medium reasoning effort"},
		{Effort: "high", Description: "High reasoning effort"},
	}
	extended := append(append([]CatalogReasoningLevel(nil), standard...),
		CatalogReasoningLevel{Effort: "xhigh", Description: "Extra high reasoning effort"})
	maxLevels := []CatalogReasoningLevel{
		{Effort: "low", Description: "Fast responses with lighter reasoning"},
		{Effort: "medium", Description: "Balances speed and reasoning depth for everyday tasks"},
		{Effort: "high", Description: "Greater reasoning depth for complex problems"},
		{Effort: "xhigh", Description: "Extra high reasoning depth for complex problems"},
		{Effort: "max", Description: "Maximum reasoning depth for the hardest problems"},
	}
	ultraLevels := append(append([]CatalogReasoningLevel(nil), maxLevels...),
		CatalogReasoningLevel{Effort: "ultra", Description: "Maximum reasoning with automatic task delegation"})
	models := []CatalogModel{
		bootstrapCatalogModel("gpt-5.6-sol", "GPT-5.6-Sol", "Latest frontier agentic coding model.", ultraLevels, "low", 272000, 1, bootstrapGPT56CatalogPlans, gpt56Raw("v2", json.RawMessage(`{"message":"Our most capable model yet. GPT-5.6 Sol can tackle complex code changes, dig into research, produce polished documents, and take on your most ambitious work. Sol is highly capable at lower reasoning efforts—try starting lower, then turn it up for harder jobs."}`))),
		bootstrapCatalogModel("gpt-5.6-terra", "GPT-5.6-Terra", "Balanced agentic coding model for everyday work.", ultraLevels, "medium", 272000, 2, bootstrapGPT56CatalogPlans, gpt56Raw("v2", nil)),
		bootstrapCatalogModel("gpt-5.6-luna", "GPT-5.6-Luna", "Fast and affordable agentic coding model.", maxLevels, "medium", 272000, 3, bootstrapGPT56CatalogPlans, gpt56Raw("v1", nil)),
		bootstrapCatalogModel("gpt-5.5", "GPT-5.5", "GPT-5.5", extended, "medium", 272000, 0, bootstrapCatalogPlans, nil),
		bootstrapCatalogModel("gpt-5.4", "GPT-5.4", "GPT-5.4", extended, "medium", 272000, 0, bootstrapCoreCatalogPlans, rawFields("max_context_window", int64(1000000))),
		bootstrapCatalogModel("gpt-5.4-mini", "GPT-5.4 Mini", "GPT-5.4 Mini", extended, "medium", 272000, 0, bootstrapCatalogPlans, nil),
		bootstrapCatalogModel("gpt-5.3-codex", "GPT-5.3 Codex", "GPT-5.3 Codex", extended, "medium", 272000, 0, bootstrapCoreCatalogPlans, nil),
		bootstrapCatalogModel("gpt-5.3-codex-spark", "GPT-5.3 Codex Spark", "GPT-5.3 Codex Spark", extended, "high", 128000, 0, bootstrapCatalogPlans, nil),
		bootstrapCatalogModel("gpt-5.2", "GPT-5.2", "GPT-5.2", extended, "medium", 272000, 0, bootstrapCatalogPlans, rawFields("truncation_policy", map[string]any{"mode": "bytes", "limit": 10000})),
		bootstrapCatalogModel("codex-auto-review", "Codex Auto Review", "Codex Auto Review", extended, "medium", 272000, 0, bootstrapCoreCatalogPlans, rawFields("visibility", "hide", "max_context_window", int64(1000000))),
	}
	result := make(map[string]CatalogModel, len(models))
	for _, model := range models {
		if model.Slug == "gpt-5.4-mini" {
			model.DefaultVerbosity = "medium"
		}
		if model.Slug == "gpt-5.3-codex-spark" {
			model.InputModalities = []string{"text"}
		}
		result[model.Slug] = model
	}
	return result
}

func bootstrapCatalogModel(slug, name, description string, levels []CatalogReasoningLevel, defaultLevel string, contextWindow int64, priority int, plans []string, overrides map[string]json.RawMessage) CatalogModel {
	raw := rawFields(
		"shell_type", "shell_command", "visibility", "list", "availability_nux", nil,
		"max_context_window", contextWindow, "truncation_policy", map[string]any{"mode": "tokens", "limit": 10000},
		"experimental_supported_tools", []string{},
	)
	for key, value := range overrides {
		raw[key] = value
	}
	return CatalogModel{
		Slug: slug, DisplayName: name, Description: description, ContextWindow: contextWindow,
		InputModalities: []string{"text", "image"}, SupportedReasoningLevels: levels,
		DefaultReasoningLevel: defaultLevel, SupportsReasoningSummaries: true, SupportVerbosity: true,
		DefaultVerbosity: "low", PreferWebsockets: true, SupportsParallelToolCalls: true,
		SupportedInAPI: true, MinimalClientVersion: minimalBootstrapVersion(slug), Priority: priority,
		AvailableInPlans: plans, SourceKind: ModelCatalogSourceSubscription, Raw: raw,
	}
}

func gpt56Raw(multiAgentVersion string, nux json.RawMessage) map[string]json.RawMessage {
	return rawFields(
		"apply_patch_tool_type", "freeform", "web_search_tool_type", "text_and_image",
		"supports_image_detail_original", true, "truncation_policy", map[string]any{"mode": "tokens", "limit": 10000},
		"tool_mode", "code_mode_only", "multi_agent_version", multiAgentVersion,
		"use_responses_lite", true, "include_skills_usage_instructions", false,
		"auto_review_model_override", nil, "max_context_window", int64(872000),
		"auto_compact_token_limit", nil, "comp_hash", "3000", "reasoning_summary_format", "experimental",
		"default_reasoning_summary", "none", "availability_nux", nux, "upgrade", nil,
		"experimental_supported_tools", []string{}, "supports_search_tool", true,
		"default_service_tier", nil, "service_tiers", []any{map[string]any{"id": "priority", "name": "Fast", "description": "1.5x speed, increased usage"}},
		"additional_speed_tiers", []string{"fast"},
	)
}

func minimalBootstrapVersion(slug string) string {
	switch slug {
	case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		return "0.144.0"
	case "gpt-5.5":
		return "0.124.0"
	case "gpt-5.2":
		return "0.0.1"
	case "gpt-5.3-codex-spark":
		return "0.100.0"
	default:
		return "0.98.0"
	}
}

func rawFields(pairs ...any) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		encoded, err := json.Marshal(pairs[i+1])
		if err != nil {
			panic(err)
		}
		result[pairs[i].(string)] = encoded
	}
	return result
}
