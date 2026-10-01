package application

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math"
	"time"

	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

type blockedCodexAdmission struct{}

func (blockedCodexAdmission) Acquire(context.Context) (func(), error) {
	return nil, &ProxyError{Code: "local_capacity_exceeded", Status: 503, Message: "Codex operation admission is not configured"}
}

func defaultCodexPrice(store ModelSourceRepository) func(context.Context, domain.Account, string) (pricing.Price, error) {
	return func(ctx context.Context, account domain.Account, model string) (pricing.Price, error) {
		if account.Kind == domain.AccountExternal {
			source, err := store.GetModelSource(ctx, account.ID)
			if err != nil {
				return pricing.Price{}, err
			}
			entry, ok := source.Model(model)
			if !ok {
				return pricing.Price{}, pricing.ErrUnpriced
			}
			return ModelSourcePrice(entry)
		}
		if model == transcriptionModel {
			// Subscription transcription has no token-usage response contract.
			// Its reservation is still enforced and settled to zero usage.
			return pricing.Price{}, nil
		}
		_, price, err := pricing.LookupCodex(model)
		return price, err
	}
}

func (s *CodexOperations) acquire(ctx context.Context) (func(), error) {
	if s.admission == nil {
		return nil, &ProxyError{Code: "local_capacity_exceeded", Status: 503, Message: "Codex operation admission is not configured"}
	}
	release, err := s.admission.Acquire(ctx)
	if err != nil && release != nil {
		release()
	}
	if release == nil && err == nil {
		release = func() {}
	}
	return release, err
}

func (s *CodexOperations) billed(
	ctx context.Context,
	options CodexOperationOptions,
	key domain.APIKey,
	account domain.Account,
	model string,
	requestKind string,
	target CodexOperationTarget,
	call func() (CodexOperationResult, error),
	sourceModel *domain.ModelSourceModel,
	requestBody []byte,
	continuationFilePinned bool,
) (CodexOperationResult, error) {
	started := s.now()
	release, err := s.acquire(ctx)
	if err != nil {
		return CodexOperationResult{}, err
	}
	defer release()
	return s.billedAdmitted(ctx, options, key, account, model, requestKind, target,
		func(reservationID string, _ CodexOperationTarget) (CodexOperationResult, error) { return call() }, sourceModel,
		requestBody, continuationFilePinned, started)
}

