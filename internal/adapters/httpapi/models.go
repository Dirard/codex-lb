package httpapi

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type ModelCatalogHandler struct {
	store   ProxyRepository
	catalog *application.ModelCatalogService
}

func NewModelCatalogHandler(store ProxyRepository, catalog *application.ModelCatalogService) *ModelCatalogHandler {
	return &ModelCatalogHandler{store: store, catalog: catalog}
}

func (h *ModelCatalogHandler) RegisterPublicRoutes(mux *http.ServeMux) {
	for _, path := range []string{"/v1/models", "/v1/models/{$}", "/backend-api/codex/models", "/backend-api/codex/models/{$}"} {
		mux.HandleFunc("GET "+path, h.models)
	}
}

func (h *ModelCatalogHandler) RegisterAdminRoutes(mux *http.ServeMux) {
	for _, path := range []string{"/api/models", "/api/models/{$}"} {
		mux.HandleFunc("GET "+path, h.dashboardModels)
	}
}

func (h *ModelCatalogHandler) models(w http.ResponseWriter, r *http.Request) {
	key, err := authenticateProxyKey(r, h.store)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/backend-api/") || strings.TrimSpace(r.URL.Query().Get("client_version")) != "" {
		entries, data, err := h.catalog.CodexModels(r.Context(), key)
		if err != nil {
			writeProxyError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Models []map[string]json.RawMessage `json:"models"`
			Object string                       `json:"object"`
			Data   []application.OpenAIModel    `json:"data"`
		}{codexEntries(entries), "list", data})
		return
	}
	models, err := h.catalog.OpenAIModels(r.Context(), key)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Object string                    `json:"object"`
		Data   []application.OpenAIModel `json:"data"`
	}{"list", models})
}

func (h *ModelCatalogHandler) dashboardModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.catalog.DashboardModels(r.Context())
	if err != nil {
		writeProxyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Models []application.DashboardModel `json:"models"`
	}{models})
}

func codexEntries(models []domain.CatalogModel) []map[string]json.RawMessage {
	result := make([]map[string]json.RawMessage, 0, len(models))
	for _, model := range models {
		result = append(result, codexEntry(model))
	}
	return result
}

func codexEntry(model domain.CatalogModel) map[string]json.RawMessage {
	skip := map[string]bool{
		"slug": true, "display_name": true, "description": true, "base_instructions": true,
		"default_reasoning_level": true, "supported_reasoning_levels": true, "supported_in_api": true,
		"priority": true, "minimal_client_version": true, "supports_reasoning_summaries": true,
		"support_verbosity": true, "default_verbosity": true, "supports_parallel_tool_calls": true,
		"context_window": true, "input_modalities": true, "available_in_plans": true,
		"prefer_websockets": true, "visibility": true, "truncation_policy": true,
		"experimental_supported_tools": true, "source_request_overrides": true,
	}
	entry := make(map[string]json.RawMessage, len(model.Raw)+22)
	for key, value := range model.Raw {
		if !skip[key] {
			entry[key] = value
		}
	}
	set := func(key string, value any) {
		if encoded, err := json.Marshal(value); err == nil {
			entry[key] = encoded
		}
	}
	setOptionalString := func(key, value string) {
		if value == "" {
			set(key, nil)
		} else {
			set(key, value)
		}
	}
	levels := make([]domain.CatalogReasoningLevel, 0, len(model.SupportedReasoningLevels))
	for _, level := range model.SupportedReasoningLevels {
		if level.Effort != "" {
			levels = append(levels, level)
		}
	}
	plans := append([]string(nil), model.AvailableInPlans...)
	sort.Strings(plans)
	modalities := append([]string(nil), model.InputModalities...)
	defaultReasoning := model.DefaultReasoningLevel
	if defaultReasoning != "" && !validCodexReasoning(defaultReasoning) {
		defaultReasoning = ""
	}
	defaultVerbosity := model.DefaultVerbosity
	switch defaultVerbosity {
	case "low", "medium", "high":
	default:
		defaultVerbosity = ""
	}
	filteredLevels := make([]domain.CatalogReasoningLevel, 0, len(levels))
	for _, level := range levels {
		if validCodexReasoning(level.Effort) {
			filteredLevels = append(filteredLevels, level)
		}
	}
	set("slug", model.Slug)
	set("display_name", model.DisplayName)
	set("description", model.Description)
	set("base_instructions", model.BaseInstructions)
	setOptionalString("default_reasoning_level", defaultReasoning)
	set("supported_reasoning_levels", filteredLevels)
	set("supported_in_api", model.SupportedInAPI)
	set("priority", model.Priority)
	setOptionalString("minimal_client_version", model.MinimalClientVersion)
	set("supports_reasoning_summaries", model.SupportsReasoningSummaries)
	set("support_verbosity", model.SupportVerbosity)
	setOptionalString("default_verbosity", defaultVerbosity)
	set("supports_parallel_tool_calls", model.SupportsParallelToolCalls)
	set("context_window", model.ContextWindow)
	set("input_modalities", modalities)
	set("available_in_plans", plans)
	set("prefer_websockets", model.PreferWebsockets)
	set("visibility", model.Visibility())
	set("truncation_policy", catalogTruncationPolicy(model))
	set("experimental_supported_tools", catalogExperimentalTools(model))
	return entry
}

func validCodexReasoning(value string) bool {
	switch value {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func catalogTruncationPolicy(model domain.CatalogModel) map[string]any {
	var policy struct {
		Mode  string `json:"mode"`
		Limit *int64 `json:"limit"`
	}
	if json.Unmarshal(model.Raw["truncation_policy"], &policy) == nil &&
		(policy.Mode == "bytes" || policy.Mode == "tokens") && policy.Limit != nil {
		return map[string]any{"mode": policy.Mode, "limit": *policy.Limit}
	}
	mode := "tokens"
	if model.Slug == "gpt-5.2" {
		mode = "bytes"
	}
	return map[string]any{"mode": mode, "limit": 10000}
}

func catalogExperimentalTools(model domain.CatalogModel) []string {
	var values []any
	if json.Unmarshal(model.Raw["experimental_supported_tools"], &values) != nil {
		return []string{}
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}
