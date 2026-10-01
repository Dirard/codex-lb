package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

func TestRuntimeResponsesRoutesAreNotRealtimeCalls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := openRuntime(ctx, testConfig(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	const secret = "synthetic-runtime-route-key"
	if err := r.data.store.SaveAPIKey(ctx, domain.APIKey{ID: "route-key", Name: "route test", KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte(secret))), KeyPrefix: "synthetic", IsActive: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	settings, err := r.data.store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.APIKeyAuthEnabled = true
	if err := r.data.store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(r.server.Handler)
	defer server.Close()
	for _, path := range []string{"/v1/responses", "/v1/responses/", "/backend-api/codex/responses", "/backend-api/codex/responses/"} {
		t.Run(path, func(t *testing.T) {
			url := "ws" + strings.TrimPrefix(server.URL, "http") + path
			connection, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}}})
			if err != nil {
				status := 0
				if response != nil {
					status = response.StatusCode
				}
				t.Fatalf("Responses upgrade reached wrong handler: status=%d err=%v", status, err)
			}
			defer connection.CloseNow()
			if err := connection.Write(ctx, websocket.MessageText, []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			_, frame, err := connection.Read(ctx)
			var event struct {
				Type  string
				Error struct{ Code string }
			}
			if err != nil || json.Unmarshal(frame, &event) != nil || event.Type != "error" || event.Error.Code != "invalid_request" {
				t.Fatalf("Responses frame validation not reached: %s %v", frame, err)
			}
			_ = connection.Close(websocket.StatusNormalClosure, "")
			for _, auth := range []bool{false, true} {
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
				req.Header.Set("Content-Type", "application/json")
				if auth {
					req.Header.Set("Authorization", "Bearer "+secret)
				}
				w := httptest.NewRecorder()
				r.server.Handler.ServeHTTP(w, req)
				want := 401
				if auth {
					want = 400
				}
				if w.Code != want {
					t.Fatalf("POST validation/auth changed: %d %s", w.Code, w.Body.String())
				}
			}
			_, rejected, err := websocket.Dial(ctx, url, nil)
			if err == nil || rejected == nil || rejected.StatusCode != 401 {
				t.Fatal("missing WebSocket key was accepted")
			}
		})
	}
	req := httptest.NewRequest(http.MethodGet, "/backend-api/codex/rtc_unknown", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	r.server.Handler.ServeHTTP(w, req)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "realtime_call_owner_not_found") {
		t.Fatal("Realtime ownership route changed")
	}
}
