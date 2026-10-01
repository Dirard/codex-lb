package application

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/domain"
)

type fakeAccountsStore struct {
	mu             sync.Mutex
	accounts       map[string]domain.Account
	creds          map[string]domain.AccountCredential
	quotas         map[string][]domain.AccountQuota
	usageSnapshots map[string]domain.AccountUsageSnapshot
	fullSaves      int
}

func newFakeAccounts() *fakeAccountsStore {
	return &fakeAccountsStore{accounts: map[string]domain.Account{}, creds: map[string]domain.AccountCredential{}}
}

func (f *fakeAccountsStore) SaveAccount(_ context.Context, a domain.Account) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fullSaves++
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Unix(1, 0).UTC()
	}
	if a.Status == "" {
		a.Status = domain.AccountActive
	}
	f.accounts[a.ID] = a
	return nil
}

func TestAccountPolicyServiceAvoidsFullSnapshotWrites(t *testing.T) {
	ctx := context.Background()
	store := newFakeAccounts()
	account := domain.Account{ID: "acct", Kind: domain.AccountChatGPT, Provider: "openai",
		Email: "synthetic@example.invalid", PlanType: "plus", Status: domain.AccountQuotaExceeded}
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	service := NewAccountsService(store, &fakeSettingsStore{}, nil, nil, time.Now)
	alias := "Policy"
	if _, _, err := service.SetAlias(ctx, "acct", &alias); err != nil {
		t.Fatal(err)
	}
	if err := service.SetLimitWarmup(ctx, "acct", true); err != nil {
		t.Fatal(err)
	}
	if err := service.SetRoutingPolicy(ctx, "acct", "preserve"); err != nil {
		t.Fatal(err)
	}
	allowed := true
	if err := service.UpdateAccount(ctx, "acct", &allowed); err != nil {
		t.Fatal(err)
	}
	if store.fullSaves != 1 || store.accounts["acct"].Status != domain.AccountQuotaExceeded {
		t.Fatalf("policy methods used stale full account save: %+v", store.accounts["acct"])
	}
	if err := service.PauseAccount(ctx, "acct"); err != nil {
		t.Fatal(err)
	}
	if err := service.ReactivateAccount(ctx, "acct"); err != nil {
		t.Fatal(err)
	}
	if store.fullSaves != 1 || store.accounts["acct"].Status != domain.AccountActive {
		t.Fatalf("status action used full account save: %+v", store.accounts["acct"])
	}
}

func (f *fakeAccountsStore) GetAccount(_ context.Context, id string) (domain.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	account, ok := f.accounts[id]
	if !ok {
		return account, domain.ErrNotFound
	}
	return account, nil
}

func (f *fakeAccountsStore) ListAccounts(_ context.Context) ([]domain.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]domain.Account, 0, len(f.accounts))
	for _, account := range f.accounts {
		result = append(result, account)
	}
	return result, nil
}

func (f *fakeAccountsStore) DeleteAccount(_ context.Context, id string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	account, ok := f.accounts[id]
	if !ok {
		return domain.ErrNotFound
	}
	account.Status = domain.AccountDeactivated
	account.DeactivationReason = "deleted"
	f.accounts[id] = account
	return nil
}

func (f *fakeAccountsStore) SaveAccountCredential(_ context.Context, c domain.AccountCredential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds[c.AccountID] = c
	return nil
}

func (f *fakeAccountsStore) SaveAccountIdentity(_ context.Context, a domain.Account, c domain.AccountCredential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a.ID != c.AccountID {
		return domain.ErrInvalid
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Unix(1, 0).UTC()
	}
	f.accounts[a.ID], f.creds[c.AccountID] = a, c
	return nil
}

