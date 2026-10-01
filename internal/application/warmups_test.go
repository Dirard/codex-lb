package application

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

type stubResponseProvider struct {
	mu      sync.Mutex
	targets []string
	bodies  []json.RawMessage
	result  ResponseResult
	err     error
}

type stubAdmission struct {
	acquired int
	released int
	err      error
}

type warmupAdmissionFunc func()

func (f warmupAdmissionFunc) Acquire(context.Context) (func(), error) {
	f()
	return func() {}, nil
}

func (s *stubAdmission) Acquire(context.Context) (func(), error) {
	s.acquired++
	if s.err != nil {
		return nil, s.err
	}
	return func() { s.released++ }, nil
}

func (s *stubResponseProvider) Respond(_ context.Context, target ResponseTarget, body json.RawMessage, _ func(ResponseEvent) error) (ResponseResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets = append(s.targets, target.Account.ID)
	s.bodies = append(s.bodies, body)
	return s.result, s.err
}

type stubLedger struct {
	mu        sync.Mutex
	events    []domain.UsageEvent
	uncertain int
	settleErr error
}

type cancelingWarmupProvider struct {
	cancel context.CancelFunc
	known  bool
}

func (p cancelingWarmupProvider) Respond(context.Context, ResponseTarget, json.RawMessage, func(ResponseEvent) error) (ResponseResult, error) {
	p.cancel()
	return ResponseResult{Usage: domain.UsageAmount{InputTokens: 1}, UsageKnown: p.known}, context.Canceled
}

type cancellationAwareWarmupLedger struct {
	stubLedger
	recorded bool
}

func (l *cancellationAwareWarmupLedger) SettleUsage(ctx context.Context, _ string, _ domain.UsageSettlement) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	l.recorded = true
	return true, nil
}

func (l *cancellationAwareWarmupLedger) MarkReservationUncertain(ctx context.Context, id string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return l.stubLedger.MarkReservationUncertain(ctx, id)
}

func (s *stubLedger) ReserveUsage(context.Context, domain.ReservationRequest) (domain.Reservation, error) {
	return domain.Reservation{}, nil
}
func (s *stubLedger) ReserveWarmupUsage(context.Context, string, string, string, int64, bool, time.Time) error {
	return nil
}
func (s *stubLedger) MarkReservationUncertain(context.Context, string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uncertain++
	return true, nil
}
func (s *stubLedger) SettleUsage(ctx context.Context, _ string, settlement domain.UsageSettlement) (bool, error) {
	if s.settleErr != nil {
		return false, s.settleErr
	}
	return s.RecordUsage(ctx, settlement.Event)
}
func (s *stubLedger) GetReservation(context.Context, string) (domain.Reservation, error) {
	return domain.Reservation{}, domain.ErrNotFound
}
func (s *stubLedger) TouchReservation(context.Context, string, time.Time) (bool, error) {
	return true, nil
}
func (s *stubLedger) ReleaseStaleReservations(context.Context, time.Time) (int, error) { return 0, nil }
func (s *stubLedger) RecordUsage(_ context.Context, event domain.UsageEvent) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return true, nil
}
func (s *stubLedger) UsageTotals(context.Context, string, string) (domain.UsageTotals, error) {
	return domain.UsageTotals{}, nil
}

type stubRefresher struct {
	refreshes []string
}

func (s *stubRefresher) RefreshAccountUsageForGeneration(_ context.Context, accountID string, _ int64) error {
	s.refreshes = append(s.refreshes, accountID)
	return nil
}

func newWarmupTestService(t *testing.T, provider *stubResponseProvider) (*WarmupService, *fakeAccountsStore, *stubLedger, *stubAdmission, *stubRefresher, *time.Time) {
	t.Helper()
	store := newFakeAccounts()
	ledger := &stubLedger{}
	admission := &stubAdmission{}
	now := time.Unix(1_800_000_000, 0).UTC()
	refresher := &stubRefresher{}
	service := NewWarmupService(store, provider, ledger, admission, refresher, func() time.Time { return now })
	return service, store, ledger, admission, refresher, &now
}

func warmupAccount(id string, status domain.AccountStatus) domain.Account {
	return domain.Account{
		ID: id, Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "cg-" + id,
		Email: id + "@example.test", PlanType: "plus", RoutingPolicy: "normal",
		Status: status, CreatedAt: time.Unix(1, 0),
	}
}

