package sqlite

const schemaV27 = `
ALTER TABLE runtime_settings ADD COLUMN proxy_account_response_create_limit INTEGER CHECK(proxy_account_response_create_limit >= 0);
ALTER TABLE runtime_settings ADD COLUMN proxy_account_stream_limit INTEGER CHECK(proxy_account_stream_limit >= 0);
ALTER TABLE runtime_settings ADD COLUMN proxy_account_stream_recovery_reserve INTEGER CHECK(proxy_account_stream_recovery_reserve >= 0);
ALTER TABLE runtime_settings ADD COLUMN proxy_api_key_fair_share_congestion_threshold_pct INTEGER CHECK(proxy_api_key_fair_share_congestion_threshold_pct BETWEEN 0 AND 100);
`
