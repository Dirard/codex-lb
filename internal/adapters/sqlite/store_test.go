package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

var fixedTime = time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex-lb.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func saveTestAccount(t *testing.T, s *Store, id string) {
	t.Helper()
	err := s.SaveAccount(context.Background(), domain.Account{ID: id, Kind: domain.AccountChatGPT,
		Email: id + "@example.invalid", PlanType: "plus", Status: domain.AccountActive, CreatedAt: fixedTime})
	if err != nil {
		t.Fatal(err)
	}
	cipher := testCiphertext()
	if err := s.SaveAccountCredential(context.Background(), domain.AccountCredential{AccountID: id,
		AccessTokenEncrypted: cipher, RefreshTokenEncrypted: cipher, IDTokenEncrypted: cipher}); err != nil {
		t.Fatal(err)
	}
}

func testCiphertext() []byte {
	raw := make([]byte, 73)
	raw[0] = 0x80
	return []byte(base64.URLEncoding.EncodeToString(raw))
}

func testKey(id string, group *string) domain.APIKey {
	hash := sha256.Sum256([]byte(id))
	return domain.APIKey{ID: id, Name: id, KeyHash: hex.EncodeToString(hash[:]),
		KeyPrefix: "sk-" + id, GroupID: group, IsActive: true, CreatedAt: fixedTime}
}

func tokenLimit(max int64) domain.LimitRule {
	return domain.LimitRule{Type: domain.LimitTotalTokens, Window: domain.WindowDaily, MaxValue: max}
}

func usageEvent(id string, amount int64) domain.UsageEvent {
	return domain.UsageEvent{RequestID: id, Model: "gpt-test", Status: "success",
		RequestedAt: fixedTime.Add(time.Minute), Usage: domain.UsageAmount{InputTokens: amount}}
}

