package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

func TestQuotaFailoverDoesNotCrossUnrelatedCompatibleProviders(t *testing.T) {
	for _, sameProvider := range []bool{false, true} {
		t.Run(fmt.Sprint(sameProvider), func(t *testing.T) {
			proxy, store, stub := proxyFixture(t)
			ctx := context.Background()
			credential, err := store.GetAccountCredential(ctx, "account-a")
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"external-a", "external-b"} {
				endpoint := "https://provider-a.invalid/v1"
				if id == "external-b" && !sameProvider {
					endpoint = "https://provider-b.invalid/v1"
				}
				if err := store.SaveAccount(ctx, domain.Account{ID: id, Kind: domain.AccountExternal, Provider: "openai_compatible", BaseURL: endpoint, Email: id + "@example.invalid", PlanType: "external", Status: domain.AccountActive, CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
				if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: id, ExternalKeyEncrypted: credential.AccessTokenEncrypted}); err != nil {
					t.Fatal(err)
				}
			}
			proxy.ResolvePrice = func(_ context.Context, a domain.Account, model string) (pricing.Price, error) {
				if a.Kind != domain.AccountExternal {
					return pricing.Price{}, pricing.ErrUnpriced
				}
				_, price, err := pricing.LookupCodex(model)
				return price, err
			}
			first, err := proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, []byte(`{"model":"gpt-6-sol","input":"context"}`), nil)
			if err != nil {
				t.Fatal(err)
			}
			stub.respond = func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				if target.Account.ID == "external-a" {
					return application.ResponseResult{}, &application.ProviderFailure{Code: "insufficient_quota", Status: 429, QuotaRefused: true, Dispatched: true}
				}
				return complete("switched", emit)
			}
			_, err = proxy.Respond(ctx, application.ResponseOptions{KeyID: "key-test"}, []byte(fmt.Sprintf(`{"model":"gpt-6-sol","previous_response_id":%q,"input":"continue"}`, first.ResponseID)), nil)
			if sameProvider && (err != nil || len(stub.accounts) != 3) {
				t.Fatalf("same provider failed to recover: %v", err)
			}
			if !sameProvider && (err == nil || len(stub.accounts) != 2) {
				t.Fatal("quota alone switched unrelated upstream provider")
			}
		})
	}
}
