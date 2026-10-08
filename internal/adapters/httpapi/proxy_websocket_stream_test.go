package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

type heartbeatProvider func(context.Context, application.ResponseTarget, json.RawMessage, func(application.ResponseEvent) error) (application.ResponseResult, error)

func (p heartbeatProvider) Respond(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
	return p(ctx, target, body, emit)
}

func heartbeatFixture(t *testing.T, provider heartbeatProvider) (*httptest.Server, *sqlite.Store) {
	t.Helper()
	store, api := localKeyFixture(t)
	ctx := context.Background()
	for _, id := range []string{"heartbeat-a", "heartbeat-b"} {
		if err := store.SaveAccount(ctx, domain.Account{ID: id, Kind: domain.AccountChatGPT, Provider: "openai", PlanType: "plus", Status: domain.AccountActive, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		secret, err := api.cipher.Encrypt([]byte("synthetic"))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: id, AccessTokenEncrypted: secret, RefreshTokenEncrypted: secret, IDTokenEncrypted: secret}); err != nil {
			t.Fatal(err)
		}
	}
	key := domain.APIKey{ID: "heartbeat-key", Name: "synthetic", KeyHash: fmt.Sprintf("%x", sha256SumTest([]byte("synthetic-heartbeat"))), KeyPrefix: "synthetic", IsActive: true, Limits: []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 100000}}}
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	proxy := application.NewProxy(store, provider, api.cipher, application.ProxyConfig{MaxStreams: 1, QueueTimeout: time.Second})
	handler := NewProxyHandler(store, proxy, nil)
	handler.websocketKeepaliveInterval = 15 * time.Millisecond
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, store
}

