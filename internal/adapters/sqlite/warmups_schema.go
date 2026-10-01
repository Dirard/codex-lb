package sqlite

// schemaV16 adds configurable limit-warmup settings columns and the durable
// attempt ledger. Registration order is owned by store.go (root).
const schemaV16 = `
ALTER TABLE runtime_settings ADD COLUMN limit_warmup_windows TEXT NOT NULL DEFAULT 'both';
ALTER TABLE runtime_settings ADD COLUMN limit_warmup_prompt TEXT NOT NULL DEFAULT 'Say OK.';
ALTER TABLE runtime_settings ADD COLUMN limit_warmup_cooldown_seconds INTEGER NOT NULL DEFAULT 3600;
ALTER TABLE runtime_settings ADD COLUMN limit_warmup_idle_threshold_percent REAL NOT NULL DEFAULT 1.0;
CREATE TABLE account_limit_warmup_attempts (
 account_id TEXT NOT NULL,
 attempt INTEGER NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('claimed','success','failed','abandoned')),
 model TEXT NOT NULL DEFAULT '',
 attempted_at INTEGER NOT NULL,
 completed_at INTEGER,
 error_code TEXT,
 PRIMARY KEY(account_id,attempt)
);
CREATE INDEX idx_limit_warmup_attempts_latest
 ON account_limit_warmup_attempts(account_id,attempt DESC);
`

// schemaV20 preserves the two optional warm-up modes and makes every paid
// attempt unique per account/window/reset tuple. Register after schemaV19.
const schemaV20 = `
ALTER TABLE runtime_settings ADD COLUMN limit_warmup_exhausted_threshold_percent REAL NOT NULL DEFAULT 99.0;
ALTER TABLE runtime_settings ADD COLUMN limit_warmup_min_available_percent REAL NOT NULL DEFAULT 100.0;
ALTER TABLE runtime_settings ADD COLUMN limit_warmup_staggered_idle_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE account_limit_warmup_attempts ADD COLUMN window TEXT NOT NULL DEFAULT '';
ALTER TABLE account_limit_warmup_attempts ADD COLUMN reset_at INTEGER NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX idx_limit_warmup_attempt_tuple
 ON account_limit_warmup_attempts(account_id,window,reset_at) WHERE window<>'';
`
