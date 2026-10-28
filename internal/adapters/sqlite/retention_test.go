package sqlite

import (
	"context"
	"strconv"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.readDB.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// R14: a 100 000-row backlog clears in one budgeted cycle of bounded batches.
func TestRetentionCycleClearsLargeQuotaHistoryBacklog(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct")
	now := fixedTime
	old := millis(now.AddDate(0, 0, -60))
	if _, err := s.db.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<99999)
 INSERT INTO account_quota_history(account_id,window,observed_at,used_percent) SELECT 'acct','primary',?-i,1 FROM n`, old); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "acct", Window: "primary", UsedPercent: 2, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	service := application.NewRetentionService(s, func() time.Time { return now })
	result, err := service.RunCycle(ctx, time.Minute)
	if err != nil || result.Backlog || result.Deleted["quota_history"] != 100000 {
		t.Fatalf("cycle = %+v %v", result, err)
	}
	if left := countRows(t, s, `SELECT count(*) FROM account_quota_history`); left != 1 {
		t.Fatalf("history rows left = %d", left)
	}
	// An explicit 0 disables the default 45-day retention.
	disabled := 0
	settings, err := s.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.UsageHistoryRetentionDays = &disabled
	if err := s.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO account_quota_history(account_id,window,observed_at,used_percent) VALUES('acct','primary',?,1)`, old); err != nil {
		t.Fatal(err)
	}
	if result, err := service.RunCycle(ctx, time.Minute); err != nil || result.Deleted["quota_history"] != 0 {
		t.Fatalf("disabled retention deleted rows: %+v %v", result, err)
	}
}

