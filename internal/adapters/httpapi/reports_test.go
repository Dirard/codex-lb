package httpapi

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/domain"
)

func TestReportsRoutesAuthenticatedContentFree(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	server := New(store, vault, Config{}, nil)
	mux := http.NewServeMux()
	server.registerReportsRoutes(mux, store)
	handler := server.requireAdmin(mux)
	now := time.Now().UTC()
	if err := store.SaveAccount(ctx, domain.Account{ID: "acct", Kind: domain.AccountChatGPT,
		Provider: "openai", Email: "synthetic@example.invalid", PlanType: "plus", Status: domain.AccountActive,
		CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordUsage(ctx, domain.UsageEvent{RequestID: "req-report", AccountID: "acct",
		Model: "gpt-test", RequestKind: "normal", Status: "error", ErrorCode: "upstream_failure",
		RequestedAt: now, ConversationID: "conversation", UserAgentGroup: "Codex",
		Usage: domain.UsageAmount{InputTokens: 8, OutputTokens: 2, CostMicrodollars: 1000}}); err != nil {
		t.Fatal(err)
	}
	request := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://localhost"+path, nil)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if got := request("/api/reports", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated report: %d", got)
	}
	token, err := server.auth.SetupPassword(ctx, "synthetic-test-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	date := now.Format("2006-01-02")
	response := request("/api/reports?start_date="+date+"&end_date="+date, token)
	var report domain.ReportsResponse
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &report) != nil || report.Summary.TotalRequests != 1 {
		t.Fatalf("report route: %d %s", response.Code, response.Body.String())
	}
	overviewResponse := request("/api/dashboard/overview?timeframe=1d", token)
	var overview domain.DashboardOverview
	if overviewResponse.Code != 200 || json.Unmarshal(overviewResponse.Body.Bytes(), &overview) != nil ||
		overview.Summary.Metrics == nil || overview.Summary.Metrics.Requests == nil ||
		*overview.Summary.Metrics.Requests != 1 || len(overview.Trends.Requests) != 24 {
		t.Fatalf("dashboard overview route: %d %s", overviewResponse.Code, overviewResponse.Body.String())
	}
	reset := now.Add(4 * time.Hour)
	minutes := 300
	for _, quota := range []domain.AccountQuota{
		{AccountID: "acct", Window: "primary", UsedPercent: 40, ResetAt: &reset, WindowMinutes: &minutes, ObservedAt: now.Add(-20 * time.Minute)},
		{AccountID: "acct", Window: "primary", UsedPercent: 50, ResetAt: &reset, WindowMinutes: &minutes, ObservedAt: now.Add(-10 * time.Minute)},
	} {
		if err := store.SaveAccountQuota(ctx, quota); err != nil {
			t.Fatal(err)
		}
	}
	projectionsResponse := request("/api/dashboard/projections", token)
	var projections domain.DashboardProjections
	if projectionsResponse.Code != 200 || json.Unmarshal(projectionsResponse.Body.Bytes(), &projections) != nil ||
		projections.DepletionPrimary == nil || projections.DepletionSecondary != nil {
		t.Fatalf("dashboard projections route: %d %s", projectionsResponse.Code, projectionsResponse.Body.String())
	}
	trendsResponse := request("/api/accounts/acct/trends", token)
	var trends domain.AccountTrendsResponse
	if trendsResponse.Code != 200 || json.Unmarshal(trendsResponse.Body.Bytes(), &trends) != nil ||
		trends.AccountID != "acct" || len(trends.Primary) != 168 {
		t.Fatalf("account trends route: %d %s", trendsResponse.Code, trendsResponse.Body.String())
	}
	if got := request("/api/dashboard/overview?timeframe=invalid", token).Code; got != 422 {
		t.Fatalf("invalid dashboard timeframe accepted: %d", got)
	}
	logs := request("/api/request-logs/?status=error&search=upstream_failure", token)
	var page domain.RequestLogsResponse
	if logs.Code != 200 || json.Unmarshal(logs.Body.Bytes(), &page) != nil || page.Total != 1 ||
		len(page.Requests) != 1 || page.Requests[0].ErrorMessage != nil {
		t.Fatalf("request logs route: %d %s", logs.Code, logs.Body.String())
	}
	if strings.Contains(logs.Body.String(), "synthetic-test-password") {
		t.Fatal("report leaked password")
	}
	if got := request("/api/request-logs/options", token).Code; got != 200 {
		t.Fatalf("options route: %d", got)
	}
	conversations := request("/api/conversations?timeframe=1d", token)
	var conversationPage domain.ConversationsResponse
	if conversations.Code != 200 || json.Unmarshal(conversations.Body.Bytes(), &conversationPage) != nil ||
		conversationPage.Total != 1 {
		t.Fatalf("conversations route: %d %s", conversations.Code, conversations.Body.String())
	}
	details := request("/api/conversations/conversation", token)
	var conversationDetails domain.ConversationDetails
	if details.Code != 200 || json.Unmarshal(details.Body.Bytes(), &conversationDetails) != nil ||
		conversationDetails.ConversationID != "conversation" {
		t.Fatalf("conversation details route: %d %s", details.Code, details.Body.String())
	}
	if got := request("/api/request-logs?limit=0", token).Code; got != 422 {
		t.Fatalf("invalid limit accepted: %d", got)
	}
	secret, err := vault.Encrypt([]byte("synthetic-pending-credential"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: "acct", AccessTokenEncrypted: secret, RefreshTokenEncrypted: secret, IDTokenEncrypted: secret}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAPIKey(ctx, domain.APIKey{ID: "pending-key", Name: "Pending key", KeyHash: strings.Repeat("0", 64), KeyPrefix: "synthetic", IsActive: true}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "pending-request", APIKeyID: "pending-key", AccountID: "acct", Model: "pending-model", Now: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkReservationUncertain(ctx, "pending-request"); err != nil {
		t.Fatal(err)
	}
	if got := request("/api/request-logs?status=reconciliation_required", "").Code; got != 401 {
		t.Fatal("pending logs bypassed admin authentication")
	}
	pending := request("/api/request-logs/?status=reconciliation_required&apiKeyId=pending-key", token)
	var pendingPage domain.RequestLogsResponse
	if pending.Code != 200 || json.Unmarshal(pending.Body.Bytes(), &pendingPage) != nil || pendingPage.Total != 1 || len(pendingPage.Requests) != 1 || pendingPage.Requests[0].CostUSD != nil || pendingPage.Requests[0].Tokens != nil {
		t.Fatalf("pending nullable route contract: %d %s", pending.Code, pending.Body.String())
	}
	if strings.Contains(pending.Body.String(), "synthetic-pending-credential") || strings.Contains(pending.Body.String(), "synthetic-test-password") {
		t.Fatal("pending report leaked credentials")
	}
	weeklyMinutes := 10080
	weeklyReset := now.Add(3 * 24 * time.Hour)
	for i, used := range []float64{20, 60} {
		if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "acct", Window: "secondary",
			UsedPercent: used, ResetAt: &weeklyReset, WindowMinutes: &weeklyMinutes,
			ObservedAt: now.Add(time.Duration(i-1) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.WeeklyPaceSmoothingMinutes = 240
	settings.WeeklyPaceWorkingDays = "0,1,2,3,4"
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/dashboard/overview", "/api/dashboard/projections"} {
		response := request(path, token)
		var payload struct {
			Pace *domain.WeeklyCreditPace `json:"weeklyCreditPace"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Pace == nil {
			t.Fatalf("weekly pace route %s: %d %s", path, response.Code, response.Body.String())
		}
		pace := payload.Pace
		if pace.AccountCount != 1 || pace.TotalFullCredits != 7560 || pace.PaceGapSmoothingMinutes != 240 ||
			math.Abs(pace.DeltaPercent-pace.SmoothedDeltaPercent-20) > 1e-6 || pace.ForecastBurnRateCreditsPerHour == nil {
			t.Fatalf("persisted settings did not reach weekly pace: %+v", pace)
		}
		if strings.Contains(response.Body.String(), "synthetic-pending-credential") {
			t.Fatal("weekly report leaked credentials")
		}
	}
}
