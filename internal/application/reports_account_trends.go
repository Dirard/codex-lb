package application

import (
	"context"
	"time"

	"codex-lb/internal/domain"
)

func (s *ReportsService) AccountTrends(ctx context.Context, id string, accounts Accounts) (domain.AccountTrendsResponse, error) {
	result := domain.AccountTrendsResponse{AccountID: id, Primary: []domain.TrendPoint{},
		Secondary: []domain.TrendPoint{}, SecondaryScheduled: []domain.TrendPoint{}}
	account, err := accounts.GetAccount(ctx, id)
	if err != nil {
		return result, err
	}
	if account.Status == domain.AccountDeactivated && account.DeactivationReason == "deleted" {
		return result, domain.ErrNotFound
	}
	now := s.now().UTC()
	since := now.Add(-7 * 24 * time.Hour)
	buckets, err := s.repo.AccountQuotaTrendBuckets(ctx, id, since, now)
	if err != nil {
		return result, err
	}
	primary, secondary := map[int64]domain.AccountQuotaTrendBucket{}, map[int64]domain.AccountQuotaTrendBucket{}
	for _, bucket := range buckets {
		epoch := bucket.At.Unix()
		weeklyPrimary := bucket.Window == "primary" && bucket.WindowMinutes != nil && *bucket.WindowMinutes >= 10080
		switch {
		case bucket.Window == "secondary" || bucket.Window == "monthly" || weeklyPrimary:
			prior, ok := secondary[epoch]
			if !ok || bucket.ObservedAt.After(prior.ObservedAt) {
				secondary[epoch] = bucket
			}
		case bucket.Window == "primary":
			primary[epoch] = bucket
		}
	}
	first := since.Truncate(time.Hour)
	if len(primary) > 0 {
		result.Primary = filledQuotaTrend(first, 168, primary)
	}
	if len(secondary) > 0 {
		result.Secondary = filledQuotaTrend(first, 168, secondary)
		result.SecondaryScheduled = scheduledQuotaTrend(first, 168, secondary)
	}
	return result, nil
}

func filledQuotaTrend(first time.Time, count int, values map[int64]domain.AccountQuotaTrendBucket) []domain.TrendPoint {
	result := make([]domain.TrendPoint, 0, count)
	last := 100.0
	for index := 0; index < count; index++ {
		at := first.Add(time.Duration(index) * time.Hour)
		if bucket, ok := values[at.Unix()]; ok {
			last = max(0, min(100, 100-bucket.UsedPercent))
		}
		result = append(result, domain.TrendPoint{T: at, V: roundTo(last, 2)})
	}
	return result
}

func scheduledQuotaTrend(first time.Time, count int, values map[int64]domain.AccountQuotaTrendBucket) []domain.TrendPoint {
	result := []domain.TrendPoint{}
	var reset *time.Time
	minutes := 0
	for index := 0; index < count; index++ {
		at := first.Add(time.Duration(index) * time.Hour)
		if bucket, ok := values[at.Unix()]; ok && bucket.ResetAt != nil && bucket.WindowMinutes != nil {
			reset, minutes = bucket.ResetAt, *bucket.WindowMinutes
		}
		if reset == nil || minutes <= 0 {
			continue
		}
		windowSeconds := float64(minutes * 60)
		remaining := max(0, min(windowSeconds, reset.Sub(at).Seconds()))
		result = append(result, domain.TrendPoint{T: at, V: roundTo(100*remaining/windowSeconds, 2)})
	}
	return result
}