// R16: thinning keeps the newest observation per series, hour, reset and window.
func TestQuotaHistoryThinningKeepsHourlyAndResetBoundaries(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct")
	hour := fixedTime.Add(-72 * time.Hour).Truncate(time.Hour)
	minutes := 10080
	firstReset, secondReset := hour.Add(time.Hour), hour.Add(8*24*time.Hour)
	// The baseline before the seven-day demand window is alone in its hour.
	if err := s.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "acct", Window: "secondary", UsedPercent: 0,
		ResetAt: &firstReset, WindowMinutes: &minutes, ObservedAt: fixedTime.Add(-8 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		reset := &firstReset
		if i >= 8 {
			reset = &secondReset
		}
		if err := s.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "acct", Window: "secondary", UsedPercent: float64(i),
			ResetAt: reset, WindowMinutes: &minutes, ObservedAt: hour.Add(time.Duration(i) * 5 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	// A recent observation stays at full resolution.
	for i := range 3 {
		if err := s.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "acct", Window: "secondary", UsedPercent: 50,
			ResetAt: &secondReset, WindowMinutes: &minutes, ObservedAt: fixedTime.Add(time.Duration(i-3) * 5 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.PositiveQuotaDeltas(ctx, fixedTime.Add(-7*24*time.Hour), fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewRetentionService(s, func() time.Time { return fixedTime })
	result, err := service.RunCycle(ctx, time.Minute)
	if err != nil || result.Deleted["quota_history_thinning"] != 10 {
		t.Fatalf("thinning = %+v %v", result, err)
	}
	rows, err := s.readDB.QueryContext(ctx, `SELECT used_percent FROM account_quota_history ORDER BY observed_at,id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kept []float64
	for rows.Next() {
		var used float64
		if err := rows.Scan(&used); err != nil {
			t.Fatal(err)
		}
		kept = append(kept, used)
	}
	if len(kept) != 6 || kept[0] != 0 || kept[1] != 7 || kept[2] != 11 || kept[3] != 50 {
		t.Fatalf("kept = %v, want the baseline, the last pre-reset (7) and post-reset (11) samples and recent rows", kept)
	}
	// Positive deltas telescope within an hour, so thinning keeps the demand.
	demand, err := s.PositiveQuotaDeltas(ctx, fixedTime.Add(-7*24*time.Hour), fixedTime)
	if err != nil || demand["acct"]["secondary"] != before["acct"]["secondary"] || demand["acct"]["secondary"] != 50 {
		t.Fatalf("demand before %+v after %+v %v", before, demand, err)
	}
}

// R17: pruning keeps every fence-relevant reservation and rowid monotonicity.
func TestSettledReservationPruningPreservesFences(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct")
	if err := s.SaveAPIKey(ctx, testKey("key", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	old, recent := millis(fixedTime.AddDate(0, 0, -30)), millis(fixedTime)
	insert := func(id, status string, updated int64) int64 {
		t.Helper()
		res, err := s.db.ExecContext(ctx, `INSERT INTO usage_reservations(id,api_key_id,account_id,model,status,created_at,updated_at)
 VALUES(?,'key','acct','m',?,?,?)`, id, status, updated, updated)
		if err != nil {
			t.Fatal(err)
		}
		rowid, _ := res.LastInsertId()
		return rowid
	}
	insert("released-old", "released", old)
	outcome := insert("released-outcome", "released", old)
	continuation := insert("released-continuation", "released", old)
	insert("with-event", "finalized", old)
	insert("reserved-old", "reserved", old)
	insert("released-recent", "released", recent)
	maxRowid := insert("released-max", "released", old)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO account_outcomes(account_id,reservation_rowid) VALUES('acct',?)`, outcome); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO continuations(response_id,key_id,account_id,provider_id,model,created_at,expires_at,reservation_rowid)
 VALUES('resp','key','acct','openai','m',?,?,?)`, recent, recent+3600000, continuation); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO usage_events(request_id,reservation_id,api_key_id,account_id,model,status,requested_at)
 VALUES('event','with-event','key','acct','m','success',?)`, old); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.PruneSettledReservations(ctx, fixedTime.AddDate(0, 0, -7), 500)
	if err != nil || deleted != 1 {
		t.Fatalf("pruned = %d %v", deleted, err)
	}
	for _, id := range []string{"released-outcome", "released-continuation", "with-event", "reserved-old", "released-recent", "released-max"} {
		if countRows(t, s, `SELECT count(*) FROM usage_reservations WHERE id=?`, id) != 1 {
			t.Fatalf("reservation %s was pruned", id)
		}
	}
	if next := insert("next", "reserved", recent); next <= maxRowid {
		t.Fatalf("new rowid %d is not above the deleted range (max %d)", next, maxRowid)
	}
	// Request-log retention removes an event and its now unreferenced reservation.
	if n, err := s.FoldAndPruneRequestLogs(ctx, fixedTime.AddDate(0, 0, -1), 500); err != nil || n != 1 {
		t.Fatalf("fold = %d %v", n, err)
	}
	if countRows(t, s, `SELECT count(*) FROM usage_reservations WHERE id='with-event'`) != 0 {
		t.Fatal("reservation of a pruned event was kept")
	}
}

// R18: automation and warm-up pruning never removes claims that can still run.
func TestAutomationAndWarmupPruningKeepsLiveClaims(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	job := domain.AutomationJob{ID: "job", Name: "w", Enabled: true,
		Schedule: domain.AutomationSchedule{Type: "daily", Time: "03:00", Timezone: "UTC", Days: []string{"mon"}},
		Model:    "m", Prompt: "ping", CreatedAt: fixedTime, UpdatedAt: fixedTime}
	if err := s.SaveAutomationJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	for cycle := range 103 {
		status := "success"
		if cycle == 0 {
			status = "pending"
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO automation_runs(id,job_id,cycle_key,slot_key,trigger_kind,status,model,scheduled_for)
 VALUES(?,'job',?,?,'scheduled',?,'m',?)`, "run-"+strconv.Itoa(cycle), "cycle-"+strconv.Itoa(cycle), "slot-"+strconv.Itoa(cycle), status,
			millis(fixedTime.Add(time.Duration(cycle)*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO automation_runs(id,job_id,cycle_key,slot_key,trigger_kind,status,model,scheduled_for)
 VALUES('orphan','deleted-job','c','orphan-slot','manual','failed','m',0)`); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.PruneAutomationRuns(ctx, 100, 500)
	if err != nil || deleted != 3 {
		t.Fatalf("automation pruned = %d %v", deleted, err)
	}
	if countRows(t, s, `SELECT count(*) FROM automation_runs WHERE id IN ('run-0','run-102')`) != 2 {
		t.Fatal("pending or newest cycle was pruned")
	}
	saveTestAccount(t, s, "acct")
	for attempt := 1; attempt <= 105; attempt++ {
		reset := fixedTime.AddDate(0, 0, -30)
		if attempt > 103 {
			reset = fixedTime
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO account_limit_warmup_attempts(account_id,attempt,status,model,attempted_at,window,reset_at)
 VALUES('acct',?,'success','m',?,'primary',?)`, attempt, millis(reset), millis(reset)+int64(attempt)); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err = s.PruneLimitWarmupAttempts(ctx, fixedTime.AddDate(0, 0, -8), 100, 500)
	if err != nil || deleted != 5 {
		t.Fatalf("warm-up pruned = %d %v", deleted, err)
	}
	if countRows(t, s, `SELECT max(attempt) FROM account_limit_warmup_attempts`) != 105 {
		t.Fatal("newest warm-up attempt pruned")
	}
}