func TestWarmupExecutesContentFreeRequestAndRecordsUsage(t *testing.T) {
	ctx := context.Background()
	provider := &stubResponseProvider{result: ResponseResult{
		ResponseID: "resp_1", Usage: domain.UsageAmount{InputTokens: 3, OutputTokens: 2}, UsageKnown: true,
	}}
	service, store, ledger, admission, _, _ := newWarmupTestService(t, provider)
	account := warmupAccount("acct_warm", domain.AccountActive)
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	reasoning := "low"
	if err := service.ExecuteWarmup(ctx, account.ID, "gpt-5.4-mini", &reasoning, "Say OK."); err != nil {
		t.Fatal(err)
	}
	if len(provider.targets) != 1 || provider.targets[0] != account.ID {
		t.Fatalf("targets = %+v", provider.targets)
	}
	var body map[string]any
	if json.Unmarshal(provider.bodies[0], &body) != nil || body["model"] != "gpt-5.4-mini" ||
		body["max_output_tokens"] != float64(16) || body["store"] != false || body["input"] != "Say OK." {
		t.Fatalf("body = %s", provider.bodies[0])
	}
	ledger.mu.Lock()
	events := append([]domain.UsageEvent(nil), ledger.events...)
	ledger.mu.Unlock()
	if len(events) != 1 || events[0].RequestKind != "warmup" || events[0].AccountID != account.ID ||
		events[0].Status != "ok" || events[0].Usage.InputTokens != 3 || events[0].Usage.OutputTokens != 2 {
		t.Fatalf("events = %+v", events)
	}
	if admission.acquired != 1 || admission.released != 1 {
		t.Fatalf("admission = %d/%d", admission.acquired, admission.released)
	}
}

func TestWarmupQuotaRefusalRecordsOutcomeWithoutInventingQuotaFromTimeout(t *testing.T) {
	ctx := context.Background()
	account := warmupAccount("acct_quota", domain.AccountActive)
	quotaProvider := &stubResponseProvider{err: &ProviderFailure{Code: "insufficient_quota", Status: 429, QuotaRefused: true, Dispatched: true}}
	service, store, _, _, _, _ := newWarmupTestService(t, quotaProvider)
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := service.ExecuteWarmup(ctx, account.ID, "m", nil, ""); !errors.Is(err, errWarmupQuotaRefused) {
		t.Fatalf("quota err = %v", err)
	}
	timeoutProvider := &stubResponseProvider{err: &ProviderFailure{Code: "network_error", Status: 0, Dispatched: false}}
	timeoutService, timeoutStore, _, _, _, _ := newWarmupTestService(t, timeoutProvider)
	if err := timeoutStore.SaveAccount(ctx, warmupAccount("acct_timeout", domain.AccountActive)); err != nil {
		t.Fatal(err)
	}
	if err := timeoutService.ExecuteWarmup(ctx, "acct_timeout", "m", nil, ""); errors.Is(err, errWarmupQuotaRefused) {
		t.Fatal("local timeout classified as quota refusal")
	}
}

func TestWarmupAccountingFailureTakesPrecedenceOverQuota(t *testing.T) {
	provider := &stubResponseProvider{result: ResponseResult{Failed: true, ErrorCode: "usage_limit_reached", UsageKnown: true}}
	service, accounts, ledger, admission, _, _ := newWarmupTestService(t, provider)
	account := warmupAccount("quota-accounting", domain.AccountActive)
	if err := accounts.SaveAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	ledger.settleErr = errors.New("synthetic ledger failure")
	err := service.ExecuteWarmup(context.Background(), account.ID, "gpt-5.4-mini", nil, "")
	var failure *ProxyError
	if !errors.As(err, &failure) || failure.Code != "usage_settlement_failed" || errors.Is(err, ErrWarmupQuotaRefused) || ledger.uncertain != 1 || len(ledger.events) != 0 || admission.released != 1 {
		t.Fatalf("accounting failure hidden or released: %v pending=%d", err, ledger.uncertain)
	}
}

func TestWarmupCancellationStillRecordsDispatchedUsage(t *testing.T) {
	for _, known := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		accounts := newFakeAccounts()
		account := warmupAccount("acct_cancel", domain.AccountActive)
		if err := accounts.SaveAccount(context.Background(), account); err != nil {
			t.Fatal(err)
		}
		ledger := &cancellationAwareWarmupLedger{}
		service := NewWarmupService(accounts, cancelingWarmupProvider{cancel: cancel, known: known}, ledger, nil, nil, time.Now)
		if err := service.ExecuteWarmup(ctx, account.ID, "gpt-5.4-mini", nil, "Say OK."); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel = %v", err)
		}
		if ledger.recorded != known || (!known && ledger.uncertain != 1) {
			t.Fatal("cancelled warmup lost its accounting certainty")
		}
	}
}

