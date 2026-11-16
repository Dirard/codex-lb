package sqlite

// schemaV51 links forks to their parent key and records who wrote them.
// Additive: older code ignores the columns, and it cannot authenticate a fork
// because fork secrets are hashed in their own domain (domain.ForkKeyHash).
const schemaV51 = `
ALTER TABLE api_keys ADD COLUMN parent_key_id TEXT REFERENCES api_keys(id);
ALTER TABLE api_keys ADD COLUMN fork_created_by TEXT CHECK(fork_created_by IS NULL OR fork_created_by IN ('owner','admin'));
ALTER TABLE api_keys ADD COLUMN fork_updated_by TEXT CHECK(fork_updated_by IS NULL OR fork_updated_by IN ('owner','admin'));
ALTER TABLE api_keys ADD COLUMN fork_updated_at INTEGER;
CREATE INDEX idx_api_keys_parent ON api_keys(parent_key_id) WHERE parent_key_id IS NOT NULL;
`
