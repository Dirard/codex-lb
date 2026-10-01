package application

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/domain"
)

type stubUsageClient struct {
	mu          sync.Mutex
	usage       map[string]UsageSnapshot
	usageErr    map[string]error
	credits     ResetCredits
	creditsErr  error
	consumeErr  error
	fetches     int
	consumed    []string
	consumeCode string
	onFetch     func()
}

func (s *stubUsageClient) FetchUsage(_ context.Context, _, accountID string) (UsageSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetches++
	if s.onFetch != nil {
		s.onFetch()
	}
	if err, ok := s.usageErr[accountID]; ok {
		return UsageSnapshot{}, err
	}
	return s.usage[accountID], nil
}

func (s *stubUsageClient) FetchResetCredits(context.Context, string, string) (ResetCredits, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.creditsErr != nil {
		return ResetCredits{}, s.creditsErr
	}
	return s.credits, nil
}

func (s *stubUsageClient) ConsumeResetCredit(_ context.Context, _, _, creditID, _ string) (ResetCreditConsume, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumed = append(s.consumed, creditID)
	if s.consumeErr != nil {
		return ResetCreditConsume{}, s.consumeErr
	}
	code := s.consumeCode
	if code == "" {
		code = "reset"
	}
	redeemed := time.Unix(1893456000, 0).UTC()
	return ResetCreditConsume{Code: code, WindowsReset: 2, RedeemedAt: &redeemed}, nil
}

type fakePinStore struct {
	mu       sync.Mutex
	pins     map[string]string
	versions map[string]int64
	outcome  func(string) int64
}

func (f *fakePinStore) GetPinnedResetCredit(_ context.Context, accountID string, accountGeneration int64, requestID string) (ResetRedemption, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := accountID + "/" + strconv.FormatInt(accountGeneration, 10) + "/" + requestID
	creditID, ok := f.pins[key]
	version := f.versions[key]
	return ResetRedemption{CreditID: creditID, OutcomeVersion: &version}, ok, nil
}

func (f *fakePinStore) PinResetCredit(_ context.Context, accountID string, accountGeneration int64, requestID, creditID string) (ResetRedemption, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := accountID + "/" + strconv.FormatInt(accountGeneration, 10) + "/" + requestID
	if existing, ok := f.pins[key]; ok {
		version := f.versions[key]
		return ResetRedemption{CreditID: existing, OutcomeVersion: &version}, nil
	}
	f.pins[key] = creditID
	if f.versions == nil {
		f.versions = map[string]int64{}
	}
	version := int64(0)
	if f.outcome != nil {
		version = f.outcome(accountID)
	}
	f.versions[key] = version
	return ResetRedemption{CreditID: creditID, OutcomeVersion: &version}, nil
}

type vaultTokenSource struct {
	cipher SecretCipher
}

