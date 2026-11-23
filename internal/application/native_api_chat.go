package application

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

func (s *NativeAPIService) directChat(
	ctx context.Context, keyID string, options ResponseOptions, body json.RawMessage, emit func(NativeAPIEvent) error,
) (NativeAPIResult, bool, error) {
	if s.chats == nil {
		return NativeAPIResult{}, false, nil
	}
	key, requestModel, body, err := prepareNativeChat(ctx, s.store, keyID, body)
	if err != nil {
		return NativeAPIResult{}, true, err
	}
	account, sourceDefined, err := selectNativeChatSource(ctx, s.store, keyID, requestModel)
	if err != nil {
		return NativeAPIResult{}, true, err
	}
	if account.ID == "" {
		if sourceDefined {
			return NativeAPIResult{}, true, &ProxyError{Code: "model_not_found", Status: 404, Message: "No available model source is configured for the requested model"}
		}
		return NativeAPIResult{}, false, nil
	}

	started := s.now()
	release, err := s.admission.Acquire(ctx)
	if err != nil && release != nil {
		release()
	}
	if err != nil {
		return NativeAPIResult{}, true, err
	}
	if release == nil {
		release = func() {}
	}
	defer release()
	price, err := s.resolvePrice(ctx, account, requestModel)
	if err != nil {
		return NativeAPIResult{}, true, err
	}
	budget := requestBudget(body, price)
	id := "req_" + rand.Text()
	attemptAt := s.now()
	if _, err := s.store.ReserveUsage(ctx, domain.ReservationRequest{
		ID: id, APIKeyID: key.ID, AccountID: account.ID, AccountGeneration: account.Generation, RouteRevision: account.RouteRevision, Model: requestModel,
		Budget: budget, Now: attemptAt,
	}); err != nil {
		return NativeAPIResult{}, true, err
	}

	result, callErr := callSafely(func() (NativeAPIResult, error) {
		return s.chats.NativeChat(ctx, ResponseTarget{Account: account, KeyID: key.ID}, body, optionsHasStream(body), emit)
	})
	if callErr == nil && !result.Failed && result.Status >= 200 && result.Status < 300 &&
		!result.UsageKnown && nativeKeyRequiresUsage(key, requestModel) {
		callErr = &ProviderFailure{Code: "usage_unavailable", Status: 502, Dispatched: true}
	}
	status := "success"
	if callErr != nil || result.Failed || result.Status < 200 || result.Status >= 300 {
		status = "error"
	}
	if status == "error" && cancelledCall(callErr) {
		status = "cancelled"
	}
	event := nativeUsageEvent(options, account, requestModel, status, result, id, started, attemptAt)
	event.RequestKind = "chat"
	if status == "cancelled" {
		event.ErrorCode = "request_cancelled"
	} else if status == "error" {
		event.ErrorCode = operationErrorCode(callErr)
		if event.ErrorCode == "" {
			event.ErrorCode = result.ErrorCode
		}
		if event.ErrorCode == "" {
			event.ErrorCode = "upstream_error"
		}
	}
	if err := s.settleNative(ctx, id, event, status, price, result, callErr); err != nil {
		return NativeAPIResult{}, true, err
	}
	if status == "success" && result.UsageKnown {
		if err := s.recordOutcome(account.ID, id, false, true); err != nil {
			return result, true, err
		}
	}
	s.recordNativeDiagnostic(ctx, options, account, requestModel, status, event, body, result.Response)
	return result, true, callErr
}

