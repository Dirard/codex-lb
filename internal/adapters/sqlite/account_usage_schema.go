package sqlite

// schemaV8 adds the durable reset-credit redemption ledger. The
// (account_id, redeem_request_id) pair pins the credit selected on the first
// attempt so an idempotent retry retargets the same upstream credit instead of
// burning a second one. Registration is owned by migrations.go.
const schemaV8 = `
CREATE TABLE reset_credit_redeem_requests (
 account_id TEXT NOT NULL,
 redeem_request_id TEXT NOT NULL,
 credit_id TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(account_id,redeem_request_id)
);
CREATE INDEX idx_reset_credit_redeem_requests_created
 ON reset_credit_redeem_requests(created_at);
`