// billedAdmitted settles the operation using its already-owned capacity lease.
func (s *CodexOperations) billedAdmitted(
	ctx context.Context, options CodexOperationOptions, key domain.APIKey,
	account domain.Account, model, requestKind string, target CodexOperationTarget,
	call func(reservationID string, target CodexOperationTarget) (CodexOperationResult, error), sourceModel *domain.ModelSourceModel,
	requestBody []byte, continuationFilePinned bool, started time.Time,
) (CodexOperationResult, error) {
	if s.accountAdmissionProxy != nil && account.Kind == domain.AccountChatGPT &&
		(requestKind == "compaction" || requestKind == "warmup") {
		lease := target.admissionLease
		if lease == nil {
			settings, err := s.store.LoadSettings(ctx)
			if err != nil {
				return CodexOperationResult{}, err
			}
			_, lease, err = s.accountAdmissionProxy.admittedAccount(ctx,
				[]accountCandidate{{account: account}}, settings, key.ID, target.ConfirmedOwner, true)
			if err != nil {
				return CodexOperationResult{}, err
			}
		}
		target.OnFirstUpstreamEvent = lease.releaseCreate
		defer lease.release()
		upstreamCall := call
		call = func(reservationID string, target CodexOperationTarget) (CodexOperationResult, error) {
			defer lease.release()
			return upstreamCall(reservationID, target)
		}
	}
	price, err := s.resolvePrice(ctx, account, model)
	if err != nil {
		return CodexOperationResult{}, err
	}
	budget := requestBudget(requestBody, price)
	if requestKind == "transcription" && budget.CostMicrodollars == 0 {
		// Subscription transcription does not expose a token-usage response; use
		// the legacy conservative unknown-model reservation cost, then settle it
		// back to the authoritative zero subscription usage.
		budget.CostMicrodollars = 2_000_000
	}
	if requestKind == "transcription" && sourceModel != nil && sourceModel.AudioPerMinute != nil {
		audioBudget := int64(math.Ceil(*sourceModel.AudioPerMinute * transcriptionMaxSeconds / 60 * 1_000_000))
		budget.CostMicrodollars = max(budget.CostMicrodollars, audioBudget)
	}
	id := "req_" + rand.Text()
	attemptAt := s.now()
	_, err = s.store.ReserveUsage(ctx, domain.ReservationRequest{
		ID: id, APIKeyID: key.ID, AccountID: account.ID, AccountGeneration: account.Generation, RouteRevision: account.RouteRevision, Continuation: true,
		Model: model, Budget: budget, Now: attemptAt,
	})
	if err != nil {
		return CodexOperationResult{}, err
	}

	result, callErr := callSafely(func() (CodexOperationResult, error) {
		if err := target.affinity.save(ctx, s.store, account.ID, id); err != nil {
			return CodexOperationResult{}, &ProviderFailure{Code: "affinity_persistence_failed", Status: 503}
		}
		if options.OnDispatch != nil {
			options.OnDispatch()
		}
		return call(id, target)
	})
	quotaRefused := compactQuotaRefusal(result, callErr)
	usage := result.Usage
	known := result.UsageKnown
	if requestKind == "transcription" && sourceModel != nil && sourceModel.AudioPerMinute != nil {
		known = result.AudioSecondsKnown
		usage = domain.UsageAmount{}
		cost := math.Round(result.AudioSeconds / 60 * *sourceModel.AudioPerMinute * 1_000_000)
		if result.AudioSeconds < 0 || result.AudioSeconds > transcriptionMaxSeconds || math.IsNaN(cost) || math.IsInf(cost, 0) || cost >= math.MaxInt64 || cost < 0 {
			known = false
			result.UsageReported, result.UsageKnown = true, false
			callErr = &ProviderFailure{Code: "invalid_upstream_usage", Status: 502, Dispatched: true, QuotaRefused: quotaRefused}
		} else if known {
			usage.CostMicrodollars = int64(cost)
		}
	} else if known {
		usage.CostMicrodollars, err = price.Cost(usage, result.ServiceTier)
		if err != nil {
			known = false
			result.UsageReported, result.UsageKnown = true, false
			callErr = &ProviderFailure{Code: "invalid_upstream_usage", Status: 502, Dispatched: true, QuotaRefused: quotaRefused}
		}
	}
	if !known && callErr == nil && !result.Failed && result.Status >= 200 && result.Status < 300 {
		callErr = &ProviderFailure{Code: "usage_unavailable", Status: 502, Dispatched: true}
	}
	status := "success"
	if callErr != nil || result.Failed || result.Status < 200 || result.Status >= 300 {
		status = "error"
	}
	if status == "error" && cancelledCall(callErr) {
		status = "cancelled"
	}
	event := s.usageEvent(options, account, model, requestKind, status, result, usage, id, started, attemptAt)
	if status == "cancelled" {
		event.ErrorCode = "request_cancelled"
	}
	if status == "error" {
		if callErr != nil {
			event.ErrorCode = operationErrorCode(callErr)
		} else if result.ErrorCode != "" {
			event.ErrorCode = result.ErrorCode
		} else {
			event.ErrorCode = "upstream_error"
		}
	}
	safeZero := quotaRefused && !known && !result.UsageReported && !result.OutputObserved
	if !known && (result.UsageReported || result.OutputObserved) || operationUsageUncertain(result.Status, known, callErr) && !safeZero {
		if err := retainUncertainUsage(ctx, s.store, id); err != nil {
			return CodexOperationResult{}, err
		}
		if quotaRefused {
			if err := s.recordOutcome(account.ID, id, true, false); err != nil {
				return CodexOperationResult{}, err
			}
		}
		s.recordDiagnostic(ctx, options, account, model, status, event, requestBody, result.Body)
		return result, callErr
	}
	settlementStatus := "finalized"
	if status == "error" {
		settlementStatus = "failed"
	}
	if err := s.settle(ctx, id, domain.UsageSettlement{Status: settlementStatus, Event: event}); err != nil {
		return CodexOperationResult{}, err
	}
	if status == "success" || quotaRefused {
		if err := s.recordOutcome(account.ID, id, quotaRefused, status == "success"); err != nil {
			return CodexOperationResult{}, err
		}
	}
	if requestKind == "compaction" && status == "success" {
		if err := s.rememberCompact(ctx, options, account, model, result, id, continuationFilePinned, target.TurnState != ""); err != nil {
			return CodexOperationResult{}, err
		}
	}
	s.recordDiagnostic(ctx, options, account, model, status, event, requestBody, result.Body)
	return result, callErr
}

func (s *CodexOperations) rememberCompact(
	ctx context.Context, options CodexOperationOptions, account domain.Account, model string,
	result CodexOperationResult, reservationID string, filePinned, forwardTurn bool,
) error {
	var response struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(result.Body, &response) != nil || response.ID == "" {
		return nil
	}
	now := s.now()
	continuation := domain.Continuation{
		ResponseID: response.ID, KeyID: options.KeyID, AccountID: account.ID, AccountGeneration: account.Generation, RouteRevision: account.RouteRevision, ProviderID: account.Provider,
		Model: model, CreatedAt: now, ExpiresAt: now.Add(s.ttl), FilePinned: filePinned,
	}
	if err := s.store.SaveContinuation(ctx, continuation, domain.ContinuationBounds{
		MaxRecords: 4096, MaxContextBytes: 128 << 20,
	}); err != nil {
		return &ProxyError{Code: "continuation_persistence_failed", Status: 503, Message: "Compaction ownership could not be persisted"}
	}
	if err := rememberConversationAliases(ctx, s.store, continuation, options.identity(), forwardTurn, reservationID, domain.ContinuationBounds{
		MaxRecords: 4096, MaxContextBytes: 128 << 20,
	}); err != nil {
		return &ProxyError{Code: "continuation_persistence_failed", Status: 503, Message: "Compaction session ownership could not be persisted"}
	}
	return nil
}

