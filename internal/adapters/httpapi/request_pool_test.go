package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// poolRoute serves the Responses routes in-process, so a synctest bubble
// controls the pool's clock.
type poolRoute struct {
	handler *httpapi.ProxyHandler
	store   *sqlite.Store
	calls   *atomic.Int64
}

func newPoolRoute(t *testing.T, pool *domain.RequestPool, fail bool) poolRoute {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "pool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.SaveAccount(ctx, domain.Account{ID: "pool-account", Kind: domain.AccountChatGPT, Provider: "openai", PlanType: "plus", Email: "synthetic@example.invalid", Status: domain.AccountActive, RoutingPolicy: "normal", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	secret, err := vault.Encrypt([]byte("synthetic-credential"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: "pool-account", AccessTokenEncrypted: secret, RefreshTokenEncrypted: secret, IDTokenEncrypted: secret}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAPIKey(ctx, domain.APIKey{ID: "pool-key", Name: "pooled", KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic-key"))), KeyPrefix: "synthetic", IsActive: true,
		Limits: []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 1_000_000}}, RequestPool: pool}, time.Now()); err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int64{}
	provider := wireProvider(func(_ context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		n := calls.Add(1)
		if fail {
			return application.ResponseResult{}, &application.ProviderFailure{Code: "upstream_unavailable", Status: http.StatusBadGateway}
		}
		return wireComplete(fmt.Sprintf("pool_%d", n), emit)
	})
	proxy := application.NewProxy(store, provider, vault, application.ProxyConfig{MaxStreams: 4, MaxQueued: 4, QueueTimeout: time.Second})
	handler := httpapi.NewProxyHandler(store, proxy, nil)
	handler.ConfigureRequestPools(application.NewRequestPools())
	return poolRoute{handler: handler, store: store, calls: calls}
}

