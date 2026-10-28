package sqlite

// schemaV42 adds hour-bucket usage rollups (design: openspec/changes/
// usage-rollups/design.md §2). A background folder advances
// usage_rollup_state.folded_rowid over usage_events; triggers journal every
// update or deletion of an already folded event in the writer's own
// transaction, so workers of any version keep the rollups consistent and
// settlement inserts fire nothing. Additive: empty tables, triggers and one
// state row; older code ignores them and its reports read raw events.
//
// Histogram columns of layout 1 are TTFT, queue latency and tokens per second
// behind daily medians, and total request latency for later percentiles.
//
// The SQL is a frozen literal: it is applied once per database. The delete
// trigger has no WHEN clause because deleting the maximum rowid, folded or
// not, must lower a cursor that the next insert could otherwise reuse.
const schemaV42 = `
CREATE TABLE usage_rollup_state (
 id INTEGER PRIMARY KEY CHECK(id=1), layout INTEGER NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('ready','rebuilding')),
 folded_rowid INTEGER NOT NULL, folded_rows INTEGER NOT NULL,
 anchor_request_id TEXT, import_watermark INTEGER, updated_at INTEGER NOT NULL
);
INSERT INTO usage_rollup_state(id,layout,status,folded_rowid,folded_rows,anchor_request_id,import_watermark,updated_at)
 VALUES(1,1,'ready',0,0,NULL,NULL,0);
CREATE TABLE usage_rollup_journal (
 seq INTEGER PRIMARY KEY, sign INTEGER NOT NULL CHECK(sign IN (-1,0,1)),
 event_rowid INTEGER NOT NULL, request_id TEXT NOT NULL, requested_at INTEGER NOT NULL,
 api_key_id TEXT, account_id TEXT, model TEXT NOT NULL, request_kind TEXT NOT NULL,
 status TEXT NOT NULL, error_code TEXT NOT NULL, legacy_request_id TEXT, legacy_deleted INTEGER NOT NULL,
 conversation_id TEXT, useragent_group TEXT, reasoning_effort TEXT,
 input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, cached_input_tokens INTEGER NOT NULL,
 reasoning_tokens INTEGER NOT NULL, reasoning_tokens_known INTEGER NOT NULL,
 cost_microdollars INTEGER NOT NULL, queue_latency_ms INTEGER NOT NULL,
 total_latency_ms INTEGER NOT NULL, latency_first_token_ms INTEGER
);
CREATE INDEX idx_usage_rollup_journal_time ON usage_rollup_journal(requested_at);
CREATE TRIGGER usage_rollup_events_delete AFTER DELETE ON usage_events BEGIN
 INSERT INTO usage_rollup_journal(sign,event_rowid,request_id,requested_at,api_key_id,account_id,model,
  request_kind,status,error_code,legacy_request_id,legacy_deleted,conversation_id,useragent_group,
  reasoning_effort,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,reasoning_tokens_known,
  cost_microdollars,queue_latency_ms,total_latency_ms,latency_first_token_ms)
 SELECT -1,OLD.rowid,OLD.request_id,OLD.requested_at,OLD.api_key_id,OLD.account_id,OLD.model,
  OLD.request_kind,OLD.status,OLD.error_code,OLD.legacy_request_id,OLD.legacy_deleted,OLD.conversation_id,
  OLD.useragent_group,OLD.reasoning_effort,OLD.input_tokens,OLD.output_tokens,OLD.cached_input_tokens,
  OLD.reasoning_tokens,OLD.reasoning_tokens_known,OLD.cost_microdollars,OLD.queue_latency_ms,
  OLD.total_latency_ms,OLD.latency_first_token_ms
 FROM usage_rollup_state WHERE id=1 AND OLD.rowid<=folded_rowid;
 UPDATE usage_rollup_state SET folded_rows=folded_rows-1 WHERE id=1 AND OLD.rowid<=folded_rowid;
 UPDATE usage_rollup_state SET folded_rowid=(SELECT coalesce(max(rowid),0) FROM usage_events),
  anchor_request_id=(SELECT request_id FROM usage_events ORDER BY rowid DESC LIMIT 1)
 WHERE id=1 AND folded_rowid>(SELECT coalesce(max(rowid),0) FROM usage_events);
END;
CREATE TRIGGER usage_rollup_events_reprice AFTER UPDATE OF cost_microdollars ON usage_events
WHEN OLD.rowid<=(SELECT folded_rowid FROM usage_rollup_state WHERE id=1)
 AND NEW.cost_microdollars<>OLD.cost_microdollars
 AND NEW.request_id IS OLD.request_id AND NEW.requested_at IS OLD.requested_at
 AND NEW.api_key_id IS OLD.api_key_id AND NEW.account_id IS OLD.account_id AND NEW.model IS OLD.model
 AND NEW.request_kind IS OLD.request_kind AND NEW.status IS OLD.status AND NEW.error_code IS OLD.error_code
 AND NEW.legacy_request_id IS OLD.legacy_request_id AND NEW.legacy_deleted IS OLD.legacy_deleted
 AND NEW.conversation_id IS OLD.conversation_id AND NEW.useragent_group IS OLD.useragent_group
 AND NEW.reasoning_effort IS OLD.reasoning_effort AND NEW.input_tokens IS OLD.input_tokens
 AND NEW.output_tokens IS OLD.output_tokens AND NEW.cached_input_tokens IS OLD.cached_input_tokens
 AND NEW.reasoning_tokens IS OLD.reasoning_tokens AND NEW.reasoning_tokens_known IS OLD.reasoning_tokens_known
 AND NEW.queue_latency_ms IS OLD.queue_latency_ms AND NEW.total_latency_ms IS OLD.total_latency_ms
 AND NEW.latency_first_token_ms IS OLD.latency_first_token_ms
BEGIN
 INSERT INTO usage_rollup_journal(sign,event_rowid,request_id,requested_at,api_key_id,account_id,model,
  request_kind,status,error_code,legacy_request_id,legacy_deleted,conversation_id,useragent_group,
  reasoning_effort,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,reasoning_tokens_known,
  cost_microdollars,queue_latency_ms,total_latency_ms,latency_first_token_ms)
 VALUES(0,OLD.rowid,OLD.request_id,OLD.requested_at,OLD.api_key_id,OLD.account_id,OLD.model,
  OLD.request_kind,OLD.status,OLD.error_code,OLD.legacy_request_id,OLD.legacy_deleted,OLD.conversation_id,
  OLD.useragent_group,OLD.reasoning_effort,OLD.input_tokens,OLD.output_tokens,OLD.cached_input_tokens,
  OLD.reasoning_tokens,OLD.reasoning_tokens_known,NEW.cost_microdollars-OLD.cost_microdollars,
  OLD.queue_latency_ms,OLD.total_latency_ms,OLD.latency_first_token_ms);
END;
CREATE TRIGGER usage_rollup_events_update AFTER UPDATE ON usage_events
WHEN OLD.rowid<=(SELECT folded_rowid FROM usage_rollup_state WHERE id=1)
 AND NOT (NEW.request_id IS OLD.request_id AND NEW.requested_at IS OLD.requested_at
 AND NEW.api_key_id IS OLD.api_key_id AND NEW.account_id IS OLD.account_id AND NEW.model IS OLD.model
 AND NEW.request_kind IS OLD.request_kind AND NEW.status IS OLD.status AND NEW.error_code IS OLD.error_code
 AND NEW.legacy_request_id IS OLD.legacy_request_id AND NEW.legacy_deleted IS OLD.legacy_deleted
 AND NEW.conversation_id IS OLD.conversation_id AND NEW.useragent_group IS OLD.useragent_group
 AND NEW.reasoning_effort IS OLD.reasoning_effort AND NEW.input_tokens IS OLD.input_tokens
 AND NEW.output_tokens IS OLD.output_tokens AND NEW.cached_input_tokens IS OLD.cached_input_tokens
 AND NEW.reasoning_tokens IS OLD.reasoning_tokens AND NEW.reasoning_tokens_known IS OLD.reasoning_tokens_known
 AND NEW.queue_latency_ms IS OLD.queue_latency_ms AND NEW.total_latency_ms IS OLD.total_latency_ms
 AND NEW.latency_first_token_ms IS OLD.latency_first_token_ms)
BEGIN
 INSERT INTO usage_rollup_journal(sign,event_rowid,request_id,requested_at,api_key_id,account_id,model,
  request_kind,status,error_code,legacy_request_id,legacy_deleted,conversation_id,useragent_group,
  reasoning_effort,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,reasoning_tokens_known,
  cost_microdollars,queue_latency_ms,total_latency_ms,latency_first_token_ms)
 VALUES(-1,OLD.rowid,OLD.request_id,OLD.requested_at,OLD.api_key_id,OLD.account_id,OLD.model,
  OLD.request_kind,OLD.status,OLD.error_code,OLD.legacy_request_id,OLD.legacy_deleted,OLD.conversation_id,
  OLD.useragent_group,OLD.reasoning_effort,OLD.input_tokens,OLD.output_tokens,OLD.cached_input_tokens,
  OLD.reasoning_tokens,OLD.reasoning_tokens_known,OLD.cost_microdollars,OLD.queue_latency_ms,
  OLD.total_latency_ms,OLD.latency_first_token_ms);
 INSERT INTO usage_rollup_journal(sign,event_rowid,request_id,requested_at,api_key_id,account_id,model,
  request_kind,status,error_code,legacy_request_id,legacy_deleted,conversation_id,useragent_group,
  reasoning_effort,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,reasoning_tokens_known,
  cost_microdollars,queue_latency_ms,total_latency_ms,latency_first_token_ms)
 VALUES(1,NEW.rowid,NEW.request_id,NEW.requested_at,NEW.api_key_id,NEW.account_id,NEW.model,
  NEW.request_kind,NEW.status,NEW.error_code,NEW.legacy_request_id,NEW.legacy_deleted,NEW.conversation_id,
  NEW.useragent_group,NEW.reasoning_effort,NEW.input_tokens,NEW.output_tokens,NEW.cached_input_tokens,
  NEW.reasoning_tokens,NEW.reasoning_tokens_known,NEW.cost_microdollars,NEW.queue_latency_ms,
  NEW.total_latency_ms,NEW.latency_first_token_ms);
END;
CREATE TABLE usage_rollup_total_hour (
 bucket_epoch INTEGER PRIMARY KEY,
 requests INTEGER NOT NULL, errors INTEGER NOT NULL, cancelled INTEGER NOT NULL,
 input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, reasoning_tokens INTEGER NOT NULL,
 reasoning_known INTEGER NOT NULL, cached_input_clamped INTEGER NOT NULL, cost_microdollars INTEGER NOT NULL,
 conversation_requests INTEGER NOT NULL, ttft_hist BLOB, queue_hist BLOB, tps_hist BLOB, total_hist BLOB
);
CREATE TABLE usage_rollup_cube_hour (
 id INTEGER PRIMARY KEY, bucket_epoch INTEGER NOT NULL,
 account_dim TEXT NOT NULL, api_key_dim TEXT NOT NULL, model TEXT NOT NULL, ua_dim TEXT NOT NULL,
 deleted INTEGER NOT NULL,
 requests INTEGER NOT NULL, errors INTEGER NOT NULL, cancelled INTEGER NOT NULL,
 input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, reasoning_tokens INTEGER NOT NULL,
 reasoning_known INTEGER NOT NULL, cached_input_clamped INTEGER NOT NULL, cost_microdollars INTEGER NOT NULL,
 ttft_hist BLOB, queue_hist BLOB, tps_hist BLOB, total_hist BLOB,
 UNIQUE(bucket_epoch,account_dim,api_key_dim,model,ua_dim,deleted)
);
CREATE INDEX idx_usage_rollup_cube_key ON usage_rollup_cube_hour(api_key_dim,bucket_epoch);
CREATE INDEX idx_usage_rollup_cube_account ON usage_rollup_cube_hour(account_dim,bucket_epoch);
CREATE INDEX idx_usage_rollup_cube_model ON usage_rollup_cube_hour(model,bucket_epoch);
CREATE TABLE usage_rollup_slice (
 kind TEXT NOT NULL CHECK(kind IN ('account','key','model','useragent')),
 grain INTEGER NOT NULL CHECK(grain IN (3600,86400)), bucket_epoch INTEGER NOT NULL, dim TEXT NOT NULL,
 requests INTEGER NOT NULL, errors INTEGER NOT NULL, cancelled INTEGER NOT NULL,
 input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, reasoning_tokens INTEGER NOT NULL,
 reasoning_known INTEGER NOT NULL, cached_input_clamped INTEGER NOT NULL, cost_microdollars INTEGER NOT NULL,
 PRIMARY KEY(kind,grain,bucket_epoch,dim)
) WITHOUT ROWID;
CREATE INDEX idx_usage_rollup_slice_dim ON usage_rollup_slice(kind,dim,grain,bucket_epoch);
CREATE TABLE usage_rollup_error_hour (
 bucket_epoch INTEGER NOT NULL, error_code TEXT NOT NULL, account_dim TEXT NOT NULL,
 api_key_dim TEXT NOT NULL, errors INTEGER NOT NULL,
 PRIMARY KEY(bucket_epoch,error_code,account_dim,api_key_dim)
) WITHOUT ROWID;
CREATE INDEX idx_usage_rollup_error_key ON usage_rollup_error_hour(api_key_dim,bucket_epoch);
CREATE TABLE usage_rollup_conversation_hour (
 id INTEGER PRIMARY KEY, bucket_epoch INTEGER NOT NULL, conversation_id TEXT NOT NULL,
 account_dim TEXT NOT NULL, api_key_dim TEXT NOT NULL, model TEXT NOT NULL, effort_dim TEXT NOT NULL,
 ua_dim TEXT NOT NULL, deleted INTEGER NOT NULL, in_reports INTEGER NOT NULL,
 requests INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL,
 cached_input_tokens INTEGER NOT NULL, cost_microdollars INTEGER NOT NULL, total_latency_ms INTEGER NOT NULL,
 first_at INTEGER NOT NULL, last_at INTEGER NOT NULL, last_request_id TEXT NOT NULL,
 UNIQUE(bucket_epoch,conversation_id,account_dim,api_key_dim,model,effort_dim,ua_dim,deleted,in_reports)
);
CREATE INDEX idx_usage_rollup_conversation_id ON usage_rollup_conversation_hour(conversation_id,bucket_epoch);
CREATE INDEX idx_usage_rollup_conversation_key ON usage_rollup_conversation_hour(api_key_dim,bucket_epoch);
CREATE TABLE usage_rollup_log_hour (
 bucket_epoch INTEGER NOT NULL, account_dim TEXT NOT NULL, api_key_dim TEXT NOT NULL,
 model TEXT NOT NULL, effort_dim TEXT NOT NULL, status TEXT NOT NULL, requests INTEGER NOT NULL,
 PRIMARY KEY(bucket_epoch,account_dim,api_key_dim,model,effort_dim,status)
) WITHOUT ROWID;
CREATE TABLE usage_rollup_dim (
 kind TEXT NOT NULL CHECK(kind IN ('account','key','model','status')),
 value TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', requests INTEGER NOT NULL,
 PRIMARY KEY(kind,value,detail)
) WITHOUT ROWID;
`
