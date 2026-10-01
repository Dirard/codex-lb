package application

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

type KeyUsageRepository interface {
	GetAPIKey(context.Context, string) (domain.APIKey, error)
	RefreshExpiredKeyLimits(context.Context, string, time.Time) error
	UsageTotals(context.Context, string, string) (domain.UsageTotals, error)
	LoadSettings(context.Context) (domain.RuntimeSettings, error)
	ListAccounts(context.Context) ([]domain.Account, error)
	GetGroup(context.Context, string) (domain.AccountGroup, error)
	ListAccountQuota(context.Context, string) ([]domain.AccountQuota, error)
}

type KeyUsageLimit struct {
	LimitType      domain.LimitType   `json:"limit_type"`
	LimitWindow    domain.LimitWindow `json:"limit_window"`
	MaxValue       int64              `json:"max_value"`
	CurrentValue   int64              `json:"current_value"`
	RemainingValue int64              `json:"remaining_value"`
	ModelFilter    *string            `json:"model_filter"`
	ResetAt        time.Time          `json:"reset_at"`
	Source         string             `json:"source"`
}

type KeyAccountPoolUsage struct {
	Primary   *float64 `json:"primary"`
	Secondary *float64 `json:"secondary"`
}

type KeySelfUsage struct {
	RequestCount      int64                `json:"request_count"`
	TotalTokens       int64                `json:"total_tokens"`
	CachedInputTokens int64                `json:"cached_input_tokens"`
	TotalCostUSD      float64              `json:"total_cost_usd"`
	Limits            []KeyUsageLimit      `json:"limits"`
	UpstreamLimits    []KeyUsageLimit      `json:"upstream_limits"`
	AccountPoolUsage  *KeyAccountPoolUsage `json:"account_pool_usage"`
}

type KeyUsageService struct {
	repo KeyUsageRepository
	now  func() time.Time
}

func NewKeyUsageService(repo KeyUsageRepository, now func() time.Time) *KeyUsageService {
	if now == nil {
		now = time.Now
	}
	return &KeyUsageService{repo: repo, now: now}
}

func (s *KeyUsageService) key(ctx context.Context, keyID string) (domain.APIKey, error) {
	key, err := s.loadKey(ctx, keyID)
	if err != nil {
		return key, err
	}
	if err := s.repo.RefreshExpiredKeyLimits(ctx, keyID, s.now().UTC()); err != nil {
		return domain.APIKey{}, err
	}
	return s.loadKey(ctx, keyID)
}

func (s *KeyUsageService) loadKey(ctx context.Context, keyID string) (domain.APIKey, error) {
	if keyID == "" || domain.IsInternalKey(keyID) {
		return domain.APIKey{}, domain.ErrInvalid
	}
	key, err := s.repo.GetAPIKey(ctx, keyID)
	if err != nil {
		return key, err
	}
	now := s.now().UTC()
	if !key.IsActive || key.ExpiresAt != nil && !key.ExpiresAt.After(now) {
		return domain.APIKey{}, domain.ErrNotFound
	}
	return key, nil
}

func (s *KeyUsageService) SelfUsage(ctx context.Context, keyID string) (KeySelfUsage, error) {
	result := KeySelfUsage{Limits: []KeyUsageLimit{}, UpstreamLimits: []KeyUsageLimit{}}
	key, err := s.key(ctx, keyID)
	if err != nil {
		return result, err
	}
	totals, err := s.repo.UsageTotals(ctx, keyID, "")
	if err != nil {
		return result, err
	}
	result.RequestCount = totals.RequestCount
	result.TotalTokens = totals.Usage.InputTokens + totals.Usage.OutputTokens
	result.CachedInputTokens = totals.Usage.CachedInputTokens
	result.TotalCostUSD = float64(totals.Usage.CostMicrodollars) / 1e6
	for _, rule := range key.Limits {
		result.Limits = append(result.Limits, keyRuleLimit(rule))
	}
	sections := usageSections(key.UsageSections)
	settings, err := s.repo.LoadSettings(ctx)
	if err != nil {
		return result, err
	}
	if settings.HideUpstreamQuotaFromKeys || !sections["upstream_limits"] && !sections["account_pool_usage"] {
		return result, nil
	}
	windows, err := s.scopedWindows(ctx, key)
	if err != nil {
		return result, err
	}
	if sections["upstream_limits"] {
		result.UpstreamLimits = aggregateCreditLimits(windows, s.now().UTC())
		if len(result.Limits) == 0 {
			result.Limits = append(result.Limits, result.UpstreamLimits...)
		}
	}
	if sections["account_pool_usage"] {
		result.AccountPoolUsage = &KeyAccountPoolUsage{
			Primary: windows["primary"].remainingPercent(), Secondary: windows["secondary"].remainingPercent()}
	}
	return result, nil
}

func usageSections(raw string) map[string]bool {
	sections := map[string]bool{}
	for _, value := range strings.Split(raw, ",") {
		sections[strings.TrimSpace(value)] = true
	}
	return sections
}

func keyRuleLimit(rule domain.LimitRule) KeyUsageLimit {
	current := max(int64(0), min(rule.CurrentValue, rule.MaxValue))
	return KeyUsageLimit{LimitType: rule.Type, LimitWindow: rule.Window, MaxValue: rule.MaxValue,
		CurrentValue: current, RemainingValue: max(int64(0), rule.MaxValue-current),
		ModelFilter: rule.ModelFilter, ResetAt: rule.ResetAt, Source: "api_key_limit"}
}

