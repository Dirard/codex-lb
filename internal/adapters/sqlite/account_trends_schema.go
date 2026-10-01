package sqlite

const schemaV15 = `
ALTER TABLE runtime_settings ADD COLUMN usage_history_retention_days INTEGER;
CREATE TABLE account_quota_history (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 legacy_id INTEGER UNIQUE,
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 window TEXT NOT NULL,
 observed_at INTEGER NOT NULL,
 used_percent REAL NOT NULL,
 reset_at INTEGER,
 window_minutes INTEGER
);
CREATE INDEX idx_account_quota_history_identity_time
 ON account_quota_history(account_id,window,observed_at DESC,id DESC);
CREATE INDEX idx_account_quota_history_observed_at
 ON account_quota_history(observed_at);
`
