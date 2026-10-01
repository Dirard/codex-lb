package sqlite

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestAccountUsageMetadataPreservesNewestExplicitEvidenceAndClear(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	account := domain.Account{ID: "acct", Kind: domain.AccountChatGPT, Provider: "openai", Email: "a@example.invalid", PlanType: "pro", Status: domain.AccountActive, CreatedAt: now}
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	reset := now.Add(time.Hour)
	has, no := true, false
	positive, zero := 12.5, 0.0
	initial := domain.AccountUsageSnapshot{AccountID: account.ID, ObservedAt: now, AdditionalReported: true,
		Credits: &domain.AccountCreditStatus{AccountID: account.ID, Has: &has, Balance: &positive, ObservedAt: now},
		AdditionalQuotas: []domain.AccountAdditionalQuota{{AccountID: account.ID, QuotaKey: "codex_spark", Window: "primary",
			UsedPercent: 10, ResetAt: &reset, ObservedAt: now}}}
	if err := store.SaveAccountUsageSnapshot(ctx, initial); err != nil {
		t.Fatal(err)
	}
	stale := domain.AccountUsageSnapshot{AccountID: account.ID, ObservedAt: now.Add(-time.Minute), AdditionalReported: true,
		Credits: &domain.AccountCreditStatus{AccountID: account.ID, Has: &no, Balance: &zero, ObservedAt: now.Add(-time.Minute)}}
	if err := store.SaveAccountUsageSnapshot(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("older in-flight sample was not fenced: %v", err)
	}
	credit, err := store.LoadAccountCreditStatus(ctx, account.ID)
	additional, additionalErr := store.ListAccountAdditionalQuotas(ctx, account.ID)
	if err != nil || additionalErr != nil || credit == nil || !credit.Usable() || len(additional) != 1 {
		t.Fatalf("stale sample overwrote metadata: credit=%+v additional=%+v errors=%v/%v", credit, additional, err, additionalErr)
	}
	newer := domain.AccountUsageSnapshot{AccountID: account.ID, ObservedAt: now.Add(time.Minute),
		Credits: &domain.AccountCreditStatus{AccountID: account.ID, Has: &no, Balance: &zero, ObservedAt: now.Add(time.Minute)}}
	if err := store.SaveAccountUsageSnapshot(ctx, newer); err != nil {
		t.Fatal(err)
	}
	credit, err = store.LoadAccountCreditStatus(ctx, account.ID)
	additional, additionalErr = store.ListAccountAdditionalQuotas(ctx, account.ID)
	if err != nil || additionalErr != nil || credit == nil || credit.Usable() || len(additional) != 1 {
		t.Fatalf("explicit zero/missing additional handling: credit=%+v additional=%+v errors=%v/%v", credit, additional, err, additionalErr)
	}
	if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: account.ID, ObservedAt: now.Add(2 * time.Minute), AdditionalReported: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	additional, err = store.ListAccountAdditionalQuotas(ctx, account.ID)
	if err != nil || len(additional) != 0 {
		t.Fatalf("explicit empty additional list survived restart: %+v %v", additional, err)
	}
}

func TestCreditBackedQuotaCandidateKeepsGroupBoundary(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	for _, id := range []string{"acct-a", "acct-b"} {
		saveTestAccount(t, store, id)
		account, err := store.GetAccount(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		account.Status = domain.AccountQuotaExceeded
		if err := store.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		has := true
		if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: id, ObservedAt: fixedTime,
			Credits: &domain.AccountCreditStatus{AccountID: id, Has: &has, ObservedAt: fixedTime}}); err != nil {
			t.Fatal(err)
		}
	}
	groupID := "group-credit"
	if err := store.SaveGroup(ctx, domain.AccountGroup{ID: groupID, Name: "Credit", AccountIDs: []string{"acct-a"}}, fixedTime); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAPIKey(ctx, testKey("key-credit", &groupID), fixedTime); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.QuotaCandidateAccounts(ctx, "key-credit")
	if err != nil || len(candidates) != 1 || candidates[0].ID != "acct-a" {
		t.Fatalf("credit candidate escaped group: %+v %v", candidates, err)
	}
	_, err = store.ReserveUsage(ctx, domain.ReservationRequest{ID: "out-of-group", APIKeyID: "key-credit", AccountID: "acct-b",
		Model: "gpt-6-sol", Budget: domain.UsageAmount{InputTokens: 1}, Now: fixedTime})
	if !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("credit override escaped group reservation gate: %v", err)
	}
}

