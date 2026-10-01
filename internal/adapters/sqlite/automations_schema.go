package sqlite

// schemaV10 stores automation jobs and durable per-account run claims. Slot
// uniqueness plus status-guarded claims make scheduling restart-safe: a run
// row exists exactly once per (job, slot, account) and only one process can
// move it pending->running (or reclaim a stale running row).
const schemaV10 = `
CREATE TABLE automation_jobs (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 enabled INTEGER NOT NULL,
 include_paused_accounts INTEGER NOT NULL DEFAULT 0,
 account_scope_all INTEGER NOT NULL DEFAULT 1,
 schedule_type TEXT NOT NULL DEFAULT 'daily',
 schedule_time TEXT NOT NULL,
 schedule_timezone TEXT NOT NULL,
 schedule_threshold_minutes INTEGER NOT NULL DEFAULT 0,
 schedule_days TEXT NOT NULL,
 model TEXT NOT NULL,
 reasoning_effort TEXT,
 prompt TEXT NOT NULL DEFAULT 'ping',
 account_ids TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE TABLE automation_runs (
 id TEXT PRIMARY KEY,
 job_id TEXT NOT NULL,
 cycle_key TEXT NOT NULL,
 slot_key TEXT NOT NULL UNIQUE,
 trigger_kind TEXT NOT NULL CHECK(trigger_kind IN ('scheduled','manual')),
 status TEXT NOT NULL CHECK(status IN ('pending','running','success','failed')),
 model TEXT NOT NULL,
 reasoning_effort TEXT,
 scheduled_for INTEGER NOT NULL,
 started_at INTEGER,
 finished_at INTEGER,
 account_id TEXT,
 attempt_count INTEGER NOT NULL DEFAULT 0,
 error_code TEXT,
 error_message TEXT
);
CREATE INDEX idx_automation_runs_job ON automation_runs(job_id, scheduled_for);
CREATE INDEX idx_automation_runs_status_due ON automation_runs(status, scheduled_for);
CREATE INDEX idx_automation_runs_cycle ON automation_runs(cycle_key);
`
