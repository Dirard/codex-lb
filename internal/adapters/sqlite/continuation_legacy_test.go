package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

func TestContinuationKeyIsolationExpiryAndBounds(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct")
	for _, id := range []string{"key-a", "key-b"} {
		if err := s.SaveAPIKey(ctx, testKey(id, nil), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	cipher := testCiphertext()
	bounds := domain.ContinuationBounds{MaxRecords: 2, MaxContextBytes: int64(2 * len(cipher))}
	makeContinuation := func(id, key string, age time.Duration) domain.Continuation {
		return domain.Continuation{ResponseID: id, KeyID: key, AccountID: "acct", ProviderID: "openai",
			Model: "gpt-test", CreatedAt: now.Add(age), ExpiresAt: now.Add(time.Hour), ContextEncrypted: cipher}
	}
	for _, c := range []domain.Continuation{
		makeContinuation("one", "key-a", -3*time.Minute),
		makeContinuation("two", "key-a", -2*time.Minute),
		makeContinuation("three", "key-b", -time.Minute),
	} {
		if err := s.SaveContinuation(ctx, c, bounds); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.GetContinuation(ctx, "key-a", "one", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oldest response not evicted: %v", err)
	}
	if _, err := s.GetContinuation(ctx, "key-a", "three", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-key response exposed: %v", err)
	}
	if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: "quota-refusal", APIKeyID: "key-b",
		AccountID: "acct", Continuation: true, Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 1}, Now: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleUsage(ctx, "quota-refusal", domain.UsageSettlement{Status: "failed",
		Event: domain.UsageEvent{RequestID: "quota-refusal", AccountID: "acct", Model: "gpt-test", Status: "error", RequestedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkContinuationQuotaRefused(ctx, "key-a", "three", "acct", "quota-refusal"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-key quota mark accepted: %v", err)
	}
	if err := s.MarkContinuationQuotaRefused(ctx, "key-b", "three", "replaced-owner", "quota-refusal"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale account could mark replacement continuation: %v", err)
	}
	if err := s.MarkContinuationQuotaRefused(ctx, "key-b", "three", "acct", "quota-refusal"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetContinuation(ctx, "key-b", "three", now)
	if err != nil || !got.QuotaRefused || string(got.ContextEncrypted) != string(cipher) {
		t.Fatalf("continuation roundtrip: %+v, %v", got, err)
	}
	if _, err := s.GetContinuation(ctx, "key-b", "three", now.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired response returned: %v", err)
	}
	changed := makeContinuation("three", "key-b", -time.Minute)
	changed.ProviderID = "another-provider"
	if err := s.SaveContinuation(ctx, changed, bounds); !errors.Is(err, ErrConflict) {
		t.Fatalf("owner was overwritten: %v", err)
	}
	b, err := json.Marshal(got)
	if err != nil || strings.Contains(string(b), string(cipher)) {
		t.Fatalf("replay ciphertext leaked to JSON: %s, %v", b, err)
	}
}

func TestContinuationQuotaMarkIsReservationFenced(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct")
	if err := s.SaveAPIKey(ctx, testKey("key", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	settledReservation := func(id, settlement string) {
		t.Helper()
		req := domain.ReservationRequest{ID: id, APIKeyID: "key", AccountID: "acct",
			Continuation: true, Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 1}, Now: now}
		if _, err := s.ReserveUsage(ctx, req); err != nil {
			t.Fatal(err)
		}
		event := domain.UsageEvent{RequestID: id, AccountID: "acct", Model: "gpt-test", RequestedAt: now}
		if settlement == "failed" {
			event.Status, event.ErrorCode = "error", "usage_limit_reached"
		} else {
			event.Status = "success"
		}
		if _, err := s.SettleUsage(ctx, id, domain.UsageSettlement{Status: settlement, Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	saveGeneration := func(responseID, reservationID string) {
		t.Helper()
		c := domain.Continuation{ResponseID: "session:fenced", KeyID: "key", AccountID: "acct",
			ProviderID: "openai", Model: "gpt-test", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
			ContextEncrypted: testCiphertext()}
		if err := s.SaveSessionContinuation(ctx, c, reservationID, domain.ContinuationBounds{MaxRecords: 10, MaxContextBytes: 1 << 20}); err != nil {
			t.Fatal(err)
		}
	}
	settledReservation("old-refusal", "failed")
	saveGeneration("old-response", "old-refusal")
	settledReservation("new-generation", "finalized")
	saveGeneration("new-response", "new-generation")
	if err := s.MarkContinuationQuotaRefused(ctx, "key", "session:fenced", "acct", "old-refusal"); err != nil {
		t.Fatalf("stale quota mark failed the request: %v", err)
	}
	if got, err := s.GetContinuation(ctx, "key", "session:fenced", now); err != nil || got.QuotaRefused {
		t.Fatalf("stale quota mark touched newer generation: %+v, %v", got, err)
	}
	settledReservation("current-refusal", "failed")
	if err := s.MarkContinuationQuotaRefused(ctx, "key", "session:fenced", "acct", "current-refusal"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetContinuation(ctx, "key", "session:fenced", now); err != nil || !got.QuotaRefused {
		t.Fatalf("current quota mark was lost: %+v, %v", got, err)
	}
}

func TestMigrateVersionTwentyTwoToCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v22.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range migrations[:22] {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("PRAGMA application_id=1129071175"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version=22"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM account_usage_observation").Scan(&count); err != nil || count != 0 {
		t.Fatalf("v22 did not gain the observation table: %d %v", count, err)
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("v22 upgrade: %d %v", version, err)
	}
}

func TestMigrateVersionOneToCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA application_id=1129071175"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("migration version: %d, %v", version, err)
	}
}

func TestMigrateVersionFourToCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{schemaV1, schemaV2, schemaV3, schemaV4} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("PRAGMA application_id=1129071175"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("v4 upgrade: %d, %v", version, err)
	}
	var tables int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='legacy_hourly_errors'`).Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("v5 satellite missing: %d, %v", tables, err)
	}
}

func TestInspectLegacySnapshotReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "source.db")
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "source.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	token, err := vault.Encrypt([]byte("synthetic credential"))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("synthetic key"))
	password, err := bcrypt.GenerateFromPassword([]byte("synthetic password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `
 CREATE TABLE accounts(id TEXT,access_token_encrypted BLOB,refresh_token_encrypted BLOB,id_token_encrypted BLOB);
 CREATE TABLE model_sources(id TEXT,api_key_encrypted BLOB);
 CREATE TABLE account_groups(id TEXT);
 CREATE TABLE api_keys(id TEXT,key_hash TEXT);
 CREATE TABLE api_key_usage_reservations(id TEXT,status TEXT);
 CREATE TABLE request_logs(id INTEGER);
 CREATE TABLE account_usage_rollups(account_id TEXT);
 CREATE TABLE api_key_usage_rollups(api_key_id TEXT);
 CREATE TABLE dashboard_settings(id INTEGER,password_hash TEXT,totp_secret_encrypted BLOB,
 upstream_proxy_routing_enabled INTEGER);
 CREATE TABLE account_proxy_bindings(id TEXT,is_active INTEGER);`)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO accounts VALUES(?,?,?,?)`, []any{"acct", token, token, token}},
		{`INSERT INTO model_sources VALUES(?,?)`, []any{"source", token}},
		{`INSERT INTO account_groups VALUES(?)`, []any{"group"}},
		{`INSERT INTO api_keys VALUES(?,?)`, []any{"key", hex.EncodeToString(hash[:])}},
		{`INSERT INTO api_key_usage_reservations VALUES(?,?)`, []any{"reservation", "reserved"}},
		{`INSERT INTO request_logs VALUES(?)`, []any{1}},
		{`INSERT INTO account_usage_rollups VALUES(?)`, []any{"acct"}},
		{`INSERT INTO api_key_usage_rollups VALUES(?)`, []any{"key"}},
		{`INSERT INTO dashboard_settings VALUES(?,?,?,?)`, []any{1, string(password), token, 0}},
		{`INSERT INTO account_proxy_bindings VALUES(?,?)`, []any{"binding", 1}},
	} {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	verify := func(cipher []byte) error {
		_, err := vault.Decrypt(cipher)
		return err
	}
	summary, err := InspectLegacySnapshot(ctx, path, verify)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Accounts != 1 || summary.ModelSources != 1 || summary.Keys != 1 ||
		summary.RequestLogs != 1 || summary.UnsettledReservations != 1 || summary.ActiveProxyBindings != 1 {
		t.Fatalf("incorrect legacy facts: %+v", summary)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("source snapshot changed during inspection")
	}
	if _, err := InspectLegacySnapshot(ctx, path, func([]byte) error { return errors.New("wrong key") }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong encryption key accepted: %v", err)
	}
}
