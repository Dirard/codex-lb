package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// perAccountListAccounts is GET /api/accounts as it was before the batch read
// (go-v1.4.0, admin.go#listAccounts): about seven point reads per account.
// It stays as the oracle every field of the batch-built list is compared with.
func (s *Server) perAccountListAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.ListAccounts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	type windows struct {
		Primary   *float64 `json:"primaryRemainingPercent"`
		Secondary *float64 `json:"secondaryRemainingPercent"`
		Monthly   *float64 `json:"monthlyRemainingPercent"`
	}
	type accountView struct {
		domain.Account
		DisplayName             string          `json:"displayName"`
		SubscriptionActiveUntil *time.Time      `json:"subscriptionActiveUntil"`
		Auth                    accountAuthView `json:"auth"`
		Usage                   windows         `json:"usage"`
		ResetPrimary            *time.Time      `json:"resetAtPrimary"`
		ResetSecondary          *time.Time      `json:"resetAtSecondary"`
		ResetMonthly            *time.Time      `json:"resetAtMonthly"`
		WindowMinutesPrimary    *int            `json:"windowMinutesPrimary"`
		WindowMinutesSecondary  *int            `json:"windowMinutesSecondary"`
		WindowMinutesMonthly    *int            `json:"windowMinutesMonthly"`
		CapacityPrimary         *float64        `json:"capacityCreditsPrimary"`
		RemainingPrimary        *float64        `json:"remainingCreditsPrimary"`
		CapacitySecondary       *float64        `json:"capacityCreditsSecondary"`
		RemainingSecondary      *float64        `json:"remainingCreditsSecondary"`
		CapacityMonthly         *float64        `json:"capacityCreditsMonthly"`
		RemainingMonthly        *float64        `json:"remainingCreditsMonthly"`
		*accountUsageDetails
		AvailableResetCredits    *int         `json:"availableResetCredits"`
		ResetCreditNearestExpiry *time.Time   `json:"resetCreditNearestExpiresAt"`
		RequestUsage             usageSummary `json:"requestUsage"`
	}
	views := make([]accountView, 0, len(accounts))
	for _, account := range accounts {
		quotas, err := s.store.ListAccountQuota(r.Context(), account.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		creditsStatus, err := s.store.LoadAccountCreditStatus(r.Context(), account.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		additionalQuotas, err := s.store.ListAccountAdditionalQuotas(r.Context(), account.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		totals, err := s.store.UsageTotals(r.Context(), "", account.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		view := accountView{Account: account, DisplayName: account.Alias, RequestUsage: summarizeUsage(totals)}
		var refusalAt *time.Time
		if account.Status == domain.AccountQuotaExceeded {
			refusalAt, err = s.store.LoadAccountQuotaRefusalAt(r.Context(), account.ID)
			if err != nil {
				s.fail(w, err)
				return
			}
		}
		view.Status = application.EffectiveAccountQuotaStatus(account, quotas, creditsStatus, refusalAt, time.Now().UTC())
		credential, err := s.store.GetAccountCredential(r.Context(), account.ID)
		if err == nil {
			view.Auth, view.SubscriptionActiveUntil = accountAuthentication(s.cipher, credential)
		}
		if view.DisplayName == "" {
			view.DisplayName = account.Email
		}
		primary, secondary, monthly := accountQuotasView(quotas, account.PlanType)
		view.Usage.Primary, view.Usage.Secondary = primary.RemainingPercent, secondary.RemainingPercent
		view.Usage.Monthly = monthly.RemainingPercent
		view.ResetPrimary, view.ResetSecondary = primary.ResetAt, secondary.ResetAt
		view.ResetMonthly = monthly.ResetAt
		view.WindowMinutesPrimary, view.WindowMinutesSecondary = primary.WindowMinutes, secondary.WindowMinutes
		view.WindowMinutesMonthly = monthly.WindowMinutes
		view.CapacityPrimary, view.RemainingPrimary = primary.CapacityCredits, primary.RemainingCredits
		view.CapacitySecondary, view.RemainingSecondary = secondary.CapacityCredits, secondary.RemainingCredits
		view.CapacityMonthly, view.RemainingMonthly = monthly.CapacityCredits, monthly.RemainingCredits
		view.accountUsageDetails = accountUsageDetailsFromStored(creditsStatus, additionalQuotas, settings.AdditionalQuotaRoutingPolicies)
		credits := perAccountResetCreditsView(r.Context(), s.usage, account)
		view.AvailableResetCredits, view.ResetCreditNearestExpiry = credits.AvailableCount, credits.NearestExpiresAt
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, struct {
		Accounts []accountView `json:"accounts"`
	}{views})
}

type perAccountResetCredits struct {
	AvailableCount   *int
	NearestExpiresAt *time.Time
}

func perAccountResetCreditsView(ctx context.Context, usage *application.AccountUsageService, account domain.Account) perAccountResetCredits {
	view := perAccountResetCredits{}
	if usage == nil {
		return view
	}
	snapshot, err := usage.ResetCreditsSnapshot(ctx, account.ID)
	if err != nil || snapshot == nil {
		return view
	}
	view.AvailableCount = &snapshot.AvailableCount
	view.NearestExpiresAt = snapshot.NearestExpiresAt
	return view
}

// accountsListUsage is an offline usage client that counts provider calls and
// returns two reset credits.
type accountsListUsage struct {
	calls   atomic.Int64
	expires time.Time
}

func (u *accountsListUsage) FetchUsage(context.Context, string, string) (application.UsageSnapshot, error) {
	u.calls.Add(1)
	return application.UsageSnapshot{}, nil
}

func (u *accountsListUsage) FetchResetCredits(context.Context, string, string) (application.ResetCredits, error) {
	u.calls.Add(1)
	return application.ResetCredits{AvailableCount: 2, Credits: []application.ResetCredit{
		{ID: "credit-1", Status: "available", ExpiresAt: &u.expires}, {ID: "credit-2", Status: "available"}}}, nil
}

func (u *accountsListUsage) ConsumeResetCredit(context.Context, string, string, string, string) (application.ResetCreditConsume, error) {
	u.calls.Add(1)
	return application.ResetCreditConsume{}, nil
}

// newAccountsListServer is a server over a temporary store whose repository
// may be wrapped, with a password session.
func newAccountsListServer(t *testing.T, wrap func(Repository) Repository) (*Server, *sqlite.Store, *http.Cookie) {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "accounts.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	var repo Repository = store
	if wrap != nil {
		repo = wrap(store)
	}
	server := New(repo, vault, Config{BootstrapToken: testBootstrapToken}, nil)
	token, err := server.auth.SetupPassword(context.Background(), "test", "", "synthetic-test-password", false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return server, store, &http.Cookie{Name: sessionCookie, Value: token}
}

func getAccounts(t testing.TB, handler http.Handler, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://localhost/api/accounts", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/accounts = %d %s", rec.Code, rec.Body.String())
	}
	return rec
}

// seedAccountsListFixture stores accounts covering every field of the list:
// aliases and duplicate e-mails, every window with an expired one, purchased
// credits (balance, exhausted, unlimited), additional quotas with a routing
// override, groups, tokens with and without expiry, expired and unreadable
// tokens, no credential, an external account, a deleted account, usage
// inside and outside the last 7 days and reset-credit snapshots.
func seedAccountsListFixture(t *testing.T, server *Server, store *sqlite.Store, usage *accountsListUsage) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	encrypt := func(plain string) []byte {
		t.Helper()
		value, err := server.cipher.Encrypt([]byte(plain))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	corrupt := func(value []byte) []byte {
		decoded, err := base64.URLEncoding.DecodeString(string(value))
		if err != nil {
			t.Fatal(err)
		}
		decoded[len(decoded)-1] ^= 0xff
		return []byte(base64.URLEncoding.EncodeToString(decoded))
	}
	at := func(d time.Duration) *time.Time { value := now.Add(d); return &value }
	minutes := func(m int) *int { return &m }
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.AdditionalQuotaRoutingPolicies = map[string]string{"codex_spark": "preserve"}
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"g-b", "g-a"} {
		if err := store.SaveGroup(ctx, domain.AccountGroup{ID: group, Name: "Group " + group}, now); err != nil {
			t.Fatal(err)
		}
	}
	type fixture struct {
		account    domain.Account
		credential *domain.AccountCredential
		quotas     []domain.AccountQuota
		credits    *domain.AccountCreditStatus
		additional []domain.AccountAdditionalQuota
		groups     []string
	}
	chatgpt := func(id, email, alias, plan string, status domain.AccountStatus) domain.Account {
		return domain.Account{ID: id, Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "cg-" + id,
			Email: email, Alias: alias, PlanType: plan, RoutingPolicy: "normal", Status: status, CreatedAt: now.Add(-time.Hour)}
	}
	alpha := chatgpt("acct-alpha", "alpha@example.test", "Alpha", "plus", domain.AccountActive)
	alpha.RoutingPolicy, alpha.LimitWarmupEnabled, alpha.SecurityWorkAuthorized = "burn_first", true, true
	alpha.WorkspaceID, alpha.WorkspaceLabel, alpha.SeatType = "org-alpha", "Alpha Team", "default"
	external := domain.Account{ID: "acct-ext", Kind: domain.AccountExternal, Provider: "zai", BaseURL: "https://api.example.test/v1",
		Email: "ext@example.test", PlanType: "", RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: now.Add(-time.Hour)}
	accessAlpha := encrypt(testJWT(map[string]any{"sub": "alpha", "exp": now.Add(time.Hour).Unix()}))
	idAlpha := encrypt(testJWT(map[string]any{"sub": "alpha", "https://api.openai.com/auth": map[string]any{
		"chatgpt_subscription_active_until": now.Add(20 * 24 * time.Hour).Format(time.RFC3339)}}))
	fixtures := []fixture{
		{account: alpha,
			credential: &domain.AccountCredential{AccessTokenEncrypted: accessAlpha, RefreshTokenEncrypted: encrypt("refresh-alpha"), IDTokenEncrypted: idAlpha},
			quotas: []domain.AccountQuota{{Window: "primary", UsedPercent: 40, ResetAt: at(2 * time.Hour), WindowMinutes: minutes(300)},
				{Window: "secondary", UsedPercent: 70, ResetAt: at(72 * time.Hour), WindowMinutes: minutes(10080)}},
			credits: &domain.AccountCreditStatus{Has: boolPtr(true), Unlimited: boolPtr(false), Balance: floatPtr(12.5)},
			additional: []domain.AccountAdditionalQuota{
				{QuotaKey: "codex_spark", Window: "primary", LimitName: "codex_spark", MeteredFeature: "codex", UsedPercent: 25, ResetAt: at(time.Hour), WindowMinutes: minutes(300)},
				{QuotaKey: "codex_spark", Window: "secondary", LimitName: "codex_spark", MeteredFeature: "codex", UsedPercent: 5},
				{QuotaKey: "mystery", Window: "primary", LimitName: "mystery_limit", MeteredFeature: "mystery", UsedPercent: 60}},
			groups: []string{"g-b", "g-a"}},
		{account: chatgpt("acct-beta", "shared@example.test", "", "pro", domain.AccountQuotaExceeded),
			credential: &domain.AccountCredential{AccessTokenEncrypted: encrypt(testJWT(map[string]any{"exp": now.Add(-time.Hour).Unix()})),
				RefreshTokenEncrypted: corrupt(encrypt("refresh-beta")), IDTokenEncrypted: encrypt("not-a-jwt")},
			quotas: []domain.AccountQuota{{Window: "primary", UsedPercent: 100, ResetAt: at(time.Hour), WindowMinutes: minutes(300)},
				{Window: "secondary", UsedPercent: 100, ResetAt: at(48 * time.Hour), WindowMinutes: minutes(10080)}},
			credits: &domain.AccountCreditStatus{Has: boolPtr(false), Balance: floatPtr(0)}, groups: []string{"g-a"}},
		{account: chatgpt("acct-gamma", "gamma@example.test", "Gamma", "free", domain.AccountActive),
			quotas: []domain.AccountQuota{{Window: "primary", UsedPercent: 100, ResetAt: at(30 * 24 * time.Hour)},
				{Window: "secondary", UsedPercent: 100, ResetAt: at(30 * 24 * time.Hour)},
				{Window: "monthly", UsedPercent: 40, ResetAt: at(30 * 24 * time.Hour), WindowMinutes: minutes(43200)}}},
		{account: chatgpt("acct-delta", "shared@example.test", "", "plus", domain.AccountPaused),
			credential: &domain.AccountCredential{AccessTokenEncrypted: encrypt("plain-access"), RefreshTokenEncrypted: encrypt("refresh"), IDTokenEncrypted: encrypt("id")},
			quotas: []domain.AccountQuota{{Window: "primary", UsedPercent: 100, ResetAt: at(-time.Hour), WindowMinutes: minutes(300)},
				{Window: "secondary", UsedPercent: 20, ResetAt: at(24 * time.Hour), WindowMinutes: minutes(10080)}},
			credits: &domain.AccountCreditStatus{Unlimited: boolPtr(true)}},
		{account: chatgpt("acct-reauth", "reauth@example.test", "Needs login", "team", domain.AccountReauthRequired),
			credential: &domain.AccountCredential{AccessTokenEncrypted: encrypt("a"), RefreshTokenEncrypted: encrypt("r"), IDTokenEncrypted: encrypt("i")}},
		{account: external, credential: &domain.AccountCredential{ExternalKeyEncrypted: encrypt("external-api-key")}},
		{account: chatgpt("acct-gone", "gone@example.test", "", "plus", domain.AccountActive),
			credential: &domain.AccountCredential{AccessTokenEncrypted: encrypt("a"), RefreshTokenEncrypted: encrypt("r"), IDTokenEncrypted: encrypt("i")}},
	}
	for _, f := range fixtures {
		if err := store.SaveAccount(ctx, f.account); err != nil {
			t.Fatal(err)
		}
		if f.credential != nil {
			credential := *f.credential
			credential.AccountID = f.account.ID
			if err := store.SaveAccountCredential(ctx, credential); err != nil {
				t.Fatal(err)
			}
		}
		if len(f.quotas) > 0 || f.credits != nil || len(f.additional) > 0 {
			snapshot := domain.AccountUsageSnapshot{AccountID: f.account.ID, ObservedAt: now, Credits: f.credits,
				AdditionalQuotas: f.additional, AdditionalReported: len(f.additional) > 0}
			if snapshot.Credits != nil {
				snapshot.Credits.AccountID, snapshot.Credits.ObservedAt = f.account.ID, now
			}
			for _, q := range f.quotas {
				q.AccountID, q.ObservedAt = f.account.ID, now
				snapshot.Quotas = append(snapshot.Quotas, q)
			}
			for i := range snapshot.AdditionalQuotas {
				snapshot.AdditionalQuotas[i].AccountID, snapshot.AdditionalQuotas[i].ObservedAt = f.account.ID, now
			}
			if err := store.SaveAccountUsageSnapshot(ctx, snapshot); err != nil {
				t.Fatal(err)
			}
		}
		if len(f.groups) > 0 {
			if err := store.SetAccountGroups(ctx, f.account.ID, f.groups); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, event := range []struct {
		account string
		ago     time.Duration
		kind    string
	}{{"acct-alpha", time.Hour, "normal"}, {"acct-alpha", 50 * time.Hour, "normal"}, {"acct-alpha", 8 * 24 * time.Hour, "normal"},
		{"acct-alpha", 2 * time.Hour, "warmup"}, {"acct-beta", 3 * time.Hour, "normal"}, {"acct-gone", time.Hour, "normal"}} {
		if _, err := store.RecordUsage(ctx, domain.UsageEvent{RequestID: fmt.Sprintf("req-%d", i), AccountID: event.account,
			Model: "gpt-test", RequestKind: event.kind, Status: "success", RequestedAt: now.Add(-event.ago),
			Usage: domain.UsageAmount{InputTokens: 1000, OutputTokens: 200, CachedInputTokens: 300, CostMicrodollars: 1500000}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteAccount(ctx, "acct-gone", false); err != nil {
		t.Fatal(err)
	}
	service := application.NewAccountUsageService(store, cipherTokenSource{cipher: server.cipher}, usage, &noPins{}, time.Now)
	for _, id := range []string{"acct-alpha", "acct-beta"} {
		if err := service.RefreshResetCredits(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	server.ConfigureUsage(service)
}

// GET /api/accounts from the batch read keeps every field of the per-account
// reads with its value, adds groups, usage7d, resetCreditsObservedAt and the
// shell status, and calls no provider (R42, R43).
func TestAccountsListKeepsEveryExistingField(t *testing.T) {
	server, store, cookie := newAccountsListServer(t, nil)
	usage := &accountsListUsage{expires: time.Now().Add(5 * 24 * time.Hour).UTC().Truncate(time.Second)}
	seedAccountsListFixture(t, server, store, usage)
	handler := server.Handler(nil, nil)
	calls := usage.calls.Load()
	rec := getAccounts(t, handler, cookie)
	if usage.calls.Load() != calls {
		t.Fatalf("the list called the provider %d times", usage.calls.Load()-calls)
	}
	if rec.Header().Get("Server-Timing") == "" {
		t.Fatal("no Server-Timing header")
	}
	oracle := httptest.NewRecorder()
	server.perAccountListAccounts(oracle, httptest.NewRequest(http.MethodGet, "http://localhost/api/accounts", nil))
	decode := func(body []byte) (accounts []map[string]any, shell map[string]any) {
		var response struct {
			Accounts []map[string]any `json:"accounts"`
			Shell    map[string]any   `json:"shell"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		return response.Accounts, response.Shell
	}
	got, shell := decode(rec.Body.Bytes())
	want, _ := decode(oracle.Body.Bytes())
	if len(got) != 6 || len(got) != len(want) {
		t.Fatalf("%d accounts, oracle %d", len(got), len(want))
	}
	added := []string{"groups", "resetCreditsObservedAt", "usage7d"}
	for i := range want {
		id := want[i]["accountId"]
		if got[i]["accountId"] != id {
			t.Fatalf("row %d is %v, oracle %v", i, got[i]["accountId"], id)
		}
		for field, value := range want[i] {
			if actual, present := got[i][field]; !present || !reflect.DeepEqual(actual, value) {
				t.Errorf("%v.%s = %#v (present %v), per-account reads %#v", id, field, actual, present, value)
			}
		}
		var extra []string
		for field := range got[i] {
			if _, present := want[i][field]; !present {
				extra = append(extra, field)
			}
		}
		sort.Strings(extra)
		if !reflect.DeepEqual(extra, added) {
			t.Errorf("%v adds %v, want %v", id, extra, added)
		}
	}
	byID := map[string]map[string]any{}
	for _, row := range got {
		byID[row["accountId"].(string)] = row
	}
	alpha := byID["acct-alpha"]
	if !reflect.DeepEqual(alpha["groups"], []any{"g-a", "g-b"}) || !reflect.DeepEqual(byID["acct-gamma"]["groups"], []any{}) {
		t.Errorf("groups: alpha %v, gamma %v", alpha["groups"], byID["acct-gamma"]["groups"])
	}
	// Two normal requests of alpha are inside the 7 days; the warm-up and
	// the 8-day-old request are not.
	if want := map[string]any{"requests": float64(2), "totalTokens": float64(2400), "costUsd": 3.0}; !reflect.DeepEqual(alpha["usage7d"], want) {
		t.Errorf("alpha usage7d = %v, want %v", alpha["usage7d"], want)
	}
	if observed, ok := alpha["resetCreditsObservedAt"].(string); !ok || observed == "" || alpha["availableResetCredits"] != float64(2) {
		t.Errorf("alpha reset credits %v observed %v", alpha["availableResetCredits"], alpha["resetCreditsObservedAt"])
	}
	// The fixture reaches every token state and the window variants.
	auth := func(id, token string) any { return byID[id]["auth"].(map[string]any)[token].(map[string]any)["state"] }
	for _, check := range []struct {
		got, want any
		name      string
	}{
		{auth("acct-alpha", "access"), "present", "alpha access"}, {auth("acct-beta", "access"), "expired", "beta access"},
		{auth("acct-beta", "refresh"), "unreadable", "beta refresh"}, {auth("acct-gamma", "access"), "", "gamma without credential"},
		{auth("acct-ext", "access"), "missing", "external access"}, {alpha["subscriptionActiveUntil"] != nil, true, "alpha subscription"},
		{byID["acct-gamma"]["usage"].(map[string]any)["monthlyRemainingPercent"], float64(60), "gamma monthly"},
		{len(alpha["additionalQuotas"].([]any)), 2, "alpha additional quotas"}, {byID["acct-delta"]["creditsUnlimited"], true, "delta unlimited"},
		{byID["acct-beta"]["status"], "quota_exceeded", "beta status"}, {byID["acct-beta"]["displayName"], "shared@example.test", "beta label"},
	} {
		if !reflect.DeepEqual(check.got, check.want) {
			t.Errorf("%s = %#v, want %#v", check.name, check.got, check.want)
		}
	}
	if byID["acct-gamma"]["resetCreditsObservedAt"] != nil {
		t.Errorf("gamma has no snapshot but observed %v", byID["acct-gamma"]["resetCreditsObservedAt"])
	}
	for _, field := range []string{"ready", "lastSyncAt", "rollupsUsable", "reauthRequiredAccounts", "availableResetCredits", "routingStrategy"} {
		if _, ok := shell[field]; !ok {
			t.Errorf("shell misses %s: %v", field, shell)
		}
	}
	if shell["reauthRequiredAccounts"] != float64(1) || shell["availableResetCredits"] != float64(4) {
		t.Errorf("shell counts: %v", shell)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("external-api-key")) || bytes.Contains(rec.Body.Bytes(), []byte("refresh-alpha")) {
		t.Fatal("the list leaks a credential")
	}
}

// callCountingRepository counts the account reads a handler issues.
type callCountingRepository struct {
	Repository
	mu    sync.Mutex
	calls map[string]int
}

func (c *callCountingRepository) count(name string) {
	c.mu.Lock()
	c.calls[name]++
	c.mu.Unlock()
}

func (c *callCountingRepository) snapshot() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := make(map[string]int, len(c.calls))
	for name, n := range c.calls {
		copied[name] = n
	}
	return copied
}

func (c *callCountingRepository) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	c.count("ListAccounts")
	return c.Repository.ListAccounts(ctx)
}

func (c *callCountingRepository) GetAccount(ctx context.Context, id string) (domain.Account, error) {
	c.count("GetAccount")
	return c.Repository.GetAccount(ctx, id)
}

func (c *callCountingRepository) GetAccountCredential(ctx context.Context, id string) (domain.AccountCredential, error) {
	c.count("GetAccountCredential")
	return c.Repository.GetAccountCredential(ctx, id)
}

func (c *callCountingRepository) ListAccountQuota(ctx context.Context, id string) ([]domain.AccountQuota, error) {
	c.count("ListAccountQuota")
	return c.Repository.ListAccountQuota(ctx, id)
}

func (c *callCountingRepository) LoadAccountCreditStatus(ctx context.Context, id string) (*domain.AccountCreditStatus, error) {
	c.count("LoadAccountCreditStatus")
	return c.Repository.LoadAccountCreditStatus(ctx, id)
}

func (c *callCountingRepository) ListAccountAdditionalQuotas(ctx context.Context, id string) ([]domain.AccountAdditionalQuota, error) {
	c.count("ListAccountAdditionalQuotas")
	return c.Repository.ListAccountAdditionalQuotas(ctx, id)
}

func (c *callCountingRepository) LoadAccountQuotaRefusalAt(ctx context.Context, id string) (*time.Time, error) {
	c.count("LoadAccountQuotaRefusalAt")
	return c.Repository.LoadAccountQuotaRefusalAt(ctx, id)
}

func (c *callCountingRepository) UsageTotals(ctx context.Context, key, account string) (domain.UsageTotals, error) {
	c.count("UsageTotals")
	return c.Repository.UsageTotals(ctx, key, account)
}

func (c *callCountingRepository) ListAccountViews(ctx context.Context, now time.Time) (domain.AccountViews, error) {
	c.count("ListAccountViews")
	return c.Repository.ListAccountViews(ctx, now)
}

func (c *callCountingRepository) LoadSettings(ctx context.Context) (domain.RuntimeSettings, error) {
	c.count("LoadSettings")
	return c.Repository.LoadSettings(ctx)
}

func (c *callCountingRepository) ShellSnapshot(ctx context.Context) (domain.ShellSnapshot, error) {
	c.count("ShellSnapshot")
	return c.Repository.ShellSnapshot(ctx)
}

// GET /api/accounts issues the same repository reads for 9 and for 100
// accounts and none per account; the statement count of ListAccountViews is
// pinned by the store's own test (R43, R49).
func TestAccountsListReadsDoNotGrowWithAccounts(t *testing.T) {
	var counts []map[string]int
	for _, n := range []int{9, 100} {
		var repo *callCountingRepository
		server, store, cookie := newAccountsListServer(t, func(store Repository) Repository {
			repo = &callCountingRepository{Repository: store, calls: map[string]int{}}
			return repo
		})
		ctx := context.Background()
		now := time.Now().UTC()
		for i := range n {
			id := fmt.Sprintf("acct-%03d", i)
			if err := store.SaveAccount(ctx, domain.Account{ID: id, Kind: domain.AccountChatGPT, Provider: "openai",
				ChatGPTAccountID: "cg-" + id, Email: id + "@example.test", PlanType: "plus", Status: domain.AccountActive, CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveAccountCredential(ctx, encryptHTTPTestToken(t, server, id)); err != nil {
				t.Fatal(err)
			}
			reset := now.Add(time.Duration(i+1) * time.Minute)
			if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: id, Window: "primary", UsedPercent: float64(i % 100),
				ResetAt: &reset, ObservedAt: now}); err != nil {
				t.Fatal(err)
			}
		}
		handler := server.Handler(nil, nil)
		before := repo.snapshot()
		var runs []time.Duration
		for range 5 {
			start := time.Now()
			rec := getAccounts(t, handler, cookie)
			runs = append(runs, time.Since(start))
			var response struct {
				Accounts []json.RawMessage `json:"accounts"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || len(response.Accounts) != n {
				t.Fatalf("%d accounts listed for %d: %v", len(response.Accounts), n, err)
			}
		}
		after := repo.snapshot()
		delta := map[string]int{}
		for name, calls := range after {
			if calls != before[name] {
				delta[name] = (calls - before[name]) / 5
			}
		}
		counts = append(counts, delta)
		sort.Slice(runs, func(i, j int) bool { return runs[i] < runs[j] })
		t.Logf("%d accounts: reads per request %v, median %v", n, delta, runs[2])
	}
	if !reflect.DeepEqual(counts[0], counts[1]) {
		t.Fatalf("reads per request: %v for 9 accounts, %v for 100", counts[0], counts[1])
	}
	for _, name := range []string{"ListAccounts", "GetAccount", "GetAccountCredential", "ListAccountQuota", "LoadAccountCreditStatus",
		"ListAccountAdditionalQuotas", "LoadAccountQuotaRefusalAt", "UsageTotals"} {
		if counts[1][name] != 0 {
			t.Errorf("%s is read %d times per request", name, counts[1][name])
		}
	}
	if counts[1]["ListAccountViews"] != 1 {
		t.Errorf("ListAccountViews is read %d times per request", counts[1]["ListAccountViews"])
	}
}
