package sqlite

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// bulkTwins creates ungrouped keys with their own limits and identical usage
// in the last day, so that saving and bulk writes backfill the same values,
// and a group with limit templates.
func bulkTwins(t *testing.T, s *Store, now time.Time, ids ...string) {
	t.Helper()
	ctx := context.Background()
	own := []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowDaily, MaxValue: 10_000}}
	for i, id := range ids {
		if err := s.SaveAPIKey(ctx, domain.APIKey{ID: id, Name: "Twin " + id, KeyHash: fmt.Sprintf("%064x", 100+i), KeyPrefix: "sk-clb-" + id,
			IsActive: true, Limits: own, CreatedAt: now.Add(-48 * time.Hour)}, now.Add(-48*time.Hour)); err != nil {
			t.Fatal(err)
		}
		for e := range 3 {
			if _, err := s.db.Exec(`INSERT INTO usage_events(request_id,api_key_id,model,status,request_kind,requested_at,
 input_tokens,output_tokens,cost_microdollars) VALUES(?,?,'m','success','normal',?,100,20,250000)`,
				fmt.Sprintf("%s-%d", id, e), id, millis(now.Add(-time.Duration(e+1)*time.Hour))); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.Exec(`UPDATE api_keys SET last_used_at=? WHERE id=?`, millis(now.Add(-time.Minute)), id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveGroup(ctx, domain.AccountGroup{ID: "grp-bulk", Name: "Bulk", Limits: []domain.LimitRule{
		{Type: domain.LimitCostUSD, Window: domain.WindowDaily, MaxValue: 5_000_000},
		{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 90_000},
	}}, now); err != nil {
		t.Fatal(err)
	}
}

// sameKeyEffect compares what a bulk write and SaveAPIKey leave behind.
func sameKeyEffect(t *testing.T, s *Store, bulk, saved string) domain.APIKey {
	t.Helper()
	a, err := s.GetAPIKey(context.Background(), bulk)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.GetAPIKey(context.Background(), saved)
	if err != nil {
		t.Fatal(err)
	}
	strip := func(rules []domain.LimitRule) []domain.LimitRule {
		out := make([]domain.LimitRule, len(rules))
		for i, rule := range rules {
			rule.ID = 0
			out[i] = rule
		}
		return out
	}
	group := func(id *string) string {
		if id == nil {
			return ""
		}
		return *id
	}
	if group(a.GroupID) != group(b.GroupID) || a.AccountAssignmentScopeEnabled != b.AccountAssignmentScopeEnabled || !slices.Equal(strip(a.Limits), strip(b.Limits)) {
		t.Fatalf("bulk write differs from saving:\n bulk %+v %v\nsaved %+v %v", a.GroupID, a.Limits, b.GroupID, b.Limits)
	}
	return a
}

// A bulk move applies the group's limit templates, backfilled from the key's
// events, and the account-assignment scope exactly as saving the key with
// that group does, keeps every other column including the last use, and
// leaving the group keeps the last effective limits (R38).
func TestMoveAPIKeyToGroupHasTheEffectOfSavingTheKey(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	now := time.Now().UTC()
	bulkTwins(t, s, now, "bulk", "saved")
	group := "grp-bulk"
	if err := s.MoveAPIKeyToGroup(ctx, "bulk", &group, now); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetAPIKey(ctx, "saved")
	if err != nil {
		t.Fatal(err)
	}
	saved.GroupID, saved.AccountAssignmentScopeEnabled = &group, true
	if err := s.SaveAPIKey(ctx, saved, now); err != nil {
		t.Fatal(err)
	}
	moved := sameKeyEffect(t, s, "bulk", "saved")
	if len(moved.Limits) != 2 || moved.Limits[0].CurrentValue != 750_000 || moved.Limits[1].CurrentValue != 360 ||
		moved.Name != "Twin bulk" || moved.LastUsedAt == nil || !moved.LastUsedAt.Equal(now.Add(-time.Minute).Truncate(time.Millisecond)) {
		t.Fatalf("moved key %+v", moved)
	}
	if err := s.MoveAPIKeyToGroup(ctx, "bulk", nil, now); err != nil {
		t.Fatal(err)
	}
	left, err := s.GetAPIKey(ctx, "bulk")
	if err != nil {
		t.Fatal(err)
	}
	if left.GroupID != nil || len(left.Limits) != 2 || left.Limits[0].MaxValue != 5_000_000 {
		t.Fatalf("leaving the group: %+v %+v", left.GroupID, left.Limits)
	}
	missing := "grp-missing"
	for name, test := range map[string]struct {
		id    string
		group *string
		want  error
	}{
		"unknown group": {"bulk", &missing, domain.ErrUnknownGroup},
		"unknown key":   {"nobody", &group, ErrNotFound},
		"internal key":  {domain.LocalProxyKeyID, &group, ErrNotFound},
	} {
		if err := s.MoveAPIKeyToGroup(ctx, test.id, test.group, now); !errors.Is(err, test.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := s.DeleteAPIKey(ctx, "saved"); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveAPIKeyToGroup(ctx, "saved", &group, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted key: %v", err)
	}
}

// Bulk limits replace an ungrouped key's limits exactly as saving it with
// those limits does, an empty list removes them, and a grouped key keeps its
// group limits with ErrKeyInGroup (R38).
func TestSetAPIKeyLimitsHasTheEffectOfSavingTheKey(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	now := time.Now().UTC()
	bulkTwins(t, s, now, "bulk", "saved", "team")
	rules := []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: 9_000_000},
		{Type: domain.LimitOutputTokens, Window: domain.WindowDaily, MaxValue: 400}}
	if err := s.SetAPIKeyLimits(ctx, "bulk", rules, now); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetAPIKey(ctx, "saved")
	if err != nil {
		t.Fatal(err)
	}
	saved.Limits = rules
	if err := s.SaveAPIKey(ctx, saved, now); err != nil {
		t.Fatal(err)
	}
	if key := sameKeyEffect(t, s, "bulk", "saved"); len(key.Limits) != 2 || key.Limits[0].CurrentValue != 750_000 {
		t.Fatalf("limits %+v", key.Limits)
	}
	group := "grp-bulk"
	if err := s.MoveAPIKeyToGroup(ctx, "team", &group, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAPIKeyLimits(ctx, "team", rules, now); !errors.Is(err, domain.ErrKeyInGroup) {
		t.Fatalf("grouped key: %v", err)
	}
	if team, err := s.GetAPIKey(ctx, "team"); err != nil || len(team.Limits) != 2 || team.Limits[0].MaxValue != 5_000_000 {
		t.Fatalf("grouped key limits changed: %v %+v", err, team.Limits)
	}
	if err := s.SetAPIKeyLimits(ctx, "bulk", []domain.LimitRule{}, now); err != nil {
		t.Fatal(err)
	}
	if key, err := s.GetAPIKey(ctx, "bulk"); err != nil || len(key.Limits) != 0 {
		t.Fatalf("cleared limits: %v %+v", err, key.Limits)
	}
	duplicate := append(slices.Clone(rules), rules[0])
	if err := s.SetAPIKeyLimits(ctx, "bulk", duplicate, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate rules: %v", err)
	}
	if err := s.SetAPIKeyLimits(ctx, domain.WarmupKeyID, rules, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("internal key: %v", err)
	}
}

// One job changes the active state of the named keys only and reports
// unknown, deleted and internal ids as not found in request order (R36).
func TestSetAPIKeysActiveReportsEachID(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	now := time.Now().UTC()
	bulkTwins(t, s, now, "a", "b", "gone")
	if err := s.DeleteAPIKey(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	found, err := s.SetAPIKeysActive(ctx, []string{"b", "gone", domain.LocalProxyKeyID, "nobody", "a"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(found, []bool{true, false, false, false, true}) {
		t.Fatalf("found %v", found)
	}
	for _, id := range []string{"a", "b"} {
		key, err := s.GetAPIKey(ctx, id)
		if err != nil || key.IsActive || len(key.Limits) != 1 || key.LastUsedAt == nil {
			t.Fatalf("%s after disable: %v %+v", id, err, key)
		}
	}
	if found, err := s.SetAPIKeysActive(ctx, []string{"a"}, true); err != nil || !found[0] {
		t.Fatalf("enable: %v %v", err, found)
	}
	if key, _ := s.GetAPIKey(ctx, "a"); !key.IsActive {
		t.Fatal("key a stays disabled")
	}
	if _, err := s.SetAPIKeysActive(ctx, nil, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty job: %v", err)
	}
}
