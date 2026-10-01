package application

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type blockedOAuth struct {
	OAuthClient
	started chan struct{}
	finish  chan struct{}
	calls   atomic.Int64
}

func (b *blockedOAuth) ExchangeCode(ctx context.Context, _ string, _ string) (OAuthTokens, error) {
	if b.calls.Add(1) == 1 {
		close(b.started)
	}
	select {
	case <-ctx.Done():
		return OAuthTokens{}, ctx.Err()
	case <-b.finish:
		return tokenSet("synthetic-user"), nil
	}
}

type closeCallback func() error

func (c closeCallback) Close() error { return c() }

func TestConcurrentOAuthCallbackExchangesCodeOnce(t *testing.T) {
	service, _, stub, _ := newTestService(t, true)
	blocked := &blockedOAuth{OAuthClient: stub, started: make(chan struct{}), finish: make(chan struct{})}
	service.oauth = blocked
	var closed atomic.Int64
	if err := service.ConfigureCallbacks(func(OAuthCallback) (io.Closer, error) {
		return closeCallback(func() error { closed.Add(1); return nil }), nil
	}); err != nil {
		t.Fatal(err)
	}
	start, err := service.StartOAuth(context.Background(), "browser", "")
	if err != nil {
		t.Fatal(err)
	}
	callback := "http://localhost:1455/auth/callback?code=synthetic&state=" + stub.lastState
	done := make(chan OAuthStatusResult, 1)
	go func() { done <- service.ManualCallback(context.Background(), callback, *start.FlowID) }()
	select {
	case <-blocked.started:
	case <-time.After(time.Second):
		t.Fatal("exchange did not start")
	}
	if second := service.ManualCallback(context.Background(), callback, *start.FlowID); second.Status != "pending" {
		t.Fatal("concurrent callback did not wait for existing exchange")
	}
	close(blocked.finish)
	if result := <-done; result.Status != "success" || blocked.calls.Load() != 1 || closed.Load() != 1 {
		t.Fatal("OAuth exchange duplicated or listener not closed")
	}
}

func TestAccountsCloseCancelsAndJoinsOAuthExchange(t *testing.T) {
	service, _, stub, _ := newTestService(t, true)
	blocked := &blockedOAuth{OAuthClient: stub, started: make(chan struct{}), finish: make(chan struct{})}
	service.oauth = blocked
	start, err := service.StartOAuth(context.Background(), "browser", "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan OAuthStatusResult, 1)
	go func() {
		done <- service.ManualCallback(context.Background(), "http://localhost:1455/auth/callback?code=synthetic&state="+stub.lastState, *start.FlowID)
	}()
	select {
	case <-blocked.started:
	case <-time.After(time.Second):
		t.Fatal("exchange did not start")
	}
	closed := make(chan struct{})
	go func() { service.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join OAuth exchange")
	}
	if result := <-done; result.Status != "error" {
		t.Fatal("cancelled OAuth marked successful")
	}
	if _, err := service.StartOAuth(context.Background(), "browser", ""); !errors.Is(err, context.Canceled) {
		t.Fatal("new flow admitted after shutdown")
	}
}
