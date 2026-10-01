package sqlite

const schemaV7 = `
ALTER TABLE usage_events ADD COLUMN conversation_id TEXT;
ALTER TABLE usage_events ADD COLUMN useragent TEXT;
ALTER TABLE usage_events ADD COLUMN useragent_group TEXT;
ALTER TABLE usage_events ADD COLUMN client_ip TEXT;
ALTER TABLE usage_events ADD COLUMN reasoning_effort TEXT;
ALTER TABLE usage_events ADD COLUMN plan_type TEXT;
ALTER TABLE usage_events ADD COLUMN source TEXT;
ALTER TABLE usage_events ADD COLUMN transport TEXT;
ALTER TABLE usage_events ADD COLUMN latency_first_token_ms INTEGER;
ALTER TABLE usage_events ADD COLUMN reasoning_tokens_known INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runtime_settings ADD COLUMN request_log_retention_days INTEGER;
ALTER TABLE legacy_hourly_usage ADD COLUMN reasoning_known_count INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_usage_events_time ON usage_events(requested_at);
CREATE INDEX idx_usage_events_conversation_time ON usage_events(conversation_id,requested_at);
`
