package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"testing"

	"codex-lb/internal/adapters/credentials"
	"golang.org/x/crypto/bcrypt"
)

const legacyFixtureSchema = `
CREATE TABLE runtime_sentinels(name TEXT,value TEXT);
CREATE TABLE account_usage_rollup_state(id INTEGER,folded_through TEXT,hourly_folded_through TEXT);
CREATE TABLE accounts(id TEXT,chatgpt_account_id TEXT,chatgpt_user_id TEXT,codex_installation_id TEXT,
 email TEXT,alias TEXT,workspace_id TEXT,workspace_label TEXT,seat_type TEXT,plan_type TEXT,
 routing_policy TEXT,access_token_encrypted BLOB,refresh_token_encrypted BLOB,id_token_encrypted BLOB,
 last_refresh TEXT,created_at TEXT,status TEXT,deactivation_reason TEXT,security_work_authorized INTEGER,
 limit_warmup_enabled INTEGER,delete_requested_at TEXT);
CREATE TABLE account_proxy_bindings(account_id TEXT,is_active INTEGER);
CREATE TABLE model_sources(id TEXT,name TEXT,kind TEXT,base_url TEXT,api_key_encrypted BLOB,
 is_enabled INTEGER,health_status TEXT,supports_chat_completions INTEGER,supports_responses INTEGER,
 supports_audio_transcriptions INTEGER,supports_embeddings INTEGER,timeout_seconds INTEGER,
 max_concurrency INTEGER,created_at TEXT,updated_at TEXT);
CREATE TABLE model_source_models(id INTEGER,source_id TEXT,model TEXT,display_name TEXT,
 context_window INTEGER,max_output_tokens INTEGER,supports_streaming INTEGER,supports_tools INTEGER,
 supports_vision INTEGER,input_per_1m REAL,cached_input_per_1m REAL,output_per_1m REAL,
 audio_per_minute REAL,raw_metadata_json TEXT,is_enabled INTEGER,created_at TEXT,updated_at TEXT);
CREATE TABLE dashboard_settings(id INTEGER,sticky_threads_enabled INTEGER,prohibit_fast_mode INTEGER,
 prefer_earlier_reset_accounts INTEGER,prefer_earlier_reset_window TEXT,import_without_overwrite INTEGER,
 totp_required_on_login INTEGER,api_key_auth_enabled INTEGER,hide_upstream_quota_from_api_keys INTEGER,
 limit_warmup_enabled INTEGER,routing_strategy TEXT,single_account_id TEXT,upstream_stream_transport TEXT,
 http_downstream_transport_policy TEXT,limit_warmup_model TEXT,dashboard_session_ttl_seconds INTEGER,
 password_hash TEXT,totp_secret_encrypted BLOB,totp_last_verified_step INTEGER,
 upstream_proxy_routing_enabled INTEGER);
CREATE TABLE account_groups(id TEXT,name TEXT,created_at TEXT);
CREATE TABLE account_group_accounts(group_id TEXT,account_id TEXT);
CREATE TABLE account_group_limits(id INTEGER,group_id TEXT,limit_type TEXT,limit_window TEXT,
 max_value INTEGER,model_filter TEXT);
CREATE TABLE api_keys(id TEXT,name TEXT,key_hash TEXT,key_prefix TEXT,group_id TEXT,
 allowed_models TEXT,apply_to_codex_model INTEGER,enforced_model TEXT,allowed_reasoning_efforts TEXT,
 enforced_reasoning_effort TEXT,enforced_service_tier TEXT,traffic_class TEXT,
 transport_policy_override TEXT,usage_sections TEXT,account_assignment_scope_enabled INTEGER,
 source_assignment_scope_enabled INTEGER,expires_at TEXT,is_active INTEGER,created_at TEXT,last_used_at TEXT);
CREATE TABLE api_key_accounts(api_key_id TEXT,account_id TEXT);
CREATE TABLE api_key_model_sources(api_key_id TEXT,source_id TEXT);
CREATE TABLE api_key_limits(id INTEGER,api_key_id TEXT,limit_type TEXT,limit_window TEXT,max_value INTEGER,
 current_value INTEGER,model_filter TEXT,reset_at TEXT);
CREATE TABLE api_key_usage_reservations(id TEXT,api_key_id TEXT,model TEXT,status TEXT,
 input_tokens INTEGER,output_tokens INTEGER,cached_input_tokens INTEGER,cost_microdollars INTEGER,
 created_at TEXT,updated_at TEXT);
CREATE TABLE api_key_usage_reservation_items(id INTEGER,reservation_id TEXT,limit_id INTEGER,
 limit_type TEXT,reserved_delta INTEGER,actual_delta INTEGER,expected_reset_at TEXT);
CREATE TABLE usage_history(id INTEGER,account_id TEXT,window TEXT,used_percent REAL,reset_at INTEGER,
 window_minutes INTEGER,recorded_at TEXT);
CREATE TABLE account_usage_rollups(account_id TEXT,request_count INTEGER,input_tokens INTEGER,
 output_tokens INTEGER,cached_input_tokens INTEGER,total_cost_usd REAL);
CREATE TABLE api_key_usage_rollups(api_key_id TEXT,request_count INTEGER,input_tokens INTEGER,
 output_tokens INTEGER,cached_input_tokens INTEGER,total_cost_usd REAL);
CREATE TABLE request_usage_hourly_rollups(bucket_epoch INTEGER,account_id TEXT,api_key_id TEXT,model TEXT,
 service_tier TEXT,request_kind TEXT,is_deleted INTEGER,request_count INTEGER,error_count INTEGER,
 cancelled_count INTEGER,input_tokens INTEGER,output_tokens INTEGER,reasoning_tokens INTEGER,
 output_or_reasoning_tokens INTEGER,cached_input_tokens INTEGER,cached_input_tokens_clamped INTEGER,
 cost_usd REAL,cost_count INTEGER);
CREATE TABLE request_usage_hourly_error_rollups(bucket_epoch INTEGER,account_id TEXT,error_code TEXT,error_count INTEGER);
CREATE TABLE request_conversation_hourly_rollups(bucket_epoch INTEGER,conversation_id TEXT,account_id TEXT,
 is_deleted INTEGER,request_count INTEGER);
CREATE TABLE request_logs(id INTEGER,account_id TEXT,api_key_id TEXT,model_source_id TEXT,
 request_id TEXT,request_kind TEXT,requested_at TEXT,model TEXT,service_tier TEXT,input_tokens INTEGER,
 output_tokens INTEGER,cached_input_tokens INTEGER,reasoning_tokens INTEGER,cost_usd REAL,status TEXT,
 error_code TEXT,latency_queue_ms INTEGER,latency_first_upstream_event_ms INTEGER,latency_ms INTEGER,
 deleted_at TEXT,conversation_id TEXT,useragent TEXT,useragent_group TEXT,client_ip TEXT,
 reasoning_effort TEXT,plan_type TEXT,source TEXT,transport TEXT,latency_first_token_ms INTEGER);
CREATE TABLE automation_jobs(id TEXT,name TEXT,enabled INTEGER,include_paused_accounts INTEGER,
 account_scope_all INTEGER,schedule_type TEXT,schedule_time TEXT,schedule_timezone TEXT,
 schedule_threshold_minutes INTEGER,schedule_days TEXT,model TEXT,reasoning_effort TEXT,prompt TEXT,
 created_at TEXT,updated_at TEXT);
CREATE TABLE automation_job_accounts(job_id TEXT,account_id TEXT,position INTEGER);
CREATE TABLE automation_runs(id TEXT,job_id TEXT,slot_key TEXT,cycle_key TEXT,trigger TEXT,model TEXT,
 reasoning_effort TEXT,scheduled_for TEXT,started_at TEXT,finished_at TEXT,status TEXT,account_id TEXT,
 error_code TEXT,error_message TEXT,attempt_count INTEGER);
`

