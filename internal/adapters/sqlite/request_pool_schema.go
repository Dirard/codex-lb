package sqlite

// schemaV50 stores a key's request pool: requests per minute and the maximum
// wait in seconds, both NULL for a key without a pool. Additive: older code
// neither reads nor writes the columns, its key upsert leaves them as they
// are, and adding nullable columns rewrites no table.
const schemaV50 = `
ALTER TABLE api_keys ADD COLUMN request_pool_rpm INTEGER CHECK(request_pool_rpm IS NULL OR request_pool_rpm BETWEEN 1 AND 10000);
ALTER TABLE api_keys ADD COLUMN request_pool_max_wait_seconds INTEGER CHECK(request_pool_max_wait_seconds IS NULL OR request_pool_max_wait_seconds BETWEEN 0 AND 60);
`