func TestGroupLimitsAndReservations(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	saveTestAccount(t, s, "acct-a")
	saveTestAccount(t, s, "acct-b")
	g1, g2 := "group-a", "group-b"
	for _, g := range []domain.AccountGroup{
		{ID: g1, Name: "A", AccountIDs: []string{"acct-a", "acct-b"}, Limits: []domain.LimitRule{tokenLimit(100)}},
		{ID: g2, Name: "B", AccountIDs: []string{"acct-a"}, Limits: []domain.LimitRule{tokenLimit(30)}},
	} {
		if err := s.SaveGroup(ctx, g, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	groupedKey := testKey("key-a", &g1)
	groupedKey.AccountAssignmentScopeEnabled = true // Legacy grouped-key view has no direct assignments.
	for _, k := range []domain.APIKey{groupedKey, testKey("key-b", &g2)} {
		if err := s.SaveAPIKey(ctx, k, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.EligibleAccounts(ctx, "key-a"); err != nil || len(got) != 2 {
		t.Fatalf("eligible accounts: %v, %v", got, err)
	}
	reserve := domain.ReservationRequest{ID: "reservation-a", APIKeyID: "key-a", AccountID: "acct-a",
		Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 40}, Now: fixedTime}
	if _, err := s.ReserveUsage(ctx, reserve); err != nil {
		t.Fatal(err)
	}
	keyA, err := s.GetAPIKey(ctx, "key-a")
	if err != nil || len(keyA.Limits) != 1 || keyA.Limits[0].CurrentValue != 40 {
		t.Fatalf("reserved limit: %+v, %v", keyA.Limits, err)
	}
	keyB, err := s.GetAPIKey(ctx, "key-b")
	if err != nil || keyB.Limits[0].CurrentValue != 0 {
		t.Fatalf("second key charged: %+v, %v", keyB.Limits, err)
	}
	event := usageEvent("request-a", 50)
	settled, err := s.SettleUsage(ctx, reserve.ID, domain.UsageSettlement{Status: "finalized", Event: event})
	if err != nil || !settled {
		t.Fatalf("settlement: %v, %v", settled, err)
	}
	settled, err = s.SettleUsage(ctx, reserve.ID, domain.UsageSettlement{Status: "finalized", Event: event})
	if err != nil || settled {
		t.Fatalf("duplicate settlement: %v, %v", settled, err)
	}
	group, err := s.GetGroup(ctx, g1)
	if err != nil {
		t.Fatal(err)
	}
	group.AccountIDs = []string{"acct-b"}
	group.Limits = []domain.LimitRule{tokenLimit(70)}
	if err := s.SaveGroup(ctx, group, fixedTime.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	keyA, err = s.GetAPIKey(ctx, "key-a")
	if err != nil || keyA.Limits[0].CurrentValue != 50 || keyA.Limits[0].MaxValue != 70 {
		t.Fatalf("membership reset usage: %+v, %v", keyA.Limits, err)
	}
	reserve.ID = "reservation-denied"
	reserve.Now = fixedTime.Add(3 * time.Minute)
	if _, err := s.ReserveUsage(ctx, reserve); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("removed account admitted: %v", err)
	}
	group.AccountIDs = []string{}
	if err := s.SaveGroup(ctx, group, fixedTime.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EligibleAccounts(ctx, "key-a"); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("empty group admitted: %v", err)
	}
	if _, err := s.ReserveUsage(ctx, reserve); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("empty group reserved: %v", err)
	}
	keyA.GroupID = nil
	if err := s.SaveAPIKey(ctx, keyA, fixedTime.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	keyA, err = s.GetAPIKey(ctx, "key-a")
	if err != nil || keyA.Limits[0].CurrentValue != 50 || keyA.Limits[0].MaxValue != 70 {
		t.Fatalf("last effective limits lost: %+v, %v", keyA.Limits, err)
	}
	if got, err := s.UsageTotals(ctx, "key-a", ""); err != nil || got.RequestCount != 1 || got.Usage.InputTokens != 50 {
		t.Fatalf("key totals: %+v, %v", got, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.UsageTotals(ctx, "", "acct-a"); err != nil || got.RequestCount != 1 || got.Usage.InputTokens != 50 {
		t.Fatalf("persistent account totals: %+v, %v", got, err)
	}
}

func TestReservationReleaseAndReset(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct")
	k := testKey("key", nil)
	k.Limits = []domain.LimitRule{tokenLimit(10)}
	if err := s.SaveAPIKey(ctx, k, fixedTime); err != nil {
		t.Fatal(err)
	}
	r := domain.ReservationRequest{ID: "r1", APIKeyID: "key", AccountID: "acct",
		Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 8}, Now: fixedTime}
	if _, err := s.ReserveUsage(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: "r2", APIKeyID: "key", AccountID: "acct",
		Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 8}, Now: fixedTime}); err != nil {
		t.Fatal(err) // Legacy semantics reserve remaining budget, not reject a larger estimate.
	}
	if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: "r3", APIKeyID: "key", AccountID: "acct",
		Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 1}, Now: fixedTime}); !errors.Is(err, ErrLimitReached) {
		t.Fatalf("exhausted limit admitted: %v", err)
	}
	if released, err := s.ReleaseStaleReservations(ctx, fixedTime.Add(time.Second)); err != nil || released != 2 {
		t.Fatalf("release stale: %d, %v", released, err)
	}
	key, err := s.GetAPIKey(ctx, "key")
	if err != nil || key.Limits[0].CurrentValue != 0 {
		t.Fatalf("release did not return budget: %+v, %v", key.Limits, err)
	}
	if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: "r4", APIKeyID: "key", AccountID: "acct",
		Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 4}, Now: fixedTime.Add(25 * time.Hour)}); err != nil {
		t.Fatalf("expired window not reset: %v", err)
	}
	key, _ = s.GetAPIKey(ctx, "key")
	if key.Limits[0].CurrentValue != 4 || !key.Limits[0].ResetAt.After(fixedTime.Add(25*time.Hour)) {
		t.Fatalf("wrong reset state: %+v", key.Limits)
	}
}

