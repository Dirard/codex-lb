package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

func capabilityPost(t *testing.T, serverURL, key, body string, headers http.Header) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, serverURL+"/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(payload)
}

func capabilitySocket(t *testing.T, serverURL string, headers http.Header) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(serverURL, "http")+"/v1/responses", &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.CloseNow() })
	return connection
}

func TestCapabilityWebSocketSynthesizedTurnStateIsDurable(t *testing.T) {
	server, store := wireFixture(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return wireComplete("resp_synthesized", emit)
	}))
	ctx := context.Background()
	account, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	account.SecurityWorkAuthorized = true
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	connection, response, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-key"}, domain.RequiredCapabilityHeader: {domain.TrustedCyberCapability}}})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	turnState := response.Header.Get("X-Codex-Turn-State")
	if !strings.HasPrefix(turnState, "turn_") {
		t.Fatalf("missing synthesized turn state: %q", turnState)
	}
	if got := capabilityFrame(t, connection, `{"type":"response.create","model":"gpt-6-sol","input":"hello"}`); !strings.Contains(got, "response.completed") {
		t.Fatalf("required turn failed: %s", got)
	}
	required, err := store.IsCapabilityRequired(ctx, domain.TrustedCyberCapability, "wire-key", []domain.CapabilityLineageAlias{{Kind: "turn_state", Value: turnState}})
	if err != nil || !required {
		t.Fatalf("synthesized turn state was not marked: %v %v", required, err)
	}
	reconnect := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}, "X-Codex-Turn-State": {turnState}})
	if got := capabilityFrame(t, reconnect, `{"type":"response.create","model":"gpt-6-sol","input":"continue"}`); !strings.Contains(got, "response.completed") {
		t.Fatalf("synthesized turn state reconnect failed: %s", got)
	}
	status, payload := capabilityPost(t, server.URL, "synthetic-key", `{"model":"gpt-6-sol","input":"http"}`, http.Header{"X-Codex-Turn-State": {turnState}})
	if status != 400 || !strings.Contains(payload, "required_capability_transport_unsupported") {
		t.Fatalf("synthesized turn state did not restore requirement: %d %s", status, payload)
	}
}

func capabilityFrame(t *testing.T, connection *websocket.Conn, body string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := connection.Write(ctx, websocket.MessageText, []byte(body)); err != nil {
		t.Fatal(err)
	}
	_, payload, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func TestCapabilityIngressLineageAndScope(t *testing.T) {
	var mu sync.Mutex
	var calls []bool
	var bodies []string
	provider := wireProvider(func(_ context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		mu.Lock()
		calls = append(calls, target.RequiredCapability)
		bodies = append(bodies, string(body))
		mu.Unlock()
		if emit != nil {
			if err := emit(application.ResponseEvent{Type: "response.created", Data: json.RawMessage(`{"type":"response.created","response":{"id":"resp_capability"}}`)}); err != nil {
				return application.ResponseResult{}, err
			}
		}
		return wireComplete("resp_capability", emit)
	})
	server, store := wireFixture(t, provider)
	ctx := context.Background()
	account, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	account.SecurityWorkAuthorized = true
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	first, handshake, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-key"}, "Session_id": {"session-capability"}}})
	dialCancel()
	if err != nil {
		t.Fatal(err)
	}
	defer first.CloseNow()
	turnState := handshake.Header.Get("X-Codex-Turn-State")
	created := capabilityFrame(t, first, `{"type":"response.create","model":"gpt-6-sol","input":"hello","client_metadata":{"Keep":"yes","x-codex-parent-thread-id":"parent-capability","x-codex-window-id":"window-capability:12","X-Codex-LB-Required-Capability":"trusted_cyber"}}`)
	if !strings.Contains(created, `"type":"response.created"`) {
		t.Fatalf("first frame = %s", created)
	}
	required, err := store.IsCapabilityRequired(ctx, domain.TrustedCyberCapability, "wire-key", []domain.CapabilityLineageAlias{{Kind: "previous_response", Value: "resp_capability"}})
	if err != nil || !required {
		t.Fatalf("response ID was exposed before durable marker: %v %v", required, err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, completed, err := first.Read(readCtx)
	cancel()
	if err != nil || !strings.Contains(string(completed), `"type":"response.completed"`) {
		t.Fatalf("terminal frame = %s, %v", completed, err)
	}
	noEcho := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}, "Session_id": {"session-capability"}})
	if got := capabilityFrame(t, noEcho, `{"type":"response.create","model":"gpt-6-sol","input":"continue"}`); !strings.Contains(got, `"type":"response.created"`) {
		t.Fatalf("no-echo session reconnect = %s", got)
	}
	second := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}})
	if got := capabilityFrame(t, second, `{"type":"response.create","model":"gpt-6-sol","previous_response_id":"resp_capability","input":"continue"}`); !strings.Contains(got, `"type":"response.created"`) {
		t.Fatalf("response-only reconnect = %s", got)
	}
	for _, test := range []struct {
		body    string
		headers http.Header
	}{
		{`{"model":"gpt-6-sol","input":"http"}`, http.Header{"Session_id": {"session-capability"}}},
		{`{"model":"gpt-6-sol","input":"http"}`, http.Header{"X-Codex-Turn-State": {turnState}}},
		{`{"model":"gpt-6-sol","previous_response_id":"resp_capability","input":"http"}`, nil},
		{`{"model":"gpt-6-sol","input":"http"}`, http.Header{"X-Codex-Parent-Thread-Id": {"parent-capability"}}},
		{`{"model":"gpt-6-sol","input":"http"}`, http.Header{"X-Codex-Window-Id": {"window-capability:13"}}},
	} {
		status, payload := capabilityPost(t, server.URL, "synthetic-key", test.body, test.headers)
		if status != 400 || !strings.Contains(payload, "required_capability_transport_unsupported") {
			t.Fatalf("inherited HTTP route = %d %s", status, payload)
		}
	}
	otherKey := "synthetic-other-key"
	if err := store.SaveAPIKey(ctx, domain.APIKey{ID: "other-key", Name: "other", KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte(otherKey))), KeyPrefix: "synthetic", IsActive: true, Limits: []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 100000}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, payload := capabilityPost(t, server.URL, otherKey, `{"model":"gpt-6-sol","input":"ordinary"}`, http.Header{"Session_id": {"session-capability"}})
	if status != 200 {
		t.Fatalf("other key inherited capability: %d %s", status, payload)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 4 || !calls[0] || !calls[1] || !calls[2] || calls[3] {
		t.Fatalf("required/ordinary dispatches = %v", calls)
	}
	if strings.Contains(strings.ToLower(bodies[0]), strings.ToLower(domain.RequiredCapabilityHeader)) || !strings.Contains(bodies[0], `"Keep":"yes"`) {
		t.Fatalf("private marker reached provider or unrelated metadata was lost: %s", bodies[0])
	}
}