func legacyFixture(t *testing.T) (string, *credentials.Vault, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy-copy.db")
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "legacy.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	token, err := vault.Encrypt([]byte("synthetic-only-token"))
	if err != nil {
		t.Fatal(err)
	}
	plainKey := "sk-clb-synthetic-fixture"
	hash := sha256.Sum256([]byte(plainKey))
	password, err := bcrypt.GenerateFromPassword([]byte("fixture-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, legacyFixtureSchema); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO runtime_sentinels VALUES('encryption_key_fingerprint',?)`, []any{vault.Fingerprint()}},
		{`INSERT INTO account_usage_rollup_state VALUES(1,'2026-09-25 00:00:00.000000','2026-09-25 00:00:00.000000')`, nil},
		{`INSERT INTO accounts VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{
			"acct-a", "chat-workspace", "chat-user", "install-a", "a@example.invalid", "A",
			"workspace", "Workspace", "seat", "plus", "normal", token, token, token,
			"2026-09-24 12:00:00.000000", "2026-09-20 12:00:00.000000", "active", nil, 1, 1, nil}},
		{`INSERT INTO account_proxy_bindings VALUES('acct-a',1)`, nil},
		{`INSERT INTO model_sources VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{
			"source-a", "Z.AI", "openai_compatible", "https://provider.example.invalid/v1", token,
			1, "healthy", 1, 1, 0, 1, 30, 2, "2026-09-20 00:00:00.000000", "2026-09-24 00:00:00.000000"}},
		{`INSERT INTO model_source_models VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{
			1, "source-a", "glm-test", "GLM Test", 200000, 8000, 1, 1, 0, 1.1, 0.2, 2.2, nil, "{}", 1,
			"2026-09-20 00:00:00.000000", "2026-09-24 00:00:00.000000"}},
		{`INSERT INTO dashboard_settings VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{
			1, 1, 0, 1, "secondary", 1, 1, 1, 0, 0, "capacity_weighted", nil, "default", "smart", "auto",
			86400, string(password), token, 11, 0}},
		{`INSERT INTO account_groups VALUES('group-a','Group A','2026-09-20 00:00:00.000000')`, nil},
		{`INSERT INTO account_group_accounts VALUES('group-a','acct-a')`, nil},
		{`INSERT INTO account_group_limits VALUES(1,'group-a','total_tokens','daily',100,NULL)`, nil},
		{`INSERT INTO api_keys VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{
			"key-a", "Key A", hex.EncodeToString(hash[:]), plainKey[:15], "group-a", "[\"gpt-test\"]",
			0, nil, nil, nil, nil, "foreground", nil, "upstream_limits,account_pool_usage", 1, 0, nil, 1,
			"2026-09-20 00:00:00.000000", nil}},
		{`INSERT INTO api_key_model_sources VALUES('key-a','source-a')`, nil},
		{`INSERT INTO api_key_limits VALUES(11,'key-a','total_tokens','daily',100,30,NULL,'2026-09-27 00:00:00.000000')`, nil},
		{`INSERT INTO api_key_usage_reservations VALUES(?,?,?,?,?,?,?,?,?,?)`, []any{
			"reservation-pending", "key-a", "gpt-test", "reserved", nil, nil, nil, nil,
			"2026-09-24 10:00:00.000000", "2026-09-24 10:00:00.000000"}},
		{`INSERT INTO api_key_usage_reservation_items VALUES(1,'reservation-pending',11,'total_tokens',10,NULL,'2026-09-27 00:00:00.000000')`, nil},
		{`INSERT INTO usage_history VALUES(1,'acct-a','primary',75.0,1790000000,300,'2026-09-24 00:00:00.000000')`, nil},
		{`INSERT INTO account_usage_rollups VALUES('acct-a',10,1000,300,100,1.25)`, nil},
		{`INSERT INTO api_key_usage_rollups VALUES('key-a',12,1100,350,110,1.50)`, nil},
		{`INSERT INTO request_usage_hourly_rollups VALUES(1790208000,'acct-a','key-a','gpt-test',char(31),'normal',0,8,1,0,800,200,10,200,80,80,1.0,8)`, nil},
		{`INSERT INTO request_usage_hourly_error_rollups VALUES(1790208000,'acct-a','old_error',1)`, nil},
		{`INSERT INTO request_conversation_hourly_rollups VALUES(1790208000,'conversation-a','acct-a',0,4)`, nil},
		{`INSERT INTO request_logs(id,account_id,api_key_id,model_source_id,request_id,
 request_kind,requested_at,model,service_tier,input_tokens,output_tokens,cached_input_tokens,
 reasoning_tokens,cost_usd,status,error_code,latency_queue_ms,latency_first_upstream_event_ms,
 latency_ms,deleted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{
			1, "acct-a", "key-a", nil, "req-one", "normal", "2026-09-25 01:00:00.000000",
			"gpt-test", nil, 20, 5, 2, 1, 0.1, "success", nil, 1, 2, 3, nil}},
		{`INSERT INTO request_logs(id,account_id,api_key_id,model_source_id,request_id,
 request_kind,requested_at,model,service_tier,input_tokens,output_tokens,cached_input_tokens,
 reasoning_tokens,cost_usd,status,error_code,latency_queue_ms,latency_first_upstream_event_ms,
 latency_ms,deleted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{
			2, "acct-a", "key-a", nil, "req-one", "normal", "2026-09-25 01:00:00.000000",
			"gpt-test", nil, 30, 6, 3, 2, 0.2, "failed", "upstream_error", 1, 2, 3, nil}},
		{`UPDATE request_logs SET conversation_id='conversation-a',useragent='Codex/1',
 useragent_group='Codex',client_ip='192.0.2.1',reasoning_effort='high',plan_type='plus',
 source='codex',transport='sse',latency_first_token_ms=4 WHERE id=2`, nil},
	} {
		if _, err := db.ExecContext(ctx, row.query, row.args...); err != nil {
			t.Fatalf("fixture insert: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, vault, plainKey
}
