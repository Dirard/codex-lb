package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

func testFork(id, parent string) domain.APIKey {
	fork := testKey(id, nil)
	fork.KeyHash = domain.ForkKeyHash(domain.ForkSecretPrefix + id)
	fork.Fork = &domain.KeyFork{ParentID: parent, CreatedBy: domain.KeyActorOwner, UpdatedBy: domain.KeyActorOwner}
	return fork
}

// Schema 50 keys gain no parent, and a fork secret is unknown to code that
// hashes secrets the way schema 50 code does.
func TestMigration51AndForkSecretsFailClosedForOlderCode(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v50.sqlite")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "_synchronous=0"}).String())
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:50] {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		fmt.Sprintf("INSERT INTO api_keys(id,name,key_hash,key_prefix,created_at) VALUES('old','Old','%064x','sk-clb-old',0)", 9),
		fmt.Sprintf("PRAGMA application_id=%d", applicationID), "PRAGMA user_version=50",
	} {
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
	old, err := store.GetAPIKey(ctx, "old")
	if err != nil || old.Fork != nil {
		t.Fatalf("migrated key %+v %v", old.Fork, err)
	}
	fork := testFork("child", "old")
	if err := store.SaveAPIKey(ctx, fork, fixedTime); err != nil {
		t.Fatal(err)
	}
	secret := domain.ForkSecretPrefix + "child"
	if _, err := store.FindAPIKeyByHash(ctx, domain.APIKeyHash(secret)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("plain hash of a fork secret found a key: %v", err)
	}
	if found, err := store.FindAPIKeyByHash(ctx, domain.ForkKeyHash(secret)); err != nil || found.ID != "child" || found.Fork.ParentName != "Old" {
		t.Fatalf("fork lookup %+v %v", found.Fork, err)
	}
}

