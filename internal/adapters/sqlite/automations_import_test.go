package sqlite

import (
	"codex-lb/internal/application"
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestImportLegacyAutomationsPreservesSchedulesAndHistory(t *testing.T) {
	ctx := context.Background()
	legacyPath := filepath.Join(t.TempDir(), "legacy.sqlite")
	legacy, err := sql.Open("sqlite", legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	schema := `
CREATE TABLE automation_jobs (id TEXT PRIMARY KEY, name TEXT, enabled BOOLEAN, include_paused_accounts BOOLEAN,
 account_scope_all BOOLEAN, schedule_type TEXT, schedule_time TEXT, schedule_timezone TEXT,
 schedule_threshold_minutes INTEGER, schedule_days TEXT, model TEXT, reasoning_effort TEXT, prompt TEXT,
 created_at DATETIME, updated_at DATETIME);
CREATE TABLE automation_job_accounts (job_id TEXT, account_id TEXT, position INTEGER, created_at DATETIME,
 PRIMARY KEY(job_id,account_id));
CREATE TABLE automation_runs (id TEXT PRIMARY KEY, job_id TEXT, trigger TEXT, slot_key TEXT UNIQUE,
 cycle_key TEXT, cycle_expected_accounts INTEGER, cycle_window_end DATETIME, model TEXT, reasoning_effort TEXT,
 prompt TEXT, scheduled_for DATETIME, started_at DATETIME, finished_at DATETIME, status TEXT,
 account_id TEXT, error_code TEXT, error_message TEXT, attempt_count INTEGER, created_at DATETIME);
`
	if _, err := legacy.Exec(schema); err != nil {
		t.Fatal(err)
	}
	createdAt := "2026-01-01 00:00:00.000000"
	if _, err := legacy.Exec(`INSERT INTO automation_jobs VALUES
 ('auto_1','Nightly',1,0,0,'daily','03:00','Europe/Moscow',30,'mon,wed','gpt-5.4-mini','low','ping',?,?)`,
		createdAt, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO automation_job_accounts VALUES ('auto_1','acct_a',0,?),
 ('auto_1','acct_b',1,?)`, createdAt, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO automation_runs VALUES
 ('run_ok','auto_1','scheduled','slot_ok','cycle_1',NULL,NULL,'gpt-5.4-mini',NULL,NULL,?,?,?,?, 'acct_a',NULL,NULL,1,?)`,
		createdAt, createdAt, createdAt, "success", createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO automation_runs VALUES
 ('run_partial','auto_1','scheduled','slot_partial','cycle_1',NULL,NULL,'gpt-5.4-mini',NULL,NULL,?,?,?,?,NULL,NULL,NULL,0,?)`,
		createdAt, createdAt, createdAt, "partial", createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO automation_runs VALUES
 ('run_stuck','auto_1','scheduled','slot_stuck','cycle_1',NULL,NULL,'gpt-5.4-mini',NULL,NULL,?,?,NULL,'running','acct_b',NULL,NULL,0,?)`,
		createdAt, createdAt, createdAt); err != nil {
		t.Fatal(err)
	}

	store := automationStore(t)
	srcTx, err := legacy.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srcTx.Rollback()
	dstTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dstTx.Rollback()
	if err := importLegacyAutomations(ctx, srcTx, dstTx, nil); err != nil {
		t.Fatal(err)
	}
	if err := dstTx.Commit(); err != nil {
		t.Fatal(err)
	}
	job, err := store.GetAutomationJob(ctx, "auto_1")
	if err != nil || job.Name != "Nightly" || job.AccountScopeAll ||
		len(job.AccountIDs) != 2 || job.AccountIDs[0] != "acct_a" || job.Schedule.Timezone != "Europe/Moscow" ||
		job.Schedule.ThresholdMinutes != 30 {
		t.Fatalf("imported job = %+v err %v", job, err)
	}
	runs, total, err := store.ListAutomationRuns(ctx, application.AutomationRunFilter{})
	if err != nil || total != 3 {
		t.Fatalf("runs total = %d err %v", total, err)
	}
	byID := map[string]domainRun{}
	for _, run := range runs {
		byID[run.ID] = domainRun{run.Status, run.ErrorCode}
	}
	if byID["run_ok"].status != "success" || byID["run_partial"].status != "failed" ||
		byID["run_stuck"].status != "failed" || byID["run_stuck"].errorCode == nil || *byID["run_stuck"].errorCode != "stale_run_abandoned" {
		t.Fatalf("imported runs = %+v", byID)
	}
}

type domainRun struct {
	status    string
	errorCode *string
}
