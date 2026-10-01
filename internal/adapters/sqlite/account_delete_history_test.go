package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func deletionTestUsage(t *testing.T, s *Store, id string, at time.Time, input, cost int64, status string) {
	t.Helper()
	ctx := context.Background()
	_, err := s.ReserveUsage(ctx, domain.ReservationRequest{
		ID: id, APIKeyID: "key", AccountID: "acct", Model: "gpt-test",
		Budget: domain.UsageAmount{InputTokens: 25}, Now: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := domain.UsageEvent{RequestID: id, AccountID: "acct", Model: "gpt-test",
		RequestKind: "normal", Status: status, RequestedAt: at,
		ConversationID: "conversation", ErrorCode: "upstream_failure",
		Usage: domain.UsageAmount{InputTokens: input, CostMicrodollars: cost}}
	if applied, err := s.SettleUsage(ctx, id, domain.UsageSettlement{Status: "finalized", Event: event}); err != nil || !applied {
		t.Fatalf("settle %s: %v %v", id, applied, err)
	}
}

func deletionTestSetup(t *testing.T) (*Store, string) {
	t.Helper()
	s, path := testStore(t)
	saveTestAccount(t, s, "acct")
	key := testKey("key", nil)
	key.Limits = []domain.LimitRule{tokenLimit(1000)}
	if err := s.SaveAPIKey(context.Background(), key, fixedTime); err != nil {
		t.Fatal(err)
	}
	deletionTestUsage(t, s, "folded", fixedTime, 5, 1000, "error")
	if n, err := s.FoldAndPruneRequestLogs(context.Background(), fixedTime.Add(time.Minute), 10); err != nil || n != 1 {
		t.Fatalf("fold: %d %v", n, err)
	}
	deletionTestUsage(t, s, "raw", fixedTime.Add(time.Hour), 7, 2000, "success")
	archive := domain.ErrorArchive{RequestID: "archived", AccountID: "acct", KeyID: "key", Transport: "http",
		OccurredAt: fixedTime, ExpiresAt: fixedTime.Add(365 * 24 * time.Hour), ContentEncrypted: testCiphertext()}
	if err := s.SaveErrorArchive(context.Background(), archive, 1024); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO account_quota_history(account_id,window,observed_at,used_percent)
 VALUES('acct','primary',?,20)`, millis(fixedTime)); err != nil {
		t.Fatal(err)
	}
	return s, path
}

func deletionTestDrain(t *testing.T, s *Store) {
	t.Helper()
	for i := 0; i < 30; i++ {
		n, err := s.CleanupDeletedAccounts(context.Background(), 1)
		if err != nil || n > 1 {
			t.Fatalf("cleanup batch %d: %d %v", i, n, err)
		}
		var done bool
		if err := s.db.QueryRow(`SELECT cleanup_done FROM account_deletions WHERE account_id='acct' AND generation=0`).Scan(&done); err != nil {
			t.Fatal(err)
		}
		if done {
			return
		}
	}
	t.Fatal("cleanup did not finish")
}

func TestDeletedAccountKeepsOrphanReportsAndSettlesOldReservation(t *testing.T) {
	ctx := context.Background()
	s, path := deletionTestSetup(t)
	if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: "late", APIKeyID: "key", AccountID: "acct",
		Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 25}, Now: fixedTime.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE accounts SET status='deactivated' WHERE id='acct'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO account_deletions(account_id,generation,delete_history) VALUES('acct',0,0)`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CleanupDeletedAccounts(ctx, 1); err != nil || n != 1 {
		t.Fatalf("first batch: %d %v", n, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	deletionTestDrain(t, s)
	for _, scope := range []struct{ key, account string }{{"key", ""}, {"", ""}} {
		totals, err := s.UsageTotals(ctx, scope.key, scope.account)
		if err != nil || totals.RequestCount != 2 || totals.Usage.InputTokens != 12 || totals.Usage.CostMicrodollars != 3000 {
			t.Fatalf("retained total: %+v %v", totals, err)
		}
	}
	if totals, err := s.UsageTotals(ctx, "", "acct"); err != nil || totals.RequestCount != 0 {
		t.Fatalf("deleted account total: %+v %v", totals, err)
	}
	var orphanRaw, orphanFolded, orphanError, orphanConversation, orphanArchive, quota int
	queries := []struct {
		query string
		out   *int
	}{
		{`SELECT count(*) FROM usage_events WHERE request_id='raw' AND account_id IS NULL AND legacy_deleted=1`, &orphanRaw},
		{`SELECT count(*) FROM legacy_hourly_usage WHERE account_id=char(31) AND is_deleted=1`, &orphanFolded},
		{`SELECT count(*) FROM legacy_hourly_errors WHERE account_id=char(31)`, &orphanError},
		{`SELECT count(*) FROM legacy_conversation_hourly WHERE account_id=char(31) AND is_deleted=1`, &orphanConversation},
		{`SELECT count(*) FROM error_archives WHERE request_id='archived' AND account_id=''`, &orphanArchive},
		{`SELECT count(*) FROM account_quota_history WHERE account_id='acct'`, &quota},
	}
	for _, item := range queries {
		if err := s.db.QueryRow(item.query).Scan(item.out); err != nil {
			t.Fatal(err)
		}
	}
	if orphanRaw != 1 || orphanFolded != 1 || orphanError != 1 || orphanConversation != 1 || orphanArchive != 1 || quota != 0 {
		t.Fatalf("orphan cleanup: raw=%d folded=%d error=%d conversation=%d archive=%d quota=%d",
			orphanRaw, orphanFolded, orphanError, orphanConversation, orphanArchive, quota)
	}
	report, err := s.KeyUsage7Day(ctx, "key", fixedTime.Add(-time.Hour), fixedTime.Add(2*time.Hour))
	if err != nil || report.TotalRequests != 2 || len(report.AccountCosts) != 1 || !report.AccountCosts[0].IsDeleted || report.AccountCosts[0].CostUSD != 0.003 {
		t.Fatalf("deleted account report: %+v %v", report, err)
	}
	if _, err := s.db.Exec(`UPDATE accounts SET generation=1,status='active' WHERE id='acct'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: "stale", APIKeyID: "key", AccountID: "acct",
		AccountGeneration: 0, Model: "gpt-test", Now: fixedTime.Add(3 * time.Hour)}); err != ErrNoAccounts {
		t.Fatalf("old generation admitted: %v", err)
	}
	event := usageEvent("late", 3)
	event.AccountID = "acct"
	if applied, err := s.SettleUsage(ctx, "late", domain.UsageSettlement{Status: "finalized", Event: event}); err != nil || !applied {
		t.Fatalf("late settlement: %v %v", applied, err)
	}
	if applied, err := s.SettleUsage(ctx, "late", domain.UsageSettlement{Status: "finalized", Event: event}); err != nil || applied {
		t.Fatalf("duplicate late settlement: %v %v", applied, err)
	}
	var account sql.NullString
	var deleted bool
	if err := s.db.QueryRow(`SELECT account_id,legacy_deleted FROM usage_events WHERE request_id='late'`).Scan(&account, &deleted); err != nil || account.Valid || !deleted {
		t.Fatalf("late event attribution: %+v %v %v", account, deleted, err)
	}
	if totals, err := s.UsageTotals(ctx, "", "acct"); err != nil || totals.RequestCount != 0 {
		t.Fatalf("late event polluted fresh account: %+v %v", totals, err)
	}
	if key, err := s.GetAPIKey(ctx, "key"); err != nil || key.Limits[0].CurrentValue != 15 {
		t.Fatalf("old reservation budget: %+v %v", key.Limits, err)
	}
}

func TestDeletedAccountRemovesOnlyAttributableTotalsAndKeepsBill(t *testing.T) {
	ctx := context.Background()
	s, _ := deletionTestSetup(t)
	if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: "late", APIKeyID: "key", AccountID: "acct",
		Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 25}, Now: fixedTime.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// An imported detail row below the hourly watermark duplicates its folded source.
	if _, err := s.db.Exec(`INSERT INTO usage_events(request_id,api_key_id,account_id,model,status,requested_at,
 input_tokens,cost_microdollars,legacy_request_id) VALUES('import-copy','key','acct','gpt-test','error',?,?,?,'old')`, millis(fixedTime), 5, 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO legacy_import_state
 (id,source_sha256,key_fingerprint,folded_through,hourly_folded_through,imported_at,sidecar_path)
 VALUES(1,'','',?,?,0,'')`, millis(fixedTime.Add(30*time.Minute)), millis(fixedTime.Add(30*time.Minute))); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct{ name, id string }{{"all", "all"}, {"key", "key"}} {
		if _, err := s.db.Exec(`UPDATE usage_totals SET request_count=request_count+4,
 input_tokens=input_tokens+50,cost_microdollars=cost_microdollars+4000 WHERE scope=? AND scope_id=?`, scope.name, scope.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE accounts SET status='deactivated' WHERE id='acct'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO account_deletions(account_id,generation,delete_history) VALUES('acct',0,1)`); err != nil {
		t.Fatal(err)
	}
	deletionTestDrain(t, s)
	for _, scope := range []struct{ key, account string }{{"key", ""}, {"", ""}} {
		totals, err := s.UsageTotals(ctx, scope.key, scope.account)
		if err != nil || totals.RequestCount != 4 || totals.Usage.InputTokens != 50 || totals.Usage.CostMicrodollars != 4000 {
			t.Fatalf("residual total: %+v %v", totals, err)
		}
	}
	report, err := s.KeyUsage7Day(ctx, "key", fixedTime.Add(-time.Hour), fixedTime.Add(2*time.Hour))
	if err != nil || report.TotalRequests != 0 || len(report.AccountCosts) != 0 {
		t.Fatalf("removed account report: %+v %v", report, err)
	}
	var rows int
	for _, query := range []string{
		`SELECT count(*) FROM usage_events WHERE account_id='acct'`,
		`SELECT count(*) FROM legacy_hourly_usage WHERE account_id='acct'`,
		`SELECT count(*) FROM legacy_hourly_errors WHERE account_id='acct'`,
		`SELECT count(*) FROM legacy_conversation_hourly WHERE account_id='acct'`,
		`SELECT count(*) FROM error_archives WHERE account_id='acct'`,
		`SELECT count(*) FROM account_quota_history WHERE account_id='acct'`,
	} {
		if err := s.db.QueryRow(query).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("history remains: %s: %d %v", query, rows, err)
		}
	}
	if _, err := s.db.Exec(`UPDATE accounts SET generation=1,status='active' WHERE id='acct'`); err != nil {
		t.Fatal(err)
	}
	event := usageEvent("late", 3)
	event.AccountID = "acct"
	if applied, err := s.SettleUsage(ctx, "late", domain.UsageSettlement{Status: "finalized", Event: event}); err != nil || !applied {
		t.Fatalf("old bill: %v %v", applied, err)
	}
	if key, err := s.GetAPIKey(ctx, "key"); err != nil || key.Limits[0].CurrentValue != 15 {
		t.Fatalf("old bill changed key limit: %+v %v", key.Limits, err)
	}
	if totals, err := s.UsageTotals(ctx, "key", ""); err != nil || totals.RequestCount != 4 {
		t.Fatalf("removed history returned: %+v %v", totals, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM usage_events WHERE request_id='late'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("removed policy recorded old event: %d %v", rows, err)
	}
}
