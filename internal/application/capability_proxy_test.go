package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type failingCapabilityStore struct {
	*sqlite.Store
	readFailure, responseWriteFailure bool
}

func (s *failingCapabilityStore) IsCapabilityRequired(ctx context.Context, capability, keyScope string, aliases []domain.CapabilityLineageAlias) (bool, error) {
	if s.readFailure {
		return false, errors.New("synthetic lineage read failure")
	}
	return s.Store.IsCapabilityRequired(ctx, capability, keyScope, aliases)
}

func (s *failingCapabilityStore) RequireCapability(ctx context.Context, capability, keyScope string, aliases []domain.CapabilityLineageAlias) ([]string, error) {
	if s.responseWriteFailure {
		for _, alias := range aliases {
			if alias.Kind == "previous_response" {
				return nil, errors.New("synthetic lineage write failure")
			}
		}
	}
	return s.Store.RequireCapability(ctx, capability, keyScope, aliases)
}

func proxyCode(err error) string {
	var proxy *application.ProxyError
	if errors.As(err, &proxy) {
		return proxy.Code
	}
	return ""
}

func TestRequiredCapabilityRejectsOrdinaryEstablishedOwner(t *testing.T) {
	proxy, store, stub := proxyFixture(t)
	ctx := context.Background()
	options := application.ResponseOptions{KeyID: "key-test", Transport: application.CapabilityTransportWebSocket, SessionID: "owner-session"}
	if _, err := proxy.Respond(ctx, options, json.RawMessage(`{"model":"gpt-6-sol","input":"hello","stream":true}`), func(application.ResponseEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(stub.accounts) != 1 || stub.accounts[0] != "account-a" {
		t.Fatalf("ordinary owner = %v", stub.accounts)
	}
	other, err := store.GetAccount(ctx, "account-b")
	if err != nil {
		t.Fatal(err)
	}
	other.SecurityWorkAuthorized = true
	if err := store.SaveAccount(ctx, other); err != nil {
		t.Fatal(err)
	}
	options.CapabilityHeaderValues = []string{domain.TrustedCyberCapability}
	_, route, err := proxy.PrepareCapability(ctx, options, json.RawMessage(`{"type":"response.create","model":"gpt-6-sol","input":"continue"}`))
	if err != nil {
		t.Fatal(err)
	}
	options.CapabilityRoute = &route
	_, err = proxy.Respond(ctx, options, json.RawMessage(`{"model":"gpt-6-sol","input":"continue","stream":true}`), func(application.ResponseEvent) error { return nil })
	if proxyCode(err) != "no_security_work_authorized_accounts" || len(stub.accounts) != 1 {
		t.Fatalf("owner moved without upstream quota refusal: %v, attempts=%v", err, stub.accounts)
	}
}

func TestCapabilityDatabaseUncertaintyFailsBeforeDispatch(t *testing.T) {
	proxy, store, stub := proxyFixture(t)
	proxy.Capabilities = application.NewCapabilityRouter(&failingCapabilityStore{Store: store, readFailure: true})
	_, err := proxy.Respond(context.Background(), application.ResponseOptions{KeyID: "key-test", Transport: application.CapabilityTransportWebSocket, SessionID: "uncertain-session"}, json.RawMessage(`{"model":"gpt-6-sol","input":"hello","stream":true}`), func(application.ResponseEvent) error { return nil })
	if proxyCode(err) != "capability_lineage_unavailable" || len(stub.accounts) != 0 {
		t.Fatalf("lineage read failure reached provider: %v, attempts=%v", err, stub.accounts)
	}
}

func TestRequiredCapabilityTransfersOnlyAfterUpstreamQuotaProof(t *testing.T) {
	for _, quotaRefused := range []bool{false, true} {
		t.Run(map[bool]string{false: "generic_rate_limit", true: "verified_quota"}[quotaRefused], func(t *testing.T) {
			proxy, store, stub := proxyFixture(t)
			ctx := context.Background()
			for _, id := range []string{"account-a", "account-b"} {
				account, err := store.GetAccount(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				account.SecurityWorkAuthorized = true
				if err := store.SaveAccount(ctx, account); err != nil {
					t.Fatal(err)
				}
			}
			options := application.ResponseOptions{KeyID: "key-test", Transport: application.CapabilityTransportWebSocket, SessionID: "quota-session", CapabilityHeaderValues: []string{domain.TrustedCyberCapability}}
			_, route, err := proxy.PrepareCapability(ctx, options, json.RawMessage(`{"type":"response.create","model":"gpt-6-sol","input":"hello"}`))
			if err != nil {
				t.Fatal(err)
			}
			options.CapabilityRoute = &route
			var required []bool
			stub.respond = func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				required = append(required, target.RequiredCapability)
				if target.Account.ID == "account-a" && len(required) == 2 {
					if quotaRefused {
						return application.ResponseResult{}, &application.ProviderFailure{Code: "usage_limit_reached", Status: 429, QuotaRefused: true, Dispatched: true}
					}
					return application.ResponseResult{}, &application.ProviderFailure{Code: "rate_limit_exceeded", Status: 429, Dispatched: true}
				}
				return complete("resp_"+target.Account.ID, emit)
			}
			first, err := proxy.Respond(ctx, options, json.RawMessage(`{"model":"gpt-6-sol","input":"hello","stream":true}`), func(application.ResponseEvent) error { return nil })
			if err != nil || first.ResponseID != "resp_account-a" {
				t.Fatalf("first required turn = %+v %v", first, err)
			}
			options.CapabilityRoute = nil
			options.CapabilityHeaderValues = nil
			followup := json.RawMessage(`{"model":"gpt-6-sol","previous_response_id":"resp_account-a","input":"continue","stream":true}`)
			_, err = proxy.Respond(ctx, options, followup, func(application.ResponseEvent) error { return nil })
			if quotaRefused {
				if err != nil || len(stub.accounts) != 3 || stub.accounts[2] != "account-b" || strings.Contains(string(stub.bodies[2]), "previous_response_id") {
					t.Fatalf("verified quota did not safely transfer: %v, accounts=%v, body=%s", err, stub.accounts, stub.bodies[len(stub.bodies)-1])
				}
			} else if err == nil || len(stub.accounts) != 2 {
				t.Fatalf("generic 429 transferred owner: %v, accounts=%v", err, stub.accounts)
			}
			for _, value := range required {
				if !value {
					t.Fatalf("required constraint was lost across quota flow: %v", required)
				}
			}
		})
	}
}

func TestResponseCreatedMarkerFailureDoesNotExposeIDOrPenalizeAccount(t *testing.T) {
	proxy, store, stub := proxyFixture(t)
	proxy.Capabilities = application.NewCapabilityRouter(&failingCapabilityStore{Store: store, responseWriteFailure: true})
	options := application.ResponseOptions{KeyID: "key-test", Transport: application.CapabilityTransportWebSocket, SessionID: "created-session"}
	signal, route, err := proxy.PrepareCapability(context.Background(), options, json.RawMessage(`{"type":"response.create","model":"gpt-6-sol","input":"hello","client_metadata":{"Keep":"yes","X-Codex-LB-Required-Capability":"trusted_cyber"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var normalized map[string]json.RawMessage
	if err := json.Unmarshal(signal.Payload, &normalized); err != nil {
		t.Fatal(err)
	}
	delete(normalized, "type")
	normalized["stream"] = json.RawMessage("true")
	body, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	options.CapabilityRoute = &route
	var diagnostic application.ErrorDiagnostic
	proxy.Diagnostics = func(_ context.Context, event application.ErrorDiagnostic) { diagnostic = event }
	account, err := store.GetAccount(context.Background(), "account-a")
	if err != nil {
		t.Fatal(err)
	}
	account.SecurityWorkAuthorized = true
	if err := store.SaveAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	var emitted []application.ResponseEvent
	_, err = proxy.Respond(context.Background(), options, body, func(event application.ResponseEvent) error {
		emitted = append(emitted, event)
		return nil
	})
	if proxyCode(err) != "capability_lineage_unavailable" || len(emitted) != 0 || len(stub.accounts) != 1 {
		t.Fatalf("unpersisted created ID escaped or replayed: %v, events=%v, attempts=%v", err, emitted, stub.accounts)
	}
	if strings.Contains(strings.ToLower(string(diagnostic.Request)), strings.ToLower(domain.RequiredCapabilityHeader)) || !strings.Contains(string(diagnostic.Request), `"Keep":"yes"`) {
		t.Fatalf("private marker reached error diagnostics or unrelated metadata was lost: %s", diagnostic.Request)
	}
	account, err = store.GetAccount(context.Background(), "account-a")
	if err != nil || account.Status != domain.AccountActive {
		t.Fatalf("local marker failure penalized account: %+v %v", account, err)
	}
}
