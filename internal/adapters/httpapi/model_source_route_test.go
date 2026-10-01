package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

func TestExternalSourceHTTPRouteEditDuringRequest(t *testing.T) {
	for _, phase := range []string{"before dispatch", "in flight success", "in flight quota"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			waitForEdit := func() {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			var oldCalls, newCalls, admitted atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n := oldCalls.Add(1)
				if n == 1 && phase != "before dispatch" {
					waitForEdit()
				}
				w.Header().Set("Content-Type", "application/json")
				if n == 1 && phase == "in flight quota" {
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = io.WriteString(w, `{"error":{"code":"usage_limit_reached"},"usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10}}`)
					return
				}
				_, _ = fmt.Fprintf(w, `{"id":"old_%d","object":"response","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10}}`, n)
			}))
			defer upstream.Close()
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				newCalls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer other.Close()
			var adapter *provider.Adapter
			server, store, proxy := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				if admitted.Add(1) == 1 && phase == "before dispatch" {
					waitForEdit()
				}
				return adapter.Respond(ctx, target, body, emit)
			}))
			proxy.ResolvePrice = func(context.Context, domain.Account, string) (pricing.Price, error) {
				return pricing.Price{Standard: pricing.Rates{Input: 1_000_000, Cached: 1_000_000, Output: 1_000_000}}, nil
			}
			vault, err := credentials.Open(filepath.Join(t.TempDir(), "source.key"), true)
			if err != nil {
				t.Fatal(err)
			}
			secret, err := vault.Encrypt([]byte("synthetic-source-key"))
			if err != nil {
				t.Fatal(err)
			}
			source := domain.ModelSource{ID: "edited-source", Name: "Offline source", Kind: domain.ModelSourceOpenAICompatible,
				BaseURL: upstream.URL + "/v1", Enabled: true, Responses: true,
				Models: []domain.ModelSourceModel{{Model: "gpt-6-sol", Enabled: true}}}
			if err := store.SaveModelSource(ctx, source, &domain.AccountCredential{AccountID: source.ID, ExternalKeyEncrypted: secret}); err != nil {
				t.Fatal(err)
			}
			account, err := store.GetAccount(ctx, "wire-account")
			if err != nil {
				t.Fatal(err)
			}
			account.Status = domain.AccountPaused
			if err := store.SaveAccount(ctx, account); err != nil {
				t.Fatal(err)
			}
			key, err := store.GetAPIKey(ctx, "wire-key")
			if err != nil {
				t.Fatal(err)
			}
			key.SourceAssignmentScopeEnabled, key.AssignedSourceIDs = true, []string{source.ID}
			if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
				t.Fatal(err)
			}
			adapter = provider.New(store, fixedTokenSource{}, vault, provider.Config{HTTPClient: upstream.Client()})
			defer adapter.Close()

			admin := httpapi.New(store, vault, httpapi.Config{}, nil).Handler(nil, nil)
			var cookie *http.Cookie
			adminRequest := func(method, path, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
				r.RemoteAddr = "127.0.0.1:5678"
				r.Header.Set("Content-Type", "application/json")
				if cookie != nil {
					r.AddCookie(cookie)
				}
				w := httptest.NewRecorder()
				admin.ServeHTTP(w, r)
				return w
			}
			setup := adminRequest(http.MethodPost, "/api/dashboard-auth/password/setup", `{"password":"synthetic-route-password"}`)
			if setup.Code != 200 || len(setup.Result().Cookies()) == 0 {
				t.Fatalf("admin setup: %d", setup.Code)
			}
			cookie = setup.Result().Cookies()[0]
			type reply struct {
				status int
				body   string
				err    error
			}
			respond := func(body string) reply {
				r, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/responses", strings.NewReader(body))
				if err != nil {
					return reply{err: err}
				}
				r.Header.Set("Authorization", "Bearer synthetic-key")
				res, err := server.Client().Do(r)
				if err != nil {
					return reply{err: err}
				}
				defer res.Body.Close()
				data, err := io.ReadAll(res.Body)
				return reply{res.StatusCode, string(data), err}
			}
			done := make(chan reply, 1)
			go func() { done <- respond(`{"model":"gpt-6-sol","input":"hello"}`) }()
			select {
			case <-entered:
			case got := <-done:
				t.Fatalf("request finished before edit: %+v", got)
			case <-ctx.Done():
				t.Fatal("request did not reach route boundary")
			}
			// The address returns to A before the old attempt continues. Endpoint
			// equality alone must not make that attempt current again.
			for _, url := range []string{other.URL + "/v1", source.BaseURL} {
				res := adminRequest(http.MethodPatch, "/api/model-sources/"+source.ID, fmt.Sprintf(`{"baseUrl":%q}`, url))
				if res.Code != http.StatusOK {
					t.Fatalf("source edit: %d %s", res.Code, res.Body.String())
				}
			}
			unblock()
			var got reply
			select {
			case got = <-done:
			case <-ctx.Done():
				t.Fatal("edited request did not finish")
			}
			wantCalls, wantTokens := int32(1), int64(10)
			if phase == "before dispatch" {
				wantCalls, wantTokens = 0, 0
				if got.status < 400 {
					t.Errorf("retired admission succeeded: %+v", got)
				}
			}
			if got.err != nil || oldCalls.Load() != wantCalls || newCalls.Load() != 0 {
				t.Fatalf("unexpected execution after edit: %+v calls=%d/%d", got, oldCalls.Load(), newCalls.Load())
			}
			totals, err := store.UsageTotals(ctx, "wire-key", source.ID)
			if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens+totals.Usage.OutputTokens != wantTokens {
				t.Fatalf("original bill lost: %+v %v", totals, err)
			}
			key, err = store.GetAPIKey(ctx, "wire-key")
			if err != nil || len(key.Limits) != 1 || key.Limits[0].CurrentValue != wantTokens {
				t.Fatalf("key charge after source edit: %+v %v", key.Limits, err)
			}
			pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			if err != nil || len(pending) != 0 {
				t.Fatalf("known route-edit bill left pending: %+v %v", pending, err)
			}
			if _, err := store.GetContinuation(ctx, "wire-key", "old_1", time.Now()); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("retired continuation returned: %v", err)
			}
			account, err = store.GetAccount(ctx, source.ID)
			if err != nil || account.Status != domain.AccountActive {
				t.Fatalf("late outcome blocked current route: %+v %v", account, err)
			}
			fresh := respond(`{"model":"gpt-6-sol","input":"new turn"}`)
			if fresh.err != nil || fresh.status != http.StatusOK || oldCalls.Load() != wantCalls+1 {
				t.Fatalf("fresh route request failed: %+v calls=%d", fresh, oldCalls.Load())
			}
		})
	}
}
