package sqlite

const schemaV1 = `
CREATE TABLE accounts (
 id TEXT PRIMARY KEY, kind TEXT NOT NULL CHECK(kind IN ('chatgpt','external')),
 provider TEXT NOT NULL, base_url TEXT NOT NULL DEFAULT '',
 chatgpt_account_id TEXT NOT NULL DEFAULT '', chatgpt_user_id TEXT NOT NULL DEFAULT '',
 codex_installation_id TEXT NOT NULL DEFAULT '', email TEXT NOT NULL, alias TEXT NOT NULL DEFAULT '',
 workspace_id TEXT NOT NULL DEFAULT '', workspace_label TEXT NOT NULL DEFAULT '',
 seat_type TEXT NOT NULL DEFAULT '', plan_type TEXT NOT NULL DEFAULT '',
 routing_policy TEXT NOT NULL DEFAULT 'normal',
 status TEXT NOT NULL CHECK(status IN ('active','rate_limited','quota_exceeded','paused','reauth_required','deactivated')),
 deactivation_reason TEXT NOT NULL DEFAULT '', requires_egress_decision INTEGER NOT NULL DEFAULT 0,
 security_work_authorized INTEGER NOT NULL DEFAULT 0, limit_warmup_enabled INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL, last_refresh INTEGER
);
CREATE TABLE account_credentials (
 account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 access_token_encrypted BLOB, refresh_token_encrypted BLOB,
 id_token_encrypted BLOB, external_key_encrypted BLOB
);
CREATE TABLE account_quotas (
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 window TEXT NOT NULL, used_percent REAL NOT NULL,
 reset_at INTEGER, window_minutes INTEGER, observed_at INTEGER NOT NULL,
 PRIMARY KEY(account_id,window)
);
CREATE TABLE account_groups (
 id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL
);
CREATE TABLE account_group_accounts (
 group_id TEXT NOT NULL REFERENCES account_groups(id) ON DELETE CASCADE,
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
 PRIMARY KEY(group_id,account_id)
);
CREATE INDEX idx_account_group_accounts_account ON account_group_accounts(account_id);
CREATE TABLE group_limits (
 group_id TEXT NOT NULL REFERENCES account_groups(id) ON DELETE CASCADE,
 limit_type TEXT NOT NULL, limit_window TEXT NOT NULL, model_filter TEXT NOT NULL DEFAULT '',
 max_value INTEGER NOT NULL CHECK(max_value > 0),
 PRIMARY KEY(group_id,limit_type,limit_window,model_filter)
);
CREATE TABLE api_keys (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, key_hash TEXT NOT NULL UNIQUE,
 key_prefix TEXT NOT NULL, group_id TEXT REFERENCES account_groups(id) ON DELETE RESTRICT,
 allowed_models TEXT, apply_to_codex_model INTEGER NOT NULL DEFAULT 0,
 enforced_model TEXT, allowed_reasoning_efforts TEXT, enforced_reasoning_effort TEXT,
 enforced_service_tier TEXT, traffic_class TEXT NOT NULL DEFAULT 'foreground',
 transport_policy_override TEXT, usage_sections TEXT NOT NULL DEFAULT 'upstream_limits,account_pool_usage',
 account_assignment_scope_enabled INTEGER NOT NULL DEFAULT 0,
 source_assignment_scope_enabled INTEGER NOT NULL DEFAULT 0,
 expires_at INTEGER, is_active INTEGER NOT NULL DEFAULT 1, deleted_at INTEGER,
 created_at INTEGER NOT NULL, last_used_at INTEGER,
 CHECK(allowed_reasoning_efforts IS NULL OR enforced_reasoning_effort IS NULL)
);
CREATE INDEX idx_api_keys_group ON api_keys(group_id);
CREATE TABLE api_key_accounts (
 api_key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
 PRIMARY KEY(api_key_id,account_id)
);
CREATE TABLE api_key_sources (
 api_key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
 PRIMARY KEY(api_key_id,account_id)
);
CREATE TABLE api_key_limits (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 api_key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE RESTRICT,
 limit_type TEXT NOT NULL, limit_window TEXT NOT NULL, model_filter TEXT NOT NULL DEFAULT '',
 max_value INTEGER NOT NULL CHECK(max_value > 0), current_value INTEGER NOT NULL DEFAULT 0 CHECK(current_value >= 0),
 reset_at INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1,
 UNIQUE(api_key_id,limit_type,limit_window,model_filter)
);
CREATE TABLE usage_reservations (
 id TEXT PRIMARY KEY, api_key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE RESTRICT,
 account_id TEXT,
 model TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('reserved','finalized','failed','released')),
 input_tokens INTEGER, output_tokens INTEGER, cached_input_tokens INTEGER, cost_microdollars INTEGER,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX idx_usage_reservations_status_age ON usage_reservations(status,updated_at);
CREATE TABLE usage_reservation_items (
 reservation_id TEXT NOT NULL REFERENCES usage_reservations(id) ON DELETE CASCADE,
 limit_id INTEGER NOT NULL REFERENCES api_key_limits(id) ON DELETE RESTRICT,
 limit_type TEXT NOT NULL, reserved_delta INTEGER NOT NULL, actual_delta INTEGER,
 expected_reset_at INTEGER NOT NULL,
 PRIMARY KEY(reservation_id,limit_id)
);
CREATE TABLE usage_events (
 request_id TEXT PRIMARY KEY, reservation_id TEXT UNIQUE REFERENCES usage_reservations(id),
 api_key_id TEXT, account_id TEXT, model_source_id TEXT, model TEXT NOT NULL,
 service_tier TEXT NOT NULL DEFAULT '', request_kind TEXT NOT NULL DEFAULT 'normal',
 status TEXT NOT NULL, error_code TEXT NOT NULL DEFAULT '', requested_at INTEGER NOT NULL,
 queue_latency_ms INTEGER NOT NULL DEFAULT 0, connect_latency_ms INTEGER NOT NULL DEFAULT 0,
 first_event_ms INTEGER NOT NULL DEFAULT 0, total_latency_ms INTEGER NOT NULL DEFAULT 0,
 input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
 cached_input_tokens INTEGER NOT NULL DEFAULT 0, reasoning_tokens INTEGER NOT NULL DEFAULT 0,
 cost_microdollars INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_usage_events_key_time ON usage_events(api_key_id,requested_at);
CREATE INDEX idx_usage_events_account_time ON usage_events(account_id,requested_at);
CREATE TABLE usage_totals (
 scope TEXT NOT NULL, scope_id TEXT NOT NULL, request_count INTEGER NOT NULL DEFAULT 0,
 failed_count INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0,
 output_tokens INTEGER NOT NULL DEFAULT 0, cached_input_tokens INTEGER NOT NULL DEFAULT 0,
 reasoning_tokens INTEGER NOT NULL DEFAULT 0, cost_microdollars INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(scope,scope_id)
);
CREATE TABLE runtime_settings (
 id INTEGER PRIMARY KEY CHECK(id=1), api_key_auth_enabled INTEGER NOT NULL DEFAULT 0,
 hide_upstream_quota_from_keys INTEGER NOT NULL DEFAULT 0,
 sticky_threads_enabled INTEGER NOT NULL DEFAULT 1,
 totp_required_on_login INTEGER NOT NULL DEFAULT 0, prohibit_fast_mode INTEGER NOT NULL DEFAULT 0,
 prefer_earlier_reset_accounts INTEGER NOT NULL DEFAULT 1,
 prefer_earlier_reset_window TEXT NOT NULL DEFAULT 'secondary',
 import_without_overwrite INTEGER NOT NULL DEFAULT 1,
 routing_strategy TEXT NOT NULL DEFAULT 'capacity_weighted', single_account_id TEXT NOT NULL DEFAULT '',
 upstream_stream_transport TEXT NOT NULL DEFAULT 'default',
 http_transport_policy TEXT NOT NULL DEFAULT 'smart', dashboard_session_ttl INTEGER NOT NULL DEFAULT 86400,
 limit_warmup_enabled INTEGER NOT NULL DEFAULT 0, limit_warmup_model TEXT NOT NULL DEFAULT 'auto',
 version INTEGER NOT NULL DEFAULT 1
);
INSERT INTO runtime_settings(id) VALUES (1);
CREATE TABLE admin_secret (
 id INTEGER PRIMARY KEY CHECK(id=1), password_hash TEXT NOT NULL,
 totp_secret_encrypted BLOB, totp_last_verified_step INTEGER
);
`

