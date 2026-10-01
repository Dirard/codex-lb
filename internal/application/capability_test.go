package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"codex-lb/internal/domain"
)

func TestParseCapabilitySignalValidMetadataAndAliases(t *testing.T) {
	body := json.RawMessage(`{"type":"response.create","previous_response_id":"resp-old","client_metadata":{"Keep":"yes","x-codex-parent-thread-id":"task-parent","x-codex-window-id":"task-window:12","X-Codex-LB-Required-Capability":"trusted_cyber"}}`)
	signal, err := ParseCapabilitySignal(CapabilitySignalRequest{
		Transport: CapabilityTransportWebSocket, APIKeyID: "key-real", Body: body,
		SessionID: " session ", ThreadID: " thread ", TurnState: "turn", PreviousResponseIDs: []string{"resp-old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !signal.Intent.RequiresTrustedCyber {
		t.Fatal("trusted intent was not established")
	}
	kinds := map[string]bool{}
	for _, alias := range signal.Aliases {
		kinds[alias.Kind+":"+alias.Value] = true
	}
	for _, want := range []string{"session_header:session", "thread_header:thread", "turn_state:turn", "previous_response:resp-old", "codex_task:task-parent", "codex_window:task-window:12", "codex_task:task-window"} {
		if !kinds[want] {
			t.Fatalf("missing lineage alias %s in %#v", want, signal.Aliases)
		}
	}
	if strings.Contains(strings.ToLower(string(signal.Payload)), strings.ToLower(domain.RequiredCapabilityHeader)) || !strings.Contains(string(signal.Payload), `"Keep":"yes"`) {
		t.Fatalf("capability marker was not stripped while unrelated metadata changed: %s", signal.Payload)
	}
}

func TestParseCapabilitySignalHeader(t *testing.T) {
	signal, err := ParseCapabilitySignal(CapabilitySignalRequest{
		Transport: CapabilityTransportWebSocket, FrameType: CapabilityResponseCreate, APIKeyID: "key-real",
		HeaderValues: []string{domain.TrustedCyberCapability}, SessionID: "session", ThreadID: "thread",
	})
	if err != nil || !signal.Intent.RequiresTrustedCyber {
		t.Fatalf("header signal failed: %+v %v", signal, err)
	}
	if len(signal.Aliases) != 2 || signal.Aliases[0].Value != "session" || signal.Aliases[1].Value != "thread" {
		t.Fatalf("header aliases = %#v", signal.Aliases)
	}
}

func TestCapabilityAndParsedPayloadDoNotCopyOrMutateCallerBytes(t *testing.T) {
	body := json.RawMessage(`{"model":"gpt-6-sol","stream":true,"unknown":true,"input":[{"role":"user","content":"Привет 🌊","unknown":{"nested":1}}],"client_metadata":{"keep":"yes"}}`)
	before := string(body)
	signal, err := ParseCapabilitySignal(CapabilitySignalRequest{APIKeyID: "key-real", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if &signal.Payload[0] != &body[0] {
		t.Fatal("ordinary capability payload copied caller bytes")
	}
	request, err := parseResponse(signal.Payload, domain.APIKey{}, domain.RuntimeSettings{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !request.WireClean || &request.Wire[0] != &body[0] {
		t.Fatal("clean response wire copied caller bytes")
	}
	if request.Input != nil || !request.DeferredInput {
		t.Fatal("large response input was materialized before dispatch")
	}
	if err := request.loadInput(); err != nil {
		t.Fatal(err)
	}
	if string(body) != before || string(request.Object["unknown"]) != `true` ||
		!strings.Contains(string(request.Input[0]), "Привет 🌊") || !strings.Contains(string(request.Input[0]), `"nested":1`) {
		t.Fatal("parsing changed caller bytes or dropped unknown Unicode input")
	}
}

func TestParseCapabilitySignalRejectsAmbiguousOrUntrustedCarriers(t *testing.T) {
	valid := `{"type":"response.create","client_metadata":{"X-Codex-LB-Required-Capability":"trusted_cyber"}}`
	tests := []struct {
		name string
		req  CapabilitySignalRequest
		code string
	}{
		{"duplicate carriers", CapabilitySignalRequest{Transport: CapabilityTransportWebSocket, APIKeyID: "key", HeaderValues: []string{domain.TrustedCyberCapability}, Body: json.RawMessage(valid)}, "unsupported_required_capability"},
		{"duplicate headers", CapabilitySignalRequest{Transport: CapabilityTransportWebSocket, APIKeyID: "key", HeaderValues: []string{domain.TrustedCyberCapability, domain.TrustedCyberCapability}}, "unsupported_required_capability"},
		{"duplicate metadata keys", CapabilitySignalRequest{Transport: CapabilityTransportWebSocket, APIKeyID: "key", Body: json.RawMessage(`{"type":"response.create","client_metadata":{"X-Codex-LB-Required-Capability":"trusted_cyber","x-codex-lb-required-capability":"trusted_cyber"}}`)}, "unsupported_required_capability"},
		{"duplicate metadata containers", CapabilitySignalRequest{Transport: CapabilityTransportWebSocket, APIKeyID: "key", Body: json.RawMessage(`{"type":"response.create","client_metadata":{"X-Codex-LB-Required-Capability":"trusted_cyber"},"client_metadata":{}}`)}, "unsupported_required_capability"},
		{"invalid metadata container hides marker", CapabilitySignalRequest{Transport: CapabilityTransportWebSocket, APIKeyID: "key", Body: json.RawMessage(`{"type":"response.create","client_metadata":"X-Codex-LB-Required-Capability"}`)}, "unsupported_required_capability"},
		{"non-object metadata container", CapabilitySignalRequest{Transport: CapabilityTransportWebSocket, APIKeyID: "key", Body: json.RawMessage(`{"type":"response.create","client_metadata":null}`)}, "unsupported_required_capability"},
		{"misplaced top-level marker", CapabilitySignalRequest{Transport: CapabilityTransportWebSocket, APIKeyID: "key", Body: json.RawMessage(`{"type":"response.create","X-Codex-LB-Required-Capability":"trusted_cyber"}`)}, "unsupported_required_capability"},
		{"local key", CapabilitySignalRequest{Transport: CapabilityTransportWebSocket, APIKeyID: domain.LocalProxyKeyID, HeaderValues: []string{domain.TrustedCyberCapability}}, "capability_signal_untrusted"},
		{"http transport", CapabilitySignalRequest{Transport: CapabilityTransportHTTP, APIKeyID: "key", HeaderValues: []string{domain.TrustedCyberCapability}}, "required_capability_transport_unsupported"},
		{"wrong frame", CapabilitySignalRequest{Transport: CapabilityTransportWebSocket, APIKeyID: "key", FrameType: "response.cancel", HeaderValues: []string{domain.TrustedCyberCapability}}, "unsupported_required_capability"},
		{"unknown capability", CapabilitySignalRequest{Transport: CapabilityTransportWebSocket, APIKeyID: "key", HeaderValues: []string{"cyber"}}, "unsupported_required_capability"},
		{"malformed json", CapabilitySignalRequest{Transport: CapabilityTransportWebSocket, APIKeyID: "key", Body: json.RawMessage("{\"no\""), HeaderValues: []string{domain.TrustedCyberCapability}}, "invalid_request"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseCapabilitySignal(test.req)
			var proxy *ProxyError
			if !errors.As(err, &proxy) || proxy.Code != test.code {
				t.Fatalf("error = %v, want %s", err, test.code)
			}
		})
	}
}

type capabilityStore struct {
	required map[string]bool
	fail     bool
	require  [][]domain.CapabilityLineageAlias
}

func (s *capabilityStore) IsCapabilityRequired(_ context.Context, capability, key string, aliases []domain.CapabilityLineageAlias) (bool, error) {
	if s.fail {
		return false, errors.New("database unavailable")
	}
	for _, alias := range aliases {
		hash, _ := domain.CapabilityLineageMarkerHash(capability, key, alias)
		if s.required[hash] {
			return true, nil
		}
	}
	return false, nil
}

func (s *capabilityStore) RequireCapability(_ context.Context, capability, key string, aliases []domain.CapabilityLineageAlias) ([]string, error) {
	if s.fail {
		return nil, errors.New("database unavailable")
	}
	s.require = append(s.require, aliases)
	hashes, _ := domain.CapabilityLineageMarkerHashes(capability, key, aliases)
	for _, hash := range hashes {
		s.required[hash] = true
	}
	return hashes, nil
}

func TestCapabilityRouterRestoresAndPersistsLineage(t *testing.T) {
	ctx := context.Background()
	store := &capabilityStore{required: map[string]bool{}}
	router := NewCapabilityRouter(store)
	aliases := []domain.CapabilityLineageAlias{{Kind: "session_header", Value: "session"}}
	route, err := router.Route(ctx, CapabilityIntent{RequiresTrustedCyber: true}, "key-a", aliases)
	if err != nil || !route.RequireSecurityWorkAuthorized || len(store.require) != 1 {
		t.Fatalf("explicit route failed: %+v %v calls=%d", route, err, len(store.require))
	}
	route, err = router.Route(ctx, CapabilityIntent{}, "key-a", aliases)
	if err != nil || !route.RequireSecurityWorkAuthorized {
		t.Fatalf("lineage was not restored: %+v %v", route, err)
	}
	route, err = router.Route(ctx, CapabilityIntent{}, "key-b", aliases)
	if err != nil || route.RequireSecurityWorkAuthorized {
		t.Fatalf("API-key scope leaked: %+v %v", route, err)
	}
	store.fail = true
	routeErr := func() error { _, err := router.Route(ctx, CapabilityIntent{}, "key-a", aliases); return err }()
	if routeErr == nil {
		t.Fatal("database uncertainty downgraded capability")
	}
	var proxy *ProxyError
	if !errors.As(routeErr, &proxy) || proxy.Code != "capability_lineage_unavailable" {
		t.Fatalf("database uncertainty returned the wrong error: %v", routeErr)
	}
}

func TestFilterCapabilityAccountsDoesNotMigrateOwner(t *testing.T) {
	ordinary := []domain.Account{{ID: "ordinary", Kind: domain.AccountChatGPT}}
	if got, err := FilterCapabilityAccounts(CapabilityRoute{}, ordinary, ""); err != nil || len(got) != 1 {
		t.Fatalf("ordinary routing changed: %#v %v", got, err)
	}
	capable := domain.Account{ID: "owner", Kind: domain.AccountChatGPT, SecurityWorkAuthorized: true}
	other := domain.Account{ID: "other", Kind: domain.AccountChatGPT, SecurityWorkAuthorized: true}
	route := CapabilityRoute{RequireSecurityWorkAuthorized: true}
	got, err := FilterCapabilityAccounts(route, []domain.Account{{ID: "ordinary", Kind: domain.AccountChatGPT}, capable, other}, "")
	if err != nil || len(got) != 2 || got[0].ID != "owner" || got[1].ID != "other" {
		t.Fatalf("capable pool = %#v %v", got, err)
	}
	got, err = FilterCapabilityAccounts(route, []domain.Account{capable, other}, "owner")
	if err != nil || len(got) != 1 || got[0].ID != "owner" {
		t.Fatalf("authorized owner was not preserved: %#v %v", got, err)
	}
	if _, err = FilterCapabilityAccounts(route, []domain.Account{capable, other}, "missing"); err == nil {
		t.Fatal("owner mismatch selected a replacement")
	}
	if _, err = FilterCapabilityAccounts(route, ordinary, ""); err == nil {
		t.Fatal("ordinary fallback was allowed")
	}
}
