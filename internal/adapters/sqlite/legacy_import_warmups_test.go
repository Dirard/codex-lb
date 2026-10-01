package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func legacyWarmupSource(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE account_limit_warmups (
 id INTEGER PRIMARY KEY, account_id TEXT NOT NULL, window TEXT NOT NULL,
 reset_at INTEGER NOT NULL, status TEXT NOT NULL, model TEXT NOT NULL,
 attempted_at TEXT NOT NULL, completed_at TEXT, error_code TEXT,
 UNIQUE(account_id,window,reset_at));`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func TestImportLegacyWarmupClaimsPreventPaidReplayAfterRestart(t *testing.T) {
	ctx := context.Background()
	source, vault, _ := legacyFixture(t)
	src := legacyWarmupSource(t, source)
	base := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		id        int
		window    string
		resetAt   time.Time
		status    string
		attempted time.Time
		completed any
		model     string
	}{
		{1, "primary", base.Add(5 * time.Hour), "pending", base, nil, "gpt-5.4-mini"},
		{2, "secondary", base.Add(7 * 24 * time.Hour), "succeeded", base.Add(time.Minute), base.Add(2 * time.Minute).Format("2006-01-02 15:04:05.000000"), "gpt-5.4-mini"},
		{3, "primary_idle", base.Add(6 * time.Hour), "failed", base.Add(2 * time.Minute), base.Add(3 * time.Minute).Format("2006-01-02 15:04:05.000000"), "gpt-5.4-mini"},
		{4, "monthly", base.Add(30 * 24 * time.Hour), "skipped", base.Add(3 * time.Minute), base.Add(4 * time.Minute).Format("2006-01-02 15:04:05.000000"), "auto"},
	} {
		if _, err := src.ExecContext(ctx, `INSERT INTO account_limit_warmups
 (id,account_id,window,reset_at,status,model,attempted_at,completed_at)
 VALUES(?,'acct-a',?,?,?,?,?,?)`, row.id, row.window, row.resetAt.Unix(), row.status,
			row.model, row.attempted.Format("2006-01-02 15:04:05.000000"), row.completed); err != nil {
			t.Fatal(err)
		}
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "new.db")
	store, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportLegacySnapshot(ctx, source, vault); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, row := range []struct {
		window, status string
		resetAt        time.Time
	}{
		{"primary", "claimed", base.Add(5 * time.Hour)},
		{"secondary", "success", base.Add(7 * 24 * time.Hour)},
		{"primary_idle", "failed", base.Add(6 * time.Hour)},
		{"monthly", "abandoned", base.Add(30 * 24 * time.Hour)},
	} {
		var status string
		if err := store.db.QueryRowContext(ctx, `SELECT status FROM account_limit_warmup_attempts
 WHERE account_id='acct-a' AND window=? AND reset_at=?`, row.window, millis(row.resetAt)).Scan(&status); err != nil || status != row.status {
			t.Fatalf("imported %s status = %q %v", row.window, status, err)
		}
		if _, claimed, err := store.ClaimLimitWarmupAttempt(ctx, "acct-a", row.window, row.resetAt, "m", 0, base.Add(2*time.Hour)); err != nil || claimed {
			t.Fatalf("replayed %s after restart = %v %v", row.window, claimed, err)
		}
	}
	if _, claimed, err := store.ClaimLimitWarmupAttempt(ctx, "acct-a", "primary", base.Add(5*time.Hour+4*time.Second), "m", 0, base.Add(2*time.Hour)); err != nil || claimed {
		t.Fatalf("jitter replay = %v %v", claimed, err)
	}
	if _, claimed, err := store.ClaimLimitWarmupAttempt(ctx, "acct-a", "primary_idle", base.Add(11*time.Hour), "m", time.Hour, base.Add(30*time.Minute)); err != nil || claimed {
		t.Fatalf("imported cooldown lost = %v %v", claimed, err)
	}
	if _, claimed, err := store.ClaimLimitWarmupAttempt(ctx, "acct-a", "primary", base.Add(10*time.Hour), "m", 0, base.Add(2*time.Hour)); err != nil || !claimed {
		t.Fatalf("new cycle blocked = %v %v", claimed, err)
	}
}

func TestImportLegacyWarmupUnknownStatusFailsClosed(t *testing.T) {
	ctx := context.Background()
	source, vault, _ := legacyFixture(t)
	src := legacyWarmupSource(t, source)
	_, err := src.ExecContext(ctx, `INSERT INTO account_limit_warmups
 (id,account_id,window,reset_at,status,model,attempted_at)
 VALUES(1,'acct-a','primary',1800000000,'unknown','m','2026-09-26 12:00:00.000000')`)
	if err != nil {
		t.Fatal(err)
	}
	_ = src.Close()
	store, err := Open(filepath.Join(t.TempDir(), "new.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.ImportLegacySnapshot(ctx, source, vault); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown claim status accepted: %v", err)
	}
	var accounts int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM accounts").Scan(&accounts); err != nil || accounts != 0 {
		t.Fatalf("partial import after unsafe claim: %d %v", accounts, err)
	}
}

func TestImportLegacyManualWarmupModel(t *testing.T) {
	ctx := context.Background()
	source, vault, _ := legacyFixture(t)
	legacy, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, `ALTER TABLE dashboard_settings ADD COLUMN warmup_model TEXT NOT NULL DEFAULT 'gpt-5.4-mini'`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, `UPDATE dashboard_settings SET warmup_model=' gpt-6-sol ' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(t.TempDir(), "new.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.ImportLegacySnapshot(ctx, source, vault); err != nil {
		t.Fatal(err)
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil || settings.WarmupModel != "gpt-6-sol" {
		t.Fatalf("legacy warmup model lost: %+v %v", settings, err)
	}
}
