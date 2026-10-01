package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

const maxResponseBody = 32 << 20
const maxReplayBytes = 8 << 20
const defaultOutputTokenEstimate = 2048

type ProxyError struct {
	Code    string
	Status  int
	Message string
	Param   string
}

func (e *ProxyError) Error() string { return e.Message }

type responseRequest struct {
	Object            map[string]json.RawMessage
	Wire              json.RawMessage
	WireClean         bool
	Model             string
	ModelEnforced     bool
	Previous          string
	Tier              string
	TierEnforced      bool
	ReasoningEffort   string
	Stream            bool
	MaxOutput         int64
	Input             []json.RawMessage
	DeferredInput     bool
	FilePinned        bool
	CompactionTrigger bool
	PreserveReasoning bool
	ResponsesLite     bool
	PreferWebsockets  bool
}

func (request *responseRequest) loadInput() error {
	if !request.DeferredInput {
		return nil
	}
	input, err := responseInput(request.Object["input"])
	if err != nil {
		return &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid Responses request"}
	}
	request.Input, request.DeferredInput = input, false
	return nil
}

func parseResponse(body json.RawMessage, key domain.APIKey, settings domain.RuntimeSettings, codex bool) (responseRequest, error) {
	var request responseRequest
	bad := &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid Responses request"}
	if len(body) > maxResponseBody {
		return request, &ProxyError{Code: "request_too_large", Status: 413, Message: "Request body is too large"}
	}
	var err error
	request.Object, request.WireClean, err = decodeResponseObject(body)
	if err != nil {
		return request, bad
	}
	request.Wire = body
	if raw, ok := request.Object["model"]; !ok || json.Unmarshal(raw, &request.Model) != nil || request.Model == "" || len(request.Model) > 256 {
		return request, bad
	}
	if key.EnforcedModel != nil && (!codex || key.ApplyToCodexModel) {
		request.ModelEnforced = true
		request.Model = *key.EnforcedModel
		request.Object["model"] = jsonString(request.Model)
		request.WireClean = false
	}
	if len(key.AllowedModels) > 0 && !modelAllowed(key.AllowedModels, request.Model) {
		return request, &ProxyError{Code: "model_not_allowed", Status: 403, Message: "This key does not allow the requested model"}
	}
	for name, dst := range map[string]*string{"previous_response_id": &request.Previous, "service_tier": &request.Tier} {
		if raw, ok := request.Object[name]; ok && string(raw) != "null" && json.Unmarshal(raw, dst) != nil {
			return request, bad
		}
	}
	if raw, ok := request.Object["stream"]; ok && json.Unmarshal(raw, &request.Stream) != nil {
		return request, bad
	}
	request.MaxOutput = defaultOutputTokenEstimate
	if raw, ok := request.Object["max_output_tokens"]; ok && (json.Unmarshal(raw, &request.MaxOutput) != nil || request.MaxOutput <= 0 || request.MaxOutput > 10_000_000) {
		return request, bad
	}
	if key.EnforcedServiceTier != nil {
		request.TierEnforced = true
		request.Tier = *key.EnforcedServiceTier
		request.Object["service_tier"] = jsonString(request.Tier)
		request.WireClean = false
	}
	if settings.ProhibitFastMode && isFastTier(request.Tier) {
		request.Tier = "default"
		request.Object["service_tier"] = jsonString(request.Tier)
		request.WireClean = false
	}
	var reasoning map[string]json.RawMessage
	if raw, ok := request.Object["reasoning"]; ok && string(raw) != "null" {
		if json.Unmarshal(raw, &reasoning) != nil || reasoning == nil {
			return request, bad
		}
	}
	var effort string
	if raw, ok := reasoning["effort"]; ok && json.Unmarshal(raw, &effort) != nil {
		return request, bad
	}
	if key.EnforcedReasoningEffort != nil {
		if reasoning == nil {
			reasoning = make(map[string]json.RawMessage)
		}
		effort = *key.EnforcedReasoningEffort
		reasoning["effort"] = jsonString(effort)
		request.Object["reasoning"], _ = json.Marshal(reasoning)
		request.WireClean = false
	}
	if key.AllowedReasoningEfforts != nil && effort != "" && !slices.Contains(key.AllowedReasoningEfforts, effort) {
		return request, &ProxyError{Code: "reasoning_effort_not_allowed", Status: 403, Message: "This key does not allow the requested reasoning effort"}
	}
	request.ReasoningEffort = effort
	rawInput := request.Object["input"]
	if len(rawInput) != 0 && rawInput[0] == '"' {
		var text string
		if json.Unmarshal(rawInput, &text) != nil {
			return request, bad
		}
		// Scalar text is a user message, never a structured compact/Lite item.
		request.FilePinned = strings.HasPrefix(text, "file-") || strings.HasPrefix(text, "file_")
		request.DeferredInput = true
		return request, nil
	}
	input, err := responseInput(request.Object["input"])
	if err != nil {
		return request, bad
	}
	request.Input = input
	request.FilePinned = accountBoundItems(input)
	triggerSeen, err := validateCompactTrigger(input)
	if err != nil {
		return request, err
	}
	request.CompactionTrigger = triggerSeen && codex
	request.ResponsesLite = inputUsesResponsesLite(input)
	request.Input, request.DeferredInput = nil, true
	return request, nil
}