const schemaV2 = `
CREATE TABLE continuations (
 response_id TEXT NOT NULL, key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE RESTRICT,
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
 provider_id TEXT NOT NULL, model TEXT NOT NULL, created_at INTEGER NOT NULL,
 expires_at INTEGER NOT NULL, context_encrypted BLOB, file_pinned INTEGER NOT NULL DEFAULT 0,
 quota_refused INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(response_id,key_id)
);
CREATE INDEX idx_continuations_expiry ON continuations(expires_at);
CREATE INDEX idx_continuations_oldest ON continuations(created_at,response_id,key_id);
`

const schemaV3 = `
CREATE TABLE account_outcomes (
 account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE RESTRICT,
 reservation_rowid INTEGER NOT NULL
);
`

const schemaV4 = `
ALTER TABLE usage_events ADD COLUMN legacy_request_id TEXT;
ALTER TABLE usage_events ADD COLUMN legacy_deleted INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_reservations ADD COLUMN needs_reconciliation INTEGER NOT NULL DEFAULT 0;
CREATE TABLE legacy_import_state (
 id INTEGER PRIMARY KEY CHECK(id=1), source_sha256 TEXT NOT NULL,
 key_fingerprint TEXT NOT NULL, folded_through INTEGER NOT NULL,
 hourly_folded_through INTEGER NOT NULL, imported_at INTEGER NOT NULL,
 sidecar_path TEXT NOT NULL
);
CREATE TABLE legacy_model_sources (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, kind TEXT NOT NULL, base_url TEXT NOT NULL,
 is_enabled INTEGER NOT NULL, health_status TEXT NOT NULL,
 supports_chat_completions INTEGER NOT NULL, supports_responses INTEGER NOT NULL,
 supports_audio_transcriptions INTEGER NOT NULL, supports_embeddings INTEGER NOT NULL,
 timeout_seconds INTEGER, max_concurrency INTEGER, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE legacy_model_source_models (
 id INTEGER PRIMARY KEY, source_id TEXT NOT NULL REFERENCES legacy_model_sources(id),
 model TEXT NOT NULL, display_name TEXT, context_window INTEGER, max_output_tokens INTEGER,
 supports_streaming INTEGER NOT NULL, supports_tools INTEGER NOT NULL, supports_vision INTEGER NOT NULL,
 input_per_1m REAL, cached_input_per_1m REAL, output_per_1m REAL, audio_per_minute REAL,
 raw_metadata_json TEXT, is_enabled INTEGER NOT NULL,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE legacy_hourly_usage (
 bucket_epoch INTEGER NOT NULL, account_id TEXT NOT NULL, api_key_id TEXT NOT NULL,
 model TEXT NOT NULL, service_tier TEXT NOT NULL, request_kind TEXT NOT NULL,
 is_deleted INTEGER NOT NULL, request_count INTEGER NOT NULL, error_count INTEGER NOT NULL,
 cancelled_count INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL,
 reasoning_tokens INTEGER NOT NULL, output_or_reasoning_tokens INTEGER NOT NULL,
 cached_input_tokens INTEGER NOT NULL, cached_input_tokens_clamped INTEGER NOT NULL,
 cost_usd REAL NOT NULL, cost_count INTEGER NOT NULL,
 PRIMARY KEY(bucket_epoch,account_id,api_key_id,model,service_tier,request_kind,is_deleted)
);
CREATE INDEX idx_legacy_hourly_key_account ON legacy_hourly_usage(api_key_id,account_id,bucket_epoch);
`

