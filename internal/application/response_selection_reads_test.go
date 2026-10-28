package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

type selectionReadCounter struct {
	*sqlite.Store
	mu    sync.Mutex
	reads map[string]int
}

func (s *selectionReadCounter) count(name string) {
	s.mu.Lock()
	s.reads[name]++
	s.mu.Unlock()
}
func (s *selectionReadCounter) ListAccountQuota(ctx context.Context, id string) ([]domain.AccountQuota, error) {
	s.count("ListAccountQuota")
	return s.Store.ListAccountQuota(ctx, id)
}
func (s *selectionReadCounter) LoadAccountCreditStatus(ctx context.Context, id string) (*domain.AccountCreditStatus, error) {
	s.count("LoadAccountCreditStatus")
	return s.Store.LoadAccountCreditStatus(ctx, id)
}
func (s *selectionReadCounter) LoadAccountQuotaRefusalAt(ctx context.Context, id string) (*time.Time, error) {
	s.count("LoadAccountQuotaRefusalAt")
	return s.Store.LoadAccountQuotaRefusalAt(ctx, id)
}
func (s *selectionReadCounter) ListAccountAdditionalQuotas(ctx context.Context, id string) ([]domain.AccountAdditionalQuota, error) {
	s.count("ListAccountAdditionalQuotas")
	return s.Store.ListAccountAdditionalQuotas(ctx, id)
}
func (s *selectionReadCounter) AccountSelectionSnapshot(ctx context.Context, ids []string) (map[string]domain.AccountSelectionState, error) {
	s.count("AccountSelectionSnapshot")
	return s.Store.AccountSelectionSnapshot(ctx, ids)
}

// One routing choice reads candidate telemetry once and prices a subscription
// model once, independent of the number of candidate accounts.
func TestAccountSelectionReadsOnceAndPricesOnce(t *testing.T) {
	_, store, stub, vault := proxyFixtureParts(t)
	ctx := context.Background()
	credential, err := store.GetAccountCredential(ctx, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("account-extra-%d", i)
		if err := store.SaveAccount(ctx, domain.Account{ID: id, Kind: domain.AccountChatGPT, Provider: "openai", Email: id + "@example.invalid",
			PlanType: "plus", Status: domain.AccountActive, RoutingPolicy: "normal", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		credential.AccountID = id
		if err := store.SaveAccountCredential(ctx, credential); err != nil {
			t.Fatal(err)
		}
	}
	counter := &selectionReadCounter{Store: store, reads: map[string]int{}}
	proxy := application.NewProxy(counter, stub, vault, application.ProxyConfig{MaxStreams: 1, MaxQueued: 1})
	prices := 0
	resolve := proxy.ResolvePrice
	proxy.ResolvePrice = func(ctx context.Context, account domain.Account, model string) (pricing.Price, error) {
		prices++
		return resolve(ctx, account, model)
	}
	if _, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, json.RawMessage(`{"model":"gpt-6-sol","input":"hello"}`), nil); err != nil {
		t.Fatal(err)
	}
	if prices != 1 || counter.reads["AccountSelectionSnapshot"] != 1 || len(counter.reads) != 1 {
		t.Fatalf("selection of 10 accounts: %d price resolutions, reads %v", prices, counter.reads)
	}
}
