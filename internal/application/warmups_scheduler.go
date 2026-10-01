package application

import (
	"context"
	"slices"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

const (
	defaultIdleWindow = 300 * time.Minute
	idleWindowMax     = 24 * time.Hour
	resetJumpMinimum  = time.Minute
	idleSlotGrace     = time.Minute
)

// LimitWarmupAttempt is a durable claim made before any paid provider call.
type LimitWarmupAttempt struct {
	AccountID   string
	Attempt     int64
	Window      string
	ResetAt     time.Time
	Status      string
	Model       string
	AttemptedAt time.Time
	CompletedAt *time.Time
	ErrorCode   *string
}

type LimitWarmupClaims interface {
	ClaimLimitWarmupAttempt(context.Context, string, string, time.Time, string, time.Duration, time.Time) (LimitWarmupAttempt, bool, error)
	CompleteLimitWarmupAttempt(context.Context, string, int64, string, time.Time, *string) error
}

// LimitWarmupService evaluates only samples from a successful background
// usage refresh. A separately timed sweep cannot prove a reset transition.
type LimitWarmupService struct {
	settings Settings
	accounts Accounts
	warmups  *WarmupService
	claims   LimitWarmupClaims
	catalog  *ModelCatalogService
	now      func() time.Time
}

func NewLimitWarmupService(settings Settings, accounts Accounts, warmups *WarmupService, claims LimitWarmupClaims, now func() time.Time) *LimitWarmupService {
	if now == nil {
		now = time.Now
	}
	return &LimitWarmupService{settings: settings, accounts: accounts, warmups: warmups, claims: claims, now: now}
}

func (s *LimitWarmupService) ConfigureCatalog(catalog *ModelCatalogService) { s.catalog = catalog }

// RunAfterUsageRefresh checks one account after all its new quota rows were
// saved. Both opt-ins and account safety are reloaded after the network fetch.
func (s *LimitWarmupService) RunAfterUsageRefresh(ctx context.Context, accountID string, generation int64, before, after []domain.AccountQuota, refreshStarted time.Time) (int, error) {
	settings, err := s.settings.LoadSettings(ctx)
	if err != nil || !settings.LimitWarmupEnabled || s.claims == nil {
		return 0, err
	}
	if len(selectedWarmupWindows(settings.LimitWarmupWindows)) == 0 || strings.TrimSpace(settings.LimitWarmupModel) == "" ||
		settings.LimitWarmupPrompt == "" || settings.LimitWarmupCooldownSeconds < 60 ||
		!(settings.LimitWarmupExhaustedPercent > 0 && settings.LimitWarmupExhaustedPercent <= 100) ||
		!(settings.LimitWarmupMinAvailablePercent > 0 && settings.LimitWarmupMinAvailablePercent <= 100) ||
		!(settings.LimitWarmupIdlePercent > 0 && settings.LimitWarmupIdlePercent <= 100) {
		return 0, domain.ErrInvalid
	}
	account, err := s.accounts.GetAccount(ctx, accountID)
	if err != nil {
		return 0, err
	}
	if generation >= 0 && account.Generation != generation {
		return 0, nil
	}
	if !automaticWarmupEligible(account) {
		return 0, nil
	}
	model := ""
	modelResolved := false
	getModel := func() string {
		if !modelResolved {
			model = s.resolveModel(ctx, settings.LimitWarmupModel, account)
			modelResolved = true
		}
		return model
	}
	prompt := settings.LimitWarmupPrompt
	exhausted := settings.LimitWarmupExhaustedPercent
	minAvailable := settings.LimitWarmupMinAvailablePercent
	now := s.now().UTC()
	executed := 0
	primaryReset := false
	for _, window := range selectedWarmupWindows(settings.LimitWarmupWindows) {
		oldQuota := effectiveWarmupQuota(before, window, account.PlanType)
		newQuota := effectiveWarmupQuota(after, window, account.PlanType)
		if !resetConfirmed(oldQuota, newQuota, exhausted, minAvailable, refreshStarted) {
			continue
		}
		if window == "primary" {
			primaryReset = true
		}
		if getModel() == "" {
			continue
		}
		claimWindow := window
		if newQuota.Window == "monthly" {
			claimWindow = "monthly"
		}
		count, err := s.sendCandidate(ctx, account, claimWindow, *newQuota.ResetAt, model, prompt, 0, now)
		if err != nil {
			return executed, err
		}
		executed += count
	}
	if !settings.LimitWarmupStaggeredIdleEnabled || primaryReset {
		return executed, nil
	}
	primary := effectiveWarmupQuota(after, "primary", account.PlanType)
	idleThreshold := settings.LimitWarmupIdlePercent
	if primary == nil || primary.ResetAt == nil || primary.UsedPercent > idleThreshold || primary.ObservedAt.Before(refreshStarted) {
		return executed, nil
	}
	duration := warmupWindowDuration(primary)
	if duration > idleWindowMax {
		return executed, nil
	}
	all, err := s.accounts.ListAccounts(ctx)
	if err != nil {
		return executed, err
	}
	ids := make([]string, 0, len(all))
	for _, candidate := range all {
		if automaticWarmupEligible(candidate) {
			ids = append(ids, candidate.ID)
		}
	}
	if !staggeredIdleDue(account.ID, ids, *primary.ResetAt, duration, refreshStarted, primary.ObservedAt, now) {
		return executed, nil
	}
	if getModel() == "" {
		return executed, nil
	}
	cooldown := time.Duration(settings.LimitWarmupCooldownSeconds) * time.Second
	count, err := s.sendCandidate(ctx, account, "primary_idle", *primary.ResetAt, model, prompt, cooldown, now)
	return executed + count, err
}

func automaticWarmupEligible(account domain.Account) bool {
	return account.Status == domain.AccountActive && account.LimitWarmupEnabled && warmupEligible(account)
}

func selectedWarmupWindows(value string) []string {
	switch value {
	case "primary":
		return []string{"primary"}
	case "secondary":
		return []string{"secondary"}
	case "both":
		return []string{"primary", "secondary"}
	default:
		return nil
	}
}

func quotaByWindow(quotas []domain.AccountQuota, window string) *domain.AccountQuota {
	for i := range quotas {
		if quotas[i].Window == window {
			return &quotas[i]
		}
	}
	return nil
}

func effectiveWarmupQuota(quotas []domain.AccountQuota, window, plan string) *domain.AccountQuota {
	primary := quotaByWindow(quotas, "primary")
	if window == "primary" {
		if primary != nil && primary.WindowMinutes != nil && *primary.WindowMinutes == 10080 {
			return nil // weekly-only plans report their weekly quota in the primary slot
		}
		return primary
	}
	secondary := quotaByWindow(quotas, "secondary")
	if strings.EqualFold(plan, "free") {
		if monthly := quotaByWindow(quotas, "monthly"); monthly != nil && monthly.ResetAt != nil {
			return monthly
		}
	}
	if primary != nil && primary.WindowMinutes != nil && *primary.WindowMinutes == 10080 {
		if secondary == nil || primary.ObservedAt.After(secondary.ObservedAt.Add(5*time.Second)) ||
			!secondary.ObservedAt.After(primary.ObservedAt.Add(5*time.Second)) &&
				primary.ResetAt != nil && (secondary.ResetAt == nil || primary.ResetAt.After(*secondary.ResetAt)) {
			return primary
		}
	}
	return secondary
}

func warmupWindowDuration(quota *domain.AccountQuota) time.Duration {
	if quota != nil && quota.WindowMinutes != nil && *quota.WindowMinutes > 0 {
		return time.Duration(*quota.WindowMinutes) * time.Minute
	}
	return defaultIdleWindow
}

func resetConfirmed(before, after *domain.AccountQuota, exhausted, minAvailable float64, refreshStarted time.Time) bool {
	if before == nil || after == nil || before.Window != after.Window || before.ResetAt == nil || after.ResetAt == nil ||
		before.UsedPercent < exhausted || after.UsedPercent >= 100 || 100-after.UsedPercent < minAvailable ||
		after.ObservedAt.Before(refreshStarted) || after.ObservedAt.Before(before.ObservedAt) ||
		after.ResetAt.Sub(*before.ResetAt) < resetJumpMinimum || !after.ObservedAt.Before(*after.ResetAt) {
		return false
	}
	crossedReset := !before.ObservedAt.After(*before.ResetAt) && !after.ObservedAt.Before(*before.ResetAt)
	windowStart := after.ResetAt.Add(-warmupWindowDuration(after))
	reanchored := after.UsedPercent < before.UsedPercent && !before.ObservedAt.After(windowStart) && !after.ObservedAt.Before(windowStart)
	return crossedReset || reanchored
}

func staggeredIdleDue(accountID string, ids []string, resetAt time.Time, duration time.Duration, refreshStarted, observedAt, now time.Time) bool {
	if len(ids) == 0 || duration <= 0 {
		return false
	}
	slices.Sort(ids)
	index, found := slices.BinarySearch(ids, accountID)
	start := resetAt.Add(-duration)
	if !found || now.Before(start) || !now.Before(resetAt) || observedAt.Before(start) || refreshStarted.After(now) {
		return false
	}
	if refreshStarted.Before(start) {
		refreshStarted = start
	}
	slot := time.Duration(index) * duration / time.Duration(len(ids))
	return !now.Before(start.Add(slot)) && now.Before(start.Add(slot+idleSlotGrace)) &&
		refreshStarted.Before(start.Add(slot+idleSlotGrace))
}

func (s *LimitWarmupService) resolveModel(ctx context.Context, configured string, account domain.Account) string {
	configured = strings.TrimSpace(configured)
	if configured != "" && !strings.EqualFold(configured, "auto") {
		return configured
	}
	if s.catalog == nil {
		return ""
	}
	snapshot := s.catalog.Snapshot()
	if snapshot == nil {
		return ""
	}
	best := ""
	var bestCost int64
	for slug, model := range snapshot.Models {
		if model.SourceKind != domain.ModelCatalogSourceSubscription || !model.SupportedInAPI || snapshot.IsSuppressed(slug) || !modelSupportsText(model) {
			continue
		}
		if known, supported := snapshot.AccountSupportsModel(account.ID, slug); !known || !supported {
			continue
		}
		if len(model.AvailableInPlans) > 0 && !contains(model.AvailableInPlans, account.PlanType) {
			continue
		}
		price, err := s.warmups.ResolvePrice(ctx, account, slug)
		if err != nil {
			continue
		}
		cost := price.Standard.Input + price.Standard.Output
		if best == "" || cost < bestCost || cost == bestCost && slug < best {
			best, bestCost = slug, cost
		}
	}
	return best
}

func modelSupportsText(model domain.CatalogModel) bool {
	if len(model.InputModalities) == 0 {
		return true
	}
	for _, modality := range model.InputModalities {
		if strings.EqualFold(modality, "text") {
			return true
		}
	}
	return false
}

func (s *LimitWarmupService) sendCandidate(ctx context.Context, account domain.Account, window string, resetAt time.Time, model, prompt string, cooldown time.Duration, now time.Time) (int, error) {
	if !now.Before(resetAt) {
		return 0, nil
	}
	attempt, claimed, err := s.claims.ClaimLimitWarmupAttempt(ctx, account.ID, window, resetAt, model, cooldown, now)
	if err != nil || !claimed {
		return 0, err
	}
	warmupErr := s.warmups.ExecuteLimitWarmup(ctx, account.ID, account.Generation, model, prompt)
	status := domain.AutomationSuccess
	var errorCode *string
	if warmupErr != nil {
		status = domain.AutomationFailed
		code := "warmup_failed"
		if ctx.Err() != nil {
			code = "warmup_cancelled"
		}
		errorCode = &code
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.claims.CompleteLimitWarmupAttempt(cleanup, account.ID, attempt.Attempt, status, s.now().UTC(), errorCode); err != nil {
		return 0, err
	}
	if warmupErr != nil {
		return 0, nil
	}
	return 1, nil
}