func (f *fakeAccountsStore) updatePolicy(id string, change func(*domain.Account)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	account, ok := f.accounts[id]
	if !ok || deletedAccount(account) {
		return domain.ErrNotFound
	}
	change(&account)
	f.accounts[id] = account
	return nil
}
func (f *fakeAccountsStore) UpdateAccountAlias(_ context.Context, id, alias string) error {
	return f.updatePolicy(id, func(a *domain.Account) { a.Alias = alias })
}
func (f *fakeAccountsStore) UpdateAccountWarmup(_ context.Context, id string, enabled bool) error {
	return f.updatePolicy(id, func(a *domain.Account) { a.LimitWarmupEnabled = enabled })
}
func (f *fakeAccountsStore) UpdateAccountRoutingPolicy(_ context.Context, id, policy string) error {
	return f.updatePolicy(id, func(a *domain.Account) { a.RoutingPolicy = policy })
}
func (f *fakeAccountsStore) UpdateAccountSecurityAuthorization(_ context.Context, id string, enabled bool) error {
	return f.updatePolicy(id, func(a *domain.Account) { a.SecurityWorkAuthorized = enabled })
}
func (f *fakeAccountsStore) TransitionAccountStatus(_ context.Context, id string, expected domain.AccountStatus, reason string, next domain.AccountStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.accounts[id]
	if !ok || deletedAccount(a) {
		return domain.ErrNotFound
	}
	if a.Status != expected || a.DeactivationReason != reason {
		return domain.ErrConflict
	}
	a.Status = next
	a.DeactivationReason = ""
	f.accounts[id] = a
	return nil
}

func (f *fakeAccountsStore) GetAccountCredential(_ context.Context, id string) (domain.AccountCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	credential, ok := f.creds[id]
	if !ok {
		return credential, domain.ErrNotFound
	}
	return credential, nil
}

func (f *fakeAccountsStore) SaveAccountQuota(_ context.Context, quota domain.AccountQuota) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.quotas == nil {
		f.quotas = map[string][]domain.AccountQuota{}
	}
	for index, existing := range f.quotas[quota.AccountID] {
		if existing.Window == quota.Window {
			f.quotas[quota.AccountID][index] = quota
			return nil
		}
	}
	f.quotas[quota.AccountID] = append(f.quotas[quota.AccountID], quota)
	return nil
}
func (f *fakeAccountsStore) SaveAccountUsageSnapshot(ctx context.Context, snapshot domain.AccountUsageSnapshot) error {
	for _, quota := range snapshot.Quotas {
		if err := f.SaveAccountQuota(ctx, quota); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.usageSnapshots == nil {
		f.usageSnapshots = make(map[string]domain.AccountUsageSnapshot)
	}
	f.usageSnapshots[snapshot.AccountID] = snapshot
	return nil
}
func (f *fakeAccountsStore) ListAccountQuota(_ context.Context, accountID string) ([]domain.AccountQuota, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.AccountQuota(nil), f.quotas[accountID]...), nil
}
func (f *fakeAccountsStore) RecordAccountOutcome(context.Context, string, string, bool, bool) error {
	return nil
}

type fakeSettingsStore struct {
	withoutOverwrite bool
	limitWarmup      bool
}

func (f *fakeSettingsStore) LoadSettings(context.Context) (domain.RuntimeSettings, error) {
	return domain.RuntimeSettings{ImportWithoutOverwrite: f.withoutOverwrite, LimitWarmupEnabled: f.limitWarmup}, nil
}
func (f *fakeSettingsStore) SaveSettings(context.Context, domain.RuntimeSettings) error { return nil }
func (f *fakeSettingsStore) LoadAdminSecret(context.Context) (domain.AdminSecret, error) {
	return domain.AdminSecret{}, nil
}
func (f *fakeSettingsStore) SaveAdminSecret(context.Context, domain.AdminSecret) error { return nil }
func (f *fakeSettingsStore) InitializeAdminSecret(context.Context, domain.AdminSecret) (bool, error) {
	return true, nil
}
func (f *fakeSettingsStore) AdvanceTOTPStep(context.Context, int64) (bool, error) { return true, nil }

type stubOAuthClient struct {
	mu            sync.Mutex
	lastState     string
	lastChallenge string
	authURL       string
	codeTokens    OAuthTokens
	codeErr       error
	exchanges     int
	device        DeviceCode
	deviceErr     error
	deviceTokens  OAuthTokens
	deviceReady   bool
	deviceCalls   int
}

func (s *stubOAuthClient) AuthorizationURL(state, challenge string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastState, s.lastChallenge = state, challenge
	return "https://auth.example.test/authorize?state=" + state
}

func (s *stubOAuthClient) ExchangeCode(_ context.Context, code, verifier string) (OAuthTokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exchanges++
	if s.codeErr != nil {
		return OAuthTokens{}, s.codeErr
	}
	if code == "" || verifier == "" {
		return OAuthTokens{}, &OAuthError{Code: "invalid_request", Message: "missing code or verifier"}
	}
	return s.codeTokens, nil
}