func (v vaultTokenSource) AccessToken(_ context.Context, _ domain.Account, credential domain.AccountCredential) (string, error) {
	plaintext, err := v.cipher.Decrypt(credential.AccessTokenEncrypted)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (v vaultTokenSource) ForceRefresh(_ context.Context, _ domain.Account, rejectedToken string) (string, error) {
	return rejectedToken, nil
}

func newUsageTestService(t *testing.T, stub *stubUsageClient) (*AccountUsageService, *fakeAccountsStore, *fakePinStore, *credentials.Vault) {
	t.Helper()
	store := newFakeAccounts()
	pins := &fakePinStore{pins: map[string]string{}}
	vault := testVault(t)
	service := NewAccountUsageService(store, vaultTokenSource{cipher: vault}, stub, pins, func() time.Time { return time.Unix(1_800_000_000, 0).UTC() })
	return service, store, pins, vault
}

func saveUsageAccount(t *testing.T, store *fakeAccountsStore, id string, status domain.AccountStatus) domain.Account {
	t.Helper()
	account := domain.Account{
		ID: id, Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "chatgpt-" + id,
		Email: id + "@example.test", PlanType: "plus", RoutingPolicy: "normal",
		Status: status, CreatedAt: time.Unix(1, 0).UTC(),
	}
	if err := store.SaveAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	return account
}

func saveUsageAccountCredentialed(t *testing.T, vault *credentials.Vault, store *fakeAccountsStore, id string, status domain.AccountStatus) domain.Account {
	t.Helper()
	account := saveUsageAccount(t, store, id, status)
	encryptForTest(t, vault, store, account.ID)
	return account
}

func encryptForTest(t *testing.T, vault *credentials.Vault, store *fakeAccountsStore, accountID string) domain.AccountCredential {
	t.Helper()
	credential, err := vault.Encrypt([]byte("access-token-" + accountID))
	if err != nil {
		t.Fatal(err)
	}
	saved := domain.AccountCredential{AccountID: accountID, AccessTokenEncrypted: credential, RefreshTokenEncrypted: credential, IDTokenEncrypted: credential}
	if err := store.SaveAccountCredential(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	return saved
}

func windowUsage(used float64) *UsageWindow {
	return &UsageWindow{UsedPercent: &used}
}

func TestRefreshAccountUsagePersistsQuotasWithoutTouchingPolicy(t *testing.T) {
	ctx := context.Background()
	stub := &stubUsageClient{usage: map[string]UsageSnapshot{}}
	service, store, _, vault := newUsageTestService(t, stub)
	account := saveUsageAccountCredentialed(t, vault, store, "acct_active", domain.AccountActive)
	originalCredential, _ := store.GetAccountCredential(ctx, account.ID)
	stub.usage[account.ChatGPTAccountID] = UsageSnapshot{Primary: windowUsage(120), Secondary: windowUsage(30)}

	if err := service.RefreshAccountUsage(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	quotas, _ := store.ListAccountQuota(ctx, account.ID)
	byWindow := map[string]domain.AccountQuota{}
	for _, quota := range quotas {
		byWindow[quota.Window] = quota
	}
	if byWindow["primary"].UsedPercent != 100 || byWindow["secondary"].UsedPercent != 30 {
		t.Fatalf("quotas = %+v", byWindow)
	}
	saved, _ := store.GetAccount(ctx, account.ID)
	if saved.Status != domain.AccountActive || saved.PlanType != "plus" || saved.RoutingPolicy != "normal" {
		t.Fatalf("account mutated by telemetry: %+v", saved)
	}
	after, _ := store.GetAccountCredential(ctx, account.ID)
	if string(after.AccessTokenEncrypted) != string(originalCredential.AccessTokenEncrypted) {
		t.Fatal("telemetry overwrote tokens")
	}
}

func TestRefreshAccountUsagePersistsPurchasedCreditsAndMergedAdditionalQuota(t *testing.T) {
	ctx := context.Background()
	stub := &stubUsageClient{usage: map[string]UsageSnapshot{}}
	service, store, _, vault := newUsageTestService(t, stub)
	account := saveUsageAccountCredentialed(t, vault, store, "acct_metadata", domain.AccountActive)
	has, unlimited, balance := true, false, "12.5"
	low, high := 25.0, 80.0
	stub.usage[account.ChatGPTAccountID] = UsageSnapshot{
		Credits: UsageCredits{Has: &has, Unlimited: &unlimited, Balance: &balance},
		AdditionalQuotas: []AdditionalQuota{
			{LimitName: "codex_other", MeteredFeature: "codex_bengalfox", Primary: &UsageWindow{UsedPercent: &low}},
			{LimitName: "GPT-5.3-Codex-Spark", MeteredFeature: "code", Primary: &UsageWindow{UsedPercent: &high}},
		},
	}
	if err := service.RefreshAccountUsage(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	stored := store.usageSnapshots[account.ID]
	if stored.Credits == nil || !stored.Credits.Usable() || stored.Credits.Balance == nil || *stored.Credits.Balance != 12.5 ||
		!stored.AdditionalReported || len(stored.AdditionalQuotas) != 1 || stored.AdditionalQuotas[0].QuotaKey != "codex_spark" ||
		stored.AdditionalQuotas[0].UsedPercent != 80 {
		t.Fatalf("persisted metadata = %+v", stored)
	}
}

func TestRefreshAccountUsageRejectsOtherWorkspaceMetadata(t *testing.T) {
	ctx := context.Background()
	stub := &stubUsageClient{usage: map[string]UsageSnapshot{}}
	service, store, _, vault := newUsageTestService(t, stub)
	account := saveUsageAccountCredentialed(t, vault, store, "acct_workspace", domain.AccountActive)
	account.WorkspaceID = "workspace-one"
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	has := true
	stub.usage[account.ChatGPTAccountID] = UsageSnapshot{WorkspaceID: "workspace-two", Primary: windowUsage(0), Credits: UsageCredits{Has: &has}}
	if err := service.RefreshAccountUsage(ctx, account.ID); err == nil {
		t.Fatal("other workspace usage accepted")
	}
	if _, ok := store.usageSnapshots[account.ID]; ok {
		t.Fatal("other workspace credits were persisted")
	}
}

func TestRefreshSkipsIneligibleAccounts(t *testing.T) {
	ctx := context.Background()
	stub := &stubUsageClient{usage: map[string]UsageSnapshot{}}
	service, store, _, _ := newUsageTestService(t, stub)
	paused := saveUsageAccount(t, store, "acct_paused", domain.AccountPaused)
	external := domain.Account{ID: "acct_ext", Kind: domain.AccountExternal, Provider: "zai", Email: "ext@example.test", PlanType: "plus", RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: time.Unix(2, 0).UTC()}
	if err := store.SaveAccount(ctx, external); err != nil {
		t.Fatal(err)
	}
	if err := service.RefreshAccountUsage(ctx, paused.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.RefreshAccountUsage(ctx, external.ID); err != nil {
		t.Fatal(err)
	}
	if quotas, _ := store.ListAccountQuota(ctx, paused.ID); len(quotas) != 0 {
		t.Fatalf("paused quotas = %+v", quotas)
	}
	if stub.fetches != 0 {
		t.Fatalf("ineligible accounts hit upstream %d times", stub.fetches)
	}
}

func TestRefreshAllContinuesPastFailuresAndCachesCredits(t *testing.T) {
	ctx := context.Background()
	stub := &stubUsageClient{
		usage:    map[string]UsageSnapshot{},
		usageErr: map[string]error{"chatgpt-acct_bad": &UsageError{Code: "tokenexpired", Message: "expired", Status: 403}},
		credits: ResetCredits{AvailableCount: 1, Credits: []ResetCredit{{
			ID: "credit-1", Status: "available", ExpiresAt: timePtrTime(time.Unix(1_800_100_000, 0).UTC()),
		}}},
	}
	service, store, _, vault := newUsageTestService(t, stub)
	saveUsageAccountCredentialed(t, vault, store, "acct_bad", domain.AccountActive)
	good := saveUsageAccountCredentialed(t, vault, store, "acct_good", domain.AccountActive)
	stub.usage["chatgpt-"+good.ID] = UsageSnapshot{Primary: windowUsage(10)}

	if err := service.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	if quotas, _ := store.ListAccountQuota(ctx, good.ID); len(quotas) != 1 {
		t.Fatalf("good quotas = %+v", quotas)
	}
	snapshot, err := service.ResetCreditsSnapshot(ctx, good.ID)
	if err != nil || snapshot == nil || snapshot.AvailableCount != 1 || snapshot.NearestExpiresAt == nil || len(snapshot.Credits) != 1 {
		t.Fatalf("snapshot = %+v err %v", snapshot, err)
	}
	if snapshot, err := service.ResetCreditsSnapshot(ctx, "missing"); err != nil || snapshot != nil {
		t.Fatalf("missing snapshot = %+v err %v", snapshot, err)
	}
}

func TestBackgroundUsageRefreshTriggersWarmupWithFreshOptIn(t *testing.T) {
	ctx := context.Background()
	stub := &stubUsageClient{usage: map[string]UsageSnapshot{}}
	usage, store, _, vault := newUsageTestService(t, stub)
	account := saveUsageAccountCredentialed(t, vault, store, "acct_warm_refresh", domain.AccountActive)
	if err := store.UpdateAccountWarmup(ctx, account.ID, true); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	old := quota(account.ID, "primary", 100, now.Add(-30*time.Second), now.Add(-2*time.Minute), 300)
	if err := store.SaveAccountQuota(ctx, old); err != nil {
		t.Fatal(err)
	}
	used, minutes, reset := 0.0, 300, now.Add(5*time.Hour)
	stub.usage[account.ChatGPTAccountID] = UsageSnapshot{Primary: &UsageWindow{UsedPercent: &used, ResetAt: &reset, WindowMinutes: &minutes}}
	provider := &stubResponseProvider{result: ResponseResult{ResponseID: "r", UsageKnown: true}}
	warmups := NewWarmupService(store, provider, &stubLedger{}, nil, nil, func() time.Time { return now })
	settings := &warmupSettingsStore{value: domain.RuntimeSettings{
		LimitWarmupEnabled: true, LimitWarmupWindows: "primary", LimitWarmupModel: "gpt-5.4-mini",
		LimitWarmupPrompt: "Say OK.", LimitWarmupCooldownSeconds: 3600,
		LimitWarmupExhaustedPercent: 99, LimitWarmupMinAvailablePercent: 100, LimitWarmupIdlePercent: 1,
	}}
	limit := NewLimitWarmupService(settings, store, warmups, &stubLimitWarmupClaims{}, func() time.Time { return now })
	usage.ConfigureLimitWarmups(limit)

	// A manual probe refresh persists the sample but does not buy another probe.
	if err := usage.RefreshAccountUsage(ctx, account.ID); err != nil || len(provider.targets) != 0 {
		t.Fatalf("manual refresh = %v targets %v", err, provider.targets)
	}
	_ = store.SaveAccountQuota(ctx, old)
	stub.onFetch = func() { _ = store.UpdateAccountWarmup(ctx, account.ID, false) }
	if err := usage.RefreshAll(ctx); err != nil || len(provider.targets) != 0 {
		t.Fatalf("opted out during fetch = %v targets %v", err, provider.targets)
	}
	_ = store.UpdateAccountWarmup(ctx, account.ID, true)
	_ = store.SaveAccountQuota(ctx, old)
	stub.onFetch = nil
	if err := usage.RefreshAll(ctx); err != nil || len(provider.targets) != 1 {
		t.Fatalf("background confirmed reset = %v targets %v", err, provider.targets)
	}
}

func TestUsageProcessCachesDoNotCrossReimport(t *testing.T) {
	ctx := context.Background()
	stub := &stubUsageClient{usage: map[string]UsageSnapshot{}, credits: ResetCredits{AvailableCount: 1}}
	service, store, _, vault := newUsageTestService(t, stub)
	account := saveUsageAccountCredentialed(t, vault, store, "acct_reimport_cache", domain.AccountActive)
	stub.usage[account.ChatGPTAccountID] = UsageSnapshot{Primary: windowUsage(10)}
	if err := service.RefreshResetCredits(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	stub.onFetch = func() {
		store.mu.Lock()
		defer store.mu.Unlock()
		reimported := store.accounts[account.ID]
		reimported.Generation = 1
		store.accounts[account.ID] = reimported
		store.creds[account.ID] = domain.AccountCredential{AccountID: account.ID, Generation: 1, AccessTokenEncrypted: []byte("new")}
	}

	if err := service.RefreshAccountUsage(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetAccount(ctx, account.ID)
	if err != nil || current.Generation != 1 {
		t.Fatalf("reimported account = %+v %v", current, err)
	}
	if service.LastUsageSnapshot(current) != nil {
		t.Fatal("old usage telemetry crossed the reimport boundary")
	}
	if snapshot, err := service.ResetCreditsSnapshot(ctx, account.ID); err != nil || snapshot != nil {
		t.Fatalf("old reset credits crossed the reimport boundary: %+v %v", snapshot, err)
	}
	if err := service.RefreshAccountUsageForGeneration(ctx, account.ID, account.Generation); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("old generation refresh ran = %v", err)
	}
}

func TestConsumeResetCreditIsPinnedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	sooner := time.Unix(1_800_000_100, 0).UTC()
	later := time.Unix(1_800_000_900, 0).UTC()
	stub := &stubUsageClient{credits: ResetCredits{AvailableCount: 2, Credits: []ResetCredit{
		{ID: "credit-later", Status: "available", ExpiresAt: &later},
		{ID: "credit-sooner", Status: "available", ExpiresAt: &sooner},
		{ID: "credit-redeemed", Status: "redeemed"},
	}}}
	service, store, pins, vault := newUsageTestService(t, stub)
	account := saveUsageAccountCredentialed(t, vault, store, "acct_redeem", domain.AccountActive)
	if err := service.RefreshResetCredits(ctx, account.ID); err != nil {
		t.Fatal(err)
	}

	first, err := service.ConsumeResetCredit(ctx, account.ID, "redeem-1")
	if err != nil || first.Code != "reset" {
		t.Fatalf("first = %+v err %v", first, err)
	}
	if len(stub.consumed) != 1 || stub.consumed[0] != "credit-sooner" {
		t.Fatalf("consumed = %+v", stub.consumed)
	}
	if pinned, ok, _ := pins.GetPinnedResetCredit(ctx, account.ID, account.Generation, "redeem-1"); !ok || pinned.CreditID != "credit-sooner" {
		t.Fatalf("pin = %+v %v", pinned, ok)
	}
	snapshot, _ := service.ResetCreditsSnapshot(ctx, account.ID)
	if snapshot != nil {
		t.Fatalf("stale credit snapshot remained after redemption: %+v", snapshot)
	}

	// Idempotent retry: no fresh selection, same pinned credit, upstream sees
	// the same redeem_request_id (already_redeemed).
	stub.consumeCode = "already_redeemed"
	stub.credits = ResetCredits{}
	retry, err := service.ConsumeResetCredit(ctx, account.ID, "redeem-1")
	if err != nil || retry.Code != "already_redeemed" {
		t.Fatalf("retry = %+v err %v", retry, err)
	}
	if len(stub.consumed) != 2 || stub.consumed[1] != "credit-sooner" {
		t.Fatalf("retry consumed = %+v", stub.consumed)
	}
}

func TestConsumeResetCreditWithoutAvailableCredit(t *testing.T) {
	ctx := context.Background()
	stub := &stubUsageClient{}
	service, store, _, vault := newUsageTestService(t, stub)
	account := saveUsageAccountCredentialed(t, vault, store, "acct_none", domain.AccountActive)
	if _, err := service.ConsumeResetCredit(ctx, account.ID, "redeem-1"); !errors.Is(err, ErrNoAvailableResetCredit) {
		t.Fatalf("err = %v", err)
	}
	if len(stub.consumed) != 0 {
		t.Fatal("unavailable credit consumed upstream")
	}
	paused := saveUsageAccount(t, store, "acct_paused_redeem", domain.AccountPaused)
	if _, err := service.ConsumeResetCredit(ctx, paused.ID, "redeem-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("paused err = %v", err)
	}
}

func timePtrTime(value time.Time) *time.Time { return &value }
