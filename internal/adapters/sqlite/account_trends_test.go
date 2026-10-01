package sqlite

import (
	"context"
	"database/sql"
	"math"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestAccountQuotaHistoryTrendProjectionAndRetention(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	minutes := 300
	reset := fixedTime.Add(4 * time.Hour)
	service := application.NewReportsService(store, func() time.Time { return fixedTime })
	for index, row := range []struct {
		at   time.Time
		used float64
	}{
		{fixedTime.Add(-100 * 24 * time.Hour), 5},
		{fixedTime.Add(-20 * time.Minute), 40},
		{fixedTime.Add(-10 * time.Minute), 50},
	} {
		if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "acct", Window: "primary",
			UsedPercent: row.used, ResetAt: &reset, WindowMinutes: &minutes, ObservedAt: row.at}); err != nil {
			t.Fatal(err)
		}
		if index == 1 {
			projection, err := service.DashboardProjections(ctx, store)
			if err != nil || projection.DepletionPrimary != nil {
				t.Fatalf("one current observation invented projection: %+v, %v", projection, err)
			}
		}
	}
	projection, err := service.DashboardProjections(ctx, store)
	if err != nil || projection.DepletionPrimary == nil || projection.DepletionSecondary != nil {
		t.Fatalf("projection not derived from primary history: %+v, %v", projection, err)
	}
	primary := projection.DepletionPrimary
	if primary.Risk != 1 || primary.RiskLevel != "critical" || math.Abs(primary.BurnRate-4.8) > 1e-6 ||
		primary.ProjectedExhaustionAt == nil || primary.SecondsUntilExhaustion == nil ||
		math.Abs(*primary.SecondsUntilExhaustion-3000) > 1e-6 {
		t.Fatalf("unexpected depletion: %+v", primary)
	}
	trend, err := service.AccountTrends(ctx, "acct", store)
	if err != nil || len(trend.Primary) != 168 || len(trend.Secondary) != 0 || trend.Primary[167].V != 55 {
		t.Fatalf("hourly trend not populated: %+v, %v", trend, err)
	}
	if count, err := service.PruneAccountQuotaHistory(ctx, 44); err == nil || count != 0 {
		t.Fatalf("unsafe history retention accepted: %d, %v", count, err)
	}
	if count, err := service.PruneAccountQuotaHistory(ctx, 0); err != nil || count != 0 {
		t.Fatalf("disabled history retention pruned rows: %d, %v", count, err)
	}
	deleted, err := store.PruneAccountQuotaHistory(ctx, fixedTime.Add(-45*24*time.Hour), 1)
	if err != nil || deleted != 1 {
		t.Fatalf("bounded history prune: %d, %v", deleted, err)
	}
	var remaining int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM account_quota_history").Scan(&remaining); err != nil || remaining != 2 {
		t.Fatalf("recent history removed: %d, %v", remaining, err)
	}
}

func TestImportLegacyQuotaHistoryAndRetentionOverride(t *testing.T) {
	ctx := context.Background()
	source, vault, _ := legacyFixture(t)
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO usage_history VALUES
 (2,'acct-a','primary',80.0,1790000000,300,'2026-09-24 01:00:00.000000');
 ALTER TABLE dashboard_settings ADD COLUMN usage_history_retention_days INTEGER;
 UPDATE dashboard_settings SET usage_history_retention_days=45 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, _ := testStore(t)
	if _, err := store.ImportLegacySnapshot(ctx, source, vault); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM account_quota_history WHERE account_id='acct-a'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("legacy observations lost: %d, %v", count, err)
	}
	quotas, err := store.ListAccountQuota(ctx, "acct-a")
	if err != nil || len(quotas) != 1 || quotas[0].UsedPercent != 80 {
		t.Fatalf("latest legacy quota changed: %+v, %v", quotas, err)
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil || settings.UsageHistoryRetentionDays == nil || *settings.UsageHistoryRetentionDays != 45 {
		t.Fatalf("history retention override lost: %+v, %v", settings, err)
	}
}
