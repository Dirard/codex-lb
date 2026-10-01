package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestAccountDeletionRevokesScopesAndReimportStartsFresh(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "deleted")
	saveTestAccount(t, s, "other")
	account, _ := s.GetAccount(ctx, "deleted")
	credential, _ := s.GetAccountCredential(ctx, account.ID)
	account.RequiresEgressDecision, account.SecurityWorkAuthorized, account.LimitWarmupEnabled = true, true, true
	account.RoutingPolicy, account.Alias = "burn_first", "old alias"
	if err := s.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	groupID := "only-deleted"
	if err := s.SaveGroup(ctx, domain.AccountGroup{ID: groupID, Name: groupID, AccountIDs: []string{account.ID}, Limits: []domain.LimitRule{tokenLimit(100)}}, fixedTime); err != nil {
		t.Fatal(err)
	}
	explicit := testKey("explicit", nil)
	explicit.AccountAssignmentScopeEnabled, explicit.AssignedAccountIDs = true, []string{account.ID}
	for _, key := range []domain.APIKey{explicit, testKey("group-key", &groupID)} {
		if err := s.SaveAPIKey(ctx, key, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("UPDATE api_key_limits SET current_value=17 WHERE api_key_id='group-key'"); err != nil {
		t.Fatal(err)
	}
	job := domain.AutomationJob{ID: "scoped", Name: "scoped", Enabled: true, Model: "gpt-test",
		Schedule: domain.AutomationSchedule{Type: "daily", Time: "03:00", Timezone: "UTC"}, AccountIDs: []string{account.ID},
		CreatedAt: fixedTime, UpdatedAt: fixedTime}
	if err := s.SaveAutomationJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"pending", "running"} {
		if err := s.EnsureAutomationRun(ctx, domain.AutomationRun{ID: id, JobID: job.ID, CycleKey: id, SlotKey: id,
			Trigger: domain.AutomationTriggerScheduled, Model: "gpt-test", ScheduledFor: fixedTime, AccountID: &account.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("UPDATE automation_runs SET status='running' WHERE id='running'"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(ctx, account.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(ctx, account.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGroup(ctx, domain.AccountGroup{ID: groupID, Name: groupID, AccountIDs: []string{account.ID}}, fixedTime); !errors.Is(err, ErrInvalid) {
		t.Fatalf("stale group save restored deleted assignment: %v", err)
	}
	if err := s.SaveAPIKey(ctx, explicit, fixedTime); !errors.Is(err, ErrInvalid) {
		t.Fatalf("stale key save restored deleted assignment: %v", err)
	}
	if err := s.SaveAutomationJob(ctx, job); !errors.Is(err, ErrInvalid) {
		t.Fatalf("stale job save restored deleted assignment: %v", err)
	}
	var deleteHistory bool
	if err := s.db.QueryRow("SELECT delete_history FROM account_deletions WHERE account_id=?", account.ID).Scan(&deleteHistory); err != nil || deleteHistory {
		t.Fatalf("repeat changed deletion policy: %v %v", deleteHistory, err)
	}
	accounts, err := s.ListAccounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].ID != "other" {
		t.Fatalf("deleted account still listed: %+v %v", accounts, err)
	}
	if _, err := s.GetAccountCredential(ctx, account.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted credential available: %v", err)
	}
	var credentialCount int
	if err := s.db.QueryRow("SELECT count(*) FROM account_credentials WHERE account_id=?", account.ID).Scan(&credentialCount); err != nil || credentialCount != 0 {
		t.Fatalf("ciphertexts retained: %d %v", credentialCount, err)
	}
	loadedJob, err := s.GetAutomationJob(ctx, job.ID)
	if err != nil || loadedJob.AccountScopeAll || len(loadedJob.AccountIDs) != 0 {
		t.Fatalf("empty automation scope opened: %+v %v", loadedJob, err)
	}
	for _, id := range []string{"pending", "running"} {
		var status string
		if err := s.db.QueryRow("SELECT status FROM automation_runs WHERE id=?", id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if id == "pending" && status != "failed" || id == "running" && status != "running" {
			t.Fatalf("claim %s altered incorrectly: %s", id, status)
		}
	}
	if err := s.SaveAccount(ctx, account); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale full save resurrected tombstone: %v", err)
	}
	if err := s.SaveAccountCredential(ctx, credential); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale credential save resurrected tombstone: %v", err)
	}
	fresh := domain.Account{ID: account.ID, Kind: domain.AccountChatGPT, Provider: "openai", Email: "fresh@example.invalid", Status: domain.AccountActive}
	if err := s.SaveAccountIdentity(ctx, fresh, credential); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAccount(ctx, account.ID)
	if err != nil || got.Generation != 1 || got.Alias != "" || got.SecurityWorkAuthorized || got.LimitWarmupEnabled ||
		got.RoutingPolicy != "normal" || !got.RequiresEgressDecision || !got.CreatedAt.After(account.CreatedAt) {
		t.Fatalf("reimport retained old identity or grants: %+v %v", got, err)
	}
	if err := s.SaveAccount(ctx, account); !errors.Is(err, ErrConflict) {
		t.Fatalf("old generation replaced fresh metadata: %v", err)
	}
	if err := s.SaveAccountCredential(ctx, credential); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old generation replaced fresh credential: %v", err)
	}
	group, err := s.GetGroup(ctx, groupID)
	if err != nil || len(group.AccountIDs) != 0 {
		t.Fatalf("group membership restored: %+v %v", group, err)
	}
	for _, id := range []string{explicit.ID, "group-key"} {
		key, err := s.GetAPIKey(ctx, id)
		if err != nil || id == explicit.ID && (!key.AccountAssignmentScopeEnabled || len(key.AssignedAccountIDs) != 0) ||
			id == "group-key" && (key.GroupID == nil || *key.GroupID != groupID || len(key.Limits) != 1 || key.Limits[0].CurrentValue != 17) {
			t.Fatalf("key scope or spend changed: %+v %v", key, err)
		}
		eligible, err := s.EligibleAccounts(ctx, id)
		if err != nil && !errors.Is(err, ErrNoAccounts) || len(eligible) != 0 {
			t.Fatalf("empty key scope opened: %+v %v", eligible, err)
		}
	}
}

func TestAccountReimportWaitsForBoundedHistoryCleanup(t *testing.T) {
	s, path := testStore(t)
	ctx := context.Background()
	saveTestAccount(t, s, "large-history")
	account, _ := s.GetAccount(ctx, "large-history")
	credential, _ := s.GetAccountCredential(ctx, account.ID)
	if _, err := s.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<2005)
 INSERT INTO account_quota_history(account_id,window,observed_at,used_percent)
 SELECT 'large-history','primary',x,50 FROM n`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(ctx, account.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccountIdentity(ctx, account, credential); !errors.Is(err, ErrConflict) {
		t.Fatalf("reimport before cleanup: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for i := 0; i < 5; i++ {
		if n, err := reopened.CleanupDeletedAccounts(ctx, 1000); err != nil || n > 1000 {
			t.Fatalf("bounded cleanup: %d %v", n, err)
		}
	}
	if err := reopened.SaveAccountIdentity(ctx, account, credential); err != nil {
		t.Fatal(err)
	}
	quotas, err := reopened.ListAccountQuota(ctx, account.ID)
	if err != nil || len(quotas) != 0 {
		t.Fatalf("reimport inherited quotas: %+v %v", quotas, err)
	}
}

func TestVersion30DeletedAccountMigrationErasesAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v30.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range migrations[:30] {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO accounts(id,kind,provider,email,status,deactivation_reason,created_at)
 VALUES('deleted','chatgpt','openai','synthetic@example.invalid','deactivated','deleted',?)`, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO account_credentials(account_id,access_token_encrypted) VALUES('deleted',?)", testCiphertext()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA application_id=1129071175; PRAGMA user_version=30"); err != nil {
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
	if err := s.db.QueryRow("SELECT count(*) FROM account_credentials").Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration retained deleted credentials: %d %v", count, err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM account_deletions WHERE delete_history=0 AND cleanup_done=0").Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration lost deletion policy: %d %v", count, err)
	}
}
