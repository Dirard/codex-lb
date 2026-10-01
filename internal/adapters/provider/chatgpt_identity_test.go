package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"

	"codex-lb/internal/application"
)

func TestChatGPTLiveClientVersionPreservesExistingConnections(t *testing.T) {
	var version atomic.Value
	version.Store("0.156.0")
	var calls, handshakes atomic.Int64
	const event = `{"type":"response.completed","response":{"id":"probe","status":"completed","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":0,"output_tokens":0}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		want := version.Load().(string)
		if r.Header.Get("Version") != want || r.Header.Get("User-Agent") != "codex_cli_rs/"+want {
			t.Errorf("live version mismatch: version=%q user-agent=%q", r.Header.Get("Version"), r.Header.Get("User-Agent"))
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			handshakes.Add(1)
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.CloseNow()
			for {
				if _, _, err := conn.Read(r.Context()); err != nil {
					return
				}
				if err := conn.Write(r.Context(), websocket.MessageText, []byte(event)); err != nil {
					return
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+event+"\n\n")
	}))
	defer server.Close()
	adapter, _ := operationAdapter(t, server)
	defer adapter.Close()
	var reads atomic.Int64
	var sourceErr error
	adapter.config.ResolveClientVersion = func(context.Context) (string, error) {
		reads.Add(1)
		return version.Load().(string), sourceErr
	}
	ctx := context.Background()
	body := []byte(`{"model":"gpt-6-luna","input":"ping","stream":true}`)
	respond := func(ws bool, session string) error {
		_, err := adapter.Respond(ctx, application.ResponseTarget{Account: operationAccount(), KeyID: "key", UseWebSocket: ws, SessionID: session}, body, func(application.ResponseEvent) error { return nil })
		return err
	}
	for _, next := range []string{"0.156.0", "0.157.0"} {
		version.Store(next)
		if err := respond(false, ""); err != nil {
			t.Fatal(err)
		}
		if err := respond(true, next); err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Compact(ctx, application.CodexOperationTarget{Account: operationAccount(), KeyID: "key"}, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := respond(true, "0.156.0"); err != nil {
		t.Fatalf("version update disrupted an existing connection: %v", err)
	}
	if handshakes.Load() != 2 || calls.Load() != 6 || reads.Load() != 7 {
		t.Fatalf("unexpected reconnection or duplicate settings read: handshakes=%d calls=%d reads=%d", handshakes.Load(), calls.Load(), reads.Load())
	}
	for _, invalid := range []string{"", "0.157.0"} {
		version.Store(invalid)
		if invalid != "" {
			sourceErr = errors.New("settings unavailable")
		}
		var failure *application.ProviderFailure
		if err := respond(false, ""); !errors.As(err, &failure) || failure.Dispatched {
			t.Fatalf("bad version did not fail before dispatch: %v", err)
		}
	}
	if calls.Load() != 6 {
		t.Fatal("settings failure dispatched using a stale fallback")
	}
}
