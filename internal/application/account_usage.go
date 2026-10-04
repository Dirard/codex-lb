package application

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

// UsageError preserves upstream usage/reset-credit failure classification.
type UsageError struct {
	Code    string
	Message string
	Status  int
}

func (e *UsageError) Error() string { return e.Message }

var ErrNoAvailableResetCredit = fmt.Errorf("no available reset credit: %w", domain.ErrConflict)

type UsageWindow struct {
	UsedPercent   *float64   `json:"-"`
	ResetAt       *time.Time `json:"-"`
	WindowMinutes *int       `json:"-"`
}

type UsageCredits struct {
	Has       *bool   `json:"-"`
	Unlimited *bool   `json:"-"`
	Balance   *string `json:"-"`
}

type AdditionalQuota struct {
	LimitName      string       `json:"-"`
	MeteredFeature string       `json:"-"`
	Primary        *UsageWindow `json:"-"`
	Secondary      *UsageWindow `json:"-"`
}

// UsageSnapshot is telemetry metadata, not an upstream quota refusal. Polling
// never disables an active owner or changes credentials; confirmed resets may
// recover an older quota block through the separately guarded recovery path.
type UsageSnapshot struct {
	PlanType         string
	WorkspaceID      string
	WorkspaceLabel   string
	SeatType         string
	RateLimitAllowed *bool
	RateLimitReached *bool
	Primary          *UsageWindow
	Secondary        *UsageWindow
	Monthly          *UsageWindow
	Credits          UsageCredits
	ResetCreditCount *int
	AdditionalQuotas []AdditionalQuota
}