const schemaV5 = `
ALTER TABLE legacy_model_sources ADD COLUMN provider_config_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE legacy_model_sources ADD COLUMN deleted_at INTEGER;
ALTER TABLE legacy_model_source_models ADD COLUMN aliases_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE legacy_model_source_models ADD COLUMN upstream_model TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_legacy_source_model_name ON legacy_model_source_models(source_id,model);
CREATE TABLE legacy_hourly_errors (
 bucket_epoch INTEGER NOT NULL, account_id TEXT NOT NULL, error_code TEXT NOT NULL,
 error_count INTEGER NOT NULL, PRIMARY KEY(bucket_epoch,account_id,error_code)
);
CREATE TABLE legacy_conversation_hourly (
 bucket_epoch INTEGER NOT NULL, conversation_id TEXT NOT NULL, account_id TEXT NOT NULL,
 is_deleted INTEGER NOT NULL, request_count INTEGER NOT NULL,
 PRIMARY KEY(bucket_epoch,conversation_id,account_id,is_deleted)
);
`

const schemaV6 = `
CREATE TABLE runtime_identity (
 id INTEGER PRIMARY KEY CHECK(id=1), key_fingerprint TEXT NOT NULL,
 encryption_probe BLOB NOT NULL
);
`

const schemaV9 = `ALTER TABLE continuations ADD COLUMN reservation_rowid INTEGER NOT NULL DEFAULT 0;`

const schemaV25 = `
ALTER TABLE runtime_settings ADD COLUMN show_reset_credit_badges INTEGER NOT NULL DEFAULT 1 CHECK(show_reset_credit_badges IN (0,1));
ALTER TABLE runtime_settings ADD COLUMN show_reset_credit_expiry_badge INTEGER NOT NULL DEFAULT 1 CHECK(show_reset_credit_expiry_badge IN (0,1));
`

const schemaV26 = `ALTER TABLE runtime_settings ADD COLUMN warmup_model TEXT NOT NULL DEFAULT 'gpt-5.4-mini';`

const schemaV33 = `ALTER TABLE runtime_settings ADD COLUMN codex_client_version TEXT NOT NULL DEFAULT '0.156.0' CHECK(length(codex_client_version) BETWEEN 1 AND 64);`
