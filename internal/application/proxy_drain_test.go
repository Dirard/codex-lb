package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/application"
)

func TestProxyDrainKeepsActiveResponseAndRejectsNewTurns(t *testing.T) {
	proxy, _, stub := proxyFixture(t)
	entered, finish := make(chan struct{}), make(chan struct{})
	stub.respond = func(ctx context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		close(entered)
		select {
		case <-finish:
		case <-ctx.Done():
			return application.ResponseResult{}, ctx.Err()
		}
		return complete("resp_drained", emit)
	}
	done := make(chan error, 1)
	body := json.RawMessage(`{"model":"gpt-6-astra","input":"hello"}`)
	go func() {
		_, err := proxy.Respond(context.Background(), application.ResponseOptions{KeyID: "key-test"}, body, nil)
		done <- err
	}()
	<-entered
	proxy.BeginDrain()
	proxy.BeginDrain()
	_, err := proxy.Respond(context.Background(), application.ResponseOptions{KeyID: "key-test"}, body, nil)
	var failure *application.ProxyError
	if !errors.As(err, &failure) || failure.Code != "server_draining" {
		t.Fatalf("new turn: %v", err)
	}
	close(finish)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("active response was abandoned")
	}
}