func TestAutomaticWarmupRechecksOptInAfterAdmission(t *testing.T) {
	ctx := context.Background()
	accounts := newFakeAccounts()
	account := warmupAccount("acct_optout", domain.AccountActive)
	account.LimitWarmupEnabled = true
	if err := accounts.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	provider := &stubResponseProvider{}
	admission := warmupAdmissionFunc(func() { _ = accounts.UpdateAccountWarmup(ctx, account.ID, false) })
	service := NewWarmupService(accounts, provider, &stubLedger{}, admission, nil, time.Now)
	if err := service.ExecuteLimitWarmup(ctx, account.ID, account.Generation, "gpt-5.4-mini", "Say OK."); !errors.Is(err, errWarmupIneligible) {
		t.Fatalf("opt-out before send = %v", err)
	}
	if len(provider.targets) != 0 {
		t.Fatalf("paid dispatch after opt-out: %v", provider.targets)
	}
}

func TestWarmupRejectsIneligibleAccounts(t *testing.T) {
	ctx := context.Background()
	provider := &stubResponseProvider{}
	service, store, _, _, _, _ := newWarmupTestService(t, provider)
	for _, account := range []domain.Account{
		warmupAccount("acct_reauth", domain.AccountReauthRequired),
		warmupAccount("acct_deactivated", domain.AccountDeactivated),
	} {
		if err := store.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		if err := service.ExecuteWarmup(ctx, account.ID, "m", nil, ""); !errors.Is(err, errWarmupIneligible) {
			t.Fatalf("%s err = %v", account.ID, err)
		}
	}
	external := warmupAccount("acct_external", domain.AccountActive)
	external.Kind = domain.AccountExternal
	if err := store.SaveAccount(ctx, external); err != nil {
		t.Fatal(err)
	}
	if err := service.ExecuteWarmup(ctx, external.ID, "m", nil, ""); !errors.Is(err, errWarmupIneligible) {
		t.Fatalf("external err = %v", err)
	}
	if len(provider.targets) != 0 {
		t.Fatalf("ineligible dispatch = %+v", provider.targets)
	}
}

func TestProbeAccountReportsQuotaSnapshots(t *testing.T) {
	ctx := context.Background()
	provider := &stubResponseProvider{result: ResponseResult{ResponseID: "probe", UsageKnown: true}}
	service, store, _, admission, _, _ := newWarmupTestService(t, provider)
	account := warmupAccount("acct_probe", domain.AccountActive)
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: account.ID, Window: "primary", UsedPercent: 12.5, ObservedAt: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	result, err := service.ProbeAccount(ctx, account.ID, "gpt-5.4-mini")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.AccountStatusBefore != "active" || result.AccountStatusAfter != "active" ||
		result.PrimaryBefore == nil || *result.PrimaryBefore != 12.5 || result.PrimaryAfter == nil || *result.PrimaryAfter != 12.5 ||
		result.ProbeStatusCode == nil || *result.ProbeStatusCode != 200 {
		t.Fatalf("probe = %+v", result)
	}
	if admission.acquired != 1 || admission.released != 1 {
		t.Fatalf("probe admission = %d/%d", admission.acquired, admission.released)
	}
}

func TestProbeAccountRefreshesUsageAfterDispatch(t *testing.T) {
	ctx := context.Background()
	provider := &stubResponseProvider{result: ResponseResult{ResponseID: "probe-refresh", UsageKnown: true}}
	service, store, _, _, refresher, _ := newWarmupTestService(t, provider)
	account := warmupAccount("acct_probe_refresh", domain.AccountActive)
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProbeAccount(ctx, account.ID, "gpt-5.4-mini"); err != nil {
		t.Fatal(err)
	}
	if len(refresher.refreshes) != 1 || refresher.refreshes[0] != account.ID {
		t.Fatalf("refresher calls = %+v", refresher.refreshes)
	}
	if len(provider.targets) != 1 {
		t.Fatalf("probe dispatches = %+v", provider.targets)
	}
}
