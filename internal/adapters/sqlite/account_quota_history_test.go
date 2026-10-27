package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestQuotaHistoryCoalescesPollingIntoBucketsAndKeepsResets(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "poll")
	minutes := 300
	firstReset, secondReset := fixedTime.Add(time.Hour), fixedTime.Add(6*time.Hour)
	start := fixedTime.Truncate(5 * time.Minute)
	for i := range 30 {
		reset := &firstReset
		used := float64(i)
		if i >= 12 {
			// Reset inside the bucket [10:10, 10:15): both sides must survive.
			reset, used = &secondReset, float64(i-12)
		}
		if err := s.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "poll", Window: "primary",
			UsedPercent: used, ResetAt: reset, WindowMinutes: &minutes, ObservedAt: start.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.readDB.QueryContext(ctx, `SELECT observed_at,used_percent,reset_at FROM account_quota_history
 WHERE account_id='poll' ORDER BY observed_at,id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type sample struct {
		at, reset int64
		used      float64
	}
	var got []sample
	for rows.Next() {
		var item sample
		if err := rows.Scan(&item.at, &item.used, &item.reset); err != nil {
			t.Fatal(err)
		}
		got = append(got, item)
	}
	want := []sample{
		{millis(start.Add(4 * time.Minute)), millis(firstReset), 4},
		{millis(start.Add(9 * time.Minute)), millis(firstReset), 9},
		{millis(start.Add(11 * time.Minute)), millis(firstReset), 11},
		{millis(start.Add(14 * time.Minute)), millis(secondReset), 2},
		{millis(start.Add(19 * time.Minute)), millis(secondReset), 7},
		{millis(start.Add(24 * time.Minute)), millis(secondReset), 12},
		{millis(start.Add(29 * time.Minute)), millis(secondReset), 17},
	}
	if len(got) != len(want) {
		t.Fatalf("history rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("history row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// An out-of-order observation is history, not a replacement of the newest row.
	if err := s.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "poll", Window: "primary",
		UsedPercent: 1, ResetAt: &secondReset, WindowMinutes: &minutes, ObservedAt: start.Add(28 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.readDB.QueryRowContext(ctx, `SELECT count(*) FROM account_quota_history WHERE account_id='poll'`).Scan(&count); err != nil || count != len(want)+1 {
		t.Fatalf("out-of-order observation rows=%d err=%v", count, err)
	}
	current, err := s.ListAccountQuota(ctx, "poll")
	if err != nil || len(current) != 1 || current[0].UsedPercent != 17 {
		t.Fatalf("current quota must remain the newest observation: %+v %v", current, err)
	}
}

// Regression for audit DB-3: with dense one-minute history the overview and
// projections failed once the history exceeded 100 000 rows (≈139 accounts).
func TestDashboardHistoryScalesBeyondObservationLimit(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	const accounts = 250
	now := fixedTime
	at := millis(now)
	if _, err := s.db.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<?)
INSERT INTO accounts(id,kind,provider,email,plan_type,status,created_at)
SELECT printf('acct-%03d',i),'chatgpt','openai',printf('a%03d@example.invalid',i),'pro','active',? FROM n`, accounts-1, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO account_quotas(account_id,window,used_percent,reset_at,window_minutes,observed_at)
SELECT id,'primary',10,?,300,? FROM accounts UNION ALL SELECT id,'secondary',20,?,10080,? FROM accounts`,
		at+3600000, at, at+86400000, at); err != nil {
		t.Fatal(err)
	}
	// Seven hours of legacy one-minute rows written before coalescing existed.
	if _, err := s.db.ExecContext(ctx, `WITH RECURSIVE m(j) AS (SELECT 0 UNION ALL SELECT j+1 FROM m WHERE j<419)
INSERT INTO account_quota_history(account_id,window,observed_at,used_percent,reset_at,window_minutes)
SELECT a.id,w.w,?-j*60000,(419-j)/30*1.0,CASE w.w WHEN 'primary' THEN ?+3600000 ELSE ?+86400000 END,
 CASE w.w WHEN 'primary' THEN 300 ELSE 10080 END
 FROM accounts a,(SELECT 'primary' AS w UNION ALL SELECT 'secondary') w,m`, at, at, at); err != nil {
		t.Fatal(err)
	}
	history, err := s.RecentAccountQuotaHistory(ctx, now.Add(-30*24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	perSeries := map[string]int{}
	for _, row := range history {
		perSeries[row.AccountID+"/"+row.Window]++
	}
	// Legacy one-minute rows: the six-hour horizon (361 rows, both ends inclusive)
	// already holds more than the 64-row tail.
	if len(perSeries) != 2*accounts {
		t.Fatalf("series = %d", len(perSeries))
	}
	for series, n := range perSeries {
		if n != 361 {
			t.Fatalf("series %s returned %d rows", series, n)
		}
	}
	service := application.NewReportsService(s, func() time.Time { return now })
	overview, err := service.DashboardOverview(ctx, "7d", s)
	if err != nil || len(overview.Accounts) != accounts || overview.WeeklyCreditPace == nil {
		t.Fatalf("overview accounts=%d pace=%v err=%v", len(overview.Accounts), overview.WeeklyCreditPace != nil, err)
	}
	projections, err := service.DashboardProjections(ctx, s)
	if err != nil || projections.WeeklyCreditPace == nil {
		t.Fatalf("projections pace=%v err=%v", projections.WeeklyCreditPace != nil, err)
	}
}

func TestQuotaHistoryReadUsesSeriesIndex(t *testing.T) {
	s, _ := testStore(t)
	rows, err := s.readDB.Query("EXPLAIN QUERY PLAN "+recentQuotaHistoryQuery, 1, 0, 0, 1, quotaPaceTail-1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "idx_account_quota_history_identity_time") || strings.Contains(plan, "SCAN h") ||
		strings.Contains(plan, "SCAN t") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("history read must stay on the series and time indexes:\n%s", plan)
	}
}
