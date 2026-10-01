package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

type PublicWarmupSubmitted struct {
	AccountID string `json:"account_id"`
	RequestID string `json:"request_id"`
	Model     string `json:"model"`
}

type PublicWarmupSkipped struct {
	AccountID string `json:"account_id"`
	Reason    string `json:"reason"`
}

type PublicWarmupFailed struct {
	AccountID    string `json:"account_id"`
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

type PublicWarmupSummary struct {
	Mode          string                  `json:"mode"`
	TotalAccounts int                     `json:"total_accounts"`
	Submitted     []PublicWarmupSubmitted `json:"submitted"`
	Skipped       []PublicWarmupSkipped   `json:"skipped"`
	Failed        []PublicWarmupFailed    `json:"failed"`
}

type publicWarmupOutcome struct {
	requestID string
	err       error
	result    CodexOperationResult
}

// PublicWarmup validates a key-scoped target set before running bounded pinned
// compact calls. Each dispatched account has its own durable key reservation.
func (s *CodexOperations) PublicWarmup(ctx context.Context, options CodexOperationOptions, mode string) (PublicWarmupSummary, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "normal" && mode != "strict" && mode != "force" {
		return PublicWarmupSummary{}, &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid warmup mode"}
	}
	summary := PublicWarmupSummary{Mode: mode, Submitted: []PublicWarmupSubmitted{}, Skipped: []PublicWarmupSkipped{}, Failed: []PublicWarmupFailed{}}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	key, err := s.activeKey(ctx, options.KeyID)
	if err != nil {
		return summary, err
	}
	settings, err := s.store.LoadSettings(ctx)
	if err != nil {
		return summary, err
	}
	body, _ := json.Marshal(map[string]any{"model": settings.WarmupModel, "instructions": "Warmup request.", "input": "warmup", "store": false})
	request, err := parseResponse(body, key, settings, false)
	if err != nil {
		return summary, err
	}
	accounts, err := s.store.EligibleAccounts(ctx, key.ID)
	if errors.Is(err, domain.ErrNoAccounts) {
		return summary, nil
	}
	if err != nil {
		return summary, err
	}
	candidates := make([]domain.Account, 0, len(accounts))
	for _, account := range accounts {
		if account.Kind == domain.AccountChatGPT && account.Status == domain.AccountActive {
			candidates = append(candidates, account)
		}
	}
	if len(candidates) == 0 {
		return summary, nil
	}
	candidates, _, err = filterCatalogAccounts(s.Catalog, &request, candidates, "", false)
	if err != nil {
		return summary, err
	}
	if _, err := s.resolvePrice(ctx, candidates[0], request.Model); err != nil {
		return summary, err
	}
	body, err = json.Marshal(request.Object)
	if err != nil {
		return summary, err
	}
	summary.TotalAccounts = len(candidates)
	selected := make([]domain.Account, 0, len(candidates))
	for _, account := range candidates {
		if mode != "force" {
			eligible, err := s.publicWarmupTelemetryEligible(ctx, account.ID)
			if err != nil {
				return PublicWarmupSummary{}, err
			}
			if !eligible {
				if mode == "strict" {
					return PublicWarmupSummary{}, &ProxyError{Code: "warmup_ineligible", Status: 400, Message: "Strict warmup requires unused primary usage for every account"}
				}
				summary.Skipped = append(summary.Skipped, PublicWarmupSkipped{AccountID: account.ID, Reason: "ineligible_primary_usage"})
				continue
			}
		}
		selected = append(selected, account)
	}
	if len(selected) == 0 {
		return summary, nil
	}
	results := make([]publicWarmupOutcome, len(selected))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(5, len(selected)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				results[index] = s.publicWarmupAccount(ctx, options, key, selected[index], request.Model, body)
			}
		}()
	}