func validateCompactTrigger(input []json.RawMessage) (bool, error) {
	triggerSeen := false
	for index, item := range input {
		var object struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(item, &object)
		if object.Type != "compaction_trigger" {
			continue
		}
		if triggerSeen || index != len(input)-1 {
			return false, &ProxyError{Code: "invalid_request_error", Status: 400, Message: "compaction_trigger must appear exactly once as the final top-level input item", Param: "input"}
		}
		triggerSeen = true
	}
	return triggerSeen, nil
}

func jsonString(value string) json.RawMessage { encoded, _ := json.Marshal(value); return encoded }

func canonicalModel(model string) string {
	// Model aliases used for policy are deliberately narrower than pricing globs:
	// a similarly prefixed, unrecognized model must not inherit another's access.
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "gpt-5.6" {
		return "gpt-5.6-sol"
	}
	for _, base := range pricing.Models() {
		if strings.HasPrefix(model, base+"-") {
			suffix := strings.TrimPrefix(model, base+"-")
			if len(suffix) == 10 && suffix[4] == '-' && suffix[7] == '-' {
				if _, err := time.Parse("2006-01-02", suffix); err == nil {
					return base
				}
			}
		}
	}
	return model
}

func responseInput(raw json.RawMessage) ([]json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, errors.New("input is required")
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
		item, err := json.Marshal(struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{"user", text})
		return []json.RawMessage{item}, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		return nil, errors.New("input must be text or an array")
	}
	for _, item := range items {
		// Unmarshal above already validated every value, including nested JSON.
		// Only its top-level kind remains to check, not another full object parse.
		if value := bytes.TrimSpace(item); len(value) == 0 || value[0] != '{' {
			return nil, errors.New("input items must be objects")
		}
	}
	return items, nil
}

type responseContext struct {
	Items []json.RawMessage `json:"items"`
}

func accountBoundItems(items []json.RawMessage) bool {
	var visit func(any) bool
	visit = func(value any) bool {
		switch value := value.(type) {
		case map[string]any:
			if value["type"] == "additional_tools" {
				encoded, _ := json.Marshal(value)
				var bundle map[string]json.RawMessage
				_ = json.Unmarshal(encoded, &bundle)
				return !replayLiteBundle(bundle)
			}
			for key, child := range value {
				if key == "file_id" || key == "file_ids" || key == "container_id" || key == "vector_store_id" || key == "vector_store_ids" || key == "call_id" && value["type"] == "input_audio" {
					return true
				}
				if key == "type" && (child == "item_reference" || child == "compaction") {
					return true
				}
				if visit(child) {
					return true
				}
			}
		case []any:
			for _, child := range value {
				if visit(child) {
					return true
				}
			}
		case string:
			return strings.HasPrefix(value, "file-") || strings.HasPrefix(value, "file_")
		}
		return false
	}
	for _, raw := range items {
		var value any
		if json.Unmarshal(raw, &value) != nil || visit(value) {
			return true
		}
	}
	return false
}

// replayBody carries complete, answered tool history, never old provider state.
// It is only used after explicit quota refusal before any visible output.
func replayBody(request responseRequest, history responseContext) (json.RawMessage, error) {
	if err := request.loadInput(); err != nil {
		return nil, err
	}
	items := append(slices.Clone(history.Items), request.Input...)
	if accountBoundItems(items) {
		return nil, errors.New("account-bound input cannot be replayed")
	}
	prepared := make([]json.RawMessage, 0, len(items))
	pending := map[[2]string]bool{}
	for _, item := range items {
		var object map[string]json.RawMessage
		if json.Unmarshal(item, &object) != nil {
			return nil, errors.New("invalid replay item")
		}
		var kind, callID string
		_ = json.Unmarshal(object["type"], &kind)
		_ = json.Unmarshal(object["call_id"], &callID)
		callKind, isOutput := compactCallKind(kind)
		pair := [2]string{callKind, callID}
		switch kind {
		case "reasoning":
			// Encrypted reasoning is account-bound. The full messages and completed
			// tool exchanges below are the reconstructible conversation context.
			if !request.PreserveReasoning || len(object["encrypted_content"]) > 0 {
				continue
			}
		case "", "message":
			if len(object["role"]) == 0 {
				return nil, errors.New("opaque replay item")
			}
		case "additional_tools":
			if !replayLiteBundle(object) {
				return nil, errors.New("account-bound Lite tools cannot be replayed")
			}
		case "function_call", "custom_tool_call", "apply_patch_call":
			if kind == "apply_patch_call" && !replayPatchCall(object) {
				return nil, errors.New("opaque apply-patch call cannot be replayed")
			}
			if callID == "" || pending[pair] {
				return nil, errors.New("ambiguous tool replay")
			}
			pending[pair] = true
		case "function_call_output", "custom_tool_call_output", "apply_patch_call_output":
			if !isOutput || !pending[pair] {
				return nil, errors.New("tool output without retained call")
			}
			delete(pending, pair)
		default:
			return nil, errors.New("unsupported replay item")
		}
		delete(object, "id")
		delete(object, "encrypted_content")
		encoded, err := json.Marshal(object)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, encoded)
	}
	if len(pending) != 0 {
		return nil, errors.New("unanswered tool calls cannot be replayed")
	}
	object := make(map[string]json.RawMessage, len(request.Object))
	for key, value := range request.Object {
		object[key] = value
	}
	delete(object, "previous_response_id")
	delete(object, "conversation")
	delete(object, "turn_state")
	if _, _, _, err := stripResponsesLiteMarker(object); err != nil {
		return nil, err
	}
	object["input"], _ = json.Marshal(prepared)
	encoded, err := json.Marshal(object)
	if len(encoded) > maxResponseBody {
		return nil, errors.New("replay exceeds request limit")
	}
	return encoded, err
}
