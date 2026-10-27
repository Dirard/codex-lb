package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func largeCiphertext(n int) []byte {
	raw := make([]byte, n)
	raw[0] = 0x80
	for i := 1; i < n; i++ {
		raw[i] = byte(i)
	}
	return []byte(base64.URLEncoding.EncodeToString(raw))
}

func assertContinuationStats(t *testing.T, store *Store) (int64, int64) {
	t.Helper()
	var records, bytes, countedRecords, countedBytes int64
	if err := store.readDB.QueryRow(`SELECT records,context_bytes FROM continuation_stats WHERE id=1`).Scan(&records, &bytes); err != nil {
		t.Fatal(err)
	}
	if err := store.readDB.QueryRow(`SELECT count(*),coalesce(sum(length(context_encrypted)),0) FROM continuations`).Scan(&countedRecords, &countedBytes); err != nil {
		t.Fatal(err)
	}
	if records != countedRecords || bytes != countedBytes {
		t.Fatalf("continuation stats %d/%d diverge from recount %d/%d", records, bytes, countedRecords, countedBytes)
	}
	return records, bytes
}

// Aliases share the owner's stored context: one blob per response, counted once.
func TestSessionAliasReferencesOwnerContext(t *testing.T) {
	ctx := context.Background()
	store, plan := finalizeFixture(t)
	f := plan("alias")
	f.Continuation.ContextEncrypted = largeCiphertext(64 << 10)
	turn := f.Aliases[0]
	turn.ResponseID = "session:turn:alias"
	f.Aliases = append(f.Aliases, turn)
	for i := range f.Aliases {
		f.Aliases[i].ContextEncrypted, f.Aliases[i].ContextResponseID = nil, f.Continuation.ResponseID
	}
	if result, err := store.FinalizeResponse(ctx, f); err != nil || result.ContinuationErr != nil {
		t.Fatalf("finalize: %+v %v", result, err)
	}
	for _, id := range []string{"session:alias", "session:turn:alias"} {
		var stored sql.NullInt64
		if err := store.readDB.QueryRow(`SELECT length(context_encrypted) FROM continuations WHERE response_id=?`, id).Scan(&stored); err != nil || stored.Valid {
			t.Fatalf("alias %s stores its own context: %v %v", id, stored, err)
		}
		alias, err := store.GetContinuation(ctx, "key", id, time.Now())
		if err != nil || !bytes.Equal(alias.ContextEncrypted, f.Continuation.ContextEncrypted) || alias.ContextResponseID != "resp-alias" {
			t.Fatalf("alias %s lost the owner's context: %d bytes %v", id, len(alias.ContextEncrypted), err)
		}
	}
	if _, contextBytes := assertContinuationStats(t, store); contextBytes != int64(len(f.Continuation.ContextEncrypted)) {
		t.Fatalf("context counted more than once: %d", contextBytes)
	}
	// The owner's eviction leaves the alias without context rather than stale.
	if _, err := store.exec(ctx, `DELETE FROM continuations WHERE response_id='resp-alias'`); err != nil {
		t.Fatal(err)
	}
	if alias, err := store.GetContinuation(ctx, "key", "session:alias", time.Now()); err != nil || len(alias.ContextEncrypted) != 0 {
		t.Fatalf("alias kept evicted context: %d %v", len(alias.ContextEncrypted), err)
	}
	assertContinuationStats(t, store)
	if err := store.SaveSessionContinuation(ctx, domain.Continuation{ResponseID: "session:bad", KeyID: "key", AccountID: "acct", ProviderID: "openai",
		Model: "gpt-test", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), ContextEncrypted: testCiphertext(), ContextResponseID: "resp-x"}, "alias", f.Bounds); err == nil {
		t.Fatal("alias with both a reference and its own context accepted")
	}
}

// Bounds use the trigger-maintained counters and evict oldest rows first.
func TestContinuationBoundsUseIncrementalCounters(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	if err := store.SaveAPIKey(ctx, testKey("key", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	blob := largeCiphertext(4 << 10)
	bounds := domain.ContinuationBounds{MaxRecords: 5, MaxContextBytes: int64(3 * len(blob))}
	start := time.Now().UTC()
	for i := 0; i < 12; i++ {
		c := domain.Continuation{ResponseID: fmt.Sprintf("resp-%02d", i), KeyID: "key", AccountID: "acct", ProviderID: "openai", Model: "gpt-test",
			CreatedAt: start.Add(time.Duration(i) * time.Second), ExpiresAt: start.Add(time.Hour)}
		if i%2 == 0 {
			c.ContextEncrypted = blob
		}
		if err := store.SaveContinuation(ctx, c, bounds); err != nil {
			t.Fatal(err)
		}
		records, contextBytes := assertContinuationStats(t, store)
		if records > int64(bounds.MaxRecords) || contextBytes > bounds.MaxContextBytes {
			t.Fatalf("bounds exceeded after %d: %d records, %d bytes", i, records, contextBytes)
		}
	}
	if _, err := store.GetContinuation(ctx, "key", "resp-11", time.Now()); err != nil {
		t.Fatalf("newest record evicted: %v", err)
	}
	if _, err := store.GetContinuation(ctx, "key", "resp-00", time.Now()); err == nil {
		t.Fatal("oldest record survived eviction")
	}
	updated := domain.Continuation{ResponseID: "resp-11", KeyID: "key", AccountID: "acct", ProviderID: "openai", Model: "gpt-test",
		CreatedAt: start, ExpiresAt: start.Add(time.Hour), ContextEncrypted: blob}
	if err := store.SaveContinuation(ctx, updated, bounds); err != nil {
		t.Fatal(err)
	}
	assertContinuationStats(t, store)
}

// Schema 33 alias rows keep their own context and the counters start exact.
func TestContinuationStatsMigrationFrom33(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v33.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:33] {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	blob := largeCiphertext(1 << 10)
	for _, statement := range []string{
		`INSERT INTO accounts(id,kind,provider,email,status,created_at) VALUES('acct','chatgpt','openai','a@example.invalid','active',0)`,
		`INSERT INTO api_keys(id,name,key_hash,key_prefix,created_at) VALUES('key','key','hash','sk',0)`,
		fmt.Sprintf("PRAGMA application_id=%d", applicationID), "PRAGMA user_version=33",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	expires := time.Now().Add(time.Hour).UnixMilli()
	for _, id := range []string{"resp-old", "session:old"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO continuations(response_id,key_id,account_id,provider_id,model,created_at,expires_at,context_encrypted)
 VALUES(?,?,?,?,?,?,?,?)`, id, "key", "acct", "openai", "gpt-test", 0, expires, blob); err != nil {
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
	if records, contextBytes := assertContinuationStats(t, store); records != 2 || contextBytes != int64(2*len(blob)) {
		t.Fatalf("backfilled stats %d/%d", records, contextBytes)
	}
	if alias, err := store.GetContinuation(ctx, "key", "session:old", time.Now()); err != nil || !bytes.Equal(alias.ContextEncrypted, blob) {
		t.Fatalf("pre-34 alias lost its own context: %d %v", len(alias.ContextEncrypted), err)
	}
}
