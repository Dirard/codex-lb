package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

type sqliteOperationProvider struct {
	calls   int
	block   chan struct{}
	started chan struct{}
}

func (p *sqliteOperationProvider) Compact(ctx context.Context, _ application.CodexOperationTarget, _ json.RawMessage) (application.CodexOperationResult, error) {
	p.calls++
	if p.started != nil {
		close(p.started)
	}
	if p.block != nil {
		select {
		case <-p.block:
		case <-ctx.Done():
			return application.CodexOperationResult{}, ctx.Err()
		}
	}
	return application.CodexOperationResult{
		Status: 200, ContentType: "application/json", Body: []byte(`{"object":"response.compact","usage":{"input_tokens":10,"output_tokens":2}}`),
		Usage: domain.UsageAmount{InputTokens: 10, OutputTokens: 2}, UsageKnown: true,
	}, nil
}
func (p *sqliteOperationProvider) Control(context.Context, application.CodexOperationTarget, application.CodexControlRequest) (application.CodexOperationResult, error) {
	return application.CodexOperationResult{Status: 200, Body: []byte(`{}`), ContentType: "application/json"}, nil
}
func (p *sqliteOperationProvider) CreateFile(context.Context, application.CodexOperationTarget, json.RawMessage) (application.CodexOperationResult, error) {
	return application.CodexOperationResult{}, nil
}
func (p *sqliteOperationProvider) FinalizeFile(context.Context, application.CodexOperationTarget, string) (application.CodexOperationResult, error) {
	return application.CodexOperationResult{}, nil
}
func (p *sqliteOperationProvider) Transcribe(context.Context, application.CodexOperationTarget, application.CodexTranscriptionRequest) (application.CodexOperationResult, error) {
	return application.CodexOperationResult{}, nil
}
func (p *sqliteOperationProvider) Realtime(context.Context, application.CodexOperationTarget, application.CodexRealtimeRequest, application.CodexRealtimeConnection) error {
	return nil
}

type sqliteAdmission struct{}

func (sqliteAdmission) Acquire(context.Context) (func(), error) { return func() {}, nil }

func TestCodexOperationsSQLiteLedgerAndOwner(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "operations.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	account := domain.Account{
		ID: "acct-owner", Kind: domain.AccountChatGPT, Provider: "openai", Email: "owner@example.test",
		PlanType: "plus", RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: now,
	}
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	token, err := vault.Encrypt([]byte("synthetic-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{
		AccountID: account.ID, AccessTokenEncrypted: token, RefreshTokenEncrypted: token, IDTokenEncrypted: token,
	}); err != nil {
		t.Fatal(err)
	}
	group := domain.AccountGroup{
		ID: "grp", Name: "grp", AccountIDs: []string{account.ID}, CreatedAt: now,
		Limits: []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 10000, ResetAt: now.Add(time.Hour)}},
	}
	if err := store.SaveGroup(ctx, group, now); err != nil {
		t.Fatal(err)
	}
	key := domain.APIKey{
		ID: "key-ops", Name: "operations", KeyHash: fmt.Sprintf("%x", make([]byte, 32)), KeyPrefix: "sk-synthetic", GroupID: &group.ID,
		IsActive: true, CreatedAt: now,
	}
	if err := store.SaveAPIKey(ctx, key, now); err != nil {
		t.Fatal(err)
	}
	provider := &sqliteOperationProvider{}
	service := application.NewCodexOperations(store, store, provider, time.Hour)
	service.ConfigureAdmission(sqliteAdmission{})
	service.ConfigureAccountSelection(application.NewProxy(store, nil, nil, application.ProxyConfig{}))
	service.ConfigurePrice(func(context.Context, domain.Account, string) (pricing.Price, error) {
		return pricing.Price{}, nil
	})

	body := []byte(`{"model":"gpt-6-sol","instructions":"summarize","input":"hello"}`)
	result, err := service.Compact(ctx, "key-ops", "", "", body)
	if err != nil || result.Status != 200 || provider.calls != 1 {
		t.Fatalf("compact failed: %+v %v calls=%d", result, err, provider.calls)
	}
	totals, err := store.UsageTotals(ctx, "key-ops", account.ID)
	if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 10 || totals.Usage.OutputTokens != 2 {
		t.Fatalf("settled totals: %+v %v", totals, err)
	}
	limits, err := store.GetAPIKey(ctx, "key-ops")
	if err != nil || len(limits.Limits) != 1 || limits.Limits[0].CurrentValue != 12 {
		t.Fatalf("limit was not adjusted to actual usage: %+v %v", limits, err)
	}
	if _, err := service.Control(ctx, "key-ops", "", "", application.CodexControlRequest{
		Method: "GET", Path: "agent-identities/jwks",
	}); err != nil {
		t.Fatal(err)
	}
	limits, err = store.GetAPIKey(ctx, "key-ops")
	if err != nil || limits.Limits[0].CurrentValue != 12 {
		t.Fatalf("content-free control consumed key limits: %+v %v", limits, err)
	}

	fileOwner := application.CodexResourceOwner{
		ResourceType: application.CodexResourceFile, ResourceID: "file-owned", KeyID: "key-ops",
		AccountID: account.ID, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.SaveCodexResourceOwner(ctx, fileOwner); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetCodexResourceOwner(ctx, application.CodexResourceFile, "file-owned", "key-ops", now)
	if err != nil || loaded.AccountID != account.ID {
		t.Fatalf("file owner persistence: %+v %v", loaded, err)
	}

	contextBytes, err := json.Marshal(map[string]any{"items": []string{"prior"}})
	if err != nil {
		t.Fatal(err)
	}
	contextEncrypted, err := vault.Encrypt(contextBytes)
	if err != nil {
		t.Fatal(err)
	}
	continuation := domain.Continuation{
		ResponseID: "resp-owner", KeyID: "key-ops", AccountID: account.ID, ProviderID: "openai",
		Model: "gpt-6-sol", CreatedAt: now, ExpiresAt: now.Add(time.Hour), ContextEncrypted: contextEncrypted,
	}
	if err := store.SaveContinuation(ctx, continuation, domain.ContinuationBounds{MaxRecords: 10, MaxContextBytes: 1024 * 1024}); err != nil {
		t.Fatal(err)
	}
	account.Status = domain.AccountQuotaExceeded
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EligibleAccounts(ctx, "key-ops"); !errors.Is(err, domain.ErrNoAccounts) {
		t.Fatalf("exhausted account remained eligible for new ownership: %v", err)
	}
	compactBody := []byte(`{"model":"gpt-6-sol","instructions":"summarize","input":"continue","previous_response_id":"resp-owner"}`)
	result, err = service.Compact(ctx, "key-ops", "", "", compactBody)
	if err != nil || result.Status != 200 || provider.calls != 2 {
		t.Fatalf("established 0%% owner was denied: %+v %v calls=%d", result, err, provider.calls)
	}

	account.Status = domain.AccountActive
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	group.Limits[0].MaxValue = 12
	if err := store.SaveGroup(ctx, group, now); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Compact(ctx, "key-ops", "", "", compactBody); !errors.Is(err, domain.ErrLimitReached) || provider.calls != 2 {
		t.Fatalf("key limit bypass: err=%v calls=%d", err, provider.calls)
	}
}

