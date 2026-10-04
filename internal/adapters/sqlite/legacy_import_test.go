package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestImportLegacySnapshotPreservesStateAndSource(t *testing.T) {
	ctx := context.Background()
	source, vault, plainKey := legacyFixture(t)
	before, err := hashFile(source)
	if err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(t.TempDir(), "new.db")
	s, err := Open(destPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	report, err := s.ImportLegacySnapshot(ctx, source, vault)
	if err != nil {
		t.Fatal(err)
	}
	if report.Accounts != 1 || report.ModelSources != 1 || report.Groups != 1 || report.Keys != 1 ||
		report.Reservations != 1 || report.UnsettledReservations != 1 || report.ActiveProxyBindings != 1 {
		t.Fatalf("import report: %+v", report)
	}
	after, err := hashFile(source)
	if err != nil || after != before {
		t.Fatalf("source changed: %v", err)
	}
	copyHash, err := hashFile(report.SidecarPath)
	if err != nil || copyHash != before {
		t.Fatalf("sidecar differs from source: %v", err)
	}
	info, err := os.Stat(report.SidecarPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("sidecar permissions: %v, %v", info, err)
	}
	account, err := s.GetAccount(ctx, "acct-a")
	if err != nil || !account.RequiresEgressDecision || account.Status != domain.AccountActive || account.ChatGPTUserID != "chat-user" {
		t.Fatalf("proxy-bound account became routable or lost identity: %+v, %v", account, err)
	}
	eligible, err := s.EligibleAccounts(ctx, "key-a")
	if err != nil || len(eligible) != 1 || eligible[0].ID != "source-a" {
		t.Fatalf("legacy eligibility must retain only the assigned source: %+v, %v", eligible, err)
	}
	credential, err := s.GetAccountCredential(ctx, "acct-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Decrypt(credential.AccessTokenEncrypted); err != nil {
		t.Fatalf("ciphertext lost: %v", err)
	}
	sourceAccount, err := s.GetAccount(ctx, "source-a")
	if err != nil || sourceAccount.Kind != domain.AccountExternal || sourceAccount.BaseURL != "https://provider.example.invalid/v1" {
		t.Fatalf("external source lost: %+v, %v", sourceAccount, err)
	}
	modelSource, err := s.GetModelSource(ctx, "source-a")
	if err != nil || len(modelSource.Models) != 1 || modelSource.Models[0].Model != "glm-test" ||
		modelSource.Models[0].InputPerMillion == nil || *modelSource.Models[0].InputPerMillion != 1.1 {
		t.Fatalf("external model catalog/pricing lost: %+v, %v", modelSource, err)
	}
	group, err := s.GetGroup(ctx, "group-a")
	if err != nil || len(group.AccountIDs) != 1 || group.AccountIDs[0] != "acct-a" || group.Limits[0].MaxValue != 100 {
		t.Fatalf("group import: %+v, %v", group, err)
	}
	hash := sha256.Sum256([]byte(plainKey))
	key, err := s.FindAPIKeyByHash(ctx, fmt.Sprintf("%x", hash[:]))
	if err != nil || key.ID != "key-a" || len(key.Limits) != 1 || key.Limits[0].CurrentValue != 30 ||
		!key.AccountAssignmentScopeEnabled || len(key.AssignedSourceIDs) != 1 || key.AssignedSourceIDs[0] != "source-a" {
		t.Fatalf("legacy key hash/policy/usage: %+v, %v", key, err)
	}
	reservation, err := s.GetReservation(ctx, "reservation-pending")
	if err != nil || reservation.Status != "reserved" || !reservation.NeedsReconciliation {
		t.Fatalf("pending ledger was cleared: %+v, %v", reservation, err)
	}
	pending, err := s.ListReservationsNeedingReconciliation(ctx, "", 10)
	if err != nil || len(pending) != 1 || pending[0].ID != "reservation-pending" {
		t.Fatalf("pending legacy ledger not enumerable: %+v, %v", pending, err)
	}
	if released, err := s.ReleaseStaleReservations(ctx, fixedTime.Add(7*24*time.Hour)); err != nil || released != 0 {
		t.Fatalf("legacy pending reservation silently released: %d, %v", released, err)
	}
	settings, err := s.LoadSettings(ctx)
	if err != nil || !settings.TOTPRequiredOnLogin || !settings.APIKeyAuthEnabled || settings.DashboardSessionTTL != 86400 ||
		settings.WarmupModel != "gpt-5.4-mini" ||
		!settings.ShowResetCreditBadges || !settings.ShowResetCreditExpiryBadge {
		t.Fatalf("settings import: %+v, %v", settings, err)
	}
	admin, err := s.LoadAdminSecret(ctx)
	if err != nil || admin.PasswordHash == "" || len(admin.TOTPSecretEncrypted) == 0 {
		t.Fatalf("admin secret import: %+v, %v", admin, err)
	}
	accountTotals, err := s.UsageTotals(ctx, "", "acct-a")
	if err != nil || accountTotals.RequestCount != 11 || accountTotals.Usage.InputTokens != 1030 ||
		accountTotals.Usage.OutputTokens != 306 || accountTotals.Usage.CostMicrodollars != 1450000 {
		t.Fatalf("account folded totals lost/doubled: %+v, %v", accountTotals, err)
	}
	keyTotals, err := s.UsageTotals(ctx, "key-a", "")
	if err != nil || keyTotals.RequestCount != 14 || keyTotals.Usage.InputTokens != 1150 ||
		keyTotals.Usage.OutputTokens != 361 || keyTotals.Usage.CostMicrodollars != 1800000 {
		t.Fatalf("key folded totals lost/doubled: %+v, %v", keyTotals, err)
	}
	global, err := s.UsageTotals(ctx, "", "")
	if err != nil || global.RequestCount != 10 || global.Usage.InputTokens != 850 || global.FailedCount != 2 {
		t.Fatalf("hourly+tail global totals: %+v, %v", global, err)
	}
	var errorRows, conversationRows int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM legacy_hourly_errors").Scan(&errorRows); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM legacy_conversation_hourly").Scan(&conversationRows); err != nil {
		t.Fatal(err)
	}
	if errorRows != 1 || conversationRows != 1 {
		t.Fatalf("historical report satellites lost: %d, %d", errorRows, conversationRows)
	}
	logs, err := s.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 50, Statuses: []string{"failed"}})
	if err != nil || logs.Total != 1 || len(logs.Requests) != 1 || logs.Requests[0].RequestID != "req-one" ||
		logs.Requests[0].UserAgentGroup == nil || *logs.Requests[0].UserAgentGroup != "Codex" {
		t.Fatalf("legacy request metadata lost: %+v, %v", logs, err)
	}
	pair, err := s.UsageTotals(ctx, "key-a", "acct-a")
	if err != nil || pair.RequestCount != 10 || pair.Usage.InputTokens != 850 {
		t.Fatalf("folded pair report lost: %+v, %v", pair, err)
	}
	if _, err := s.ImportLegacySnapshot(ctx, source, vault); !errors.Is(err, ErrConflict) {
		t.Fatalf("same source imported twice: %v", err)
	}
	reopened, err := Open(destPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if saved, err := reopened.UsageTotals(ctx, "key-a", ""); err != nil || saved.RequestCount != 14 {
		t.Fatalf("imported totals did not survive reopen: %+v, %v", saved, err)
	}
}