func heartbeatConnect(t *testing.T, ctx context.Context, server *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+path, &websocket.DialOptions{HTTPHeader: map[string][]string{"Authorization": {"Bearer synthetic-heartbeat"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-6-sol","input":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	return conn
}

func heartbeatRead(t *testing.T, ctx context.Context, conn *websocket.Conn) (string, string) {
	t.Helper()
	_, body, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Type     string `json:"type"`
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatal(err)
	}
	return event.Type, event.Response.ID
}

func heartbeatWait(ctx context.Context, gate <-chan struct{}) error {
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestNativeWebSocketHeartbeatsPreservePreludeAndAccounting(t *testing.T) {
	for _, path := range []string{"/backend-api/codex/responses", "/backend-api/codex/responses/"} {
		t.Run(path, func(t *testing.T) {
			created, finish := make(chan struct{}), make(chan struct{})
			server, store := heartbeatFixture(t, func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				if err := heartbeatWait(ctx, created); err != nil {
					return application.ResponseResult{}, err
				}
				if err := emit(application.ResponseEvent{Type: "response.created", Data: []byte(`{"type":"response.created","response":{"id":"local-response","status":"in_progress"}}`)}); err != nil {
					return application.ResponseResult{}, err
				}
				if err := heartbeatWait(ctx, finish); err != nil {
					return application.ResponseResult{}, err
				}
				return (localReplyProvider{}).Respond(ctx, target, body, emit)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn := heartbeatConnect(t, ctx, server, path)
			for range 2 {
				kind, id := heartbeatRead(t, ctx, conn)
				if kind != "codex.keepalive" || id != "" {
					t.Fatalf("pre-created heartbeat: %s %q", kind, id)
				}
			}
			close(created)
			for {
				kind, id := heartbeatRead(t, ctx, conn)
				if kind == "codex.keepalive" {
					continue
				}
				if kind != "response.in_progress" || id != "local-response" {
					t.Fatalf("buffered startup leaked or fake ID: %s %q", kind, id)
				}
				break
			}
			close(finish)
			var real []string
			for {
				kind, _ := heartbeatRead(t, ctx, conn)
				if kind == "codex.keepalive" || kind == "response.in_progress" {
					continue
				}
				real = append(real, kind)
				if kind == "response.completed" {
					break
				}
				if kind != "response.created" {
					t.Fatalf("unexpected event: %s", kind)
				}
			}
			if strings.Join(real, ",") != "response.created,response.completed" {
				t.Fatal(real)
			}
			key, err := store.GetAPIKey(ctx, "heartbeat-key")
			if err != nil || key.Limits[0].CurrentValue != 2 {
				t.Fatalf("heartbeat changed billing: %+v %v", key.Limits, err)
			}
			quiet, stop := context.WithTimeout(ctx, 60*time.Millisecond)
			defer stop()
			if _, _, err := conn.Read(quiet); err == nil {
				t.Fatal("event after terminal")
			}
		})
	}
}

func TestGenericWebSocketDoesNotReceiveNativeHeartbeats(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/responses/"} {
		t.Run(path, func(t *testing.T) {
			server, _ := heartbeatFixture(t, func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				timer := time.NewTimer(80 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					return application.ResponseResult{}, ctx.Err()
				}
				return (localReplyProvider{}).Respond(ctx, target, body, emit)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if kind, _ := heartbeatRead(t, ctx, heartbeatConnect(t, ctx, server, path)); kind != "response.completed" {
				t.Fatalf("generic synthetic event: %s", kind)
			}
		})
	}
}

func TestWebSocketHeartbeatDoesNotPreventQuotaFailover(t *testing.T) {
	refuse, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	accounts := make(chan string, 2)
	server, store := heartbeatFixture(t, func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		accounts <- target.Account.ID
		if calls.Add(1) == 1 {
			if err := emit(application.ResponseEvent{Type: "response.created", Data: []byte(`{"type":"response.created","response":{"id":"refused-response"}}`)}); err != nil {
				return application.ResponseResult{}, err
			}
			if err := heartbeatWait(ctx, refuse); err != nil {
				return application.ResponseResult{}, err
			}
			return application.ResponseResult{}, &application.ProviderFailure{Code: "insufficient_quota", Status: 429, QuotaRefused: true, Dispatched: true}
		}
		if err := heartbeatWait(ctx, finish); err != nil {
			return application.ResponseResult{}, err
		}
		return (localReplyProvider{}).Respond(ctx, target, body, emit)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := heartbeatConnect(t, ctx, server, "/backend-api/codex/responses")
	for {
		kind, id := heartbeatRead(t, ctx, conn)
		if kind == "codex.keepalive" {
			continue
		}
		if kind != "response.in_progress" || id != "refused-response" {
			t.Fatalf("first heartbeat: %s %q", kind, id)
		}
		break
	}
	close(refuse)
	for {
		kind, _ := heartbeatRead(t, ctx, conn)
		if kind == "codex.keepalive" {
			break
		}
		if kind != "response.in_progress" {
			t.Fatalf("quota leaked: %s", kind)
		}
	}
	close(finish)
	for {
		kind, id := heartbeatRead(t, ctx, conn)
		if kind == "response.completed" {
			if id != "local-response" {
				t.Fatal(id)
			}
			break
		}
		if kind != "codex.keepalive" {
			t.Fatalf("failed prelude leaked: %s %q", kind, id)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("quota failover suppressed or duplicated")
	}
	if first, second := <-accounts, <-accounts; first == second {
		t.Fatal("quota-refused account reused")
	}
	key, err := store.GetAPIKey(ctx, "heartbeat-key")
	if err != nil || key.Limits[0].CurrentValue != 2 {
		t.Fatalf("quota heartbeat billed: %+v %v", key.Limits, err)
	}
}

func TestWebSocketHeartbeatDisconnectReleasesWorker(t *testing.T) {
	for _, disconnect := range []bool{true, false} {
		t.Run(fmt.Sprint("disconnect=", disconnect), func(t *testing.T) {
			stopped := make(chan struct{})
			started := make(chan struct{})
			var calls atomic.Int32
			server, _ := heartbeatFixture(t, func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				if calls.Add(1) == 1 {
					close(started)
					<-ctx.Done()
					close(stopped)
					return application.ResponseResult{}, ctx.Err()
				}
				return (localReplyProvider{}).Respond(ctx, target, body, emit)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn := heartbeatConnect(t, ctx, server, "/backend-api/codex/responses")
			if err := heartbeatWait(ctx, started); err != nil {
				t.Fatal("provider did not start")
			}
			if kind, _ := heartbeatRead(t, ctx, conn); kind != "codex.keepalive" {
				t.Fatal(kind)
			}
			if disconnect {
				conn.CloseNow()
			} else if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.cancel"}`)); err != nil {
				t.Fatal(err)
			}
			if err := heartbeatWait(ctx, stopped); err != nil {
				t.Fatal("worker not cancelled")
			}
			next := conn
			if disconnect {
				next = heartbeatConnect(t, ctx, server, "/backend-api/codex/responses")
			} else {
				for {
					kind, _ := heartbeatRead(t, ctx, conn)
					if kind == "error" {
						break
					}
					if kind != "codex.keepalive" {
						t.Fatal(kind)
					}
				}
				if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-6-sol","input":"next"}`)); err != nil {
					t.Fatal(err)
				}
			}
			for {
				kind, _ := heartbeatRead(t, ctx, next)
				if kind == "response.completed" {
					break
				}
				if kind != "codex.keepalive" {
					t.Fatal(kind)
				}
			}
			if calls.Load() != 2 {
				t.Fatal("cancelled generation was replayed")
			}
		})
	}
}
