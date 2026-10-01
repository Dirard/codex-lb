package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"codex-lb/internal/domain"
)

func TestSetAccountGroupsReplacesOnlyAccountMembership(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct-a")
	saveTestAccount(t, s, "acct-b")
	groupA, groupB, groupC := "group-a", "group-b", "group-c"
	groups := []domain.AccountGroup{
		{ID: groupA, Name: "A", AccountIDs: []string{"acct-a", "acct-b"}, Limits: []domain.LimitRule{tokenLimit(100)}},
		{ID: groupB, Name: "B", AccountIDs: []string{"acct-b"}, Limits: []domain.LimitRule{tokenLimit(50)}},
		{ID: groupC, Name: "C", AccountIDs: []string{}, Limits: []domain.LimitRule{tokenLimit(30)}},
	}
	for _, group := range groups {
		if err := s.SaveGroup(ctx, group, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	key := testKey("key-group-a", &groupA)
	if err := s.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	reservation, err := s.ReserveUsage(ctx, domain.ReservationRequest{
		ID: "reservation-groups", APIKeyID: key.ID, AccountID: "acct-a",
		Model: "gpt-test", Budget: domain.UsageAmount{InputTokens: 40}, Now: fixedTime,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetAccountGroups(ctx, "acct-a", []string{groupB, groupC}); err != nil {
		t.Fatal(err)
	}
	assertGroupAccounts(t, s, groupA, []string{"acct-b"})
	assertGroupAccounts(t, s, groupB, []string{"acct-a", "acct-b"})
	assertGroupAccounts(t, s, groupC, []string{"acct-a"})

	if err := s.SetAccountGroups(ctx, "acct-a", []string{groupA, "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown group error = %v", err)
	}
	assertGroupAccounts(t, s, groupA, []string{"acct-b"})
	assertGroupAccounts(t, s, groupB, []string{"acct-a", "acct-b"})

	storedKey, err := s.GetAPIKey(ctx, key.ID)
	if err != nil || storedKey.GroupID == nil || *storedKey.GroupID != groupA ||
		len(storedKey.Limits) != 1 || storedKey.Limits[0].MaxValue != 100 || storedKey.Limits[0].CurrentValue != 40 {
		t.Fatalf("group key changed: %+v, %v", storedKey, err)
	}
	if stored, err := s.GetReservation(ctx, reservation.ID); err != nil || stored.ID != reservation.ID {
		t.Fatalf("reservation changed: %+v, %v", stored, err)
	}
	for _, group := range groups {
		stored, err := s.GetGroup(ctx, group.ID)
		if err != nil || !reflect.DeepEqual(stored.Limits, group.Limits) {
			t.Fatalf("limits changed for %s: %+v, %v", group.ID, stored.Limits, err)
		}
	}

	if err := s.SetAccountGroups(ctx, "acct-a", []string{}); err != nil {
		t.Fatal(err)
	}
	assertGroupAccounts(t, s, groupA, []string{"acct-b"})
	assertGroupAccounts(t, s, groupB, []string{"acct-b"})
	assertGroupAccounts(t, s, groupC, []string{})
}

func TestSetAccountGroupsValidation(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct-a")
	if err := s.SaveGroup(ctx, domain.AccountGroup{ID: "group-a", Name: "A"}, fixedTime); err != nil {
		t.Fatal(err)
	}
	for name, groupIDs := range map[string][]string{
		"missing array": nil,
		"empty id":      {""},
		"duplicate":     {"group-a", "group-a"},
	} {
		if err := s.SetAccountGroups(ctx, "acct-a", groupIDs); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s error = %v", name, err)
		}
	}
	if err := s.SetAccountGroups(ctx, "missing", []string{"group-a"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown account error = %v", err)
	}
}

func TestSetAccountGroupsRejectsOversizeUpdateBeforeSQL(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct-a")
	if err := s.SaveGroup(ctx, domain.AccountGroup{
		ID: "group-a", Name: "A", AccountIDs: []string{"acct-a"},
	}, fixedTime); err != nil {
		t.Fatal(err)
	}
	groupIDs := make([]string, maxAccountGroupMemberships+1)
	for i := range groupIDs {
		groupIDs[i] = fmt.Sprintf("missing-%d", i)
	}

	if err := s.SetAccountGroups(ctx, "acct-a", groupIDs); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize error = %v", err)
	}
	assertGroupAccounts(t, s, "group-a", []string{"acct-a"})
}

func assertGroupAccounts(t *testing.T, s *Store, groupID string, want []string) {
	t.Helper()
	group, err := s.GetGroup(context.Background(), groupID)
	if err != nil || !reflect.DeepEqual(group.AccountIDs, want) {
		t.Fatalf("group %s accounts = %v, want %v, err %v", groupID, group.AccountIDs, want, err)
	}
}