func TestForkParentIsFixedAndForksStayFlat(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	group := "group"
	if err := s.SaveGroup(ctx, domain.AccountGroup{ID: group, Name: "Group"}, fixedTime); err != nil {
		t.Fatal(err)
	}
	for _, key := range []domain.APIKey{testKey("parent", nil), testKey("other", nil)} {
		if err := s.SaveAPIKey(ctx, key, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveAPIKey(ctx, testFork("fork", "parent"), fixedTime); err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]domain.APIKey{
		"reparented":      testFork("fork", "other"),
		"no longer fork":  testKey("fork", nil),
		"parent to fork":  testFork("parent", "other"),
		"grandchild":      testFork("grandchild", "fork"),
		"unknown parent":  testFork("orphan", "missing"),
		"internal parent": testFork("internal", domain.LocalProxyKeyID),
	} {
		if err := s.SaveAPIKey(ctx, key, fixedTime); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s saved: %v", name, err)
		}
	}
	// A fork's scope is its parent's: a group sent for it is not stored, and
	// a group move is not applied.
	fork := testFork("fork", "parent")
	fork.GroupID = &group
	if err := s.SaveAPIKey(ctx, fork, fixedTime); err != nil {
		t.Fatal(err)
	}
	if stored, err := s.GetStoredAPIKey(ctx, "fork"); err != nil || stored.GroupID != nil {
		t.Fatalf("fork group %v %v", stored.GroupID, err)
	}
	if err := s.MoveAPIKeyToGroup(ctx, "fork", &group, fixedTime); !errors.Is(err, domain.ErrKeyIsFork) {
		t.Fatalf("group move of a fork: %v", err)
	}
	parent, _ := s.GetStoredAPIKey(ctx, "parent")
	parent.GroupID, parent.AccountAssignmentScopeEnabled = &group, true
	if err := s.SaveAPIKey(ctx, parent, fixedTime); err != nil {
		t.Fatal(err)
	}
	if served, err := s.GetAPIKey(ctx, "fork"); err != nil || served.GroupID == nil || *served.GroupID != group {
		t.Fatalf("fork does not serve in its parent's group: %+v %v", served.GroupID, err)
	}
	// Deleting a parent deletes its forks with it.
	if err := s.DeleteAPIKey(ctx, "parent"); err != nil {
		t.Fatal(err)
	}
	var deleted sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT deleted_at FROM api_keys WHERE id='fork'`).Scan(&deleted); err != nil || !deleted.Valid {
		t.Fatalf("fork kept after its parent was deleted: %v %v", deleted, err)
	}
}

// A fork's reservation takes budget on its parent's limits; settlement and
// repricing adjust them through the reservation items, and a new parent
// limit backfills the fork's usage.
func TestForkUsageCountsOnItsParentsLimits(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct")
	parent := testKey("parent", nil)
	parent.Limits = []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: 1_000_000}}
	if err := s.SaveAPIKey(ctx, parent, fixedTime); err != nil {
		t.Fatal(err)
	}
	fork := testFork("fork", "parent")
	fork.Limits = []domain.LimitRule{tokenLimit(1000)}
	if err := s.SaveAPIKey(ctx, fork, fixedTime); err != nil {
		t.Fatal(err)
	}
	reserve := domain.ReservationRequest{ID: "reservation", APIKeyID: "fork", AccountID: "acct", Model: "gpt-6-sol",
		Budget: domain.UsageAmount{InputTokens: 100, OutputTokens: 100, CostMicrodollars: 500}, Now: fixedTime}
	if _, err := s.ReserveUsage(ctx, reserve); err != nil {
		t.Fatal(err)
	}
	limitValue := func(key string) int64 {
		t.Helper()
		var value int64
		if err := s.db.QueryRowContext(ctx, `SELECT current_value FROM api_key_limits WHERE api_key_id=?`, key).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if limitValue("parent") != 500 || limitValue("fork") != 200 {
		t.Fatalf("reserved parent %d fork %d", limitValue("parent"), limitValue("fork"))
	}
	event := domain.UsageEvent{RequestID: "request", APIKeyID: "fork", AccountID: "acct", Model: "gpt-6-sol", RequestKind: "normal",
		Status: "success", RequestedAt: fixedTime, Usage: domain.UsageAmount{InputTokens: 10}}
	if applied, err := s.SettleUsage(ctx, "reservation", domain.UsageSettlement{Status: "finalized", Event: event}); err != nil || !applied {
		t.Fatalf("settle %v %v", applied, err)
	}
	var cost int64
	if err := s.db.QueryRowContext(ctx, `SELECT cost_microdollars FROM usage_events WHERE request_id='request'`).Scan(&cost); err != nil || cost <= 0 {
		t.Fatalf("event cost %d %v", cost, err)
	}
	if limitValue("parent") != cost || limitValue("fork") != 10 {
		t.Fatalf("settled parent %d (cost %d) fork %d", limitValue("parent"), cost, limitValue("fork"))
	}
	service := application.NewModelPricingService(s, func() time.Time { return fixedTime })
	if _, err := service.Save(ctx, "gpt-6-sol", pricing.Price{Standard: pricing.Rates{Input: 50_000_000}}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT cost_microdollars FROM usage_events WHERE request_id='request'`).Scan(&cost); err != nil || cost != 500 {
		t.Fatalf("repriced cost %d %v", cost, err)
	}
	if limitValue("parent") != 500 {
		t.Fatalf("repricing left the parent's limit at %d", limitValue("parent"))
	}
	// A limit the parent gains later counts what its fork already used.
	parent, _ = s.GetStoredAPIKey(ctx, "parent")
	parent.Limits = append(parent.Limits, domain.LimitRule{Type: domain.LimitInputTokens, Window: domain.WindowWeekly, MaxValue: 1000})
	if err := s.SaveAPIKey(ctx, parent, fixedTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var backfilled int64
	if err := s.db.QueryRowContext(ctx, `SELECT current_value FROM api_key_limits WHERE api_key_id='parent' AND limit_type='input_tokens'`).Scan(&backfilled); err != nil || backfilled != 10 {
		t.Fatalf("backfilled %d %v", backfilled, err)
	}
}

func TestForkServesInItsParentsScope(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "a")
	saveTestAccount(t, s, "b")
	parent := testKey("parent", nil)
	parent.AccountAssignmentScopeEnabled, parent.AssignedAccountIDs = true, []string{"b"}
	if err := s.SaveAPIKey(ctx, parent, fixedTime); err != nil {
		t.Fatal(err)
	}
	fork := testFork("fork", "parent")
	fork.AccountAssignmentScopeEnabled, fork.AssignedAccountIDs = true, []string{"a"}
	if err := s.SaveAPIKey(ctx, fork, fixedTime); err != nil {
		t.Fatal(err)
	}
	accounts, err := s.EligibleAccounts(ctx, "fork")
	if err != nil || len(accounts) != 1 || accounts[0].ID != "b" {
		t.Fatalf("fork accounts %+v %v", accounts, err)
	}
	if _, err := s.ScopedAccountForOwner(ctx, "fork", "a", time.Now()); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("fork owner outside its parent's scope: %v", err)
	}
	if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: "r", APIKeyID: "fork", AccountID: "a", Model: "m", Budget: domain.UsageAmount{InputTokens: 1}, Now: time.Now()}); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("reservation outside the parent's scope: %v", err)
	}
	parent, _ = s.GetStoredAPIKey(ctx, "parent")
	parent.IsActive = false
	if err := s.SaveAPIKey(ctx, parent, fixedTime); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EligibleAccounts(ctx, "fork"); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("fork of a disabled parent: %v", err)
	}
	entities, err := s.OverviewEntities(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range entities.Keys {
		if key.ID == "fork" && (key.IsActive || key.ParentID == nil || *key.ParentID != "parent") {
			t.Fatalf("overview fork %+v", key)
		}
	}
}
