package application

import (
	"context"
	"time"

	"codex-lb/internal/domain"
)

func dashboardTimeframe(key string) (domain.DashboardTimeframe, error) {
	switch key {
	case "", "7d":
		return domain.DashboardTimeframe{Key: "7d", WindowMinutes: 10080, BucketSeconds: 21600, BucketCount: 28}, nil
	case "1d":
		return domain.DashboardTimeframe{Key: "1d", WindowMinutes: 1440, BucketSeconds: 3600, BucketCount: 24}, nil
	case "30d":
		return domain.DashboardTimeframe{Key: "30d", WindowMinutes: 43200, BucketSeconds: 86400, BucketCount: 30}, nil
	default:
		return domain.DashboardTimeframe{}, domain.ErrInvalid
	}
}

type DashboardAccountRepository interface {
	Accounts
	AccountQuotaMetadata
	UsageTotals(context.Context, string, string) (domain.UsageTotals, error)
}

func (s *ReportsService) DashboardOverview(ctx context.Context, key string, accounts DashboardAccountRepository) (domain.DashboardOverview, error) {
	var overview domain.DashboardOverview
	timeframe, err := dashboardTimeframe(key)
	if err != nil {
		return overview, err
	}
	now := s.now().UTC()
	since := now.Add(-time.Duration(timeframe.WindowMinutes) * time.Minute)
	traffic, err := s.repo.QueryDashboardTraffic(ctx, since, now, timeframe.BucketSeconds, timeframe.BucketCount)
	if err != nil {
		return overview, err
	}
	rows, err := accounts.ListAccounts(ctx)
	if err != nil {
		return overview, err
	}
	overview.Timeframe = timeframe
	overview.Accounts = []domain.DashboardAccount{}
	overview.AdditionalQuotas = []domain.DashboardAdditionalQuota{}
	overview.Windows.Primary = domain.DashboardUsageWindow{WindowKey: "primary", Accounts: []domain.DashboardUsageAccount{}}
	secondaryWindow := domain.DashboardUsageWindow{WindowKey: "secondary", Accounts: []domain.DashboardUsageAccount{}}
	primarySummary := domain.DashboardUsageSummaryWindow{}
	secondarySummary := domain.DashboardUsageSummaryWindow{}
	primaryMinutes, secondaryMinutes := 300, 10080
	primarySummary.WindowMinutes = &primaryMinutes
	secondarySummary.WindowMinutes = &secondaryMinutes
	overview.Windows.Primary.WindowMinutes = &primaryMinutes
	secondaryWindow.WindowMinutes = &secondaryMinutes
	primaryUsed, secondaryUsed := 0.0, 0.0
	hasSecondary := false
	accountQuotas := make(map[string][]domain.AccountQuota, len(rows))
	for _, account := range rows {
		if account.Kind != domain.AccountChatGPT || account.Status == domain.AccountDeactivated && account.DeactivationReason == "deleted" {
			continue
		}
		quotas, err := accounts.ListAccountQuota(ctx, account.ID)
		if err != nil {
			return overview, err
		}
		accountQuotas[account.ID] = quotas
		displayQuotas := make([]domain.AccountQuota, 0, len(quotas))
		for _, quota := range quotas {
			q := quota
			if q.ResetAt != nil && !q.ResetAt.After(now) {
				q.UsedPercent = 0
				q.ResetAt = nil
			}
			displayQuotas = append(displayQuotas, q)
			if overview.LastSyncAt == nil || q.ObservedAt.After(*overview.LastSyncAt) {
				observed := q.ObservedAt
				overview.LastSyncAt = &observed
			}
		}
		primary, secondary := selectionQuotaWindows(account, displayQuotas)
		view := domain.DashboardAccount{Account: account}
		credits, err := accounts.LoadAccountCreditStatus(ctx, account.ID)
		if err != nil {
			return overview, err
		}
		var refusalAt *time.Time
		if account.Status == domain.AccountQuotaExceeded {
			refusalAt, err = accounts.LoadAccountQuotaRefusalAt(ctx, account.ID)
			if err != nil {
				return overview, err
			}
		}
		view.Status = EffectiveAccountQuotaStatus(account, quotas, credits, refusalAt, now)
		if credits != nil {
			view.CreditsHas, view.CreditsUnlimited, view.CreditsBalance = credits.Has, credits.Unlimited, credits.Balance
		}
		totals, err := accounts.UsageTotals(ctx, "", account.ID)
		if err != nil {
			return overview, err
		}
		view.RequestUsage.RequestCount = totals.RequestCount
		view.RequestUsage.TotalTokens = totals.Usage.InputTokens + totals.Usage.OutputTokens
		view.RequestUsage.CachedInputTokens = totals.Usage.CachedInputTokens
		view.RequestUsage.TotalCostUSD = float64(totals.Usage.CostMicrodollars) / 1e6
		primaryEntry := quotaAccount(account, primary, "primary")
		secondaryEntry := quotaAccount(account, secondary, "secondary")
		if primary != nil {
			view.Usage.PrimaryRemainingPercent = primaryEntry.RemainingPercentAvg
			view.ResetAtPrimary = primary.ResetAt
			view.WindowMinutesPrimary = primary.WindowMinutes
			view.CapacityCreditsPrimary, view.RemainingCreditsPrimary = dashboardAccountCredits(account.PlanType, "primary", primaryEntry)
			addQuotaSummary(&primarySummary, &primaryUsed, primaryEntry, primary)
		}
		if secondary != nil {
			hasSecondary = true
			if secondary.Window == "monthly" {
				view.Usage.MonthlyRemainingPercent = secondaryEntry.RemainingPercentAvg
				view.ResetAtMonthly, view.WindowMinutesMonthly = secondary.ResetAt, secondary.WindowMinutes
				view.CapacityCreditsMonthly, view.RemainingCreditsMonthly = dashboardAccountCredits(account.PlanType, "monthly", secondaryEntry)
			} else {
				view.Usage.SecondaryRemainingPercent = secondaryEntry.RemainingPercentAvg
				view.ResetAtSecondary, view.WindowMinutesSecondary = secondary.ResetAt, secondary.WindowMinutes
				view.CapacityCreditsSecondary, view.RemainingCreditsSecondary = dashboardAccountCredits(account.PlanType, "secondary", secondaryEntry)
			}
			addQuotaSummary(&secondarySummary, &secondaryUsed, secondaryEntry, secondary)
		}
		overview.Accounts = append(overview.Accounts, view)
		overview.Windows.Primary.Accounts = append(overview.Windows.Primary.Accounts, primaryEntry)
		secondaryWindow.Accounts = append(secondaryWindow.Accounts, secondaryEntry)
	}
	finalizeQuotaSummary(&primarySummary, primaryUsed)
	overview.Summary.PrimaryWindow = primarySummary
	if hasSecondary {
		finalizeQuotaSummary(&secondarySummary, secondaryUsed)
		overview.Summary.SecondaryWindow = &secondarySummary
		overview.Windows.Secondary = &secondaryWindow
	}
	overview.Summary.Cost.Currency = "USD"
	overview.Summary.Cost.TotalUSD = roundTo(traffic.CurrentCostUSD, 4)
	requests := float64(traffic.Current.RequestCount)
	tokens := float64(traffic.Current.Usage.InputTokens + traffic.Current.Usage.OutputTokens)
	cached := float64(traffic.Current.Usage.CachedInputTokens)
	errors := float64(traffic.CurrentErrors)
	cancelled, conversations := traffic.CurrentCancelled, traffic.CurrentConversations
	metrics := domain.DashboardMetrics{Requests: &requests, Tokens: &tokens, CachedInputTokens: &cached,
		ErrorCount: &errors, CancelledCount: &cancelled, TopError: traffic.TopError,
		Conversations: &conversations, ConversationRequests: traffic.ConversationRequests}
	if requests > 0 {
		rate := errors / requests
		metrics.ErrorRate = &rate
	}
	overview.Summary.Metrics = &metrics
	overview.Summary.Comparison.CanCompare = traffic.CanCompare
	overview.Summary.Comparison.Previous.Requests = traffic.Previous.RequestCount
	overview.Summary.Comparison.Previous.Tokens = traffic.Previous.Usage.InputTokens + traffic.Previous.Usage.OutputTokens
	overview.Summary.Comparison.Previous.CostUSD = roundTo(traffic.PreviousCostUSD, 4)
	overview.Trends = domain.DashboardTrends{Requests: []domain.TrendPoint{}, Tokens: []domain.TrendPoint{},
		Cost: []domain.TrendPoint{}, ErrorRate: []domain.TrendPoint{}, Conversations: []domain.TrendPoint{}}
	for _, bucket := range traffic.Buckets {
		at := bucket.At
		overview.Trends.Requests = append(overview.Trends.Requests, domain.TrendPoint{T: at, V: float64(bucket.Requests)})
		overview.Trends.Tokens = append(overview.Trends.Tokens, domain.TrendPoint{T: at, V: float64(bucket.Input + bucket.Output)})
		overview.Trends.Cost = append(overview.Trends.Cost, domain.TrendPoint{T: at, V: roundTo(bucket.CostUSD, 6)})
		rate := 0.0
		if bucket.Requests > 0 {
			rate = float64(bucket.Errors) / float64(bucket.Requests)
		}
		overview.Trends.ErrorRate = append(overview.Trends.ErrorRate, domain.TrendPoint{T: at, V: roundTo(rate, 4)})
		overview.Trends.Conversations = append(overview.Trends.Conversations, domain.TrendPoint{T: at, V: float64(bucket.Conversations)})
	}
	history, err := s.repo.RecentAccountQuotaHistory(ctx, now.Add(-30*24*time.Hour), now)
	if err != nil {
		return overview, err
	}
	overview.WeeklyCreditPace, err = s.weeklyCreditPace(ctx, rows, accountQuotas, history, now)
	return overview, err
}