func TestCodexOperationsSQLiteCancellationRetainsUnknownReservation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "operations.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	account := domain.Account{ID: "acct-cancel", Kind: domain.AccountChatGPT, Provider: "openai", Email: "cancel@example.test", PlanType: "plus", RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: now}
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	token, _ := vault.Encrypt([]byte("token"))
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: account.ID, AccessTokenEncrypted: token, RefreshTokenEncrypted: token, IDTokenEncrypted: token}); err != nil {
		t.Fatal(err)
	}
	key := domain.APIKey{ID: "key-cancel", Name: "cancel", KeyHash: fmt.Sprintf("%x", make([]byte, 32)), KeyPrefix: "sk-synthetic", IsActive: true, CreatedAt: now, Limits: []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 10000, ResetAt: now.Add(time.Hour)}}}
	if err := store.SaveAPIKey(ctx, key, now); err != nil {
		t.Fatal(err)
	}
	provider := &sqliteOperationProvider{block: make(chan struct{}), started: make(chan struct{})}
	service := application.NewCodexOperations(store, store, provider, time.Hour)
	service.ConfigureAdmission(sqliteAdmission{})
	service.ConfigureAccountSelection(application.NewProxy(store, nil, nil, application.ProxyConfig{}))
	service.ConfigurePrice(func(context.Context, domain.Account, string) (pricing.Price, error) {
		return pricing.Price{}, nil
	})
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := service.Compact(cancelCtx, "key-cancel", "", "", []byte(`{"model":"gpt-6-sol","instructions":"","input":"x"}`))
		finished <- err
	}()
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("compact never reached the provider")
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("compact cancellation not returned: %v", err)
	}
	limits, err := store.GetAPIKey(ctx, "key-cancel")
	if err != nil || len(limits.Limits) != 1 || limits.Limits[0].CurrentValue <= 0 {
		t.Fatalf("possibly dispatched cancellation forgave unknown usage: %+v %v", limits, err)
	}
	uncertain, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
	if err != nil || len(uncertain) != 1 || !uncertain[0].NeedsReconciliation {
		t.Fatalf("cancelled reservation not retained: %+v %v", uncertain, err)
	}
	if released, err := store.ReleaseStaleReservations(ctx, time.Now().Add(24*time.Hour)); err != nil || released != 0 {
		t.Fatalf("unknown cancellation was stale-released: %d %v", released, err)
	}
}
