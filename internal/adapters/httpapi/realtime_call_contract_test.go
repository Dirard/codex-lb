package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestRealtimeCallPreservesOfferMediaQueryAndPrivateOwner(t *testing.T) {
	for _, tc := range []struct{ name, media, body string }{
		{"JSON", "application/json; charset=utf-8", `{"sdp":"v=offer\r\na=ice-pwd:synthetic-private\r\n","session":{"type":"realtime"}}`},
		{"SDP", "application/sdp", "v=offer\r\na=ice-pwd:synthetic-private\r\n"},
		{"multipart", "multipart/form-data; boundary=e2e", "--e2e\r\nContent-Disposition: form-data; name=\"sdp\"\r\n\r\nv=offer\r\n--e2e--\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const location = "https://api.openai.com/v1/realtime/calls/rtc_owned?private-query=secret#private-fragment"
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if r.URL.Path != "/codex/realtime/calls" || r.Method != "POST" || string(body) != tc.body || r.Header.Get("Content-Type") != tc.media || r.URL.Query().Get("intent") != "quicksilver" || r.URL.Query().Get("architecture") != "avas" || len(r.URL.Query()["tag"]) != 2 || r.Header.Get("OpenAI-Alpha") != "quicksilver=v2" || r.Header.Get("OpenAI-Beta") != "realtime=v1" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Private") != "" {
					t.Error("realtime offer body/media/query changed")
				}
				w.Header().Set("Location", location)
				w.Header().Set("Content-Type", "application/sdp")
				w.WriteHeader(201)
				fmt.Fprint(w, "v=answer\r\n")
			}))
			defer upstream.Close()
			_, store, proxy := wireFixtureWithProxy(t, nil)
			adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
			defer adapter.Close()
			ops := application.NewCodexOperations(store, store, adapter, time.Hour)
			ops.ConfigureAdmission(proxy)
			var archives atomic.Int32
			ops.ConfigureDiagnostics(func(context.Context, application.ErrorDiagnostic) { archives.Add(1) })
			handler := httpapi.NewCodexOperationsHandler(store, ops)
			request := func(body, media, encoding, query, token string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "/backend-api/codex/realtime/calls"+query, strings.NewReader(body))
				r.Header.Set("Content-Type", media)
				r.Header.Set("Content-Encoding", encoding)
				r.Header.Set("OpenAI-Alpha", "quicksilver=v2")
				r.Header.Set("OpenAI-Beta", "responses=experimental, realtime=v1, responses_websockets=2026-02-06")
				r.Header.Set("Cookie", "synthetic-private-cookie")
				r.Header.Set("X-Private", "synthetic-private-header")
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			result := request(tc.body, tc.media, "", "?intent=quicksilver&architecture=avas&tag=one&tag=two", "synthetic-key")
			if result.Code != 201 || result.Body.String() != "v=answer\r\n" || result.Header().Get("Location") != location || result.Header().Get("Content-Type") != "application/sdp" {
				t.Fatalf("offer failed: %d %s", result.Code, result.Body.String())
			}
			owner, err := store.GetCodexResourceOwner(context.Background(), application.CodexResourceRealtime, "rtc_owned", "wire-key", time.Now())
			if err != nil || owner.AccountID != "wire-account" {
				t.Fatalf("call owner not durable: %+v %v", owner, err)
			}
			for _, bad := range []struct {
				body, media, encoding, query, key string
				status                            int
			}{
				{tc.body, tc.media, "", "", "", 401},
				{"invalid", "application/json", "", "", "synthetic-key", 400},
				{"v=offer", "multipart/form-data", "", "", "synthetic-key", 415},
				{"v=offer", "application/sdp", "gzip", "", "synthetic-key", 415},
				{strings.Repeat("x", application.MaxRealtimeCallBytes+1), "application/sdp", "", "", "synthetic-key", 413},
				{"v=offer", "application/sdp", "", "?x=" + strings.Repeat("x", 513), "synthetic-key", 400},
			} {
				if got := request(bad.body, bad.media, bad.encoding, bad.query, bad.key); got.Code != bad.status {
					t.Fatalf("invalid offer status=%d want=%d", got.Code, bad.status)
				}
			}
			if calls.Load() != 1 || archives.Load() != 0 {
				t.Fatalf("invalid/private offer dispatched or archived: calls=%d archives=%d", calls.Load(), archives.Load())
			}
		})
	}
}

type failingRealtimeOwnerStore struct {
	application.CodexResourceOwners
}

func (f failingRealtimeOwnerStore) SaveCodexResourceOwner(context.Context, application.CodexResourceOwner) error {
	return errors.New("synthetic-private-owner-error")
}

func TestRealtimeCallFailuresNeverExposeOrArchivePrivatePayloads(t *testing.T) {
	for _, mode := range []string{"upstream rejection", "owner persistence", "invalid location"} {
		t.Run(mode, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/v1/live/rtc_safe?private=synthetic-private")
				if mode == "upstream rejection" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(403)
					fmt.Fprint(w, `{"error":{"code":"synthetic-private-code","message":"synthetic-private-SDP"}}`)
					return
				}
				if mode == "invalid location" {
					w.Header().Set("Location", "/v1/live/..")
				}
				w.Header().Set("Content-Type", "application/sdp")
				w.WriteHeader(201)
				fmt.Fprint(w, "v=answer\r\na=ice-pwd:synthetic-private\r\n")
			}))
			defer upstream.Close()
			_, store, proxy := wireFixtureWithProxy(t, nil)
			adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
			defer adapter.Close()
			var owners application.CodexResourceOwners = store
			if mode == "owner persistence" {
				owners = failingRealtimeOwnerStore{store}
			}
			ops := application.NewCodexOperations(store, owners, adapter, time.Hour)
			ops.ConfigureAdmission(proxy)
			var archives atomic.Int32
			ops.ConfigureDiagnostics(func(context.Context, application.ErrorDiagnostic) { archives.Add(1) })
			handler := httpapi.NewCodexOperationsHandler(store, ops)
			r := httptest.NewRequest("POST", "/backend-api/codex/realtime/calls", strings.NewReader(`{"sdp":"synthetic-private-SDP"}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer synthetic-key")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			want := 503
			code := "realtime_call_binding_failed"
			if mode == "upstream rejection" {
				want = 403
				code = "realtime_call_unavailable"
			}
			if w.Code != want || !strings.Contains(w.Body.String(), code) || strings.Contains(w.Body.String(), "synthetic-private") || w.Header().Get("Location") != "" || archives.Load() != 0 {
				t.Fatalf("private failure escaped: status=%d archive=%d", w.Code, archives.Load())
			}
			logs, err := store.ListRequestLogs(context.Background(), domain.RequestLogFilter{Limit: 10})
			if err != nil || len(logs.Requests) != 1 {
				t.Fatalf("private failure not logged: %v", err)
			}
			encoded, _ := json.Marshal(logs)
			if strings.Contains(string(encoded), "synthetic-private") || !strings.Contains(string(encoded), code) {
				t.Fatal("request statistics leaked or lost safe failure")
			}
			if _, err := store.GetCodexResourceOwner(context.Background(), application.CodexResourceRealtime, "rtc_safe", "wire-key", time.Now()); !errors.Is(err, domain.ErrNotFound) {
				t.Fatal("failed creation published owner")
			}
		})
	}
}
