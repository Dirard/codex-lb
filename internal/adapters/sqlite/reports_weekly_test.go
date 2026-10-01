package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestWeeklyPaceHistoryCoversSixHoursAndDemandIncludesBoundary(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "pace")
	minutes := 10080
	reset := fixedTime.Add(24 * time.Hour)
	save := func(at time.Time, used float64) {
		t.Helper()
		if err := s.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "pace", Window: "secondary", UsedPercent: used, ResetAt: &reset, WindowMinutes: &minutes, ObservedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	save(fixedTime.Add(-8*24*time.Hour), 20)
	save(fixedTime.Add(-7*24*time.Hour), 25)
	save(fixedTime.Add(-6*24*time.Hour), 3) // Reset is not positive demand.
	for i := 0; i < 101; i++ {
		save(fixedTime.Add(time.Duration(i-100)*3*time.Minute), float64(i)*0.5+3)
	}
	history, err := s.RecentAccountQuotaHistory(ctx, fixedTime.Add(-30*24*time.Hour), fixedTime)
	if err != nil || len(history) != 101 || !history[0].ObservedAt.Equal(fixedTime.Add(-5*time.Hour)) {
		t.Fatalf("pace history silently truncated to the old 64-row tail: count=%d err=%v", len(history), err)
	}
	demand, err := s.PositiveQuotaDeltas(ctx, fixedTime.Add(-7*24*time.Hour), fixedTime)
	if err != nil || demand["pace"]["secondary"] != 55 {
		t.Fatalf("demand boundary/reset accounting: %+v %v", demand, err)
	}
}

func TestWeeklyPaceTopKeysRanksBothMetricsAndExcludesSyntheticTraffic(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	for i, event := range []struct {
		key, model, kind string
		input, cached    int
		at               time.Time
	}{
		{"many", "small", "normal", 2, 1, fixedTime.Add(-time.Hour)},
		{"many", "small", "normal", 2, 1, fixedTime.Add(-time.Hour)},
		{"many", "large", "normal", 20, 4, fixedTime.Add(-time.Hour)},
		{"tokens", "large", "normal", 1000, 10, fixedTime.Add(-time.Hour)},
		{"third", "small", "normal", 3, 0, fixedTime.Add(-time.Hour)},
		{"fourth", "small", "normal", 4, 0, fixedTime.Add(-time.Hour)},
		{"internal", "small", "warmup", 9999, 0, fixedTime.Add(-time.Hour)},
		{"internal", "small", "limit_warmup", 9999, 0, fixedTime.Add(-time.Hour)},
		{"old", "small", "normal", 9999, 0, fixedTime.Add(-3 * time.Hour)},
	} {
		_, err := s.db.ExecContext(ctx, `INSERT INTO usage_events(request_id,api_key_id,model,request_kind,status,requested_at,input_tokens,cached_input_tokens)
 VALUES(?,?,?,?,'success',?,?,?)`, fmt.Sprintf("pace-%d", i), event.key, event.model, event.kind, millis(event.at), event.input, event.cached)
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.WeeklyPaceTopKeys(ctx, fixedTime.Add(-2*time.Hour), fixedTime)
	if err != nil || len(rows) != 3 {
		t.Fatalf("unexpected ranked consumers: %+v %v", rows, err)
	}
	first := rows[0]
	if first.APIKeyID == nil || *first.APIKeyID != "many" || first.Requests != 3 || first.BillableTokens != 24 || first.CachedTokens != 6 || first.DominantModel != "small" {
		t.Fatalf("key totals/dominant model: %+v", first)
	}
	if rows[1].APIKeyID == nil || *rows[1].APIKeyID != "tokens" {
		t.Fatal("high-token consumer omitted")
	}
}
