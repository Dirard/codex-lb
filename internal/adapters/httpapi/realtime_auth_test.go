package httpapi_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestRealtimeRequiresBearerThroughKeylessServerIngress(t *testing.T) {
	_, store, proxy := wireFixtureWithProxy(t, nil)
	ctx := context.Background()
	if err := store.EnsureLocalProxyKey(ctx); err != nil {
		t.Fatal(err)
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.APIKeyAuthEnabled = false
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAPIKey(ctx, domain.APIKey{ID: "second-key", Name: "Other caller", KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic-other-key"))), KeyPrefix: "synthetic", IsActive: true, CreatedAt: time.Now()}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Location", "/v1/live/rtc_private")
		w.Header().Set("Content-Type", "application/sdp")
		w.WriteHeader(201)
		fmt.Fprint(w, "v=answer\r\n")
	}))
	defer upstream.Close()
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	defer adapter.Close()
	ops := application.NewCodexOperations(store, store, adapter, time.Hour)
	ops.ConfigureAdmission(proxy)
	api := httpapi.New(store, nil, httpapi.Config{UnauthenticatedClientCIDRs: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}}, nil)
	handler := api.Handler(httpapi.NewCodexOperationsHandler(store, ops), nil)
	call := func(method, path, peer, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost:2455"+path, strings.NewReader("v=offer\r\n"))
		r.RemoteAddr = peer
		r.Header.Set("Content-Type", "application/sdp")
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, peer := range []string{"127.0.0.1:1234", "192.0.2.8:1234"} {
		for _, key := range []string{"", "invalid"} {
			for _, path := range []string{"/backend-api/codex/realtime/calls", "/backend-api/codex/realtime/calls/"} {
				if w := call("POST", path, peer, key); w.Code != 401 {
					t.Fatalf("keyless create accepted: %s %s status=%d", peer, path, w.Code)
				}
			}
			for _, path := range []string{"/backend-api/codex/rtc_private", "/v1/live/rtc_private", "/v1/realtime?call_id=rtc_private", "/v1/realtime/?call_id=rtc_private"} {
				if w := call("GET", path, peer, key); w.Code != 401 {
					t.Fatalf("keyless sideband accepted: %s %s status=%d", peer, path, w.Code)
				}
			}
		}
	}
	if upstreamCalls.Load() != 0 {
		t.Fatal("keyless realtime reached upstream")
	}
	if w := call("POST", "/backend-api/codex/realtime/calls", "127.0.0.1:1234", "synthetic-key"); w.Code != 201 {
		t.Fatalf("valid key rejected: %d %s", w.Code, w.Body.String())
	}
	owner, err := store.GetCodexResourceOwner(ctx, application.CodexResourceRealtime, "rtc_private", "wire-key", time.Now())
	if err != nil || owner.KeyID != "wire-key" {
		t.Fatalf("user key was replaced by internal principal: %+v %v", owner, err)
	}
	for _, path := range []string{"/backend-api/codex/rtc_private", "/v1/live/rtc_private", "/v1/realtime?call_id=rtc_private", "/v1/realtime/?call_id=rtc_private"} {
		if w := call("GET", path, "127.0.0.1:1234", "synthetic-other-key"); w.Code != 409 {
			t.Fatalf("foreign key escaped owner scope: %d", w.Code)
		}
	}
	if upstreamCalls.Load() != 1 {
		t.Fatal("foreign key attached to another key's call")
	}
}