func TestCapabilityIngressRejectsUntrustedAndDuplicateSignals(t *testing.T) {
	var calls int
	server, _ := wireFixture(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		calls++
		return wireComplete("resp_unexpected", emit)
	}))
	for _, test := range []struct {
		body, code string
		headers    http.Header
	}{
		{`{"model":"gpt-6-sol","input":"http"}`, "required_capability_transport_unsupported", http.Header{domain.RequiredCapabilityHeader: {domain.TrustedCyberCapability}}},
		{`{"model":"gpt-6-sol","client_metadata":{"X-Codex-LB-Required-Capability":"trusted_cyber"}}`, "required_capability_transport_unsupported", nil},
		{`{"model":"gpt-6-sol","client_metadata":{"X-Codex-LB-Required-Capability":"trusted_cyber"},"client_metadata":{}}`, "unsupported_required_capability", nil},
		{`{"model":"gpt-6-sol","input":"http"}`, "unsupported_required_capability", http.Header{domain.RequiredCapabilityHeader: {domain.TrustedCyberCapability, domain.TrustedCyberCapability}}},
	} {
		status, payload := capabilityPost(t, server.URL, "synthetic-key", test.body, test.headers)
		if status != 400 || !strings.Contains(payload, test.code) {
			t.Fatalf("HTTP signal = %d %s, want %s", status, payload, test.code)
		}
	}
	connection := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}, domain.RequiredCapabilityHeader: {domain.TrustedCyberCapability}})
	if got := capabilityFrame(t, connection, `{"type":"response.create","model":"gpt-6-sol","input":"hello"}`); !strings.Contains(got, "no_security_work_authorized_accounts") {
		t.Fatalf("empty capable pool = %s", got)
	}
	if got := capabilityFrame(t, connection, `{"type":"response.create","model":"gpt-6-sol","client_metadata":{"X-Codex-LB-Required-Capability":"trusted_cyber"},"client_metadata":{}}`); !strings.Contains(got, "unsupported_required_capability") {
		t.Fatalf("duplicate metadata was normalized: %s", got)
	}
	if got := capabilityFrame(t, connection, `{"type":"response.cancel","client_metadata":{"X-Codex-LB-Required-Capability":"trusted_cyber"}}`); !strings.Contains(got, "unsupported_required_capability") {
		t.Fatalf("marker on cancel was accepted: %s", got)
	}
	if got := capabilityFrame(t, connection, `{"type":"response.cancel","client_metadata":{"X-Codex-LB-Required-Capability":"trusted_cyber"},"client_metadata":{}}`); !strings.Contains(got, "unsupported_required_capability") {
		t.Fatalf("duplicate marker on cancel was accepted: %s", got)
	}
	if calls != 0 {
		t.Fatalf("rejected request reached provider %d times", calls)
	}
}

