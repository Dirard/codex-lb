package sqlite

const schemaV11 = `
CREATE TABLE codex_resource_owners (
 resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
 key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE RESTRICT,
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
 created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL,
 PRIMARY KEY(resource_type,resource_id)
);
CREATE INDEX idx_codex_resource_owners_expiry ON codex_resource_owners(expires_at);
CREATE INDEX idx_codex_resource_owners_key ON codex_resource_owners(key_id);
`
