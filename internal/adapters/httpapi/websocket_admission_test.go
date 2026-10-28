package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

func TestWebSocketAdmissionRoutesShareGlobalCapacity(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/responses/", "/backend-api/codex/responses", "/backend-api/codex/responses/"} {
		t.Run(path, func(t *testing.T) {
			server, store := heartbeatFixture(t, nil)
			handler := server.Config.Handler.(*ProxyHandler)
			if usage := handler.bodies.Usage(); cap(handler.websockets) != 4096 || usage.LimitBytes != 128<<20 || usage.KeyLimitBytes != 64<<20 {
				t.Fatal("unexpected default budgets")
			}
			handler.websockets = make(chan struct{}, 3)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := store.SaveAPIKey(ctx, domain.APIKey{ID: "second-socket-key", Name: "synthetic", KeyHash: fmt.Sprintf("%x", sha256SumTest([]byte("synthetic-second"))), KeyPrefix: "synthetic", IsActive: true}, time.Now()); err != nil {
				t.Fatal(err)
			}
			for range 5 {
				req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+path, nil)
				req.Header.Set("Authorization", "Bearer synthetic-heartbeat")
				res, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
				if res.StatusCode == 503 {
					t.Fatal("failed upgrade leaked budget")
				}
			}
			dial := func(token string) (*websocket.Conn, *http.Response, error) {
				return websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+path, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
			}
			open := func(token string) *websocket.Conn {
				conn, _, err := dial(token)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { conn.CloseNow() })
				return conn
			}
			refuse := func(token string, status int, text string) {
				conn, res, err := dial(token)
				if err == nil {
					conn.CloseNow()
					t.Fatal("overflow accepted")
				}
				if res == nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				body, _ := io.ReadAll(res.Body)
				if res.StatusCode != status || !strings.Contains(string(body), text) || status == 503 && res.Header.Get("Retry-After") != "2" {
					t.Fatalf("rejection: %d %s", res.StatusCode, body)
				}
			}
			first := open("synthetic-heartbeat")
			open("synthetic-heartbeat")
			open("synthetic-heartbeat")
			refuse("synthetic-heartbeat", 503, "Too many open client WebSockets")
			refuse("synthetic-second", 503, "Too many open client WebSockets")
			refuse("invalid", 401, "invalid_api_key")
			first.CloseNow()
			for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
				conn, res, err := dial("synthetic-heartbeat")
				if err == nil {
					conn.CloseNow()
					break
				}
				if res != nil {
					res.Body.Close()
				}
				if time.Now().After(deadline) {
					t.Fatal("disconnect leaked a connection slot")
				}
			}
		})
	}
}

func TestWebSocketAdmissionDrainReleasesCounts(t *testing.T) {
	server, _ := heartbeatFixture(t, nil)
	p := server.Config.Handler.(*ProxyHandler)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connections := []*websocket.Conn{frameConnection(t, ctx, server), frameConnection(t, ctx, server)}
	p.proxy.BeginDrain()
	for _, conn := range connections {
		if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusGoingAway {
			t.Fatalf("idle connection did not drain: %v", err)
		}
	}
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		if len(p.websockets) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("drain leaked: %d sockets", len(p.websockets))
		}
	}
}