func quotaAccount(account domain.Account, quota *domain.AccountQuota, window string) domain.DashboardUsageAccount {
	capacity := keyUsageCapacity(account.PlanType, window)
	entry := domain.DashboardUsageAccount{AccountID: account.ID, CapacityCredits: capacity}
	if quota != nil {
		remaining := max(0, min(100, 100-quota.UsedPercent))
		entry.RemainingPercentAvg = &remaining
		entry.RemainingCredits = capacity * remaining / 100
	}
	return entry
}

func addQuotaSummary(summary *domain.DashboardUsageSummaryWindow, used *float64, entry domain.DashboardUsageAccount, quota *domain.AccountQuota) {
	summary.CapacityCredits += entry.CapacityCredits
	*used += entry.CapacityCredits * quota.UsedPercent / 100
	if quota.ResetAt != nil && (summary.ResetAt == nil || quota.ResetAt.Before(*summary.ResetAt)) {
		summary.ResetAt = quota.ResetAt
	}
	if quota.WindowMinutes != nil {
		summary.WindowMinutes = quota.WindowMinutes
	}
}

func finalizeQuotaSummary(summary *domain.DashboardUsageSummaryWindow, used float64) {
	summary.RemainingCredits = max(0, summary.CapacityCredits-used)
	if summary.CapacityCredits > 0 {
		summary.RemainingPercent = summary.RemainingCredits / summary.CapacityCredits * 100
	}
}

func dashboardAccountCredits(plan, window string, entry domain.DashboardUsageAccount) (capacity, remaining *float64) {
	capacity = domain.SubscriptionCreditCapacity(plan, window)
	if capacity != nil && entry.RemainingPercentAvg != nil {
		value := *capacity * *entry.RemainingPercentAvg / 100
		remaining = &value
	}
	return capacity, remaining
}