func TestAdditionalQuotaReservationRequiresElapsedRefusalAndFreshEvidence(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	account, err := store.GetAccount(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	account.PlanType = "pro"
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAPIKey(ctx, testKey("key-spark", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reserve := domain.ReservationRequest{ID: "refused", APIKeyID: "key-spark", AccountID: "acct",
		Model: "gpt-5.3-codex-spark", Budget: domain.UsageAmount{InputTokens: 1}, Now: now}
	if _, err := store.ReserveUsage(ctx, reserve); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleUsage(ctx, reserve.ID, domain.UsageSettlement{Status: "failed", Event: domain.UsageEvent{
		RequestID: "refused", AccountID: "acct", Model: reserve.Model, Status: "error", ErrorCode: "quota_exceeded", RequestedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAccountOutcome(ctx, "acct", reserve.ID, true, false); err != nil {
		t.Fatal(err)
	}
	reset := now.Add(time.Hour)
	if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: "acct", ObservedAt: now.Add(time.Second), AdditionalReported: true,
		AdditionalQuotas: []domain.AccountAdditionalQuota{{AccountID: "acct", QuotaKey: "codex_spark", Window: "primary",
			UsedPercent: 0, ResetAt: &reset, ObservedAt: now.Add(time.Second)}}}); err != nil {
		t.Fatal(err)
	}
	reserve.ID, reserve.Now = "before-cooldown", now.Add(time.Second)
	if _, err := store.ReserveUsage(ctx, reserve); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("unelapsed quota refusal bypassed: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE usage_reservations SET updated_at=? WHERE id='refused'", millis(now.Add(-3*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE account_quota_block_evidence SET blocked_at=? WHERE account_id='acct'", millis(now.Add(-3*time.Minute))); err != nil {
		t.Fatal(err)
	}
	reserve.ID = "after-cooldown"
	if _, err := store.ReserveUsage(ctx, reserve); err != nil {
		t.Fatalf("fresh separate quota still blocked after cooldown: %v", err)
	}
	if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: "acct", ObservedAt: now.Add(2 * time.Second), AdditionalReported: true}); err != nil {
		t.Fatal(err)
	}
	reserve.ID = "missing-evidence"
	if _, err := store.ReserveUsage(ctx, reserve); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("missing separate quota reserved: %v", err)
	}
}

func TestWorkspaceLessFreeDowngradeNeedsTwoObservationsAndReauthResetsCount(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	account, err := store.GetAccount(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.GetAccountCredential(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	observation := func(expected domain.Account, at time.Time) domain.AccountUsageSnapshot {
		return domain.AccountUsageSnapshot{AccountID: expected.ID, ExpectedAccount: &expected, ExpectedCredential: &credential,
			FetchStartedAt: at, ObservedAt: at, ReportedPlanType: "free"}
	}
	if err := store.SaveAccountUsageSnapshot(ctx, observation(account, base)); !errors.Is(err, domain.ErrPlanConfirmationPending) {
		t.Fatalf("single free observation committed: %v", err)
	}
	reauthenticated := account
	refreshTime := base.Add(time.Second)
	reauthenticated.LastRefresh = &refreshTime
	if err := store.SaveAccountIdentity(ctx, reauthenticated, credential); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountUsageSnapshot(ctx, observation(account, base.Add(1500*time.Millisecond))); !errors.Is(err, ErrConflict) {
		t.Fatalf("pre-reauth usage replaced new identity: %v", err)
	}
	reauthenticated, err = store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountUsageSnapshot(ctx, observation(reauthenticated, base.Add(2*time.Second))); !errors.Is(err, domain.ErrPlanConfirmationPending) {
		t.Fatalf("reauth retained stale downgrade count: %v", err)
	}
	if err := store.SaveAccountUsageSnapshot(ctx, observation(reauthenticated, base.Add(3*time.Second))); err != nil {
		t.Fatalf("confirmed free plan rejected: %v", err)
	}
	updated, err := store.GetAccount(ctx, account.ID)
	if err != nil || updated.PlanType != "free" {
		t.Fatalf("confirmed plan not persisted: %+v %v", updated, err)
	}
}

func TestWorkspaceBoundFreeDowngradeRequiresMatchingSlot(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	account, err := store.GetAccount(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	account.WorkspaceID = "workspace-one"
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	credential, err := store.GetAccountCredential(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	missingSlot := domain.AccountUsageSnapshot{AccountID: account.ID, ExpectedAccount: &account, ExpectedCredential: &credential,
		FetchStartedAt: base, ObservedAt: base, ReportedPlanType: "free"}
	if err := store.SaveAccountUsageSnapshot(ctx, missingSlot); !errors.Is(err, ErrInvalid) {
		t.Fatalf("workspace-less free report demoted a bound seat: %v", err)
	}
	confirmed := missingSlot
	confirmed.FetchStartedAt, confirmed.ObservedAt = base.Add(time.Second), base.Add(time.Second)
	confirmed.ReportedWorkspaceID = account.WorkspaceID
	if err := store.SaveAccountUsageSnapshot(ctx, confirmed); err != nil {
		t.Fatalf("matching workspace free report rejected: %v", err)
	}
	if updated, err := store.GetAccount(ctx, account.ID); err != nil || updated.PlanType != "free" {
		t.Fatalf("matching workspace did not update plan: %+v %v", updated, err)
	}
}

func TestUsageIdentityFenceDetectsCredentialReplacementWithSameTimestamp(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	account, err := store.GetAccount(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.GetAccountCredential(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	newCredential := credential
	raw := make([]byte, 73)
	raw[0], raw[1] = 0x80, 1
	newCredential.AccessTokenEncrypted = []byte(base64.URLEncoding.EncodeToString(raw))
	if err := store.SaveAccountIdentity(ctx, account, newCredential); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	stale := domain.AccountUsageSnapshot{AccountID: account.ID, ObservedAt: now, FetchStartedAt: now,
		ExpectedAccount: &account, ExpectedCredential: &credential, ReportedPlanType: "pro"}
	if err := store.SaveAccountUsageSnapshot(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("old credential's usage changed account identity: %v", err)
	}
}

func TestUsageIdentityFenceDetectsReimportedIncarnation(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	account, err := store.GetAccount(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.GetAccountCredential(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAccount(ctx, account.ID, false); err != nil {
		t.Fatal(err)
	}
	reimported := credential
	raw := make([]byte, 73)
	raw[0], raw[1] = 0x80, 2
	reimported.AccessTokenEncrypted = []byte(base64.URLEncoding.EncodeToString(raw))
	if err := store.SaveAccountIdentity(ctx, account, reimported); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetAccount(ctx, account.ID)
	if err != nil || current.Generation != 1 {
		t.Fatalf("reimport generation = %+v %v", current, err)
	}
	now := time.Now().UTC()
	stale := domain.AccountUsageSnapshot{AccountID: account.ID, ObservedAt: now, FetchStartedAt: now,
		ExpectedAccount: &account, ExpectedCredential: &credential, ReportedPlanType: "pro"}
	if err := store.SaveAccountUsageSnapshot(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("old usage changed a reimported incarnation: %v", err)
	}
}