func (p poolRoute) post(ctx context.Context, path string, stream bool) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"model":"gpt-6-sol","input":"hi","stream":%t}`, stream)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer synthetic-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.handler.ServeHTTP(rec, req)
	return rec
}

func (p poolRoute) postAsync(ctx context.Context, path string) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- p.post(ctx, path, false) }()
	return done
}

func (p poolRoute) setPool(t *testing.T, pool *domain.RequestPool) {
	t.Helper()
	key, err := p.store.GetAPIKey(context.Background(), "pool-key")
	if err != nil {
		t.Fatal(err)
	}
	key.RequestPool = pool
	if err := p.store.SaveAPIKey(context.Background(), key, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func poolHeaders(t *testing.T, rec *httptest.ResponseRecorder, limit, remaining, reset string) {
	t.Helper()
	got := [3]string{rec.Header().Get("X-Ratelimit-Limit-Requests"), rec.Header().Get("X-Ratelimit-Remaining-Requests"), rec.Header().Get("X-Ratelimit-Reset-Requests")}
	if got != [3]string{limit, remaining, reset} {
		t.Fatalf("pool headers %q, want %q (status %d)", got, [3]string{limit, remaining, reset}, rec.Code)
	}
}

func poolRefused(t *testing.T, rec *httptest.ResponseRecorder, retryAfter string) {
	t.Helper()
	var body struct {
		Error struct{ Type, Code, Message string }
	}
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != retryAfter ||
		json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error.Type != "requests" || body.Error.Code != "rate_limit_exceeded" ||
		!strings.Contains(body.Error.Message, "Please try again in "+retryAfter+"s.") {
		t.Fatalf("refusal %d Retry-After=%q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
}

func TestRequestPoolQueuedResponseWaitsForAFreedSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		route := newPoolRoute(t, &domain.RequestPool{RequestsPerMinute: 2, MaxWaitSeconds: 30}, false)
		ctx := context.Background()
		first := route.post(ctx, "/backend-api/codex/responses", true)
		if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "response.completed") {
			t.Fatalf("first %d %s", first.Code, first.Body)
		}
		poolHeaders(t, first, "2", "1", "1m0s")
		time.Sleep(40 * time.Second)
		second := route.post(ctx, "/v1/responses", false)
		if second.Code != http.StatusOK {
			t.Fatalf("second %d %s", second.Code, second.Body)
		}
		poolHeaders(t, second, "2", "0", "1m0s")
		third := route.postAsync(ctx, "/v1/responses")
		synctest.Wait()
		select {
		case rec := <-third:
			t.Fatalf("over the pool served at once: %d", rec.Code)
		default:
		}
		if route.calls.Load() != 2 {
			t.Fatalf("queued request reached upstream: %d calls", route.calls.Load())
		}
		time.Sleep(20 * time.Second)
		synctest.Wait()
		rec := <-third
		if rec.Code != http.StatusOK || route.calls.Load() != 3 {
			t.Fatalf("queued %d after %d calls: %s", rec.Code, route.calls.Load(), rec.Body)
		}
		poolHeaders(t, rec, "2", "0", "1m0s")
	})
}

func TestRequestPoolRefusesBeyondTheMaximumWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		route := newPoolRoute(t, &domain.RequestPool{RequestsPerMinute: 1, MaxWaitSeconds: 10}, false)
		ctx := context.Background()
		if rec := route.post(ctx, "/v1/responses", false); rec.Code != http.StatusOK {
			t.Fatalf("first %d", rec.Code)
		}
		for _, stream := range []bool{false, true} {
			rec := route.post(ctx, "/backend-api/codex/responses", stream)
			poolRefused(t, rec, "60")
			poolHeaders(t, rec, "1", "0", "1m0s")
		}
		time.Sleep(45 * time.Second)
		rec := route.post(ctx, "/v1/responses", false)
		poolRefused(t, rec, "15")
		poolHeaders(t, rec, "1", "0", "15s")
		if route.calls.Load() != 1 {
			t.Fatalf("refused requests reached upstream: %d calls", route.calls.Load())
		}
	})
}

func TestRequestPoolClientLeavingTheQueueTakesNoSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		route := newPoolRoute(t, &domain.RequestPool{RequestsPerMinute: 1, MaxWaitSeconds: 60}, false)
		start := time.Now()
		if rec := route.post(context.Background(), "/v1/responses", false); rec.Code != http.StatusOK {
			t.Fatalf("first %d", rec.Code)
		}
		time.Sleep(time.Second)
		ctx, disconnect := context.WithCancel(context.Background())
		leaving := route.postAsync(ctx, "/v1/responses")
		synctest.Wait()
		time.Sleep(time.Second)
		disconnect()
		synctest.Wait()
		if rec := <-leaving; rec.Code != 499 {
			t.Fatalf("leaving %d %s", rec.Code, rec.Body)
		}
		// Had the leaving request kept its place, this one would get the slot
		// after it, a minute later, beyond its maximum wait.
		next := route.postAsync(context.Background(), "/v1/responses")
		time.Sleep(time.Minute - 2*time.Second)
		synctest.Wait()
		rec := <-next
		if rec.Code != http.StatusOK || !time.Now().Equal(start.Add(time.Minute)) || route.calls.Load() != 2 {
			t.Fatalf("next %d at %v after %d calls", rec.Code, time.Since(start), route.calls.Load())
		}
	})
}

func TestRequestPoolChangesApplyWithoutRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		route := newPoolRoute(t, &domain.RequestPool{RequestsPerMinute: 1, MaxWaitSeconds: 0}, false)
		ctx := context.Background()
		route.post(ctx, "/v1/responses", false)
		poolRefused(t, route.post(ctx, "/v1/responses", false), "60")
		route.setPool(t, &domain.RequestPool{RequestsPerMinute: 2, MaxWaitSeconds: 0})
		rec := route.post(ctx, "/v1/responses", false)
		if rec.Code != http.StatusOK {
			t.Fatalf("raised pool %d", rec.Code)
		}
		poolHeaders(t, rec, "2", "0", "1m0s")
		route.setPool(t, nil)
		rec = route.post(ctx, "/v1/responses", false)
		if rec.Code != http.StatusOK {
			t.Fatalf("removed pool %d", rec.Code)
		}
		poolHeaders(t, rec, "", "", "")
	})
}

func TestRequestPoolHeadersOnFailuresAndUnpooledKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		route := newPoolRoute(t, &domain.RequestPool{RequestsPerMinute: 5, MaxWaitSeconds: 30}, true)
		for _, stream := range []bool{false, true} {
			rec := route.post(context.Background(), "/backend-api/codex/responses", stream)
			if rec.Code == http.StatusOK && !stream {
				t.Fatalf("failing upstream answered %d", rec.Code)
			}
			remaining := "4"
			if stream {
				remaining = "3"
			}
			poolHeaders(t, rec, "5", remaining, "1m0s")
		}
	})
	synctest.Test(t, func(t *testing.T) {
		route := newPoolRoute(t, nil, false)
		for range 3 {
			rec := route.post(context.Background(), "/v1/responses", false)
			if rec.Code != http.StatusOK {
				t.Fatalf("unpooled %d", rec.Code)
			}
			poolHeaders(t, rec, "", "", "")
			if rec.Header().Get("Retry-After") != "" {
				t.Fatal("unpooled answer carries Retry-After")
			}
		}
	})
}

func TestAdminKeyAPISetsKeepsAndClearsTheRequestPool(t *testing.T) {
	store, server := keyAdminFixture(t)
	key := createAdminKey(t, server, `{"name":"pooled","requestPool":{"requestsPerMinute":120}}`)
	if key.RequestPool == nil || *key.RequestPool != (domain.RequestPool{RequestsPerMinute: 120, MaxWaitSeconds: 30}) {
		t.Fatalf("created pool %+v", key.RequestPool)
	}
	key = patchAdminKey(t, server, key.ID, `{"name":"renamed"}`, http.StatusOK)
	if key.RequestPool == nil || key.RequestPool.RequestsPerMinute != 120 {
		t.Fatalf("absent field changed the pool: %+v", key.RequestPool)
	}
	key = patchAdminKey(t, server, key.ID, `{"requestPool":{"requestsPerMinute":5,"maxWaitSeconds":0}}`, http.StatusOK)
	if *key.RequestPool != (domain.RequestPool{RequestsPerMinute: 5}) {
		t.Fatalf("patched pool %+v", key.RequestPool)
	}
	for _, invalid := range []string{`{"requestPool":{}}`, `{"requestPool":{"requestsPerMinute":0}}`, `{"requestPool":{"requestsPerMinute":10001}}`,
		`{"requestPool":{"requestsPerMinute":5,"maxWaitSeconds":61}}`, `{"requestPool":{"requestsPerMinute":5,"burst":2}}`, `{"requestPool":5}`} {
		patchAdminKey(t, server, key.ID, invalid, http.StatusUnprocessableEntity)
	}
	key = patchAdminKey(t, server, key.ID, `{"requestPool":null}`, http.StatusOK)
	if key.RequestPool != nil {
		t.Fatalf("null kept the pool: %+v", key.RequestPool)
	}
	stored, err := store.GetAPIKey(context.Background(), key.ID)
	if err != nil || stored.RequestPool != nil {
		t.Fatalf("stored pool %+v %v", stored.RequestPool, err)
	}
}

// keyAdmin drives the admin key API with a signed-in administrator.
type keyAdmin struct {
	handler http.Handler
	cookie  *http.Cookie
}

func (a *keyAdmin) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Content-Type", "application/json")
	if a.cookie != nil {
		r.AddCookie(a.cookie)
	}
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, r)
	return w
}

func keyAdminFixture(t *testing.T) (*sqlite.Store, *keyAdmin) {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "admin.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	admin := &keyAdmin{handler: httpapi.New(store, vault, httpapi.Config{BootstrapToken: "synthetic-bootstrap-token-0123456789abcdef"}, nil).Handler(nil, nil)}
	setup := admin.do(t, "POST", "/api/dashboard-auth/password/setup", `{"password":"synthetic-admin-password","bootstrapToken":"synthetic-bootstrap-token-0123456789abcdef"}`)
	if setup.Code != http.StatusOK || len(setup.Result().Cookies()) != 1 {
		t.Fatalf("admin setup %d", setup.Code)
	}
	admin.cookie = setup.Result().Cookies()[0]
	return store, admin
}

func createAdminKey(t *testing.T, admin *keyAdmin, body string) domain.APIKey {
	t.Helper()
	rec := admin.do(t, "POST", "/api/api-keys", body)
	var key domain.APIKey
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &key) != nil {
		t.Fatalf("create key %d %s", rec.Code, rec.Body)
	}
	return key
}

func patchAdminKey(t *testing.T, admin *keyAdmin, id, body string, status int) domain.APIKey {
	t.Helper()
	rec := admin.do(t, "PATCH", "/api/api-keys/"+id, body)
	var key domain.APIKey
	if rec.Code != status || status == http.StatusOK && json.Unmarshal(rec.Body.Bytes(), &key) != nil {
		t.Fatalf("patch %s: %d %s", body, rec.Code, rec.Body)
	}
	if status == http.StatusUnprocessableEntity && !strings.Contains(rec.Body.String(), "invalid_request_pool") {
		t.Fatalf("patch %s refused as %s", body, rec.Body)
	}
	return key
}
