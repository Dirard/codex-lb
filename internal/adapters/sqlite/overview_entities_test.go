package sqlite

import (
	"context"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// seedEntityFixture creates two groups, a deleted and an external account,
// grouped, ungrouped, disabled, expired, deleted and internal keys.
func seedEntityFixture(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []string{"acct-a", "acct-b", "acct-gone"} {
		saveTestAccount(t, s, id)
	}
	if err := s.SaveAccount(ctx, domain.Account{ID: "ext", Kind: domain.AccountExternal, Provider: "openai", BaseURL: "https://upstream.example.invalid",
		Email: "ext@example.invalid", Alias: "External", Status: domain.AccountActive, CreatedAt: fixedTime}); err != nil {
		t.Fatal(err)
	}
	for _, group := range []domain.AccountGroup{{ID: "grp-1", Name: "One", AccountIDs: []string{"acct-a", "acct-gone"}},
		{ID: "grp-2", Name: "Two", AccountIDs: []string{"acct-a", "acct-b"}}} {
		if err := s.SaveGroup(ctx, group, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	g1 := "grp-1"
	expired := fixedTime.Add(-time.Hour)
	for _, key := range []domain.APIKey{testKey("key-grouped", &g1), testKey("key-free", nil), testKey("key-off", &g1),
		testKey("key-expired", nil), testKey("key-deleted", &g1)} {
		switch key.ID {
		case "key-off":
			key.IsActive = false
		case "key-expired":
			key.ExpiresAt = &expired
		case "key-free":
			key.Limits = []domain.LimitRule{tokenLimit(100)}
		}
		if err := s.SaveAPIKey(ctx, key, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteAPIKey(ctx, "key-deleted"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO api_keys(id,name,key_hash,key_prefix,created_at) VALUES(?,?,?,?,?)`,
		domain.WarmupKeyID, "warmup", "ff", "sk-warm", millis(fixedTime)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(ctx, "acct-gone", false); err != nil {
		t.Fatal(err)
	}
}

// The directory lists only non-deleted, non-internal keys with their status,
// counts only non-deleted member accounts and keys per group, and labels
// accounts by alias, then e-mail (R3).
func TestDirectoryExcludesDeletedAndInternalEntries(t *testing.T) {
	s, _ := testStore(t)
	seedEntityFixture(t, s)
	directory, err := s.Directory(context.Background(), fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, key := range directory.Keys {
		status[key.ID] = key.Status
	}
	want := map[string]string{"key-grouped": "active", "key-free": "active", "key-off": "disabled", "key-expired": "expired"}
	if len(status) != len(want) {
		t.Fatalf("keys %v, want %v", status, want)
	}
	for id, value := range want {
		if status[id] != value {
			t.Fatalf("key %s status %q, want %q", id, status[id], value)
		}
	}
	counts := map[string][2]int{}
	for _, group := range directory.Groups {
		counts[group.ID] = [2]int{group.AccountCount, group.KeyCount}
	}
	if counts["grp-1"] != [2]int{1, 2} || counts["grp-2"] != [2]int{2, 0} {
		t.Fatalf("group counts %v", counts)
	}
	labels := map[string]string{}
	for _, account := range directory.Accounts {
		labels[account.ID] = account.Label
	}
	if len(labels) != 3 || labels["ext"] != "External" || labels["acct-a"] != "acct-a@example.invalid" || labels["acct-gone"] != "" {
		t.Fatalf("accounts %v", labels)
	}
}

// The entity snapshot projects expired limit windows without writes and
// carries memberships of non-deleted accounts only.
func TestOverviewEntitiesProjectLimitsAndMemberships(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	seedEntityFixture(t, s)
	if _, err := s.db.Exec(`UPDATE api_key_limits SET current_value=60,reset_at=? WHERE api_key_id='key-free'`, millis(fixedTime.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	entities, err := s.OverviewEntities(ctx, fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]domain.OverviewKey{}
	for _, key := range entities.Keys {
		keys[key.ID] = key
	}
	if len(keys) != 4 || keys["key-deleted"].ID != "" {
		t.Fatalf("keys %v", keys)
	}
	free := keys["key-free"].Limits
	if len(free) != 1 || free[0].CurrentValue != 0 || !free[0].ResetAt.After(fixedTime) {
		t.Fatalf("expired window must project to zero with the advanced reset: %+v", free)
	}
	var stored int64
	if err := s.db.QueryRow(`SELECT current_value FROM api_key_limits WHERE api_key_id='key-free'`).Scan(&stored); err != nil || stored != 60 {
		t.Fatalf("projection must not write: %d %v", stored, err)
	}
	groups := map[string][]string{}
	for _, account := range entities.Accounts {
		groups[account.Account.ID] = account.GroupIDs
	}
	if len(groups) != 3 || len(groups["acct-a"]) != 2 || len(groups["acct-b"]) != 1 {
		t.Fatalf("memberships %v", groups)
	}
	if len(entities.Groups) != 2 || entities.Groups[0].Name != "One" {
		t.Fatalf("groups %+v", entities.Groups)
	}
}

// The shell snapshot's sync time is the newest quota observation of
// non-deleted ChatGPT accounts, and fresh rollups are usable.
func TestShellSnapshotSyncTimeAndRollupState(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	seedEntityFixture(t, s)
	for id, observed := range map[string]time.Time{"acct-a": fixedTime.Add(-2 * time.Minute), "acct-b": fixedTime.Add(-time.Minute),
		"ext": fixedTime, "acct-gone": fixedTime.Add(time.Minute)} {
		if _, err := s.db.Exec(`INSERT INTO account_quotas(account_id,window,used_percent,observed_at) VALUES(?, 'primary', 10, ?)`, id, millis(observed)); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := s.ShellSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LastSyncAt == nil || !snapshot.LastSyncAt.Equal(fixedTime.Add(-time.Minute)) || len(snapshot.Accounts) != 3 || !snapshot.RollupsUsable {
		t.Fatalf("snapshot %+v", snapshot)
	}
}