type ResetCredit struct {
	ID          string     `json:"id"`
	ResetType   string     `json:"resetType"`
	Status      string     `json:"status"`
	GrantedAt   *time.Time `json:"grantedAt"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	RedeemedAt  *time.Time `json:"redeemedAt"`
}

type ResetCredits struct {
	AvailableCount   int           `json:"availableCount"`
	NearestExpiresAt *time.Time    `json:"nearestExpiresAt"`
	Credits          []ResetCredit `json:"credits"`
}

type ResetCreditConsume struct {
	Code         string     `json:"code"`
	WindowsReset int        `json:"windowsReset"`
	RedeemedAt   *time.Time `json:"redeemedAt"`
}

// UsageClient is the ChatGPT usage protocol port; tests provide offline stubs.
type UsageClient interface {
	FetchUsage(ctx context.Context, accessToken, chatgptAccountID string) (UsageSnapshot, error)
	FetchResetCredits(ctx context.Context, accessToken, chatgptAccountID string) (ResetCredits, error)
	ConsumeResetCredit(ctx context.Context, accessToken, chatgptAccountID, creditID, redeemRequestID string) (ResetCreditConsume, error)
}

// ResetRedemptionStore durably pins (account, redeem request) to one credit.
type ResetRedemptionStore interface {
	GetPinnedResetCredit(ctx context.Context, accountID string, accountGeneration int64, redeemRequestID string) (ResetRedemption, bool, error)
	PinResetCredit(ctx context.Context, accountID string, accountGeneration int64, redeemRequestID, creditID string) (ResetRedemption, error)
}

// UsageTokenSource is the narrow token boundary shared with the proxy: access
// tokens come from the refresh-aware TokenService, never from raw decryption.
type UsageTokenSource interface {
	AccessToken(ctx context.Context, account domain.Account, credential domain.AccountCredential) (string, error)
	ForceRefresh(ctx context.Context, account domain.Account, rejectedToken string) (string, error)
}

type AccountUsageAccounts interface {
	Accounts
	SaveAccountUsageSnapshot(context.Context, domain.AccountUsageSnapshot) error
}

type AccountUsageService struct {
	accounts      AccountUsageAccounts
	tokens        UsageTokenSource
	usage         UsageClient
	pins          ResetRedemptionStore
	now           func() time.Time
	limitWarmup   *LimitWarmupService
	quotaRecovery AccountQuotaRecovery

	mu          sync.Mutex
	snapshots   map[accountGenerationKey]ResetCredits
	creditEpoch map[accountGenerationKey]uint64
	usageSeen   map[accountGenerationKey]UsageSnapshot
	redeeming   map[accountGenerationKey]*sync.Mutex
}

// ConfigureLimitWarmups wires automatic evaluation into successful background
// usage refreshes. Manual probes and one-account refreshes do not trigger it.
func (s *AccountUsageService) ConfigureLimitWarmups(service *LimitWarmupService) {
	s.limitWarmup = service
}

func NewAccountUsageService(accounts AccountUsageAccounts, tokens UsageTokenSource, usage UsageClient, pins ResetRedemptionStore, now func() time.Time) *AccountUsageService {
	if now == nil {
		now = time.Now
	}
	return &AccountUsageService{
		accounts: accounts, tokens: tokens, usage: usage, pins: pins, now: now,
		snapshots: make(map[accountGenerationKey]ResetCredits), creditEpoch: make(map[accountGenerationKey]uint64), usageSeen: make(map[accountGenerationKey]UsageSnapshot), redeeming: make(map[accountGenerationKey]*sync.Mutex),
	}
}

func usageEligible(account domain.Account) bool {
	if deletedAccount(account) || account.Kind != domain.AccountChatGPT || account.ChatGPTAccountID == "" || account.RequiresEgressDecision {
		return false
	}
	switch account.Status {
	case domain.AccountPaused, domain.AccountReauthRequired, domain.AccountDeactivated:
		return false
	}
	return true
}

func (s *AccountUsageService) accessToken(ctx context.Context, account domain.Account) (string, error) {
	credential, err := s.accounts.GetAccountCredential(ctx, account.ID)
	if err != nil {
		return "", err
	}
	if credential.Generation != account.Generation {
		return "", domain.ErrConflict
	}
	return s.tokens.AccessToken(ctx, account, credential)
}

// RefreshAccountUsage persists polled quota windows and may clear an older
// quota block after a confirmed reset. It never disables an active owner or
// overwrites credentials, operator policy or a newer provider outcome.
func (s *AccountUsageService) RefreshAccountUsage(ctx context.Context, accountID string) error {
	_, _, err := s.refreshAccountUsage(ctx, accountID, -1, false)
	return err
}

func (s *AccountUsageService) RefreshAccountUsageForGeneration(ctx context.Context, accountID string, generation int64) error {
	_, _, err := s.refreshAccountUsage(ctx, accountID, generation, false)
	return err
}

func (s *AccountUsageService) refreshAccountUsage(ctx context.Context, accountID string, generation int64, captureBefore bool) ([]domain.AccountQuota, []domain.AccountQuota, error) {
	account, err := s.accounts.GetAccount(ctx, accountID)
	if generation >= 0 && account.Generation != generation {
		return nil, nil, domain.ErrConflict
	}
	if err != nil || !usageEligible(account) {
		return nil, nil, err
	}
	var before []domain.AccountQuota
	canRecover := s.quotaRecovery != nil && quotaBlockedStatus(account.Status) && !account.RequiresEgressDecision
	var outcomeVersion int64
	if canRecover {
		outcomeVersion, err = s.quotaRecovery.CaptureQuotaOutcome(ctx, accountID)
		if err != nil {
			return nil, nil, err
		}
	}
	if captureBefore || canRecover {
		before, err = s.accounts.ListAccountQuota(ctx, accountID)
		if err != nil {
			return nil, nil, err
		}
	}
	refreshStarted := s.now().UTC()
	credential, err := s.accounts.GetAccountCredential(ctx, account.ID)
	if err != nil {
		return nil, nil, err
	}
	if credential.Generation != account.Generation {
		return nil, nil, domain.ErrConflict
	}
	token, err := s.tokens.AccessToken(ctx, account, credential)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := s.fetchUsageWithRefresh(ctx, account, token)
	if err != nil {
		return nil, nil, err
	}
	if account.WorkspaceID != "" && snapshot.WorkspaceID != "" && account.WorkspaceID != snapshot.WorkspaceID {
		return nil, nil, &UsageError{Code: "identity_mismatch", Message: "Usage response belongs to another workspace", Status: 502}
	}
	after := make([]domain.AccountQuota, 0, 3)
	observedAt := s.now().UTC()
	completeQuotas := true
	for window, usage := range map[string]*UsageWindow{"primary": snapshot.Primary, "secondary": snapshot.Secondary, "monthly": snapshot.Monthly} {
		if usage == nil {
			continue
		}
		if usage.UsedPercent == nil {
			completeQuotas = false
			continue
		}
		used := clampPercent(*usage.UsedPercent)
		quota := domain.AccountQuota{
			AccountID: account.ID, Window: window, UsedPercent: used,
			ResetAt: usage.ResetAt, WindowMinutes: usage.WindowMinutes, ObservedAt: observedAt,
		}
		after = append(after, quota)
	}
	reportedPlan := strings.ToLower(strings.TrimSpace(snapshot.PlanType))
	if !accountPlanTypes[reportedPlan] {
		reportedPlan = ""
	}
	windowsAvailable := quotaWindowsAvailable(before, after, refreshStarted)
	replaceQuotas := completeQuotas && len(after) > 0
	if canRecover && !windowsAvailable {
		replaceQuotas = false
	}
	stored := domain.AccountUsageSnapshot{AccountID: account.ID, ObservedAt: observedAt,
		FetchStartedAt: refreshStarted, ExpectedAccount: &account, ExpectedCredential: &credential,
		ReportedPlanType:       reportedPlan,
		ReportedWorkspaceID:    strings.TrimSpace(snapshot.WorkspaceID),
		ReportedWorkspaceLabel: strings.TrimSpace(snapshot.WorkspaceLabel), ReportedSeatType: strings.TrimSpace(snapshot.SeatType),
		Quotas: after, ReplaceQuotaWindows: replaceQuotas,
		AdditionalReported: snapshot.AdditionalQuotas != nil}
	if snapshot.Credits.Has != nil || snapshot.Credits.Unlimited != nil || snapshot.Credits.Balance != nil {
		credit := &domain.AccountCreditStatus{AccountID: account.ID, Has: snapshot.Credits.Has,
			Unlimited: snapshot.Credits.Unlimited, ObservedAt: observedAt}
		if snapshot.Credits.Balance != nil {
			if balance, err := strconv.ParseFloat(*snapshot.Credits.Balance, 64); err == nil && !math.IsNaN(balance) && !math.IsInf(balance, 0) {
				credit.Balance = &balance
			}
		}
		if credit.Has != nil || credit.Unlimited != nil || credit.Balance != nil {
			stored.Credits = credit
		}
	}
	stored.AdditionalQuotas = mergeAdditionalQuotas(account.ID, snapshot.AdditionalQuotas, observedAt)
	if err := s.accounts.SaveAccountUsageSnapshot(ctx, stored); err != nil {
		if errors.Is(err, domain.ErrPlanConfirmationPending) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	s.mu.Lock()
	s.usageSeen[accountGenerationKey{accountID: account.ID, generation: account.Generation}] = snapshot
	s.mu.Unlock()
	permission, denied := rateLimitPermission(snapshot)
	if canRecover && !denied && completeQuotas &&
		(quotaResetAvailable(before, after, refreshStarted, observedAt) || permission && windowsAvailable) {
		if _, err := s.quotaRecovery.RecoverAccountQuota(ctx, accountID, outcomeVersion, account.Generation); err != nil {
			return nil, nil, err
		}
	}
	return before, after, nil
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

// fetchUsageWithRefresh retries exactly once after a definitive upstream 401
// by rotating the rejected access token through ForceRefresh.
func (s *AccountUsageService) fetchUsageWithRefresh(ctx context.Context, account domain.Account, token string) (UsageSnapshot, error) {
	snapshot, err := s.usage.FetchUsage(ctx, token, account.ChatGPTAccountID)
	if !definitiveAuthRejection(err) {
		return snapshot, err
	}
	refreshed, refreshErr := s.tokens.ForceRefresh(ctx, account, token)
	if refreshErr != nil {
		return UsageSnapshot{}, refreshErr
	}
	return s.usage.FetchUsage(ctx, refreshed, account.ChatGPTAccountID)
}

func (s *AccountUsageService) fetchCreditsWithRefresh(ctx context.Context, account domain.Account, token string) (ResetCredits, error) {
	credits, err := s.usage.FetchResetCredits(ctx, token, account.ChatGPTAccountID)
	if !definitiveAuthRejection(err) {
		return credits, err
	}
	refreshed, refreshErr := s.tokens.ForceRefresh(ctx, account, token)
	if refreshErr != nil {
		return ResetCredits{}, refreshErr
	}
	return s.usage.FetchResetCredits(ctx, refreshed, account.ChatGPTAccountID)
}

func (s *AccountUsageService) consumeWithRefresh(ctx context.Context, account domain.Account, token, creditID, redeemRequestID string) (ResetCreditConsume, error) {
	result, err := s.usage.ConsumeResetCredit(ctx, token, account.ChatGPTAccountID, creditID, redeemRequestID)
	if !definitiveAuthRejection(err) {
		return result, err
	}
	refreshed, refreshErr := s.tokens.ForceRefresh(ctx, account, token)
	if refreshErr != nil {
		return ResetCreditConsume{}, refreshErr
	}
	return s.usage.ConsumeResetCredit(ctx, refreshed, account.ChatGPTAccountID, creditID, redeemRequestID)
}

func definitiveAuthRejection(err error) bool {
	var usageErr *UsageError
	return errors.As(err, &usageErr) && usageErr.Status == 401
}

// RefreshResetCredits refreshes the process-local snapshot cache. Upstream
// failures keep the previous snapshot and never mutate account state.
func (s *AccountUsageService) RefreshResetCredits(ctx context.Context, accountID string) error {
	account, err := s.accounts.GetAccount(ctx, accountID)
	if err != nil || !usageEligible(account) {
		return err
	}
	key := accountGenerationKey{accountID: account.ID, generation: account.Generation}
	s.mu.Lock()
	epoch := s.creditEpoch[key]
	s.mu.Unlock()
	token, err := s.accessToken(ctx, account)
	if err != nil {
		return err
	}
	credits, err := s.fetchCreditsWithRefresh(ctx, account, token)
	if err != nil {
		return err
	}
	credits = normalizeSnapshot(credits)
	s.mu.Lock()
	if s.creditEpoch[key] == epoch {
		s.snapshots[key] = credits
	}
	s.mu.Unlock()
	return nil
}

// RefreshAll polls every eligible account. One failing account never aborts
// the loop; errors are per-account only (the caller logs/observes elsewhere).
func (s *AccountUsageService) RefreshAll(ctx context.Context) error {
	accounts, err := s.accounts.ListAccounts(ctx)
	if err != nil {
		return err
	}
	for _, account := range accounts {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !usageEligible(account) {
			continue
		}
		refreshStarted := s.now().UTC()
		before, after, err := s.refreshAccountUsage(ctx, account.ID, account.Generation, s.limitWarmup != nil)
		if err == nil && s.limitWarmup != nil && len(after) != 0 {
			_, _ = s.limitWarmup.RunAfterUsageRefresh(ctx, account.ID, account.Generation, before, after, refreshStarted)
		}
		_ = s.RefreshResetCredits(ctx, account.ID)
	}
	return nil
}

// RunUsagePoller blocks until ctx is done, polling every interval. Wire it as
// a single goroutine from the composition root.
func (s *AccountUsageService) RunUsagePoller(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		_ = s.RefreshAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ResetCreditsSnapshot returns the cached snapshot, or nil when the poller has
// not observed one (dashboard reads never hit upstream, mirroring legacy).
func (s *AccountUsageService) ResetCreditsSnapshot(ctx context.Context, accountID string) (*ResetCredits, error) {
	account, err := s.accounts.GetAccount(ctx, accountID)
	if errors.Is(err, domain.ErrNotFound) {
		s.invalidateCreditSnapshot(accountID)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !usageEligible(account) {
		s.invalidateCreditSnapshot(accountID)
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := accountGenerationKey{accountID: account.ID, generation: account.Generation}
	if snapshot, ok := s.snapshots[key]; ok {
		return &snapshot, nil
	}
	return nil, nil
}

// LastUsageSnapshot returns the most recent polled usage telemetry, or nil.
func (s *AccountUsageService) LastUsageSnapshot(account domain.Account) *UsageSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snapshot, ok := s.usageSeen[accountGenerationKey{accountID: account.ID, generation: account.Generation}]; ok {
		return &snapshot
	}
	return nil
}

func soonestAvailableCredit(credits ResetCredits) *ResetCredit {
	if credits.AvailableCount <= 0 {
		return nil
	}
	var selected *ResetCredit
	for index := range credits.Credits {
		credit := &credits.Credits[index]
		if credit.Status != "available" {
			continue
		}
		if selected == nil || (credit.ExpiresAt != nil && (selected.ExpiresAt == nil || credit.ExpiresAt.Before(*selected.ExpiresAt))) {
			selected = credit
		}
	}
	return selected
}

func normalizeSnapshot(snapshot ResetCredits) ResetCredits {
	if snapshot.Credits == nil {
		snapshot.Credits = []ResetCredit{}
	}
	var nearest *time.Time
	for _, credit := range snapshot.Credits {
		if credit.Status != "available" {
			continue
		}
		if credit.ExpiresAt != nil && (nearest == nil || credit.ExpiresAt.Before(*nearest)) {
			value := *credit.ExpiresAt
			nearest = &value
		}
	}
	snapshot.NearestExpiresAt = nearest
	return snapshot
}
