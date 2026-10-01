package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

func TestWaitingWebSocketFailurePreservesNonexecutionProof(t *testing.T) {
	for _, mode := range []string{"capacity", "cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				if _, _, err := conn.Read(ctx); err != nil {
					return
				}
				calls.Add(1)
				if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"primary"}}`)); err != nil {
					return
				}
				select {
				case <-release:
				case <-ctx.Done():
					return
				}
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"primary","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`))
			}))
			defer server.Close()
			store := &sourceStore{credential: domain.AccountCredential{AccountID: "account"}}
			adapter := New(store, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client(), ChatGPTBaseURL: server.URL})
			defer adapter.Close()
			defer unblock()
			target := application.ResponseTarget{Account: domain.Account{ID: "account", Kind: domain.AccountChatGPT}, KeyID: "key", SessionID: "busy-session", UseWebSocket: true}
			body := json.RawMessage(`{"model":"gpt-6-luna","input":"synthetic","stream":true}`)
			ready, finished := make(chan struct{}), make(chan error, 1)
			go func() {
				result, err := adapter.Respond(ctx, target, body, func(event application.ResponseEvent) error {
					if event.Type == "response.created" {
						close(ready)
					}
					return nil
				})
				if err == nil && (!result.UsageKnown || result.Usage.InputTokens != 2) {
					err = errors.New("primary usage lost")
				}
				finished <- err
			}()
			select {
			case <-ready:
			case err := <-finished:
				t.Fatalf("primary failed: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			waiting := ctx
			var wantCause error
			switch mode {
			case "cancel":
				child, stop := context.WithCancel(ctx)
				stop()
				waiting = child
				wantCause = context.Canceled
			case "deadline":
				child, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer stop()
				waiting = child
				wantCause = context.DeadlineExceeded
			}
			_, err := adapter.Respond(waiting, target, body, func(application.ResponseEvent) error { t.Error("queued call emitted output"); return nil })
			var failure *application.ProviderFailure
			if !errors.As(err, &failure) || !failure.RejectedBeforeExecution || (wantCause != nil && !errors.Is(err, wantCause)) {
				t.Fatalf("lost nonexecution proof/cancellation: %v", err)
			}
			if mode == "capacity" && failure.Code != "local_capacity_exceeded" {
				t.Fatalf("wrong refusal: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatal("queued failure dispatched another response.create")
			}
			unblock()
			if err := <-finished; err != nil {
				t.Fatal("queued failure interrupted the primary response", err)
			}
		})
	}
}

func TestRemoteWebSocketCapacityAndDisconnectedResponseRemainUncertain(t *testing.T) {
	for _, mode := range []string{"handshake503", "stream503", "disconnect", "cancel_after_create"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "handshake503" {
					w.WriteHeader(503)
					_, _ = w.Write([]byte(`{"error":{"code":"local_capacity_exceeded"}}`))
					return
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				if _, _, err := conn.Read(ctx); err != nil {
					return
				}
				if mode == "cancel_after_create" {
					cancel()
					return
				}
				if mode == "stream503" {
					_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"error","status":503,"error":{"code":"local_capacity_exceeded"}}`))
				}
			}))
			defer server.Close()
			store := &sourceStore{credential: domain.AccountCredential{AccountID: "account"}}
			adapter := New(store, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client(), ChatGPTBaseURL: server.URL})
			defer adapter.Close()
			_, err := adapter.Respond(ctx, application.ResponseTarget{Account: domain.Account{ID: "account", Kind: domain.AccountChatGPT}, KeyID: "key", UseWebSocket: true}, []byte(`{"model":"gpt-6-luna","input":"synthetic","stream":true}`), func(application.ResponseEvent) error { return nil })
			var failure *application.ProviderFailure
			if err == nil || errors.As(err, &failure) && (!failure.Dispatched || failure.RejectedBeforeExecution) {
				t.Fatalf("remote/ambiguous failure was declared free: %v", err)
			}
		})
	}
}
