package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestReportsLogsAndRetention(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct")
	if err := s.SaveAPIKey(ctx, testKey("key", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		id := "reservation-one"
		requestID := "request-one"
		status := "success"
		settlement := "finalized"
		cost := int64(1000)
		if index == 1 {
			id = "reservation-two"
			requestID = "request-two"
			status = "error"
			settlement = "failed"
			cost = 2000
		}
		if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: "key", AccountID: "acct",
			Model: "gpt-test", Now: fixedTime.Add(time.Duration(index) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
		event := domain.UsageEvent{RequestID: requestID, APIKeyID: "key", AccountID: "acct", Model: "gpt-test",
			ConversationID: "conversation-one", UserAgent: "Codex/1", UserAgentGroup: "Codex", ClientIP: "192.0.2.1",
			ReasoningEffort: "high", PlanType: "plus", Source: "openai", Transport: "http", ServiceTier: "default",
			RequestKind: "normal", Status: status, RequestedAt: fixedTime.Add(time.Duration(index) * time.Hour),
			QueueLatencyMS: 10, FirstTokenMS: 50, TotalLatencyMS: 1000, ReasoningTokensKnown: true,
			Usage: domain.UsageAmount{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 2, CostMicrodollars: cost}}
		if status == "error" {
			event.ErrorCode = "upstream_failure"
		}
		if _, err := s.SettleUsage(ctx, id, domain.UsageSettlement{Status: settlement, Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	service := application.NewReportsService(s, func() time.Time { return fixedTime.Add(2 * time.Hour) })
	if _, err := service.PruneRequestLogs(ctx, 7); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unsafe retention window accepted: %v", err)
	}
	report, err := service.Reports(ctx, application.ReportParams{StartDate: "2026-09-26", EndDate: "2026-09-26",
		Timezone: "Europe/Moscow"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.TotalRequests != 2 || report.Summary.TotalErrors != 1 ||
		report.Summary.TotalConversations != 1 || report.Summary.TotalCostUSD != 0.003 ||
		len(report.Daily) != 1 || report.Daily[0].MedianTTFTMS != 50 || len(report.ByModel) != 1 {
		t.Fatalf("report shape/measures: %+v", report)
	}
	logs, err := service.RequestLogs(ctx, application.RequestLogParams{Statuses: []string{"error"}, Search: "upstream_failure"})
	if err != nil || logs.Total != 1 || len(logs.Requests) != 1 || logs.Requests[0].RequestID != "request-two" ||
		logs.Requests[0].UserAgentGroup == nil || *logs.Requests[0].UserAgentGroup != "Codex" {
		t.Fatalf("filtered logs: %+v, %v", logs, err)
	}
	serialized, err := json.Marshal(logs)
	if err != nil || strings.Contains(string(serialized), "synthetic-not-a-real-token") {
		t.Fatalf("content-free log leaked secret: %v", err)
	}
	options, err := service.RequestLogOptions(ctx, application.RequestLogParams{Statuses: []string{"error"}})
	if err != nil || len(options.Statuses) != 2 || len(options.APIKeys) != 1 || len(options.ModelOptions) != 1 {
		t.Fatalf("filter options: %+v, %v", options, err)
	}
	before, err := s.UsageTotals(ctx, "key", "")
	if err != nil {
		t.Fatal(err)
	}
	pruned, err := s.FoldAndPruneRequestLogs(ctx, fixedTime.Add(3*time.Hour), 100)
	if err != nil || pruned != 2 {
		t.Fatalf("retention count: %d, %v", pruned, err)
	}
	if pruned, err := s.FoldAndPruneRequestLogs(ctx, fixedTime.Add(3*time.Hour), 100); err != nil || pruned != 0 {
		t.Fatalf("retention not idempotent: %d, %v", pruned, err)
	}
	after, err := s.UsageTotals(ctx, "key", "")
	if err != nil || after != before {
		t.Fatalf("lifetime totals changed: %+v -> %+v, %v", before, after, err)
	}
	pair, err := s.UsageTotals(ctx, "key", "acct")
	if err != nil || pair.RequestCount != 2 || pair.Usage.CostMicrodollars != 3000 {
		t.Fatalf("per-key account totals lost after fold: %+v, %v", pair, err)
	}
	report, err = service.Reports(ctx, application.ReportParams{StartDate: "2026-09-26", EndDate: "2026-09-26",
		Timezone: "Europe/Moscow"})
	if err != nil || report.Summary.TotalRequests != 2 || report.Summary.TotalConversations != 1 ||
		report.Summary.TotalCostUSD != 0.003 {
		t.Fatalf("folded report changed: %+v, %v", report, err)
	}
	if len(report.ByUserAgent) != 1 || report.ByUserAgent[0].UserAgent != "Historical (unattributed)" ||
		report.ByUserAgent[0].CostUSD != 0.003 {
		t.Fatalf("folded useragent cost disappeared: %+v", report.ByUserAgent)
	}
	logs, err = service.RequestLogs(ctx, application.RequestLogParams{})
	if err != nil || logs.Total != 0 {
		t.Fatalf("retained details still present: %+v, %v", logs, err)
	}
}

func TestImportedReportFoldDoesNotDoubleCount(t *testing.T) {
	ctx := context.Background()
	source, vault, _ := legacyFixture(t)
	s, _ := testStore(t)
	if _, err := s.ImportLegacySnapshot(ctx, source, vault); err != nil {
		t.Fatal(err)
	}
	service := application.NewReportsService(s, func() time.Time { return fixedTime })
	params := application.ReportParams{StartDate: "2026-09-23", EndDate: "2026-09-26", Timezone: "UTC"}
	before, err := service.Reports(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if before.Summary.TotalRequests != 10 {
		t.Fatalf("historical summary: %+v", before.Summary)
	}
	if _, err := s.FoldAndPruneRequestLogs(ctx, fixedTime.Add(time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	after, err := service.Reports(ctx, params)
	if err != nil || after.Summary.TotalRequests != before.Summary.TotalRequests ||
		after.Summary.TotalCostUSD != before.Summary.TotalCostUSD {
		t.Fatalf("imported raw double-counted during retention: %+v -> %+v, %v", before.Summary, after.Summary, err)
	}
}

func TestCancelledTrafficDoesNotInflateErrors(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	for index, status := range []string{"success", "cancelled", "error"} {
		code := ""
		if status == "cancelled" {
			code = "client_disconnected"
		}
		if status == "error" {
			code = "upstream_500"
		}
		if _, err := s.RecordUsage(ctx, domain.UsageEvent{RequestID: "request-" + string(rune('a'+index)),
			Model: "gpt-test", RequestKind: "normal", Status: status, ErrorCode: code,
			RequestedAt: fixedTime.Add(time.Duration(index) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	service := application.NewReportsService(s, func() time.Time { return fixedTime })
	params := application.ReportParams{StartDate: "2026-09-26", EndDate: "2026-09-26"}
	for _, fold := range []bool{false, true} {
		if fold {
			if _, err := s.FoldAndPruneRequestLogs(ctx, fixedTime.Add(time.Hour), 100); err != nil {
				t.Fatal(err)
			}
		}
		report, err := service.Reports(ctx, params)
		if err != nil || report.Summary.TotalRequests != 3 || report.Summary.TotalErrors != 1 ||
			report.Summary.TotalCancelled != 1 || report.Daily[0].ErrorCount != 1 || report.Daily[0].CancelledCount != 1 {
			t.Fatalf("cancelled/error split after fold=%t: %+v, %v", fold, report.Summary, err)
		}
	}
	var cancelledErrorRows int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM legacy_hourly_errors
 WHERE error_code='client_disconnected'`).Scan(&cancelledErrorRows); err != nil || cancelledErrorRows != 0 {
		t.Fatalf("cancelled row entered error satellite: %d, %v", cancelledErrorRows, err)
	}
}

func TestDashboardOverviewUsesPersistedQuotaAndTraffic(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct")
	reset := fixedTime.Add(5 * time.Hour)
	minutesPrimary, minutesSecondary := 300, 10080
	for _, quota := range []domain.AccountQuota{
		{AccountID: "acct", Window: "primary", UsedPercent: 25, ResetAt: &reset, WindowMinutes: &minutesPrimary, ObservedAt: fixedTime},
		{AccountID: "acct", Window: "secondary", UsedPercent: 50, WindowMinutes: &minutesSecondary, ObservedAt: fixedTime},
	} {
		if err := s.SaveAccountQuota(ctx, quota); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RecordUsage(ctx, domain.UsageEvent{RequestID: "dashboard-1", AccountID: "acct",
		Model: "gpt-test", ConversationID: "conversation", RequestKind: "normal", Status: "error",
		ErrorCode: "upstream_500", RequestedAt: fixedTime,
		Usage: domain.UsageAmount{InputTokens: 10, OutputTokens: 5, CostMicrodollars: 2500}}); err != nil {
		t.Fatal(err)
	}
	service := application.NewReportsService(s, func() time.Time { return fixedTime.Add(time.Hour) })
	view, err := service.DashboardOverview(ctx, "1d", s)
	if err != nil {
		t.Fatal(err)
	}
	if view.Timeframe.Key != "1d" || len(view.Accounts) != 1 || view.Summary.PrimaryWindow.CapacityCredits != 225 ||
		view.Summary.PrimaryWindow.RemainingPercent != 75 || view.Summary.SecondaryWindow == nil ||
		view.Summary.SecondaryWindow.CapacityCredits != 7560 || view.Summary.SecondaryWindow.RemainingPercent != 50 ||
		view.LastSyncAt == nil || len(view.Trends.Requests) != 24 {
		t.Fatalf("quota dashboard shape: %+v", view)
	}
	metrics := view.Summary.Metrics
	if metrics == nil || metrics.Requests == nil || *metrics.Requests != 1 || metrics.ErrorCount == nil ||
		*metrics.ErrorCount != 1 || metrics.TopError == nil || *metrics.TopError != "upstream_500" ||
		metrics.Conversations == nil || *metrics.Conversations != 1 || view.Summary.Cost.TotalUSD != 0.0025 {
		t.Fatalf("traffic dashboard metrics: %+v", view.Summary)
	}
	trendRequests := 0.0
	for _, point := range view.Trends.Requests {
		trendRequests += point.V
	}
	if trendRequests != 1 {
		t.Fatalf("dashboard trend lost request: %+v", view.Trends.Requests)
	}
}
