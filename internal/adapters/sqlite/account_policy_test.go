package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestAccountPolicyWritesPreserveFreshStatusAndIdentity(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct")
	stale, err := s.GetAccount(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	refresh := time.Now().UTC()
	fresh := stale
	fresh.Status = domain.AccountQuotaExceeded
	fresh.LastRefresh = &refresh
	if err := s.SaveAccount(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAccountAlias(ctx, "acct", "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAccountWarmup(ctx, "acct", true); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAccountRoutingPolicy(ctx, "acct", "preserve"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAccountSecurityAuthorization(ctx, "acct", true); err != nil {
		t.Fatal(err)
	}
	actual, err := s.GetAccount(ctx, "acct")
	if err != nil || actual.Status != domain.AccountQuotaExceeded || actual.LastRefresh == nil ||
		!actual.LastRefresh.Equal(refresh.Truncate(time.Millisecond)) || actual.Alias != "renamed" ||
		!actual.LimitWarmupEnabled || actual.RoutingPolicy != "preserve" || !actual.SecurityWorkAuthorized {
		t.Fatalf("policy update rewrote status/identity: %+v, %v", actual, err)
	}
	if err := s.TransitionAccountStatus(ctx, "acct", stale.Status, stale.DeactivationReason, domain.AccountPaused); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale pause overwrote quota: %v", err)
	}
	if err := s.TransitionAccountStatus(ctx, "acct", domain.AccountQuotaExceeded, "", domain.AccountPaused); err != nil {
		t.Fatal(err)
	}
	actual, err = s.GetAccount(ctx, "acct")
	if err != nil || actual.Status != domain.AccountPaused {
		t.Fatalf("pause failed: %+v, %v", actual, err)
	}
	if err := s.TransitionAccountStatus(ctx, "acct", domain.AccountPaused, "", domain.AccountActive); err != nil {
		t.Fatal(err)
	}
	actual, err = s.GetAccount(ctx, "acct")
	if err != nil || actual.Status != domain.AccountActive {
		t.Fatalf("reactivate failed: %+v, %v", actual, err)
	}
	if err := s.DeleteAccount(ctx, "acct", false); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAccountAlias(ctx, "acct", "resurrect"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted account policy changed: %v", err)
	}
}
