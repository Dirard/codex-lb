package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

func TestCapabilityThreadOnlyReconnectPreservesLineage(t *testing.T) {
	var mu sync.Mutex
	var calls []bool
	server, store := wireFixture(t, wireProvider(func(_ context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		mu.Lock()
		calls = append(calls, target.RequiredCapability)
		mu.Unlock()
		return wireComplete("resp_thread_capability", emit)
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
	first, handshake, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization": {"Bearer synthetic-key"}, "Session_id": {"session-thread-capability"}, "Thread-Id": {"thread-capability"},
	}})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer first.CloseNow()
	turnState := handshake.Header.Get("X-Codex-Turn-State")
	if turnState == "" {
		t.Fatal("missing synthesized turn state")
	}
	body := `{"type":"response.create","model":"gpt-6-sol","input":"hello","client_metadata":{"x-codex-parent-thread-id":"parent-thread-capability","x-codex-window-id":"window-thread-capability:7","X-Codex-LB-Required-Capability":"trusted_cyber"}}`
	if got := capabilityFrame(t, first, body); !strings.Contains(got, `"type":"response.completed"`) {
		t.Fatalf("first required thread turn = %s", got)
	}
	for _, alias := range []domain.CapabilityLineageAlias{
		{Kind: "session_header", Value: "session-thread-capability"},
		{Kind: "thread_header", Value: "thread-capability"},
		{Kind: "turn_state", Value: turnState},
		{Kind: "previous_response", Value: "resp_thread_capability"},
		{Kind: "codex_task", Value: "parent-thread-capability"},
		{Kind: "codex_window", Value: "window-thread-capability:7"},
		{Kind: "codex_task", Value: "window-thread-capability"},
	} {
		required, err := store.IsCapabilityRequired(ctx, domain.TrustedCyberCapability, "wire-key", []domain.CapabilityLineageAlias{alias})
		if err != nil || !required {
			t.Fatalf("lineage marker was not persisted: %v %v %v", alias, required, err)
		}
	}

	reconnect := capabilitySocket(t, server.URL, http.Header{"Authorization": {"Bearer synthetic-key"}, "Thread-Id": {"thread-capability"}})
	if got := capabilityFrame(t, reconnect, `{"type":"response.create","model":"gpt-6-sol","input":"continue"}`); !strings.Contains(got, `"type":"response.completed"`) {
		t.Fatalf("thread-only reconnect downgraded capability: %s", got)
	}
	status, payload := capabilityPost(t, server.URL, "synthetic-key", `{"model":"gpt-6-sol","input":"http"}`, http.Header{"Thread-Id": {"thread-capability"}})
	if status != 400 || !strings.Contains(payload, "required_capability_transport_unsupported") {
		t.Fatalf("thread-only HTTP lineage = %d %s", status, payload)
	}

	otherKey := "synthetic-other-thread-key"
	if err := store.SaveAPIKey(ctx, domain.APIKey{ID: "other-thread-key", Name: "other", KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte(otherKey))), KeyPrefix: "synthetic", IsActive: true, Limits: []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 100000}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, payload = capabilityPost(t, server.URL, otherKey, `{"model":"gpt-6-sol","input":"ordinary"}`, http.Header{"Thread-Id": {"thread-capability"}})
	if status != 200 {
		t.Fatalf("capability leaked across API keys: %d %s", status, payload)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 || !calls[0] || !calls[1] || calls[2] {
		t.Fatalf("dispatch requirements = %v", calls)
	}
}
