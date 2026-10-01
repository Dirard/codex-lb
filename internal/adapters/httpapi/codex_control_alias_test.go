package httpapi_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
)

func TestCodexControlMethodAndSlashAliases(t *testing.T) {
	var method, path, body string
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		data, _ := io.ReadAll(r.Body)
		if r.Method != method || r.URL.Path != "/codex/"+path || string(data) != body || r.URL.Query().Get("version") != "2" {
			t.Error("control alias changed method/path/body/query")
		}
		w.Header().Set("Content-Type", "application/json")
		if path == "realtime/calls" {
			w.Header().Set("Location", "/v1/live/rtc_alias")
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer upstream.Close()
	_, store, proxy := wireFixtureWithProxy(t, nil)
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	defer adapter.Close()
	ops := application.NewCodexOperations(store, store, adapter, time.Hour)
	ops.ConfigureAdmission(proxy)
	handler := httpapi.NewCodexOperationsHandler(store, ops)
	for _, route := range []struct{ method, path string }{
		{"GET", "thread/goal/get"}, {"POST", "thread/goal/get"}, {"POST", "thread/goal/set"}, {"POST", "thread/goal/clear"},
		{"POST", "analytics-events/events"}, {"POST", "memories/trace_summarize"}, {"POST", "safety/arc"}, {"POST", "alpha/search"},
		{"GET", "agent-identities/jwks"}, {"POST", "realtime/calls"},
	} {
		method, path = route.method, route.path
		body = ""
		if method == "POST" {
			body = `{"retained":"value"}`
		}
		for _, suffix := range []string{"", "/"} {
			request := func(authorized bool) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, "/backend-api/codex/"+path+suffix+"?version=2", strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				if authorized {
					r.Header.Set("Authorization", "Bearer synthetic-key")
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			before := calls
			if w := request(false); w.Code != 401 || calls != before {
				t.Fatalf("alias bypassed authentication: %s %s status=%d", method, path+suffix, w.Code)
			}
			if w := request(true); w.Code != 200 || calls != before+1 {
				t.Fatalf("alias failed: %s %s status=%d body=%s", method, path+suffix, w.Code, w.Body.String())
			}
		}
	}
	before := calls
	r := httptest.NewRequest("DELETE", "/backend-api/codex/thread/goal/get/", nil)
	r.Header.Set("Authorization", "Bearer synthetic-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 405 || calls != before {
		t.Fatal("unsupported control method dispatched")
	}
}
