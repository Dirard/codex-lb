package sqlite

// A deleted identity remains an accounting tombstone. Reimport increments its
// generation so old asynchronous work cannot acquire the new identity's rights.
const schemaV31 = `
ALTER TABLE accounts ADD COLUMN generation INTEGER NOT NULL DEFAULT 0 CHECK(generation>=0);
ALTER TABLE usage_reservations ADD COLUMN account_generation INTEGER NOT NULL DEFAULT 0 CHECK(account_generation>=0);
ALTER TABLE usage_events ADD COLUMN account_generation INTEGER NOT NULL DEFAULT 0 CHECK(account_generation>=0);
ALTER TABLE reset_credit_redeem_requests ADD COLUMN account_generation INTEGER NOT NULL DEFAULT 0 CHECK(account_generation>=0);
ALTER TABLE automation_runs ADD COLUMN account_generation INTEGER NOT NULL DEFAULT 0 CHECK(account_generation>=0);
CREATE TABLE account_deletions (
 account_id TEXT NOT NULL REFERENCES accounts(id),
 generation INTEGER NOT NULL CHECK(generation>=0),
 delete_history INTEGER NOT NULL CHECK(delete_history IN (0,1)),
 cleanup_done INTEGER NOT NULL DEFAULT 0 CHECK(cleanup_done IN (0,1)),
 PRIMARY KEY(account_id,generation)
);
CREATE INDEX idx_account_deletions_pending ON account_deletions(cleanup_done,account_id,generation);
`
