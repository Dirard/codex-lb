package sqlite

const schemaV22 = `
CREATE TABLE account_usage_credits (
 account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 credits_has INTEGER, credits_unlimited INTEGER, credits_balance REAL,
 observed_at INTEGER NOT NULL
);
CREATE TABLE account_quota_block_evidence (
 account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 blocked_at INTEGER NOT NULL
);
CREATE TABLE account_additional_quota_sync (
 account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 observed_at INTEGER NOT NULL
);
CREATE TABLE account_additional_quotas (
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 quota_key TEXT NOT NULL, window TEXT NOT NULL,
 limit_name TEXT NOT NULL, metered_feature TEXT NOT NULL,
 used_percent REAL NOT NULL, reset_at INTEGER, window_minutes INTEGER,
 observed_at INTEGER NOT NULL,
 PRIMARY KEY(account_id,quota_key,window)
);
CREATE INDEX idx_account_additional_quotas_key_time
 ON account_additional_quotas(quota_key,observed_at);
ALTER TABLE runtime_settings ADD COLUMN additional_quota_routing_policies TEXT NOT NULL DEFAULT '{}';
`

const schemaV23 = `
CREATE TABLE account_usage_observation (
 account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 fetch_started_at INTEGER NOT NULL,
 pending_free_from_plan TEXT NOT NULL DEFAULT '',
 pending_free_count INTEGER NOT NULL DEFAULT 0
);
`
