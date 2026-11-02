package sqlite

// schemaV49 keeps the client secrets browser apps use for Realtime. Additive:
// older code never reads the table.
const schemaV49 = `
CREATE TABLE realtime_client_secrets (
 secret_hash TEXT PRIMARY KEY CHECK(length(secret_hash) = 64),
 api_key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
 session_encrypted BLOB,
 expires_at INTEGER NOT NULL,
 created_at INTEGER NOT NULL
);
CREATE INDEX idx_realtime_client_secrets_expiry ON realtime_client_secrets(expires_at);
`