func TestAccountOutcomeOrderAndAdministrativeStatus(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct")
	if err := s.SaveAPIKey(ctx, testKey("key", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id, status string
	}{{"r1", "failed"}, {"r2", "finalized"}} {
		if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: item.id, APIKeyID: "key", AccountID: "acct",
			Model: "gpt-test", Now: fixedTime}); err != nil {
			t.Fatal(err)
		}
		event := usageEvent(item.id+"-event", 0)
		if item.status == "failed" {
			event.Status = "failed"
		}
		if _, err := s.SettleUsage(ctx, item.id, domain.UsageSettlement{Status: item.status, Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordAccountOutcome(ctx, "acct", "r2", false, true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAccountOutcome(ctx, "acct", "r1", true, false); err != nil {
		t.Fatal(err)
	}
	account, err := s.GetAccount(ctx, "acct")
	if err != nil || account.Status != domain.AccountActive {
		t.Fatalf("older quota error overwrote success: %+v, %v", account, err)
	}
	if err := s.RecordAccountOutcome(ctx, "acct", "r1", false, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("failed reservation reported success: %v", err)
	}
	if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: "r3", APIKeyID: "key", AccountID: "acct",
		Model: "gpt-test", Now: fixedTime.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	event := usageEvent("r3-event", 0)
	event.Status = "failed"
	if _, err := s.SettleUsage(ctx, "r3", domain.UsageSettlement{Status: "failed", Event: event}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAccountOutcome(ctx, "acct", "r3", true, false); err != nil {
		t.Fatal(err)
	}
	account, _ = s.GetAccount(ctx, "acct")
	if account.Status != domain.AccountQuotaExceeded {
		t.Fatalf("quota failure not persisted: %s", account.Status)
	}
	if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: "r4", APIKeyID: "key", AccountID: "acct",
		Continuation: true, Model: "gpt-test", Now: fixedTime.Add(2 * time.Minute)}); err != nil {
		t.Fatalf("existing owner blocked by quota telemetry: %v", err)
	}
	if _, err := s.SettleUsage(ctx, "r4", domain.UsageSettlement{Status: "finalized", Event: usageEvent("r4-event", 0)}); err != nil {
		t.Fatal(err)
	}
	account.Status = domain.AccountPaused
	if err := s.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAccountOutcome(ctx, "acct", "r4", false, true); err != nil {
		t.Fatal(err)
	}
	account, _ = s.GetAccount(ctx, "acct")
	if account.Status != domain.AccountPaused {
		t.Fatalf("provider outcome unpaused account: %s", account.Status)
	}
}

func TestSecretBoundaryAndSettingsCAS(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct")
	if err := s.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: "acct",
		AccessTokenEncrypted: []byte("plain"), RefreshTokenEncrypted: []byte("plain"), IDTokenEncrypted: []byte("plain")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("plaintext accepted: %v", err)
	}
	cipher := testCiphertext()
	credential := domain.AccountCredential{AccountID: "acct", AccessTokenEncrypted: cipher,
		RefreshTokenEncrypted: cipher, IDTokenEncrypted: cipher}
	if err := s.SaveAccountCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetAccountCredential(ctx, "acct")
	if err != nil || string(loaded.AccessTokenEncrypted) != string(cipher) {
		t.Fatalf("credential roundtrip: %v", err)
	}
	b, err := json.Marshal(loaded)
	if err != nil || strings.Contains(string(b), string(cipher)) {
		t.Fatalf("ciphertext leaked to JSON: %s, %v", b, err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("secret password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	secret := domain.AdminSecret{PasswordHash: string(hash), TOTPSecretEncrypted: cipher}
	if initialized, err := s.InitializeAdminSecret(ctx, secret); err != nil || !initialized {
		t.Fatalf("initial setup: %v, %v", initialized, err)
	}
	if initialized, err := s.InitializeAdminSecret(ctx, secret); err != nil || initialized {
		t.Fatalf("setup repeated: %v, %v", initialized, err)
	}
	if advanced, err := s.AdvanceTOTPStep(ctx, 10); err != nil || !advanced {
		t.Fatalf("TOTP advance: %v, %v", advanced, err)
	}
	if advanced, err := s.AdvanceTOTPStep(ctx, 10); err != nil || advanced {
		t.Fatalf("TOTP replay: %v, %v", advanced, err)
	}
	settings, err := s.LoadSettings(ctx)
	if err != nil || settings.Version != 2 || !settings.APIKeyAuthEnabled || settings.DashboardSessionTTL < 60 {
		t.Fatalf("settings defaults: %+v, %v", settings, err)
	}
	settings.TOTPRequiredOnLogin = true
	if err := s.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSettings(ctx, settings); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale settings write accepted: %v", err)
	}
	if b, err := json.Marshal(secret); err != nil || strings.Contains(string(b), string(hash)) {
		t.Fatalf("admin secret leaked to JSON: %s, %v", b, err)
	}
	if err := s.SaveAdminSecret(ctx, domain.AdminSecret{}); err != nil {
		t.Fatalf("remove password: %v", err)
	}
	if saved, err := s.LoadAdminSecret(ctx); err != nil || saved.PasswordHash != "" {
		t.Fatalf("password remained after removal: %+v, %v", saved, err)
	}
	settings, err = s.LoadSettings(ctx)
	if err != nil || settings.TOTPRequiredOnLogin {
		t.Fatalf("TOTP requirement remained after removal: %+v, %v", settings, err)
	}
	settings.TOTPRequiredOnLogin = true
	if err := s.SaveSettings(ctx, settings); !errors.Is(err, ErrInvalid) {
		t.Fatalf("TOTP enabled without secret: %v", err)
	}
}

func TestRejectLegacyDatabaseWithoutMutation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE accounts(id TEXT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign database opened for migration: %v", err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var journal string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil || journal != "delete" {
		t.Fatalf("foreign journal mode changed: %s, %v", journal, err)
	}
	var tables int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='runtime_settings'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("legacy schema modified: %d, %v", tables, err)
	}
}
