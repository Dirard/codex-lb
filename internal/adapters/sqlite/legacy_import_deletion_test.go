package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"codex-lb/internal/domain"
)

func TestLegacyImportRetainsPendingDeletionWithoutCredentials(t *testing.T) {
	for _, choice := range []string{"absent", "keep", "remove"} {
		t.Run(choice, func(t *testing.T) {
			ctx := context.Background()
			source, vault, _ := legacyFixture(t)
			db, err := sql.Open("sqlite", source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE accounts SET status='deactivated',delete_requested_at='2026-09-26 00:00:00.000000',
 access_token_encrypted=X'',refresh_token_encrypted=X'',id_token_encrypted=X''`); err != nil {
				t.Fatal(err)
			}
			if choice != "absent" {
				if _, err := db.Exec("ALTER TABLE accounts ADD COLUMN delete_history_requested INTEGER NOT NULL DEFAULT 0"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("UPDATE accounts SET delete_history_requested=?", choice == "remove"); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := hashFile(source)
			if err != nil {
				t.Fatal(err)
			}
			s, _ := testStore(t)
			if _, err := s.ImportLegacySnapshot(ctx, source, vault); err != nil {
				t.Fatal(err)
			}
			after, err := hashFile(source)
			if err != nil || after != before {
				t.Fatal("pending deletion import modified source")
			}
			accounts, err := s.ListAccounts(ctx)
			if err != nil || len(accounts) != 1 || accounts[0].ID != "source-a" {
				t.Fatalf("pending deletion became visible: %+v %v", accounts, err)
			}
			if _, err := s.GetAccountCredential(ctx, "acct-a"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("pending deletion imported a credential: %v", err)
			}
			group, err := s.GetGroup(ctx, "group-a")
			if err != nil || len(group.AccountIDs) != 0 {
				t.Fatalf("pending deletion imported grants: %+v %v", group, err)
			}
			key, err := s.GetAPIKey(ctx, "key-a")
			if err != nil || len(key.Limits) != 1 || key.Limits[0].CurrentValue != 30 || key.GroupID == nil {
				t.Fatalf("pending deletion changed spend or scope: %+v %v", key, err)
			}
			var discard, done bool
			if err := s.db.QueryRow("SELECT delete_history,cleanup_done FROM account_deletions WHERE account_id='acct-a' AND generation=0").Scan(&discard, &done); err != nil || discard != (choice == "remove") || done {
				t.Fatalf("pending deletion policy lost: %v %v %v", discard, done, err)
			}
			for i := 0; i < 3; i++ {
				if _, err := s.CleanupDeletedAccounts(ctx, 1000); err != nil {
					t.Fatal(err)
				}
			}
			var raw, attributed int
			if err := s.db.QueryRow("SELECT count(*),count(account_id) FROM usage_events").Scan(&raw, &attributed); err != nil || attributed != 0 || discard && raw != 0 || !discard && raw != 2 {
				t.Fatalf("cleanup did not preserve imported choice: %d %d %v", raw, attributed, err)
			}
			ciphertext, err := vault.Encrypt([]byte("synthetic-restored-credential"))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SaveAccountIdentity(ctx, domain.Account{ID: "acct-a", Kind: domain.AccountChatGPT, Provider: "openai"},
				domain.AccountCredential{AccountID: "acct-a", AccessTokenEncrypted: ciphertext, RefreshTokenEncrypted: ciphertext, IDTokenEncrypted: ciphertext}); err != nil {
				t.Fatal(err)
			}
			account, err := s.GetAccount(ctx, "acct-a")
			if err != nil || account.Generation != 1 || !account.RequiresEgressDecision || account.SecurityWorkAuthorized || account.LimitWarmupEnabled {
				t.Fatalf("restored identity bypassed policy: %+v %v", account, err)
			}
		})
	}
}
