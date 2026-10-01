package application

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

const MaxReportDays = 730

type ReportsRepository interface {
	LoadSettings(context.Context) (domain.RuntimeSettings, error)
	PositiveQuotaDeltas(context.Context, time.Time, time.Time) (map[string]map[string]float64, error)
	WeeklyPaceTopKeys(context.Context, time.Time, time.Time) ([]domain.WeeklyCreditAPIKeyAttribution, error)
	QueryReports(context.Context, domain.ReportFilter) (domain.ReportsResponse, error)
	KeyReportLimits(context.Context, string, time.Time) (domain.KeyReportLimitSummary, error)
	QueryDashboardTraffic(context.Context, time.Time, time.Time, int, int) (domain.DashboardTraffic, error)
	AccountQuotaTrendBuckets(context.Context, string, time.Time, time.Time) ([]domain.AccountQuotaTrendBucket, error)
	RecentAccountQuotaHistory(context.Context, time.Time, time.Time) ([]domain.AccountQuota, error)
	PruneAccountQuotaHistory(context.Context, time.Time, int) (int, error)
	KeyTrendBuckets(context.Context, string, time.Time, time.Time) ([]domain.APIKeyTrendBucket, error)
	KeyUsage7Day(context.Context, string, time.Time, time.Time) (domain.APIKeyUsage7DayResponse, error)
	ListConversations(context.Context, domain.ConversationFilter) (domain.ConversationsResponse, error)
	ConversationDetails(context.Context, string) (domain.ConversationDetails, error)
	ListRequestLogs(context.Context, domain.RequestLogFilter) (domain.RequestLogsResponse, error)
	RequestLogOptions(context.Context, domain.RequestLogFilter) (domain.RequestLogOptions, error)
	FoldAndPruneRequestLogs(context.Context, time.Time, int) (int, error)
}

type ReportParams struct {
	StartDate, EndDate, Timezone string
	AccountIDs, APIKeyIDs        []string
	Model, UserAgentGroup        string
}

type RequestLogParams struct {
	Limit, Offset                                                           int
	Search, ConversationID, Timeframe                                       string
	Since, Until                                                            *time.Time
	AccountIDs, APIKeyIDs, Statuses, Models, ReasoningEfforts, ModelOptions []string
}

type ReportsService struct {
	repo ReportsRepository
	now  func() time.Time
}

func NewReportsService(repo ReportsRepository, now func() time.Time) *ReportsService {
	if now == nil {
		now = time.Now
	}
	return &ReportsService{repo: repo, now: now}
}

// KeyLimits reads current authorized limits independently of report filters.
func (s *ReportsService) KeyLimits(ctx context.Context, keyID string) (domain.KeyReportLimitSummary, error) {
	return s.repo.KeyReportLimits(ctx, keyID, s.now().UTC())
}

func (s *ReportsService) Reports(ctx context.Context, p ReportParams) (domain.ReportsResponse, error) {
	loc := time.UTC
	if p.Timezone != "" {
		if parsed, err := time.LoadLocation(p.Timezone); err == nil {
			loc = parsed
		}
	}
	end := s.now().In(loc)
	if p.EndDate != "" {
		parsed, err := time.ParseInLocation("2006-01-02", p.EndDate, loc)
		if err != nil {
			return domain.ReportsResponse{}, fmt.Errorf("end_date: %w", domain.ErrInvalid)
		}
		end = parsed
	}
	endDay := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, loc)
	startDay := endDay.AddDate(0, 0, -6)
	if p.StartDate != "" {
		parsed, err := time.ParseInLocation("2006-01-02", p.StartDate, loc)
		if err != nil {
			return domain.ReportsResponse{}, fmt.Errorf("start_date: %w", domain.ErrInvalid)
		}
		startDay = parsed
	}
	days := 0
	for day := startDay; !day.After(endDay); day = day.AddDate(0, 0, 1) {
		days++
		if days > MaxReportDays {
			break
		}
	}
	if days == 0 || days > MaxReportDays {
		return domain.ReportsResponse{}, fmt.Errorf("report date range: %w", domain.ErrInvalid)
	}
	filter := domain.ReportFilter{Start: startDay.UTC(), End: endDay.AddDate(0, 0, 1).UTC(),
		PreviousStart: startDay.AddDate(0, 0, -days).UTC(), Location: loc,
		AccountIDs: uniqueNonblank(p.AccountIDs), APIKeyIDs: uniqueNonblank(p.APIKeyIDs),
		Model: strings.TrimSpace(p.Model), UserAgentGroup: strings.TrimSpace(p.UserAgentGroup)}
	if len(filter.AccountIDs) > 100 || len(filter.APIKeyIDs) > 100 || len(filter.Model) > 256 || len(filter.UserAgentGroup) > 128 {
		return domain.ReportsResponse{}, domain.ErrInvalid
	}
	response, err := s.repo.QueryReports(ctx, filter)
	if err != nil {
		return response, err
	}
	response.Summary.AvgCostPerDay = roundTo(response.Summary.TotalCostUSD/float64(days), 4)
	response.Summary.AvgRequestsPerDay = roundTo(float64(response.Summary.TotalRequests)/float64(days), 2)
	return response, nil
}

