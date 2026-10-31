package sqlite

// schemaV44 adds the separate video pool. Additive: older code never reads
// these tables, so it keeps working on the result and rollback is safe.
const schemaV44 = `
CREATE TABLE video_accounts (
 id TEXT PRIMARY KEY, xai_user_id TEXT NOT NULL UNIQUE, email TEXT NOT NULL DEFAULT '', label TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL CHECK(status IN ('active','paused','reauth_required','access_denied')),
 access_token_encrypted BLOB NOT NULL, refresh_token_encrypted BLOB NOT NULL, oidc_client_id TEXT NOT NULL,
 access_expires_at INTEGER NOT NULL, token_version INTEGER NOT NULL DEFAULT 0,
 refresh_claimed_until INTEGER NOT NULL DEFAULT 0, cooldown_until INTEGER NOT NULL DEFAULT 0,
 last_refusal_code TEXT NOT NULL DEFAULT '', last_used_at INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE video_allowed_keys (
 api_key_id TEXT PRIMARY KEY REFERENCES api_keys(id) ON DELETE CASCADE, created_at INTEGER NOT NULL
);
CREATE TABLE video_jobs (
 id TEXT PRIMARY KEY, api_key_id TEXT NOT NULL, account_id TEXT NOT NULL, upstream_request_id TEXT UNIQUE,
 model TEXT NOT NULL, mode TEXT NOT NULL, requested_seconds INTEGER NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('dispatching','pending','done','failed','expired','refused','rejected','unknown')),
 video_url TEXT NOT NULL DEFAULT '', duration_seconds REAL, cost_microdollars INTEGER, error_code TEXT NOT NULL DEFAULT '',
 next_poll_at INTEGER, created_at INTEGER NOT NULL, finished_at INTEGER
);
CREATE INDEX idx_video_jobs_key_created ON video_jobs(api_key_id,created_at DESC,id DESC);
CREATE INDEX idx_video_jobs_created ON video_jobs(created_at DESC,id DESC);
CREATE INDEX idx_video_jobs_unfinished ON video_jobs(account_id) WHERE status IN ('dispatching','pending');
CREATE INDEX idx_video_jobs_due ON video_jobs(next_poll_at) WHERE status='pending';
CREATE INDEX idx_video_jobs_finished ON video_jobs(finished_at) WHERE finished_at IS NOT NULL;
`

// schemaV45 keeps the last read of each video account's subscription pool.
// Additive: older code selects its own columns and ignores these.
const schemaV45 = `
ALTER TABLE video_accounts ADD COLUMN usage_percent REAL;
ALTER TABLE video_accounts ADD COLUMN usage_period_end INTEGER NOT NULL DEFAULT 0;
ALTER TABLE video_accounts ADD COLUMN usage_checked_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE video_accounts ADD COLUMN usage_error TEXT NOT NULL DEFAULT '';
`
