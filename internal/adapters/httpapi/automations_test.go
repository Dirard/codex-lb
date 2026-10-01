package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
)

type routeWarmupStub struct {
	calls []string
}

func (s *routeWarmupStub) ExecuteWarmupForGeneration(_ context.Context, accountID string, _ int64, model string, _ *string, _ string) error {
	s.calls = append(s.calls, accountID)
	return nil
}

func newAutomationsTestServer(t *testing.T) (*Server, *application.AutomationsService, *routeWarmupStub, http.Handler) {
	t.Helper()
	server, store, _ := newAccountsTestServer(t, &httpStubOAuth{})
	warmups := &routeWarmupStub{}
	automations := application.NewAutomationsService(store, store, warmups, time.Now)
	admin := http.NewServeMux()
	server.registerAutomationsRoutes(admin, automations)
	protected := server.requireAdmin(admin)
	root := http.NewServeMux()
	server.registerAuth(root)
	root.Handle("/api/", protected)
	return server, automations, warmups, root
}

func TestAutomationsRoutesCRUDRunNowAndRuns(t *testing.T) {
	server, _, warmups, handler := newAutomationsTestServer(t)
	_ = server
	setup := httptest.NewRequest(http.MethodPost, "http://localhost:2455/api/dashboard-auth/password/setup", strings.NewReader(`{"password":"synthetic-test-password"}`))
	setup.Header.Set("Content-Type", "application/json")
	setup.RemoteAddr = "127.0.0.1:5678"
	setupRec := httptest.NewRecorder()
	handler.ServeHTTP(setupRec, setup)
	if setupRec.Code != http.StatusOK {
		t.Fatalf("setup = %d %s", setupRec.Code, setupRec.Body.String())
	}
	cookie := setupRec.Result().Cookies()[0]
	request := func(method, target string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://localhost:2455"+target, bytes.NewReader(body))
		req.RemoteAddr = "127.0.0.1:5678"
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	unauth := httptest.NewRequest(http.MethodGet, "http://localhost:2455/api/automations", nil)
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauth)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth list = %d", unauthRec.Code)
	}

	created := request(http.MethodPost, "/api/automations", []byte(`{
		"name":"Nightly warmups","schedule":{"type":"daily","time":"03:00","timezone":"UTC","thresholdMinutes":0,"days":["mon","tue","wed","thu","fri","sat","sun"]},
		"model":"gpt-5.4-mini","reasoningEffort":"low","prompt":"ping","accountIds":[]
	}`))
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"accountScopeAll":true`) ||
		!strings.Contains(created.Body.String(), `"nextRunAt"`) || !strings.Contains(created.Body.String(), `"lastRun":null`) {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	var job struct {
		ID string `json:"id"`
	}
	decodeJSONBody(t, created.Body.Bytes(), &job)
	if job.ID == "" {
		t.Fatal("job id missing")
	}
	invalid := request(http.MethodPost, "/api/automations", []byte(`{"name":"x","schedule":{"type":"daily","time":"99:00","timezone":"UTC","days":["mon"]},"model":"m"}`))
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "invalid_schedule_time") {
		t.Fatalf("invalid create = %d %s", invalid.Code, invalid.Body.String())
	}
	list := request(http.MethodGet, "/api/automations?limit=10", nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"total":1`) || !strings.Contains(list.Body.String(), `"hasMore":false`) {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	patched := request(http.MethodPatch, "/api/automations/"+job.ID, []byte(`{"enabled":false}`))
	if patched.Code != http.StatusOK || !strings.Contains(patched.Body.String(), `"enabled":false`) || strings.Contains(patched.Body.String(), `"nextRunAt":"`) {
		t.Fatalf("patch = %d %s", patched.Code, patched.Body.String())
	}
	runNow := request(http.MethodPost, "/api/automations/"+job.ID+"/run-now", nil)
	if runNow.Code != http.StatusAccepted {
		t.Fatalf("run-now = %d %s", runNow.Code, runNow.Body.String())
	}
	if len(warmups.calls) != 0 {
		t.Fatalf("disabled job ran warmups: %+v", warmups.calls)
	}
	runs := request(http.MethodGet, "/api/automations/runs", nil)
	if runs.Code != http.StatusOK || !strings.Contains(runs.Body.String(), `"no_available_accounts"`) {
		t.Fatalf("runs = %d %s", runs.Code, runs.Body.String())
	}
	var runList struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	decodeJSONBody(t, runs.Body.Bytes(), &runList)
	if len(runList.Items) != 1 {
		t.Fatalf("runs items = %s", runs.Body.String())
	}
	details := request(http.MethodGet, "/api/automations/runs/"+runList.Items[0].ID+"/details", nil)
	if details.Code != http.StatusOK || !strings.Contains(details.Body.String(), `"totalAccounts":1`) || !strings.Contains(details.Body.String(), `"completedAccounts":1`) {
		t.Fatalf("details = %d %s", details.Code, details.Body.String())
	}
	options := request(http.MethodGet, "/api/automations/options", nil)
	if options.Code != http.StatusOK || !strings.Contains(options.Body.String(), `"scheduleTypes":["daily"]`) {
		t.Fatalf("options = %d %s", options.Code, options.Body.String())
	}
	deleted := request(http.MethodDelete, "/api/automations/"+job.ID, nil)
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"status":"deleted"`) {
		t.Fatalf("delete = %d %s", deleted.Code, deleted.Body.String())
	}
}

func decodeJSONBody(t *testing.T, raw []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatal(err)
	}
}
