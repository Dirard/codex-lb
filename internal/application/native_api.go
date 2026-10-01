package application

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

type NativeAPIEvent struct {
	Type string
	Data json.RawMessage
	Wire string
}

type NativeAPIResult struct {
	ResponseID  string
	Response    json.RawMessage
	Status      int
	Usage       domain.UsageAmount
	UsageKnown  bool
	Failed      bool
	ErrorCode   string
	ServiceTier string
}

type NativeEmbeddingProvider interface {
	Embeddings(context.Context, ResponseTarget, json.RawMessage) (NativeAPIResult, error)
}

type NativeChatProvider interface {
	NativeChat(context.Context, ResponseTarget, json.RawMessage, bool, func(NativeAPIEvent) error) (NativeAPIResult, error)
}

type NativeAPIStore interface {
	Accounts
	APIKeys
	Settings
	UsageLedger
	ModelSourceRepository
}

type NativeAPIService struct {
	store        NativeAPIStore
	embeddings   NativeEmbeddingProvider
	chats        NativeChatProvider
	proxy        *Proxy
	admission    CodexAdmission
	diagnostics  DiagnosticRecorder
	resolvePrice func(context.Context, domain.Account, string) (pricing.Price, error)
	now          func() time.Time
}

func NewNativeAPIService(store NativeAPIStore, embeddings NativeEmbeddingProvider, chat NativeChatProvider, proxy *Proxy) *NativeAPIService {
	return &NativeAPIService{
		store: store, embeddings: embeddings, chats: chat, proxy: proxy,
		admission: blockedCodexAdmission{}, resolvePrice: defaultCodexPrice(store), now: time.Now,
	}
}

func (s *NativeAPIService) ConfigureAdmission(admission CodexAdmission) { s.admission = admission }
func (s *NativeAPIService) ConfigureDiagnostics(recorder DiagnosticRecorder) {
	s.diagnostics = recorder
}
func (s *NativeAPIService) ConfigurePrice(resolve func(context.Context, domain.Account, string) (pricing.Price, error)) {
	if resolve != nil {
		s.resolvePrice = resolve
	}
}

func (s *NativeAPIService) Chat(ctx context.Context, keyID string, options ResponseOptions, body json.RawMessage, emit func(NativeAPIEvent) error) (NativeAPIResult, error) {
	direct, handled, err := s.directChat(ctx, keyID, options, body, emit)
	if handled {
		return direct, err
	}
	if err != nil {
		return NativeAPIResult{}, err
	}
	responsesBody, err := ChatCompletionsToResponses(body)
	if err != nil {
		return NativeAPIResult{}, err
	}
	result, err := s.proxy.Respond(ctx, options, responsesBody, func(event ResponseEvent) error {
		return emit(NativeAPIEvent{Type: event.Type, Data: event.Data, Wire: "responses"})
	})
	if err != nil {
		return NativeAPIResult{}, err
	}
	if optionsHasStream(body) && emit != nil {
		return NativeAPIResult{ResponseID: result.ResponseID, Usage: result.Usage, Failed: result.Failed, ErrorCode: result.ErrorCode}, nil
	}
	if result.Failed {
		return NativeAPIResult{ResponseID: result.ResponseID, Response: result.Response, Usage: result.Usage, Failed: true, ErrorCode: result.ErrorCode}, nil
	}
	chatResponse, err := ResponsesToChatCompletion(result.Response)
	if err != nil {
		return NativeAPIResult{}, &ProviderFailure{Code: "invalid_upstream_response", Status: 502, Dispatched: true}
	}
	return NativeAPIResult{ResponseID: result.ResponseID, Response: chatResponse, Usage: result.Usage, Failed: result.Failed, ErrorCode: result.ErrorCode}, nil
}