send:
	for index := range selected {
		select {
		case <-ctx.Done():
			break send
		case jobs <- index:
		}
	}
	close(jobs)
	workers.Wait() // Already-started reservations finish cleanup even after cancellation.
	if err := ctx.Err(); err != nil {
		return PublicWarmupSummary{}, err
	}
	for index, outcome := range results {
		accountID := selected[index].ID
		if outcome.err == nil && !outcome.result.Failed && outcome.result.Status >= 200 && outcome.result.Status < 300 {
			summary.Submitted = append(summary.Submitted, PublicWarmupSubmitted{AccountID: accountID, RequestID: outcome.requestID, Model: request.Model})
		} else {
			code := publicWarmupErrorCode(outcome.err, outcome.result)
			summary.Failed = append(summary.Failed, PublicWarmupFailed{AccountID: accountID, ErrorCode: code, ErrorMessage: "Warmup request failed"})
		}
	}
	return summary, nil
}

func (s *CodexOperations) publicWarmupTelemetryEligible(ctx context.Context, accountID string) (bool, error) {
	quotas, err := s.store.ListAccountQuota(ctx, accountID)
	if err != nil {
		return false, err
	}
	for _, quota := range quotas {
		if quota.Window == "primary" && quota.WindowMinutes != nil && *quota.WindowMinutes == 300 && quota.UsedPercent <= 0 {
			return true, nil
		}
	}
	return false, nil
}

func (s *CodexOperations) publicWarmupAccount(ctx context.Context, options CodexOperationOptions, key domain.APIKey, account domain.Account, model string, body json.RawMessage) publicWarmupOutcome {
	started := s.now()
	release, err := s.acquire(ctx)
	if err != nil {
		return publicWarmupOutcome{err: err}
	}
	defer release()
	target := CodexOperationTarget{Account: account, KeyID: key.ID}
	var requestID string
	result, err := s.billedAdmitted(ctx, options, key, account, model, "warmup", target, func(id string, target CodexOperationTarget) (CodexOperationResult, error) {
		requestID = id
		if ctx.Err() != nil {
			return CodexOperationResult{}, &ProviderFailure{Code: "request_cancelled", Dispatched: false}
		}
		currentKey, keyErr := s.activeKey(ctx, key.ID)
		if keyErr != nil || currentKey.EnforcedModel != nil && canonicalModel(*currentKey.EnforcedModel) != canonicalModel(model) ||
			len(currentKey.AllowedModels) > 0 && !modelAllowed(currentKey.AllowedModels, model) {
			return CodexOperationResult{}, &ProviderFailure{Code: "warmup_scope_changed", Dispatched: false}
		}
		current, scopeErr := s.store.ScopedAccountForOwner(ctx, key.ID, account.ID, s.now())
		if scopeErr != nil || current.Kind != domain.AccountChatGPT || current.Status != domain.AccountActive || current.RequiresEgressDecision {
			return CodexOperationResult{}, &ProviderFailure{Code: "warmup_scope_changed", Dispatched: false}
		}
		target.Account = current
		return s.provider.Compact(ctx, target, body)
	}, nil, body, false, started)
	return publicWarmupOutcome{requestID: requestID, result: result, err: err}
}

func publicWarmupErrorCode(err error, result CodexOperationResult) string {
	if errors.Is(err, domain.ErrLimitReached) {
		return "api_key_limit_exceeded"
	}
	if errors.Is(err, domain.ErrNoAccounts) || errors.Is(err, domain.ErrNotFound) {
		return "warmup_scope_changed"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "request_cancelled"
	}
	var failure *ProviderFailure
	if errors.As(err, &failure) {
		switch failure.Code {
		case "warmup_scope_changed", "request_cancelled", "usage_unavailable":
			return failure.Code
		}
	}
	if compactQuotaRefusal(result, err) {
		return "upstream_quota_refused"
	}
	var proxy *ProxyError
	if errors.As(err, &proxy) {
		switch proxy.Code {
		case "local_capacity_exceeded", "usage_settlement_failed":
			return proxy.Code
		}
	}
	return "upstream_error"
}
