package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// An installation that ran go-v1.19.0 (schema 54) with Antigravity sources
// upgrades to a release without them: the sources and their Google refresh
// tokens are gone, other sources and the usage history stay.
func TestMigration55RetiresAntigravitySources(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v54.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:54] {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	now := int64(1795363599184)
	for _, source := range []struct {
		id, kind string
		deleted  bool
	}{{"src_live_antigravity", "antigravity", false}, {"src_old_antigravity", "antigravity", true}, {"src_glm", "zai", false}} {
		deletedAt := any(nil)
		status, reason := "active", ""
		if source.deleted {
			deletedAt, status, reason = now, "deactivated", "deleted"
		}
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO legacy_model_sources(id,name,kind,base_url,is_enabled,health_status,supports_chat_completions,supports_responses,
 supports_audio_transcriptions,supports_embeddings,created_at,updated_at,deleted_at) VALUES(?,?,?,?,1,'unknown',1,1,0,0,?,?,?)`,
				[]any{source.id, source.id, source.kind, "https://example.test", now, now, deletedAt}},
			{`INSERT INTO accounts(id,kind,provider,base_url,email,alias,plan_type,routing_policy,status,deactivation_reason,created_at)
 VALUES(?,'external',?,?,?,?,'external','normal',?,?,?)`, []any{source.id, source.kind, "https://example.test", source.id, source.id, status, reason, now}},
			{`INSERT INTO account_credentials(account_id,external_key_encrypted) VALUES(?,?)`, []any{source.id, []byte("synthetic-" + source.id)}},
			{`INSERT INTO usage_events(request_id,api_key_id,account_id,model_source_id,model,status,requested_at,input_tokens,output_tokens)
 VALUES(?,?,?,?,?,'success',?,10,5)`, []any{"req_" + source.id, "key", source.id, source.id, "model", now}},
		} {
			if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
				t.Fatalf("%s: %v", source.id, err)
			}
		}
	}
	for _, statement := range []string{fmt.Sprintf("PRAGMA application_id=%d", applicationID), "PRAGMA user_version=54"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version int
	if err := store.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("user_version %d %v", version, err)
	}
	for _, id := range []string{"src_live_antigravity", "src_old_antigravity"} {
		if _, err := store.GetModelSource(ctx, id); err == nil {
			t.Fatalf("%s is still a source", id)
		}
		account, err := store.GetAccount(ctx, id)
		if err != nil || account.Status != "deactivated" || account.DeactivationReason != "deleted" {
			t.Fatalf("%s account: %+v %v", id, account, err)
		}
		var credentials int
		if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM account_credentials WHERE account_id=?", id).Scan(&credentials); err != nil || credentials != 0 {
			t.Fatalf("%s kept its refresh token: %d %v", id, credentials, err)
		}
	}
	if source, err := store.GetModelSource(ctx, "src_glm"); err != nil || !source.Enabled {
		t.Fatalf("another source changed: %+v %v", source, err)
	}
	var credentials, events int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM account_credentials WHERE account_id='src_glm'").Scan(&credentials); err != nil || credentials != 1 {
		t.Fatalf("another source lost its key: %d %v", credentials, err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM usage_events").Scan(&events); err != nil || events != 3 {
		t.Fatalf("usage history changed: %d %v", events, err)
	}
}
