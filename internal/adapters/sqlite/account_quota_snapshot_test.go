package sqlite

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestCompleteQuotaSnapshotReplacesWindowsAndRepairsWeeklyHistory(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	for _, id := range []string{"weekly", "other"} {
		saveTestAccount(t, store, id)
	}
	base := time.Now().UTC().Add(-time.Hour)
	week, fiveHour := 10080, 300
	for _, quota := range []domain.AccountQuota{
		{AccountID: "weekly", Window: "primary", UsedPercent: 30, WindowMinutes: &week, ObservedAt: base},
		{AccountID: "weekly", Window: "monthly", UsedPercent: 5, ObservedAt: base},
		{AccountID: "other", Window: "primary", UsedPercent: 8, WindowMinutes: &fiveHour, ObservedAt: base},
	} {
		if err := store.SaveAccountQuota(ctx, quota); err != nil {
			t.Fatal(err)
		}
	}
	at := base.Add(time.Minute)
	snapshot := domain.AccountUsageSnapshot{AccountID: "weekly", ObservedAt: at, ReplaceQuotaWindows: true,
		Quotas: []domain.AccountQuota{{AccountID: "weekly", Window: "secondary", UsedPercent: 41, WindowMinutes: &week, ObservedAt: at}}}
	if err := store.SaveAccountUsageSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	quotas, err := store.ListAccountQuota(ctx, "weekly")
	if err != nil || len(quotas) != 1 || quotas[0].Window != "secondary" || quotas[0].UsedPercent != 41 {
		t.Fatalf("current quota set was not replaced: %+v %v", quotas, err)
	}
	var wrong, preserved int
	if err := store.readDB.QueryRowContext(ctx, "SELECT count(*) FROM account_quota_history WHERE account_id='weekly' AND window='primary' AND window_minutes=10080").Scan(&wrong); err != nil || wrong != 0 {
		t.Fatal("mislabeled weekly history was not corrected")
	}
	if err := store.readDB.QueryRowContext(ctx, "SELECT count(*) FROM account_quota_history WHERE account_id='weekly' AND window='secondary' AND used_percent=30 AND observed_at=?", millis(base)).Scan(&preserved); err != nil || preserved != 1 {
		t.Fatal("history correction did not preserve the original observation")
	}
	other, err := store.ListAccountQuota(ctx, "other")
	if err != nil || len(other) != 1 || other[0].Window != "primary" || other[0].UsedPercent != 8 {
		t.Fatal("replacement crossed the account boundary")
	}
	// A partial/absent observation does not mean that a quota ceased to exist.
	if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: "weekly", ObservedAt: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if quotas, err = store.ListAccountQuota(ctx, "weekly"); err != nil || len(quotas) != 1 || quotas[0].UsedPercent != 41 {
		t.Fatal("absent observation erased known quota")
	}
	if err := store.SaveAccountUsageSnapshot(ctx, snapshot); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale observation was not rejected: %v", err)
	}
	// An invalid write after replacement must roll back both deletion and metadata.
	badAt := at.Add(2 * time.Second)
	bad := domain.AccountUsageSnapshot{AccountID: "weekly", ObservedAt: badAt, ReplaceQuotaWindows: true,
		Quotas: []domain.AccountQuota{{AccountID: "weekly", Window: "primary", UsedPercent: math.NaN(), ObservedAt: badAt}}}
	if err := store.SaveAccountUsageSnapshot(ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid snapshot was accepted: %v", err)
	}
	if quotas, err = store.ListAccountQuota(ctx, "weekly"); err != nil || len(quotas) != 1 || quotas[0].Window != "secondary" || quotas[0].UsedPercent != 41 {
		t.Fatal("invalid snapshot deleted previous quota")
	}
	if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: "weekly", ObservedAt: badAt, ReplaceQuotaWindows: true}); !errors.Is(err, ErrInvalid) {
		t.Fatal("an empty replacement was accepted")
	}
}

func TestCompleteQuotaSnapshotPreservesNewerDirectObservation(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	now := time.Now().UTC()
	if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "acct", Window: "primary", UsedPercent: 77, ObservedAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: "acct", ObservedAt: now, ReplaceQuotaWindows: true,
		Quotas: []domain.AccountQuota{{AccountID: "acct", Window: "secondary", UsedPercent: 41, ObservedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	quotas, err := store.ListAccountQuota(ctx, "acct")
	if err != nil || len(quotas) != 2 || quotas[0].Window != "primary" || quotas[0].UsedPercent != 77 {
		t.Fatal("older complete observation erased a newer window")
	}
}

func TestCompleteQuotaSnapshotWaitsForPlanConfirmation(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	account, err := store.GetAccount(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.GetAccountCredential(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "acct", Window: "primary", UsedPercent: 40, ObservedAt: base}); err != nil {
		t.Fatal(err)
	}
	observation := func(at time.Time) domain.AccountUsageSnapshot {
		return domain.AccountUsageSnapshot{AccountID: "acct", ObservedAt: at, ExpectedAccount: &account, ExpectedCredential: &credential,
			ReportedPlanType: "free", ReplaceQuotaWindows: true,
			Quotas: []domain.AccountQuota{{AccountID: "acct", Window: "monthly", UsedPercent: 5, ObservedAt: at}}}
	}
	if err := store.SaveAccountUsageSnapshot(ctx, observation(base.Add(time.Second))); !errors.Is(err, domain.ErrPlanConfirmationPending) {
		t.Fatalf("first downgrade did not require confirmation: %v", err)
	}
	quotas, err := store.ListAccountQuota(ctx, "acct")
	if err != nil || len(quotas) != 1 || quotas[0].Window != "primary" || quotas[0].UsedPercent != 40 {
		t.Fatal("unconfirmed downgrade replaced current quota")
	}
	if err := store.SaveAccountUsageSnapshot(ctx, observation(base.Add(2*time.Second))); err != nil {
		t.Fatal(err)
	}
	if quotas, err = store.ListAccountQuota(ctx, "acct"); err != nil || len(quotas) != 1 || quotas[0].Window != "monthly" {
		t.Fatal("confirmed monthly plan retained an obsolete quota")
	}
}