func (s *NativeAPIService) Embeddings(ctx context.Context, keyID string, options ResponseOptions, body json.RawMessage) (NativeAPIResult, error) {
	started := s.now()
	key, requestModel, err := nativeEmbeddingsModel(ctx, s.store, keyID, body)
	if err != nil {
		return NativeAPIResult{}, err
	}
	body = nativeRequestModel(body, requestModel)
	account, _, _, err := selectEmbeddingsSource(ctx, s.store, keyID, requestModel)
	if err != nil {
		return NativeAPIResult{}, err
	}
	release, err := s.admission.Acquire(ctx)
	if err != nil && release != nil {
		release()
	}
	if err != nil {
		return NativeAPIResult{}, err
	}
	if release == nil {
		release = func() {}
	}
	defer release()

	price, err := s.resolvePrice(ctx, account, requestModel)
	if err != nil {
		return NativeAPIResult{}, err
	}
	budget := embeddingsBudget(body, price)
	id := "req_" + rand.Text()
	attemptAt := s.now()
	_, err = s.store.ReserveUsage(ctx, domain.ReservationRequest{
		ID: id, APIKeyID: key.ID, AccountID: account.ID, AccountGeneration: account.Generation, RouteRevision: account.RouteRevision, Model: requestModel,
		Budget: budget, Now: attemptAt,
	})
	if err != nil {
		return NativeAPIResult{}, err
	}

	result, callErr := callSafely(func() (NativeAPIResult, error) {
		return s.embeddings.Embeddings(ctx, ResponseTarget{Account: account, KeyID: key.ID}, body)
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
		return NativeAPIResult{}, err
	}
	if status == "success" && result.UsageKnown {
		if err := s.recordOutcome(account.ID, id, false, true); err != nil {
			return result, err
		}
	}
	s.recordNativeDiagnostic(ctx, options, account, requestModel, status, event, body, result.Response)
	return result, callErr
}

func (s *NativeAPIService) settleNative(ctx context.Context, id string, event domain.UsageEvent, status string, price pricing.Price, result NativeAPIResult, callErr error) error {
	if nativeUsageUncertain(result, callErr) {
		return s.markNativeUncertain(ctx, id)
	}
	cost, err := price.Cost(event.Usage, event.ServiceTier)
	if err != nil {
		if markErr := s.markNativeUncertain(ctx, id); markErr != nil {
			return markErr
		}
		return &ProxyError{Code: "invalid_upstream_usage", Status: 502, Message: "Operation usage could not be priced; reservation retained"}
	}
	event.Usage.CostMicrodollars = cost
	settlement := "finalized"
	if status != "success" {
		settlement = "failed"
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := s.store.SettleUsage(cleanup, id, domain.UsageSettlement{Status: settlement, Event: event}); err != nil {
		return &ProxyError{Code: "usage_settlement_failed", Status: 503, Message: "Operation accounting could not be persisted"}
	}
	return nil
}

func nativeUsageUncertain(result NativeAPIResult, callErr error) bool {
	return operationUsageUncertain(result.Status, result.UsageKnown, callErr)
}

func (s *NativeAPIService) markNativeUncertain(ctx context.Context, id string) error {
	return retainUncertainUsage(ctx, s.store, id)
}

func nativeKeyRequiresUsage(key domain.APIKey, model string) bool {
	for _, rule := range key.Limits {
		if rule.Type != domain.LimitCredits && (rule.ModelFilter == nil || *rule.ModelFilter == model) {
			return true
		}
	}
	return false
}

// The caller has validated the object and selected the effective model; use it
// on the wire as well as for source selection and billing. Raw values retain
// unknown fields, explicit nulls and the provider's numeric precision.
func nativeRequestModel(body json.RawMessage, model string) json.RawMessage {
	var object map[string]json.RawMessage
	_ = json.Unmarshal(body, &object)
	object["model"] = jsonString(model)
	encoded, _ := json.Marshal(object)
	return encoded
}

func (s *NativeAPIService) recordOutcome(accountID, reservationID string, quota, success bool) error {
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.store.RecordAccountOutcome(cleanup, accountID, reservationID, quota, success)
}

func (s *NativeAPIService) recordNativeDiagnostic(ctx context.Context, options ResponseOptions, account domain.Account, model, status string, event domain.UsageEvent, request, response json.RawMessage) {
	if s.diagnostics == nil || status != "error" {
		return
	}
	s.diagnostics(ctx, ErrorDiagnostic{
		RequestID: event.RequestID, AccountID: account.ID, AccountGeneration: account.Generation, KeyID: options.KeyID,
		Transport: boundedMetadata(options.Transport, 32), Model: model, Status: "error",
		ErrorCode: event.ErrorCode, OccurredAt: s.now(), Request: request, Response: response,
		Events: []json.RawMessage{},
	})
}

func nativeEmbeddingsModel(ctx context.Context, store NativeAPIStore, keyID string, body json.RawMessage) (domain.APIKey, string, error) {
	now := time.Now()
	key, err := store.GetAPIKey(ctx, keyID)
	if err != nil || !key.IsActive || key.ExpiresAt != nil && !key.ExpiresAt.After(now) {
		return domain.APIKey{}, "", &ProxyError{Code: "invalid_api_key", Status: 401, Message: "Invalid or expired API key"}
	}
	var request struct {
		Model string          `json:"model"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &request) != nil || strings.TrimSpace(request.Model) == "" || len(request.Input) == 0 || string(request.Input) == "null" {
		return domain.APIKey{}, "", &ProxyError{Code: "invalid_request", Status: 400, Message: "model and input are required"}
	}
	model := strings.TrimSpace(request.Model)
	if key.EnforcedModel != nil {
		model = *key.EnforcedModel
	}
	if len(key.AllowedModels) != 0 && !modelAllowed(key.AllowedModels, model) {
		return domain.APIKey{}, "", &ProxyError{Code: "model_not_allowed", Status: 403, Message: "This key does not allow the requested model"}
	}
	return key, model, nil
}

func selectEmbeddingsSource(ctx context.Context, store NativeAPIStore, keyID, model string) (domain.Account, domain.ModelSource, domain.ModelSourceModel, error) {
	accounts, err := store.EligibleAccounts(ctx, keyID)
	if err != nil && !errors.Is(err, domain.ErrNoAccounts) {
		return domain.Account{}, domain.ModelSource{}, domain.ModelSourceModel{}, err
	}
	eligible := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		eligible[account.ID] = true
	}
	sources, err := store.ListModelSources(ctx)
	if err != nil {
		return domain.Account{}, domain.ModelSource{}, domain.ModelSourceModel{}, err
	}
	for _, source := range sources {
		if !source.Enabled || !source.Embeddings || source.Kind != domain.ModelSourceOpenAICompatible || !eligible[source.ID] {
			continue
		}
		if sourceModel, ok := source.Model(model); ok {
			for _, candidate := range accounts {
				if candidate.ID == source.ID && candidate.Kind == domain.AccountExternal && candidate.Status == domain.AccountActive && !candidate.RequiresEgressDecision {
					return candidate, source, sourceModel, nil
				}
			}
		}
	}
	return domain.Account{}, domain.ModelSource{}, domain.ModelSourceModel{}, &ProxyError{Code: "model_not_found", Status: 404, Message: "No capable model source is configured for the requested model"}
}

func embeddingsBudget(body json.RawMessage, price pricing.Price) domain.UsageAmount {
	var request struct {
		Input json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(body, &request)
	budget := domain.UsageAmount{InputTokens: max(1, min(8192, int64(len(request.Input)/4))), OutputTokens: 1}
	budget.CostMicrodollars, _ = price.Cost(budget, "")
	return budget
}

func optionsHasStream(body json.RawMessage) bool {
	var request struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &request)
	return request.Stream
}

func nativeUsageEvent(options ResponseOptions, account domain.Account, model, status string, result NativeAPIResult, id string, started, attempted time.Time) domain.UsageEvent {
	return domain.UsageEvent{
		RequestID: id, APIKeyID: options.KeyID, AccountID: account.ID, AccountGeneration: account.Generation, ModelSourceID: account.ID,
		Model: model, ConversationID: boundedMetadata(options.ConversationID, 256),
		UserAgent: boundedMetadata(options.UserAgent, 256), UserAgentGroup: boundedMetadata(options.UserAgentGroup, 128),
		ClientIP: boundedMetadata(options.ClientIP, 64), PlanType: account.PlanType,
		Source: string(account.Kind), Transport: boundedMetadata(options.Transport, 32),
		RequestKind: "embeddings", Status: status, RequestedAt: attempted.UTC(),
		ServiceTier:    boundedMetadata(result.ServiceTier, 64),
		TotalLatencyMS: time.Since(started).Milliseconds(), Usage: result.Usage,
		ReasoningTokensKnown: result.Usage.ReasoningTokens > 0,
	}
}
