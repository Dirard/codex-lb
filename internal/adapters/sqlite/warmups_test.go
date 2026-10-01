package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestLimitWarmupClaimDurablyDeduplicatesTupleAndJitter(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "warmups.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	reset := now.Add(5 * time.Hour)
	first, claimed, err := store.ClaimLimitWarmupAttempt(ctx, "acct", "primary", reset, "m", 0, now)
	if err != nil || !claimed || first.Attempt != 1 || first.Status != "claimed" {
		t.Fatalf("first = %+v %v %v", first, claimed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, at := range []time.Time{reset, reset.Add(4 * time.Second)} {
		if _, claimed, err := store.ClaimLimitWarmupAttempt(ctx, "acct", "primary", at, "m", 0, now.Add(2*time.Hour)); err != nil || claimed {
			t.Fatalf("crashed tuple replay at %s = %v %v", at, claimed, err)
		}
	}
	secondary, claimed, err := store.ClaimLimitWarmupAttempt(ctx, "acct", "secondary", reset, "m", 0, now)
	if err != nil || !claimed || secondary.Attempt != 2 {
		t.Fatalf("secondary = %+v %v %v", secondary, claimed, err)
	}
	if err := store.CompleteLimitWarmupAttempt(ctx, "acct", first.Attempt, domain.AutomationSuccess, now.Add(time.Minute), nil); err != nil {
		t.Fatalf("earlier concurrent claim abandoned: %v", err)
	}
	if err := store.CompleteLimitWarmupAttempt(ctx, "acct", secondary.Attempt, domain.AutomationSuccess, now.Add(time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteLimitWarmupAttempt(ctx, "acct", first.Attempt, domain.AutomationSuccess, now, nil); err == nil {
		t.Fatal("double completion accepted")
	}
}

func TestLimitWarmupIdleCooldownAndCycleDedup(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "warmups.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1_800_000_000, 0).UTC()
	reset := now.Add(5 * time.Hour)
	first, claimed, err := store.ClaimLimitWarmupAttempt(ctx, "acct", "primary_idle", reset, "m", time.Hour, now)
	if err != nil || !claimed {
		t.Fatalf("first idle = %+v %v %v", first, claimed, err)
	}
	if _, claimed, err := store.ClaimLimitWarmupAttempt(ctx, "acct", "primary_idle", reset.Add(5*time.Hour), "m", time.Hour, now.Add(30*time.Minute)); err != nil || claimed {
		t.Fatalf("cooldown = %v %v", claimed, err)
	}
	second, claimed, err := store.ClaimLimitWarmupAttempt(ctx, "acct", "primary_idle", reset.Add(5*time.Hour), "m", time.Hour, now.Add(2*time.Hour))
	if err != nil || !claimed || second.Attempt != 2 {
		t.Fatalf("next cycle = %+v %v %v", second, claimed, err)
	}
	var oldStatus string
	if err := store.db.QueryRowContext(ctx, "SELECT status FROM account_limit_warmup_attempts WHERE account_id='acct' AND attempt=1").Scan(&oldStatus); err != nil || oldStatus != "abandoned" {
		t.Fatalf("crashed claim = %q %v", oldStatus, err)
	}
	if _, claimed, err := store.ClaimLimitWarmupAttempt(ctx, "acct", "primary_idle", reset, "m", time.Hour, now.Add(8*time.Hour)); err != nil || claimed {
		t.Fatalf("old cycle replay = %v %v", claimed, err)
	}
}

func TestLimitWarmupSettingsRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "warmups.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.LimitWarmupEnabled || settings.LimitWarmupStaggeredIdleEnabled || settings.LimitWarmupExhaustedPercent != 99 || settings.LimitWarmupMinAvailablePercent != 100 {
		t.Fatalf("unsafe defaults: %+v", settings)
	}
	settings.LimitWarmupEnabled = true
	settings.LimitWarmupStaggeredIdleEnabled = true
	settings.LimitWarmupExhaustedPercent = 98.5
	settings.LimitWarmupMinAvailablePercent = 25
	settings.LimitWarmupIdlePercent = 2.5
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadSettings(ctx)
	if err != nil || !got.LimitWarmupEnabled || !got.LimitWarmupStaggeredIdleEnabled || got.LimitWarmupExhaustedPercent != 98.5 || got.LimitWarmupMinAvailablePercent != 25 || got.LimitWarmupIdlePercent != 2.5 {
		t.Fatalf("round trip = %+v %v", got, err)
	}
	got.LimitWarmupExhaustedPercent = 0
	if err := store.SaveSettings(ctx, got); err == nil {
		t.Fatal("invalid threshold accepted")
	}
}

func TestImportLegacyWarmupSettingsPreservesOptionalControls(t *testing.T) {
	ctx := context.Background()
	source, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	_, err = source.ExecContext(ctx, `CREATE TABLE dashboard_settings (
 id INTEGER PRIMARY KEY, limit_warmup_windows TEXT, limit_warmup_prompt TEXT,
 limit_warmup_cooldown_seconds INTEGER, limit_warmup_exhausted_threshold_percent REAL,
 limit_warmup_idle_threshold_percent REAL, limit_warmup_min_available_percent REAL,
 limit_warmup_staggered_idle_enabled INTEGER);
 INSERT INTO dashboard_settings VALUES(1,'secondary','Wake.',120,98.5,2.5,25,1);`)
	if err != nil {
		t.Fatal(err)
	}
	target, err := Open(filepath.Join(t.TempDir(), "go.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	srcTx, err := source.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer srcTx.Rollback()
	dstTx, err := target.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dstTx.Rollback()
	if err := importLegacyWarmupSettings(ctx, srcTx, dstTx); err != nil {
		t.Fatal(err)
	}
	if err := dstTx.Commit(); err != nil {
		t.Fatal(err)
	}
	settings, err := target.LoadSettings(ctx)
	if err != nil || settings.LimitWarmupWindows != "secondary" || settings.LimitWarmupPrompt != "Wake." ||
		settings.LimitWarmupCooldownSeconds != 120 || settings.LimitWarmupExhaustedPercent != 98.5 ||
		settings.LimitWarmupIdlePercent != 2.5 || settings.LimitWarmupMinAvailablePercent != 25 ||
		!settings.LimitWarmupStaggeredIdleEnabled {
		t.Fatalf("imported controls = %+v %v", settings, err)
	}
}
