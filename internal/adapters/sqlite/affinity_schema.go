package sqlite

const schemaV29 = `
ALTER TABLE runtime_settings ADD COLUMN openai_cache_affinity_max_age_seconds INTEGER NOT NULL DEFAULT 1800;
ALTER TABLE runtime_settings ADD COLUMN sticky_reallocation_primary_budget_threshold_pct REAL NOT NULL DEFAULT 95;
ALTER TABLE runtime_settings ADD COLUMN sticky_reallocation_secondary_budget_threshold_pct REAL NOT NULL DEFAULT 100;
CREATE TABLE affinity_bindings (
 key TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('prompt_cache','sticky_thread','codex_session')),
 api_key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 version INTEGER NOT NULL DEFAULT 1 CHECK(version>0),
 PRIMARY KEY(kind,key)
);
CREATE INDEX idx_affinity_updated ON affinity_bindings(kind,updated_at);
CREATE INDEX idx_affinity_account ON affinity_bindings(account_id);
CREATE TRIGGER remove_unusable_account_affinity AFTER UPDATE OF status ON accounts
 WHEN NEW.status IN ('reauth_required','deactivated') BEGIN
 DELETE FROM affinity_bindings WHERE account_id=NEW.id;
END;
`