func TestCapabilityWebSocketPendingOrdinaryTurnCannotChangeRequirement(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var calls atomic.Int64
	var requiredCalls atomic.Int64
	server, store := wireFixture(t, wireProvider(func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		id := calls.Add(1)
		if target.RequiredCapability {
			requiredCalls.Add(1)
		}
		if id == 1 {
			close(started)
			<-release
		}
		return wireComplete(fmt.Sprintf("resp_pending_%d", id), emit)
	}))
	ctx := context.Background()
	account, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	account.SecurityWorkAuthorized = true
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	connection := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}, "Session_id": {"pending-session"}})
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := connection.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-6-sol","input":"ordinary"}`)); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("ordinary turn did not start")
	}
	if got := capabilityFrame(t, connection, `{"type":"response.create","model":"gpt-6-sol","input":"required","client_metadata":{"X-Codex-LB-Required-Capability":"trusted_cyber"}}`); !strings.Contains(got, "capability_routing_unavailable") {
		t.Fatalf("pending ordinary turn was changed: %s", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("required frame dispatched during ordinary turn: %d", calls.Load())
	}
	close(release)
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, terminal, err := connection.Read(readCtx)
	cancel()
	if err != nil || !strings.Contains(string(terminal), "response.completed") {
		t.Fatalf("pending ordinary turn did not finish: %s %v", terminal, err)
	}
	if got := capabilityFrame(t, connection, `{"type":"response.create","model":"gpt-6-sol","input":"required","client_metadata":{"X-Codex-LB-Required-Capability":"trusted_cyber"}}`); !strings.Contains(got, "response.completed") {
		t.Fatalf("idle capable reselection failed: %s", got)
	}
	if calls.Load() != 2 || requiredCalls.Load() != 1 {
		t.Fatalf("socket selection class was reused: attempts=%d required=%d", calls.Load(), requiredCalls.Load())
	}
}

func TestCapabilityWebSocketGrantRevocationFailsBeforeReuse(t *testing.T) {
	var calls atomic.Int64
	server, store := wireFixture(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return wireComplete(fmt.Sprintf("resp_revoked_%d", calls.Add(1)), emit)
	}))
	ctx := context.Background()
	account, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	account.SecurityWorkAuthorized = true
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	connection := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}, "Session_id": {"revocation-session"}, domain.RequiredCapabilityHeader: {domain.TrustedCyberCapability}})
	if got := capabilityFrame(t, connection, `{"type":"response.create","model":"gpt-6-sol","input":"hello"}`); !strings.Contains(got, "response.completed") {
		t.Fatalf("first required turn = %s", got)
	}
	account.SecurityWorkAuthorized = false
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if got := capabilityFrame(t, connection, `{"type":"response.create","model":"gpt-6-sol","input":"again"}`); !strings.Contains(got, "no_security_work_authorized_accounts") {
		t.Fatalf("revoked grant reused required session: %s", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("revoked grant reached provider: %d", calls.Load())
	}
}

func TestCapabilityWebSocketRejectedFramesNeverReachUpstream(t *testing.T) {
	var calls atomic.Int64
	server, _ := wireFixture(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return wireComplete(fmt.Sprintf("resp_rejected_%d", calls.Add(1)), emit)
	}))
	connection := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}})
	if got := capabilityFrame(t, connection, `{"type":"response.create","model":"gpt-6-sol","input":"hello"}`); !strings.Contains(got, "response.completed") {
		t.Fatalf("initial ordinary turn = %s", got)
	}
	for _, body := range []string{
		`{"type":"response.create","model":"gpt-6-sol","client_metadata":{"X-Codex-LB-Required-Capability":"trusted_cyber","x-codex-lb-required-capability":"trusted_cyber"}}`,
		`{"type":"response.create","model":"gpt-6-sol","client_metadata":{"X-Codex-LB-Required-Capability":"trusted_cyber"},"client_metadata":{}}`,
		`{"type":"response.create","model":"gpt-6-sol","X-Codex-LB-Required-Capability":"trusted_cyber"}`,
	} {
		if got := capabilityFrame(t, connection, body); !strings.Contains(got, "unsupported_required_capability") {
			t.Fatalf("spoofed/duplicate signal = %s", got)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("rejected frame reached already-open upstream: %d", calls.Load())
	}
}

func TestCapabilityWebSocketBinaryFrameIsRejected(t *testing.T) {
	var calls atomic.Int64
	server, _ := wireFixture(t, wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		calls.Add(1)
		return wireComplete("resp_binary", emit)
	}))
	connection := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := connection.Write(ctx, websocket.MessageBinary, []byte(`{"type":"response.create","model":"gpt-6-sol"}`)); err != nil {
		t.Fatal(err)
	}
	_, payload, err := connection.Read(ctx)
	if err != nil || !strings.Contains(string(payload), `"code":"invalid_request"`) || calls.Load() != 0 {
		t.Fatalf("binary frame reached provider or lacked typed error: %s %v calls=%d", payload, err, calls.Load())
	}
}