type wrongFingerprintVault struct{ LegacyVault }

func (wrongFingerprintVault) Fingerprint() string { return "sha256:wrong" }

func TestImportLegacySnapshotRejectsWrongFingerprint(t *testing.T) {
	ctx := context.Background()
	source, vault, _ := legacyFixture(t)
	s, _ := testStore(t)
	_, err := s.ImportLegacySnapshot(ctx, source, wrongFingerprintVault{vault})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong fingerprint accepted: %v", err)
	}
	accounts, err := s.ListAccounts(ctx)
	if err != nil || len(accounts) != 0 {
		t.Fatalf("partial import after fingerprint failure: %d, %v", len(accounts), err)
	}
}

func TestImportLegacyGlobalProxyPolicyStaysBlocked(t *testing.T) {
	ctx := context.Background()
	source, vault, _ := legacyFixture(t)
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE dashboard_settings SET upstream_proxy_routing_enabled=1`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, _ := testStore(t)
	report, err := s.ImportLegacySnapshot(ctx, source, vault)
	if err != nil {
		t.Fatal(err)
	}
	if !report.GlobalProxyRouting {
		t.Fatal("global proxy policy not detected")
	}
	for _, id := range []string{"acct-a", "source-a"} {
		account, err := s.GetAccount(ctx, id)
		if err != nil || !account.RequiresEgressDecision {
			t.Fatalf("%s silently routed direct: %+v, %v", id, account, err)
		}
	}
}

func TestImportLegacySettlingReservationRequiresReconciliation(t *testing.T) {
	ctx := context.Background()
	source, vault, _ := legacyFixture(t)
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE api_key_usage_reservations SET status='settling'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(t.TempDir(), "new.db")
	s, err := Open(destPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.ImportLegacySnapshot(ctx, source, vault)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("settling reservation silently imported: %v", err)
	}
	accounts, err := s.ListAccounts(ctx)
	if err != nil || len(accounts) != 0 {
		t.Fatalf("partial import after unresolved ledger: %d, %v", len(accounts), err)
	}
	if _, err := os.Stat(destPath + ".legacy-source.sqlite"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sidecar materialized before validation: %v", err)
	}
}
