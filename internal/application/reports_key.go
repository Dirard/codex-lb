package application

import (
	"context"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

type reportKeyLookup interface {
	GetAPIKey(context.Context, string) (domain.APIKey, error)
}

func (s *ReportsService) APIKeyTrends(ctx context.Context, keyID string, keys reportKeyLookup) (domain.APIKeyTrendsResponse, error) {
	response := domain.APIKeyTrendsResponse{KeyID: keyID, Cost: []domain.TrendPoint{}, Tokens: []domain.TrendPoint{}}
	if err := checkReportKey(ctx, keyID, keys); err != nil {
		return response, err
	}
	now := s.now().UTC()
	since := now.Add(-7 * 24 * time.Hour)
	buckets, err := s.repo.KeyTrendBuckets(ctx, keyID, since, now)
	if err != nil {
		return response, err
	}
	byHour := make(map[int64]domain.APIKeyTrendBucket, len(buckets))
	for _, bucket := range buckets {
		byHour[bucket.At.Unix()] = bucket
	}
	for at := since.Truncate(time.Hour); at.Before(now); at = at.Add(time.Hour) {
		bucket := byHour[at.Unix()]
		response.Cost = append(response.Cost, domain.TrendPoint{T: at, V: roundTo(bucket.CostUSD, 6)})
		response.Tokens = append(response.Tokens, domain.TrendPoint{T: at, V: float64(bucket.Tokens)})
	}
	return response, nil
}

func (s *ReportsService) APIKeyUsage7Day(ctx context.Context, keyID string, keys reportKeyLookup) (domain.APIKeyUsage7DayResponse, error) {
	response := domain.APIKeyUsage7DayResponse{KeyID: keyID, AccountCosts: []domain.APIKeyAccountCost{}}
	if err := checkReportKey(ctx, keyID, keys); err != nil {
		return response, err
	}
	now := s.now().UTC()
	response, err := s.repo.KeyUsage7Day(ctx, keyID, now.Add(-7*24*time.Hour), now)
	if err != nil {
		return response, err
	}
	response.KeyID = keyID
	return response, nil
}

func checkReportKey(ctx context.Context, id string, keys reportKeyLookup) error {
	if strings.TrimSpace(id) == "" || len(id) > 256 || domain.IsInternalKey(id) {
		return domain.ErrInvalid
	}
	_, err := keys.GetAPIKey(ctx, id)
	return err
}
