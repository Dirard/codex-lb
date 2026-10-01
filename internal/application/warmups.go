package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

const (
	warmupRequestKind  = "warmup"
	warmupMaxOutput    = 16
	warmupKeyID        = domain.WarmupKeyID
	defaultWarmupModel = "gpt-6-luna"
)

var errWarmupIneligible = errors.New("warmup account ineligible")
var errWarmupQuotaRefused = errors.New("warmup quota refused")

// Exported sentinel errors for safe HTTP mapping.
var ErrWarmupIneligible = errWarmupIneligible
var ErrWarmupQuotaRefused = errWarmupQuotaRefused

// WarmupAdmission is the shared bounded-admission boundary owned by Proxy.
type WarmupAdmission interface {
	Acquire(context.Context) (func(), error)
}

// WarmupLedger shares settlement and reconciliation, not client key budgets.
type WarmupLedger interface {
	ReserveWarmupUsage(context.Context, string, string, string, int64, bool, time.Time) error
	SettleUsage(context.Context, string, domain.UsageSettlement) (bool, error)
	MarkReservationUncertain(context.Context, string) (bool, error)
}

// UsageRefresher re-polls upstream quota telemetry after a probe dispatch.
type UsageRefresher interface {
	RefreshAccountUsageForGeneration(context.Context, string, int64) error
}

// WarmupService executes a pinned synthetic admin request through the real
// provider pipeline. A private accounting principal owns its reservation;
// the caller (manual run-now or an automation job) owns target permission.
type WarmupService struct {
	accounts              Accounts
	provider              ResponseProvider
	ledger                WarmupLedger
	admission             WarmupAdmission
	refresher             UsageRefresher
	timeout               time.Duration
	now                   func() time.Time
	ResolvePrice          func(context.Context, domain.Account, string) (pricing.Price, error)
	Diagnostics           DiagnosticRecorder
	accountAdmissionProxy *Proxy
}

func (s *WarmupService) ConfigureAccountAdmission(proxy *Proxy) { s.accountAdmissionProxy = proxy }

func NewWarmupService(accounts Accounts, provider ResponseProvider, ledger WarmupLedger, admission WarmupAdmission, refresher UsageRefresher, now func() time.Time) *WarmupService {
	if now == nil {
		now = time.Now
	}
	return &WarmupService{
		accounts: accounts, provider: provider, ledger: ledger, admission: admission,
		refresher: refresher, timeout: 2 * time.Minute, now: now,
		ResolvePrice: func(_ context.Context, account domain.Account, model string) (pricing.Price, error) {
			if account.Kind != domain.AccountChatGPT {
				return pricing.Price{}, pricing.ErrUnpriced
			}
			_, price, err := pricing.LookupCodex(model)
			return price, err
		},
	}
}

func warmupEligible(account domain.Account) bool {
	if deletedAccount(account) || account.Kind != domain.AccountChatGPT {
		return false
	}
	// Imported proxy-bound accounts must never receive direct egress from a
	// warmup: no egress route is configured for them in the Go runtime.
	if account.RequiresEgressDecision {
		return false
	}
	switch account.Status {
	case domain.AccountReauthRequired, domain.AccountDeactivated:
		return false
	}
	// Paused is allowed only on the explicit admin paths that call this
	// service; those callers already enforced their own includePaused policy.
	return true
}

func warmupBody(model string, reasoningEffort *string, prompt string) (json.RawMessage, error) {
	if prompt == "" {
		prompt = domain.DefaultAutomationPrompt
	}
	object := map[string]any{
		"model":             model,
		"input":             prompt,
		"max_output_tokens": warmupMaxOutput,
		"store":             false,
	}
	if reasoningEffort != nil && *reasoningEffort != "" {
		object["reasoning"] = map[string]string{"effort": *reasoningEffort}
	}
	return json.Marshal(object)
}

// ExecuteWarmup performs one content-free synthetic request and records the
// usage event + quota outcome. A transport timeout or local failure never
// marks quota: only an explicit provider quota refusal does.
func (s *WarmupService) ExecuteWarmup(ctx context.Context, accountID, model string, reasoningEffort *string, prompt string) error {
	return s.executeWarmup(ctx, accountID, -1, model, reasoningEffort, prompt, false)
}

func (s *WarmupService) ExecuteWarmupForGeneration(ctx context.Context, accountID string, generation int64, model string, reasoningEffort *string, prompt string) error {
	return s.executeWarmup(ctx, accountID, generation, model, reasoningEffort, prompt, false)
}

// ExecuteLimitWarmup applies the stricter automatic-send gate immediately
// before provider dispatch, after the scheduler has durably claimed the tuple.
func (s *WarmupService) ExecuteLimitWarmup(ctx context.Context, accountID string, generation int64, model, prompt string) error {
	return s.executeWarmup(ctx, accountID, generation, model, nil, prompt, true)
}