func (s *stubOAuthClient) RequestDeviceCode(context.Context) (DeviceCode, error) {
	if s.deviceErr != nil {
		return DeviceCode{}, s.deviceErr
	}
	return s.device, nil
}

func (s *stubOAuthClient) ExchangeDeviceToken(context.Context, string, string) (OAuthTokens, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deviceCalls++
	return s.deviceTokens, s.deviceReady, nil
}

func testVault(t *testing.T) *credentials.Vault {
	t.Helper()
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	return vault
}

func testJWT(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func tokenSet(sub string) OAuthTokens {
	return OAuthTokens{
		AccessToken:  testJWT(map[string]any{"sub": sub, "exp": 1900000000}),
		RefreshToken: "refresh-" + sub,
		IDToken: testJWT(map[string]any{
			"email": sub + "@example.test", "sub": sub,
			"https://api.openai.com/auth": map[string]any{
				"chatgpt_account_id": "chatgpt-1", "user_id": sub, "chatgpt_plan_type": "Team",
				"workspace_id": "org-1", "workspace_label": "Org One", "seat_type": "Owner-Admin",
			},
		}),
	}
}

func newTestService(t *testing.T, withoutOverwrite bool) (*AccountsService, *fakeAccountsStore, *stubOAuthClient, *time.Time) {
	t.Helper()
	store := newFakeAccounts()
	now := time.Unix(1_800_000_000, 0).UTC()
	stub := &stubOAuthClient{}
	service := NewAccountsService(store, &fakeSettingsStore{withoutOverwrite: withoutOverwrite}, testVault(t), stub, func() time.Time { return now })
	t.Cleanup(func() { service.Close() })
	return service, store, stub, &now
}

func TestAccountAdminCRUDTransitions(t *testing.T) {
	ctx := context.Background()
	service, store, _, _ := newTestService(t, true)
	account := domain.Account{ID: "acct_1", Kind: domain.AccountChatGPT, Provider: "openai", Email: "a@example.test", PlanType: "plus", RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: time.Now()}
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := service.PauseAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	if paused, _ := store.GetAccount(ctx, account.ID); paused.Status != domain.AccountPaused {
		t.Fatalf("pause status = %s", paused.Status)
	}
	if err := service.ReactivateAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	id, alias, err := service.SetAlias(ctx, account.ID, stringPtr("  Team A  "))
	if err != nil || id != account.ID || alias == nil || *alias != "Team A" {
		t.Fatalf("alias = %v %v %v", id, alias, err)
	}
	if _, alias, err := service.SetAlias(ctx, account.ID, stringPtr("   ")); err != nil || alias != nil {
		t.Fatalf("blank alias = %v %v", alias, err)
	}
	if err := service.SetLimitWarmup(ctx, account.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := service.SetRoutingPolicy(ctx, account.ID, "burn_first"); err != nil {
		t.Fatal(err)
	}
	if err := service.SetRoutingPolicy(ctx, account.ID, "invalid"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid policy err = %v", err)
	}
	authorized := true
	if err := service.UpdateAccount(ctx, account.ID, &authorized); err != nil {
		t.Fatal(err)
	}
	if err := service.UpdateAccount(ctx, account.ID, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty update err = %v", err)
	}
	saved, _ := store.GetAccount(ctx, account.ID)
	if !saved.LimitWarmupEnabled || saved.RoutingPolicy != "burn_first" || !saved.SecurityWorkAuthorized || saved.Alias != "" {
		t.Fatalf("saved account = %+v", saved)
	}
	if err := store.SaveAccount(ctx, domain.Account{ID: "acct_2", Kind: domain.AccountChatGPT, Provider: "openai", Email: "b@example.test", PlanType: "plus", RoutingPolicy: "normal", Status: domain.AccountReauthRequired, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReactivateAccount(ctx, "acct_2"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("reauth reactivate err = %v", err)
	}
	if err := service.PauseAccount(ctx, "acct_2"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("reauth pause err = %v", err)
	}
	if err := service.PauseAccount(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing pause err = %v", err)
	}
	if err := service.DeleteAccount(ctx, account.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := service.PauseAccount(ctx, account.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleted account visible err = %v", err)
	}
}

func TestImportAccountIdentityAndEncryption(t *testing.T) {
	ctx := context.Background()
	service, store, _, _ := newTestService(t, true)
	tokens := tokenSet("user-1")
	raw, _ := json.Marshal(map[string]any{
		"tokens": map[string]any{
			"idToken": tokens.IDToken, "accessToken": tokens.AccessToken, "refreshToken": tokens.RefreshToken,
		},
		"last_refresh": "2026-01-01T00:00:00.000000Z",
	})
	result, err := service.ImportAccount(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountID != "chatgpt-1_"+hashPrefix("org-1", 8) || result.Email != "user-1@example.test" || result.Status != "active" {
		t.Fatalf("import result = %+v", result)
	}
	saved, _ := store.GetAccount(ctx, result.AccountID)
	if saved.PlanType != "team" || saved.SeatType != "owner_admin" || saved.WorkspaceID != "org-1" || saved.ChatGPTUserID != "user-1" {
		t.Fatalf("claims not mapped: %+v", saved)
	}
	credential, _ := store.GetAccountCredential(ctx, result.AccountID)
	if string(credential.AccessTokenEncrypted) == tokens.AccessToken {
		t.Fatal("access token stored in plaintext")
	}
	if _, err := service.ImportAccount(ctx, []byte("not-json")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid import err = %v", err)
	}
}

func TestImportMergeByEmailWhenOverwriteAllowed(t *testing.T) {
	ctx := context.Background()
	service, store, _, _ := newTestService(t, false)
	existing := domain.Account{ID: "existing_1", Kind: domain.AccountChatGPT, Provider: "openai", Email: "user-1@example.test", PlanType: "plus", RoutingPolicy: "normal", Status: domain.AccountPaused, CreatedAt: time.Unix(100, 0)}
	if err := store.SaveAccount(ctx, existing); err != nil {
		t.Fatal(err)
	}
	tokens := tokenSet("user-1")
	raw, _ := json.Marshal(map[string]any{"tokens": map[string]any{"idToken": tokens.IDToken, "accessToken": tokens.AccessToken, "refreshToken": tokens.RefreshToken}})
	result, err := service.ImportAccount(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountID != "existing_1" {
		t.Fatalf("merge kept id %s", result.AccountID)
	}
	accounts, _ := store.ListAccounts(ctx)
	if len(accounts) != 1 {
		t.Fatalf("merge created duplicates: %d", len(accounts))
	}
	if err := store.SaveAccount(ctx, domain.Account{ID: "dup_1", Kind: domain.AccountChatGPT, Provider: "openai", Email: "user-1@example.test", PlanType: "plus", RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportAccount(ctx, raw); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate import err = %v", err)
	}
}

func TestExportAuthReturnsAuthorizedTokensOnlyFromStorage(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := newTestService(t, true)
	tokens := tokenSet("user-1")
	raw, _ := json.Marshal(map[string]any{"tokens": map[string]any{"idToken": tokens.IDToken, "accessToken": tokens.AccessToken, "refreshToken": tokens.RefreshToken}})
	imported, err := service.ImportAccount(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	export, err := service.ExportAuth(ctx, imported.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if export.Tokens.AccessToken != tokens.AccessToken || export.Tokens.RefreshToken != tokens.RefreshToken || export.Tokens.IDToken != tokens.IDToken {
		t.Fatalf("export tokens = %+v", export.Tokens)
	}
	if export.Tokens.ExpiresAtMs != 1900000000*1000 {
		t.Fatalf("expiresAtMs = %d", export.Tokens.ExpiresAtMs)
	}
	if export.Account.AccountID != imported.AccountID || export.Account.Email != "user-1@example.test" {
		t.Fatalf("export account = %+v", export.Account)
	}
	if export.CodexAuthJSON.Tokens.AccessToken != tokens.AccessToken || export.OpenCodeAuthJSON.OpenAI.Access != tokens.AccessToken {
		t.Fatalf("export payloads = %+v %+v", export.CodexAuthJSON, export.OpenCodeAuthJSON)
	}
	if !strings.HasPrefix(export.Filename, "opencode-auth-") || !strings.HasSuffix(export.Filename, ".json") {
		t.Fatalf("filename = %q", export.Filename)
	}
	if _, err := service.ExportAuth(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing export err = %v", err)
	}
}