type pooledWindow struct {
	capacity, used float64
	resetAt        *time.Time
	minutes        int
}

func (w pooledWindow) remainingPercent() *float64 {
	if w.capacity <= 0 {
		return nil
	}
	value := max(0, min(100, 100*(w.capacity-w.used)/w.capacity))
	return &value
}

func (s *KeyUsageService) scopedWindows(ctx context.Context, key domain.APIKey) (map[string]pooledWindow, error) {
	windows := map[string]pooledWindow{
		"primary": {minutes: 300}, "secondary": {minutes: 10080}, "monthly": {minutes: 43200}}
	var allowed map[string]bool
	if key.GroupID != nil {
		group, err := s.repo.GetGroup(ctx, *key.GroupID)
		if err != nil {
			return nil, err
		}
		allowed = make(map[string]bool, len(group.AccountIDs))
		for _, id := range group.AccountIDs {
			allowed[id] = true
		}
	} else if key.AccountAssignmentScopeEnabled {
		allowed = make(map[string]bool, len(key.AssignedAccountIDs))
		for _, id := range key.AssignedAccountIDs {
			allowed[id] = true
		}
	}
	accounts, err := s.repo.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	for _, account := range accounts {
		if account.Kind != domain.AccountChatGPT || account.Status == domain.AccountDeactivated ||
			account.Status == domain.AccountPaused || account.RequiresEgressDecision ||
			allowed != nil && !allowed[account.ID] {
			continue
		}
		quotas, err := s.repo.ListAccountQuota(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		byWindow := make(map[string]domain.AccountQuota, len(quotas))
		for _, quota := range quotas {
			byWindow[quota.Window] = quota
		}
		if primary, ok := byWindow["primary"]; ok && primary.WindowMinutes != nil {
			switch {
			case *primary.WindowMinutes >= 43200:
				if _, exists := byWindow["monthly"]; !exists {
					byWindow["monthly"] = primary
				}
				delete(byWindow, "primary")
			case *primary.WindowMinutes >= 10080:
				if _, exists := byWindow["secondary"]; !exists {
					byWindow["secondary"] = primary
				}
				delete(byWindow, "primary")
			}
		}
		for _, window := range []string{"primary", "secondary", "monthly"} {
			quota, ok := byWindow[window]
			if !ok || quota.ResetAt != nil && !quota.ResetAt.After(now) {
				continue
			}
			capacity := keyUsageCapacity(account.PlanType, window)
			if capacity <= 0 {
				continue
			}
			summary := windows[window]
			summary.capacity += capacity
			summary.used += capacity * max(0, min(100, quota.UsedPercent)) / 100
			if quota.ResetAt != nil && (summary.resetAt == nil || quota.ResetAt.Before(*summary.resetAt)) {
				summary.resetAt = quota.ResetAt
			}
			if quota.WindowMinutes != nil && *quota.WindowMinutes > 0 {
				summary.minutes = *quota.WindowMinutes
			}
			windows[window] = summary
		}
	}
	return windows, nil
}

func keyUsageCapacity(plan, window string) float64 {
	if capacity := domain.SubscriptionCreditCapacity(plan, window); capacity != nil {
		return *capacity
	}
	return 0
}

func aggregateCreditLimits(windows map[string]pooledWindow, now time.Time) []KeyUsageLimit {
	result := []KeyUsageLimit{}
	for _, item := range []struct{ key, label string }{
		{"primary", "5h"}, {"secondary", "7d"}, {"monthly", "monthly"},
	} {
		window := windows[item.key]
		if window.capacity <= 0 || window.resetAt == nil || !window.resetAt.After(now) {
			continue
		}
		maximum := int64(math.Round(window.capacity))
		current := max(int64(0), min(int64(math.Round(window.used)), maximum))
		result = append(result, KeyUsageLimit{LimitType: domain.LimitCredits,
			LimitWindow: domain.LimitWindow(item.label), MaxValue: maximum, CurrentValue: current,
			RemainingValue: maximum - current, ResetAt: *window.resetAt, Source: "aggregate"})
	}
	return result
}

// QuotaHeaders exposes only retained, scoped upstream windows. Credit-balance
// headers are absent because quota percentages do not establish a balance.
func (s *KeyUsageService) QuotaHeaders(ctx context.Context, keyID string) (map[string]string, error) {
	headers := map[string]string{}
	key, err := s.loadKey(ctx, keyID)
	if err != nil {
		return headers, err
	}
	settings, err := s.repo.LoadSettings(ctx)
	if err != nil || settings.HideUpstreamQuotaFromKeys || !usageSections(key.UsageSections)["upstream_limits"] {
		return headers, err
	}
	windows, err := s.scopedWindows(ctx, key)
	if err != nil {
		return headers, err
	}
	for _, window := range []string{"primary", "secondary", "monthly"} {
		value := windows[window]
		if value.capacity <= 0 {
			continue
		}
		prefix := "x-codex-" + window + "-"
		headers[prefix+"used-percent"] = strconv.FormatFloat(100*value.used/value.capacity, 'f', -1, 64)
		headers[prefix+"window-minutes"] = strconv.Itoa(value.minutes)
		if value.resetAt != nil {
			headers[prefix+"reset-at"] = strconv.FormatInt(value.resetAt.Unix(), 10)
		}
	}
	return headers, nil
}
