package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

type blockingProvider struct{ entered, cancelled chan struct{} }

func (p *blockingProvider) Respond(ctx context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
	close(p.entered)
	if emit != nil {
		_ = emit(application.ResponseEvent{Type: "response.output_text.delta", Data: json.RawMessage(`{"type":"response.output_text.delta","delta":"started"}`)})
	}
	<-ctx.Done()
	close(p.cancelled)
	return application.ResponseResult{}, ctx.Err()
}

func TestShutdownJoinsStreamingAndWebSocketSettlement(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.shutdownGrace = 10 * time.Millisecond
			r, err := openRuntime(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.close() })
			// Background quota/catalog refresh sees synthetic accounts too. Keep
			// every runtime HTTP client offline, not only the response provider.
			r.transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("network disabled in offline runtime test")
			}
			account := domain.Account{ID: "a", Kind: domain.AccountChatGPT, Provider: "openai", Email: "synthetic@example.invalid", Status: domain.AccountActive, PlanType: "plus", CreatedAt: time.Now()}
			ctx := context.Background()
			if err := r.data.store.SaveAccount(ctx, account); err != nil {
				t.Fatal(err)
			}
			secret, _ := r.data.vault.Encrypt([]byte("synthetic"))
			if err := r.data.store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: "a", AccessTokenEncrypted: secret, RefreshTokenEncrypted: secret, IDTokenEncrypted: secret}); err != nil {
				t.Fatal(err)
			}
			key := domain.APIKey{ID: "k", Name: "test", KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic"))), KeyPrefix: "synthetic", IsActive: true, CreatedAt: time.Now()}
			if err := r.data.store.SaveAPIKey(ctx, key, time.Now()); err != nil {
				t.Fatal(err)
			}
			upstream := &blockingProvider{make(chan struct{}), make(chan struct{})}
			r.proxy = application.NewProxy(r.data.store, upstream, r.data.vault, application.ProxyConfig{})
			r.server.Handler = r.requests.track(httpapi.NewProxyHandler(r.data.store, r.proxy, nil))
			listener, err := net.Listen("tcp", cfg.listen)
			if err != nil {
				t.Fatal(err)
			}
			lifetime, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- r.serveListener(lifetime, listener) }()
			clientCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			defer stop()
			base := "http://" + listener.Addr().String() + "/v1/responses"
			if transport == "http" {
				request, _ := http.NewRequestWithContext(clientCtx, "POST", base, strings.NewReader(`{"model":"gpt-6-sol","input":"hello","stream":true}`))
				request.Header.Set("Authorization", "Bearer synthetic")
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != 200 {
					t.Fatalf("stream: %d", response.StatusCode)
				}
			} else {
				connection, _, err := websocket.Dial(clientCtx, "ws"+strings.TrimPrefix(base, "http"), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic"}}})
				if err != nil {
					t.Fatal(err)
				}
				defer connection.CloseNow()
				if err := connection.Write(clientCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-6-sol","input":"hello"}`)); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-upstream.entered:
			case <-clientCtx.Done():
				t.Fatal("upstream did not start")
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-clientCtx.Done():
				t.Fatal("shutdown did not join streaming handler")
			}
			select {
			case <-upstream.cancelled:
			default:
				t.Fatal("provider was abandoned")
			}
			store, err := sqlite.Open(cfg.dataDir + "/codex-lb.sqlite3")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			totals, err := store.UsageTotals(ctx, "k", "")
			pending, pendingErr := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			if err != nil || totals.RequestCount != 0 || pendingErr != nil || len(pending) != 1 {
				t.Fatalf("uncertain reservation was not retained before close: totals=%+v pending=%+v errors=%v/%v", totals, pending, err, pendingErr)
			}
		})
	}
}
