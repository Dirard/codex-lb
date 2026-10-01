package chatgpt

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
)

func TestClosingCallbackListenerDoesNotAbortSuccessfulReply(t *testing.T) {
	var listener io.Closer
	var err error
	listener, err = ListenCallback("127.0.0.1:0", func(context.Context, string) application.OAuthStatusResult {
		listener.Close()
		return application.OAuthStatusResult{Status: "success"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := listener.(*callbackListener).listener.Addr().String()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + address + "/auth/callback?state=synthetic&code=synthetic")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || !strings.Contains(string(body), "Login complete") || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("closing listener aborted callback result")
	}
}