func prepareNativeChat(ctx context.Context, store NativeAPIStore, keyID string, body json.RawMessage) (domain.APIKey, string, json.RawMessage, error) {
	now := time.Now()
	key, err := store.GetAPIKey(ctx, keyID)
	if err != nil || !key.IsActive || key.ExpiresAt != nil && !key.ExpiresAt.After(now) {
		return domain.APIKey{}, "", nil, &ProxyError{Code: "invalid_api_key", Status: 401, Message: "Invalid or expired API key"}
	}
	var request struct {
		Model    string          `json:"model"`
		Messages json.RawMessage `json:"messages"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &request) != nil || strings.TrimSpace(request.Model) == "" ||
		len(request.Messages) == 0 && len(request.Input) == 0 {
		return domain.APIKey{}, "", nil, &ProxyError{Code: "invalid_request", Status: 400, Message: "model and messages/input are required"}
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		return domain.APIKey{}, "", nil, err
	}
	var object map[string]json.RawMessage
	_ = json.Unmarshal(body, &object)
	// Apply the same key/model/tier/reasoning rules as Responses, but do not
	// translate native messages/tools or lose unknown/null provider fields.
	policy := map[string]json.RawMessage{"model": jsonString(strings.TrimSpace(request.Model)), "input": jsonString("")}
	if tier, exists := object["service_tier"]; exists {
		policy["service_tier"] = tier
	}
	if effort, exists := object["reasoning_effort"]; exists {
		policy["reasoning"], _ = json.Marshal(map[string]json.RawMessage{"effort": effort})
	}
	encoded, _ := json.Marshal(policy)
	checked, err := parseResponse(encoded, key, settings, false)
	if err != nil {
		return domain.APIKey{}, "", nil, err
	}
	if err := applyNativeReasoningAliases(object, policy, key, settings); err != nil {
		return domain.APIKey{}, "", nil, err
	}
	object["model"] = checked.Object["model"]
	if tier, exists := checked.Object["service_tier"]; exists {
		object["service_tier"] = tier
	}
	if checked.ReasoningEffort != "" {
		object["reasoning_effort"] = jsonString(checked.ReasoningEffort)
	}
	prepared, _ := json.Marshal(object)
	return key, checked.Model, prepared, nil
}

// applyNativeReasoningAliases subjects each explicit native effort to the same
// key policy before selection or reservation, while retaining its provider shape.
func applyNativeReasoningAliases(object, policy map[string]json.RawMessage, key domain.APIKey, settings domain.RuntimeSettings) error {
	check := func(fields map[string]json.RawMessage, name string) error {
		raw, exists := fields[name]
		if !exists {
			return nil
		}
		policy["reasoning"], _ = json.Marshal(map[string]json.RawMessage{"effort": raw})
		encoded, _ := json.Marshal(policy)
		checked, err := parseResponse(encoded, key, settings, false)
		if err != nil {
			return err
		}
		if key.EnforcedReasoningEffort != nil {
			fields[name] = jsonString(checked.ReasoningEffort)
		}
		return nil
	}
	if err := check(object, "reasoningEffort"); err != nil {
		return err
	}
	for _, name := range []string{"reasoning", "thinking"} {
		var nested map[string]json.RawMessage
		if json.Unmarshal(object[name], &nested) != nil || nested == nil {
			continue
		}
		if name == "thinking" && (string(nested["type"]) == `"disabled"` || string(nested["enabled"]) == "false") {
			continue
		}
		if err := check(nested, "effort"); err != nil {
			return err
		}
		if key.EnforcedReasoningEffort != nil {
			object[name], _ = json.Marshal(nested)
		}
	}
	var thinking string
	if json.Unmarshal(object["thinking"], &thinking) == nil {
		switch thinking {
		case "", "disabled", "off", "none", "enabled", "adaptive", "auto":
		default:
			return check(object, "thinking")
		}
	}
	return nil
}

func selectNativeChatSource(ctx context.Context, store NativeAPIStore, keyID, model string) (domain.Account, bool, error) {
	accounts, err := store.EligibleAccounts(ctx, keyID)
	if err != nil && !errors.Is(err, domain.ErrNoAccounts) {
		return domain.Account{}, true, err
	}
	eligible := make(map[string]domain.Account, len(accounts))
	for _, account := range accounts {
		if account.Kind == domain.AccountExternal && account.Status == domain.AccountActive && !account.RequiresEgressDecision {
			eligible[account.ID] = account
		}
	}
	sources, err := store.ListModelSources(ctx)
	if err != nil {
		return domain.Account{}, true, err
	}
	sourceDefined := false
	for _, source := range sources {
		if !source.Enabled || !source.Chat {
			continue
		}
		if _, supported := source.Model(model); !supported {
			continue
		}
		sourceDefined = true
		if account, ok := eligible[source.ID]; ok {
			return account, true, nil
		}
	}
	return domain.Account{}, sourceDefined, nil
}
