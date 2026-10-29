package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
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
	buckets := map[int64]bool{}
	for i := 0; i < 101; i++ {
		at := fixedTime.Add(time.Duration(i-100) * 3 * time.Minute)
		buckets[millis(at)/quotaHistoryBucketMillis] = true
		save(at, float64(i)*0.5+3)
	}
	// Three-minute polling coalesces into five-minute buckets; the horizon still
	// covers all five hours and the 64-row tail reaches the older samples.
	history, err := s.RecentAccountQuotaHistory(ctx, fixedTime.Add(-30*24*time.Hour), fixedTime)
	if err != nil || len(history) != len(buckets)+3 || !history[0].ObservedAt.Equal(fixedTime.Add(-8*24*time.Hour)) ||
		!history[len(history)-1].ObservedAt.Equal(fixedTime) || history[len(history)-1].UsedPercent != 53 {
		t.Fatalf("pace history lost horizon buckets or the tail: count=%d buckets=%d err=%v", len(history), len(buckets), err)
	}
	if len(buckets) >= quotaPaceTail {
		t.Fatalf("fixture must leave room for the tail: %d buckets", len(buckets))
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

// The Groups page's pool runway card shows no key attribution, which grouped
// every raw event of the last two hours (about 20 000 in production, most of
// GET /api/dashboard/projections): the projections leave it empty and read
// no usage events, while the deprecated overview keeps it.
func TestProjectionsLeaveTheKeyAttributionToTheOverview(t *testing.T) {
	ctx := context.Background()
	s, counter := countingStore(t)
	now := fixedTime
	at := millis(now)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<2)
INSERT INTO accounts(id,kind,provider,email,plan_type,status,created_at)
SELECT printf('acct-%d',i),'chatgpt','openai',printf('a%d@example.invalid',i),'pro','active',? FROM n`, at)
	exec(`INSERT INTO account_quotas(account_id,window,used_percent,reset_at,window_minutes,observed_at)
SELECT id,'primary',10,?,300,? FROM accounts UNION ALL SELECT id,'secondary',20,?,10080,? FROM accounts`,
		at+3600000, at, at+86400000, at)
	exec(`WITH RECURSIVE m(j) AS (SELECT 0 UNION ALL SELECT j+1 FROM m WHERE j<83)
INSERT INTO account_quota_history(account_id,window,observed_at,used_percent,reset_at,window_minutes)
SELECT a.id,w.w,?-j*300000,(83-j)/8*1.0,CASE w.w WHEN 'primary' THEN ?+3600000 ELSE ?+86400000 END,
 CASE w.w WHEN 'primary' THEN 300 ELSE 10080 END
 FROM accounts a,(SELECT 'primary' AS w UNION ALL SELECT 'secondary') w,m`, at, at, at)
	for i, key := range []string{"busy", "busy", "other"} {
		exec(`INSERT INTO usage_events(request_id,api_key_id,model,request_kind,status,requested_at,input_tokens,cached_input_tokens)
 VALUES(?,?,'model','normal','success',?,100,10)`, fmt.Sprintf("attr-%d", i), key, at-int64(i+1)*600000)
	}
	service := application.NewReportsService(s, func() time.Time { return now })
	overview, err := service.DashboardOverview(ctx, "7d", s)
	if err != nil || overview.WeeklyCreditPace == nil || len(overview.WeeklyCreditPace.TopAPIKeys) != 2 {
		t.Fatalf("overview pace %+v: %v", overview.WeeklyCreditPace, err)
	}
	mark := counter.count()
	projections, err := service.DashboardProjections(ctx, s)
	if err != nil || projections.WeeklyCreditPace == nil {
		t.Fatalf("projections pace %+v: %v", projections.WeeklyCreditPace, err)
	}
	if keys := projections.WeeklyCreditPace.TopAPIKeys; keys == nil || len(keys) != 0 {
		t.Fatalf("projections key attribution %+v, want an empty list", keys)
	}
	for _, query := range counter.since(mark) {
		if strings.Contains(query, "usage_events") {
			t.Fatalf("projections read usage events:\n%s", query)
		}
	}
	overview.WeeklyCreditPace.TopAPIKeys = projections.WeeklyCreditPace.TopAPIKeys
	if !reflect.DeepEqual(overview.WeeklyCreditPace, projections.WeeklyCreditPace) {
		t.Fatalf("the pace differs beyond the key attribution:\n overview    %+v\n projections %+v", overview.WeeklyCreditPace, projections.WeeklyCreditPace)
	}
}