func (s *ReportsService) RequestLogs(ctx context.Context, p RequestLogParams) (domain.RequestLogsResponse, error) {
	filter, err := s.requestLogFilter(p)
	if err != nil {
		return domain.RequestLogsResponse{}, err
	}
	return s.repo.ListRequestLogs(ctx, filter)
}

func (s *ReportsService) RequestLogOptions(ctx context.Context, p RequestLogParams) (domain.RequestLogOptions, error) {
	filter, err := s.requestLogFilter(p)
	if err != nil {
		return domain.RequestLogOptions{}, err
	}
	return s.repo.RequestLogOptions(ctx, filter)
}

func (s *ReportsService) requestLogFilter(p RequestLogParams) (domain.RequestLogFilter, error) {
	if p.Timeframe != "" && p.Since != nil {
		return domain.RequestLogFilter{}, domain.ErrInvalid
	}
	if p.Limit == 0 {
		p.Limit = 50
	}
	if p.Limit < 1 || p.Limit > 1000 || p.Offset < 0 || len(p.Search) > 256 || len(p.ConversationID) > 256 {
		return domain.RequestLogFilter{}, domain.ErrInvalid
	}
	since := p.Since
	if p.Timeframe != "" {
		var duration time.Duration
		switch p.Timeframe {
		case "1h":
			duration = time.Hour
		case "24h":
			duration = 24 * time.Hour
		case "7d":
			duration = 7 * 24 * time.Hour
		default:
			return domain.RequestLogFilter{}, domain.ErrInvalid
		}
		value := s.now().Add(-duration).UTC()
		since = &value
	}
	if since != nil && p.Until != nil && !since.Before(*p.Until) {
		return domain.RequestLogFilter{}, domain.ErrInvalid
	}
	filter := domain.RequestLogFilter{Limit: p.Limit, Offset: p.Offset, Search: strings.TrimSpace(p.Search),
		ConversationID: strings.TrimSpace(p.ConversationID), Since: since, Until: p.Until,
		AccountIDs: uniqueNonblank(p.AccountIDs), APIKeyIDs: uniqueNonblank(p.APIKeyIDs),
		Statuses: uniqueNonblank(p.Statuses), Models: uniqueNonblank(p.Models),
		ReasoningEfforts: uniqueNonblank(p.ReasoningEfforts), ModelOptions: uniqueNonblank(p.ModelOptions)}
	for _, values := range [][]string{filter.AccountIDs, filter.APIKeyIDs, filter.Statuses, filter.Models, filter.ReasoningEfforts, filter.ModelOptions} {
		if len(values) > 100 {
			return domain.RequestLogFilter{}, domain.ErrInvalid
		}
		for _, v := range values {
			if len(v) > 256 {
				return domain.RequestLogFilter{}, domain.ErrInvalid
			}
		}
	}
	return filter, nil
}

func (s *ReportsService) PruneRequestLogs(ctx context.Context, retentionDays int) (int, error) {
	if retentionDays == 0 {
		return 0, nil
	}
	if retentionDays < 30 || retentionDays > 3650 {
		return 0, domain.ErrInvalid
	}
	return s.repo.FoldAndPruneRequestLogs(ctx, s.now().UTC().AddDate(0, 0, -retentionDays), 1000)
}

func (s *ReportsService) PruneAccountQuotaHistory(ctx context.Context, retentionDays int) (int, error) {
	if retentionDays == 0 {
		return 0, nil
	}
	if retentionDays < 45 || retentionDays > 3650 {
		return 0, domain.ErrInvalid
	}
	return s.repo.PruneAccountQuotaHistory(ctx, s.now().UTC().AddDate(0, 0, -retentionDays), 1000)
}

func uniqueNonblank(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func roundTo(value float64, places int) float64 {
	factor := math.Pow10(places)
	return math.Round(value*factor) / factor
}