func (s *WarmupService) executeWarmup(ctx context.Context, accountID string, generation int64, model string, reasoningEffort *string, prompt string, automatic bool) error {
	if model == "" {
		model = defaultWarmupModel
	}
	body, err := warmupBody(model, reasoningEffort, prompt)
	if err != nil {
		return err
	}
	if s.admission != nil {
		release, err := s.admission.Acquire(ctx)
		if err != nil {
			return err
		}
		defer release()
	}
	account, err := s.accounts.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if generation >= 0 && account.Generation != generation {
		return domain.ErrConflict
	}
	if !warmupEligible(account) || (automatic && (account.Status != domain.AccountActive || !account.LimitWarmupEnabled)) {
		return errWarmupIneligible
	}
	var lease *accountLease
	if s.accountAdmissionProxy != nil {
		settings, err := s.accountAdmissionProxy.store.LoadSettings(ctx)
		if err != nil {
			return err
		}
		_, lease, err = s.accountAdmissionProxy.admittedAccount(ctx,
			[]accountCandidate{{account: account}}, settings, "", false, true)
		if err != nil {
			return err
		}
		defer lease.release()
	}
	requestID := warmupRequestID()
	started := s.now().UTC()
	if err := s.ledger.ReserveWarmupUsage(ctx, requestID, account.ID, model, account.Generation, automatic, started); err != nil {
		return err
	}
	callCtx, cancelCall := context.WithTimeout(ctx, s.timeout)
	defer cancelCall()
	target := ResponseTarget{Account: account, KeyID: warmupKeyID}
	if lease != nil {
		target.OnFirstUpstreamEvent = lease.releaseCreate
	}
	result, callErr := s.dispatch(callCtx, target, body)
	lease.release()
	finished := s.now().UTC()

	quotaRefused := quotaFailure(result, callErr)
	if callErr == nil && result.Failed {
		callErr = &ProviderFailure{Code: result.ErrorCode, Status: 502, Dispatched: true, QuotaRefused: quotaRefused}
	}
	if callErr == nil && !result.UsageKnown {
		callErr = &ProviderFailure{Code: "usage_unavailable", Status: 502, Dispatched: true}
	}
	err = s.recordWarmup(ctx, requestID, account, model, result, started, finished, callErr, quotaRefused)
	if s.Diagnostics != nil && (err != nil || callErr != nil) {
		code := operationErrorCode(callErr)
		if err != nil {
			code = "usage_settlement_failed"
		}
		s.Diagnostics(ctx, ErrorDiagnostic{RequestID: requestID, AccountID: account.ID, AccountGeneration: account.Generation, Model: model, Status: "error", ErrorCode: code, OccurredAt: started, Request: body, Response: result.Response})
	}
	if err != nil {
		return err
	}
	if quotaRefused {
		return errWarmupQuotaRefused
	}
	if callErr != nil {
		return callErr
	}
	return nil
}

func (s *WarmupService) dispatch(ctx context.Context, target ResponseTarget, body json.RawMessage) (result ResponseResult, err error) {
	return callSafely(func() (ResponseResult, error) { return s.provider.Respond(ctx, target, body, nil) })
}

func (s *WarmupService) recordWarmup(ctx context.Context, requestID string, account domain.Account, model string, result ResponseResult, started, finished time.Time, callErr error, quotaRefused bool) error {
	safeZero := quotaRefused && !result.OutputObserved && !result.UsageKnown && !result.UsageReported
	if operationUsageUncertain(200, result.UsageKnown, callErr) && !safeZero {
		return retainUncertainUsage(ctx, s.ledger, requestID)
	}
	status, settlement := "ok", "finalized"
	if callErr != nil || result.Failed {
		status, settlement = "error", "failed"
	}
	event := domain.UsageEvent{
		RequestID:         requestID,
		AccountID:         account.ID,
		AccountGeneration: account.Generation,
		Model:             model,
		ServiceTier:       result.ServiceTier,
		RequestKind:       warmupRequestKind,
		Status:            status,
		RequestedAt:       started,
		TotalLatencyMS:    finished.Sub(started).Milliseconds(),
		FirstEventMS:      result.FirstEventMS,
		ConnectLatencyMS:  result.ConnectLatencyMS,
		Usage:             result.Usage,
		ErrorCode:         operationErrorCode(callErr),
	}
	if !result.UsageKnown {
		event.Usage = domain.UsageAmount{}
	}
	if quotaRefused {
		event.Status, event.ErrorCode = "quota_refused", "quota_refused"
	}
	cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelCleanup()
	if event.Usage.InputTokens != 0 || event.Usage.OutputTokens != 0 {
		price, err := s.ResolvePrice(cleanup, account, model)
		if err == nil {
			event.Usage.CostMicrodollars, err = price.Cost(event.Usage, result.ServiceTier)
		}
		if err != nil {
			if holdErr := retainUncertainUsage(cleanup, s.ledger, requestID); holdErr != nil {
				return holdErr
			}
			return &ProxyError{Code: "invalid_upstream_usage", Status: 502, Message: "Warmup usage could not be priced; reconciliation required"}
		}
	}
	_, err := s.ledger.SettleUsage(cleanup, requestID, domain.UsageSettlement{Status: settlement, Event: event})
	if err != nil {
		if markErr := retainUncertainUsage(cleanup, s.ledger, requestID); markErr != nil {
			return markErr
		}
		return &ProxyError{Code: "usage_settlement_failed", Status: 503, Message: "Warmup accounting could not be persisted; reconciliation required"}
	}
	return nil
}

func warmupRequestID() string {
	entropy := make([]byte, 12)
	if _, err := rand.Read(entropy); err != nil {
		return "warmup_" + hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))[:24]
	}
	return "warmup_" + hex.EncodeToString(entropy)
}

var _ WarmupExecutor = (*WarmupService)(nil)