func (s *CodexOperations) contentFree(
	ctx context.Context,
	options CodexOperationOptions,
	account domain.Account,
	model string,
	requestKind string,
	requestBody []byte,
	target CodexOperationTarget,
	call func() (CodexOperationResult, error),
) (CodexOperationResult, error) {
	_ = target
	started := s.now()
	release, err := s.acquire(ctx)
	if err != nil {
		return CodexOperationResult{}, err
	}
	defer release()
	attemptAt := s.now()
	result, callErr := callSafely(call)
	status := "success"
	if callErr != nil || result.Failed || result.Status < 200 || result.Status >= 300 {
		status = "error"
	}
	if status == "error" && cancelledCall(callErr) {
		status = "cancelled"
	}
	event := s.usageEvent(options, account, model, requestKind, status, result, domain.UsageAmount{}, "req_"+rand.Text(), started, attemptAt)
	if status == "cancelled" {
		event.ErrorCode = "request_cancelled"
	}
	if status == "error" {
		event.ErrorCode = operationErrorCode(callErr)
		if event.ErrorCode == "" {
			event.ErrorCode = result.ErrorCode
		}
		if event.ErrorCode == "" {
			event.ErrorCode = "upstream_error"
		}
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.store.RecordCodexContentFreeUsage(cleanup, event); err != nil {
		return CodexOperationResult{}, &ProxyError{Code: "usage_settlement_failed", Status: 503, Message: "Operation statistics could not be persisted"}
	}
	if requestKind != "realtime_call" && requestKind != "realtime_live" {
		s.recordDiagnostic(ctx, options, account, model, status, event, requestBody, result.Body)
	}
	return result, callErr
}

func cancelledCall(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (s *CodexOperations) recordDiagnostic(
	ctx context.Context, options CodexOperationOptions, account domain.Account, model, status string,
	event domain.UsageEvent, request, response []byte,
) {
	if s.Diagnostics == nil || status != "error" || event.RequestID == "" {
		return
	}
	s.Diagnostics(ctx, ErrorDiagnostic{
		RequestID: event.RequestID, AccountID: account.ID, AccountGeneration: account.Generation, KeyID: options.KeyID,
		Transport: boundedMetadata(options.Transport, 32), Model: model, Status: "error",
		ErrorCode: event.ErrorCode, OccurredAt: s.now(),
		Request: append(json.RawMessage(nil), request...), Response: append(json.RawMessage(nil), response...),
		Events: []json.RawMessage{}, EventsTruncated: false,
	})
}

func (s *CodexOperations) usageEvent(
	options CodexOperationOptions, account domain.Account, model, requestKind, status string,
	result CodexOperationResult, usage domain.UsageAmount, id string, started, attempted time.Time,
) domain.UsageEvent {
	return domain.UsageEvent{
		RequestID: id, APIKeyID: options.KeyID, AccountID: account.ID, AccountGeneration: account.Generation,
		ModelSourceID: modelSourceID(account), Model: model,
		ConversationID: boundedMetadata(options.ConversationID, 256), UserAgent: boundedMetadata(options.UserAgent, 256),
		UserAgentGroup: boundedMetadata(options.UserAgentGroup, 128), ClientIP: boundedMetadata(options.ClientIP, 64),
		PlanType: account.PlanType, Source: string(account.Kind), Transport: boundedMetadata(options.Transport, 32),
		ServiceTier: result.ServiceTier, RequestKind: requestKind, Status: status, ErrorCode: result.ErrorCode,
		RequestedAt: attempted.UTC(), TotalLatencyMS: s.now().Sub(started).Milliseconds(), Usage: usage,
		ReasoningTokensKnown: usage.ReasoningTokens > 0,
	}
}

func (s *CodexOperations) settle(ctx context.Context, id string, settlement domain.UsageSettlement) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, err := s.store.SettleUsage(cleanup, id, settlement)
	if err != nil {
		return &ProxyError{Code: "usage_settlement_failed", Status: 503, Message: "Operation accounting could not be persisted"}
	}
	return nil
}

func (s *CodexOperations) recordOutcome(accountID, reservationID string, quota, success bool) error {
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.store.RecordAccountOutcome(cleanup, accountID, reservationID, quota, success)
}

func requestBudget(body []byte, price pricing.Price) domain.UsageAmount {
	budget := domain.UsageAmount{InputTokens: max(1, min(8192, int64(len(body)/4))), OutputTokens: 2048}
	budget.CostMicrodollars, _ = price.Cost(budget, "")
	return budget
}

func operationErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var failure *ProviderFailure
	if errors.As(err, &failure) {
		return failure.Code
	}
	return "upstream_error"
}

func modelFromCompact(body json.RawMessage) string {
	var request struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &request)
	return request.Model
}

func controlModel(path string) string { return "codex-control:" + path }

func modelSourceID(account domain.Account) string {
	if account.Kind == domain.AccountExternal {
		return account.ID
	}
	return ""
}

func boundedMetadata(value string, maximum int) string {
	if len(value) > maximum {
		return value[:maximum]
	}
	return value
}
