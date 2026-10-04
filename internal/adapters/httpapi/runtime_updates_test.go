package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

type runtimeUpdaterStub struct {
	status                  domain.RuntimeUpdateStatus
	checkErr, applyErr      error
	rollbackErr             error
	checks, applies         int
	rollbacks               int
	applyTarget, rollbackTo string
}

func (u *runtimeUpdaterStub) Status(context.Context) (domain.RuntimeUpdateStatus, error) {
	return u.status, nil
}
func (u *runtimeUpdaterStub) Check(context.Context) (domain.RuntimeUpdateStatus, error) {
	u.checks++
	return u.status, u.checkErr
}
func (u *runtimeUpdaterStub) Apply(_ context.Context, version string) (domain.RuntimeUpdateStatus, error) {
	u.applies++
	u.applyTarget = version
	return u.status, u.applyErr
}
func (u *runtimeUpdaterStub) Rollback(_ context.Context, version string) (domain.RuntimeUpdateStatus, error) {
	u.rollbacks++
	u.rollbackTo = version
	return u.status, u.rollbackErr
}

func runtimeUpdateRequest(handler http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost:2455"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestRuntimeUpdateRoutesRequireAdminAndCSRF(t *testing.T) {
	server, _, _ := reportSessionServer(t)
	updater := &runtimeUpdaterStub{status: domain.RuntimeUpdateStatus{CurrentVersion: "go-v1.0.0", Supported: true, Phase: "idle", ReleaseURL: runtimeReleasesURL}}
	server.ConfigureRuntimeUpdater(updater)
	handler := server.Handler(nil, nil)
	adminToken, err := server.auth.SetupPassword(context.Background(), "synthetic-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := &http.Cookie{Name: sessionCookie, Value: adminToken}
	reportLogin := reportSessionRequest(handler, "POST", "/api/key-reports/session", "synthetic-a", nil)
	if reportLogin.Code != http.StatusOK || len(reportLogin.Result().Cookies()) != 1 {
		t.Fatalf("report session setup failed: %d", reportLogin.Code)
	}
	reportCookie := reportLogin.Result().Cookies()[0]
	for _, endpoint := range []struct{ method, path, body string }{
		{"GET", "/api/runtime/updates", ""},
		{"POST", "/api/runtime/updates/check", ""},
		{"POST", "/api/runtime/updates/apply", `{"version":"go-v1.1.0"}`},
		{"POST", "/api/runtime/updates/rollback", `{"version":"go-v1.0.0"}`},
	} {
		if got := runtimeUpdateRequest(handler, endpoint.method, endpoint.path, endpoint.body).Code; got != http.StatusUnauthorized {
			t.Fatalf("anonymous %s %s = %d", endpoint.method, endpoint.path, got)
		}
		if got := runtimeUpdateRequest(handler, endpoint.method, endpoint.path, endpoint.body, reportCookie).Code; got != http.StatusUnauthorized {
			t.Fatalf("key-report %s %s = %d", endpoint.method, endpoint.path, got)
		}
	}
	request := httptest.NewRequest("POST", "http://localhost:2455/api/runtime/updates/check", nil)
	request.AddCookie(adminCookie)
	request.Header.Set("Origin", "https://attacker.invalid")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != http.StatusForbidden || updater.checks != 0 {
		t.Fatalf("cross-origin update accepted: %d calls=%d", w.Code, updater.checks)
	}
}

func TestRuntimeUpdateRoutesStatusAndActions(t *testing.T) {
	server, _, _ := reportSessionServer(t)
	checkedAt := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	updater := &runtimeUpdaterStub{status: domain.RuntimeUpdateStatus{
		CurrentVersion: "go-v1.0.0", LatestVersion: "go-v1.1.0", UpdateAvailable: true,
		CheckedAt: &checkedAt, Source: "github", ReleaseURL: runtimeReleasesURL,
		Supported: true, PreviousVersion: "go-v0.9.0", CanRollback: true, Phase: "idle",
	}}
	server.ConfigureRuntimeUpdater(updater)
	handler := server.Handler(nil, nil)
	token, err := server.auth.SetupPassword(context.Background(), "synthetic-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	for _, path := range []string{"/api/runtime/updates", "/api/runtime/version"} {
		response := runtimeUpdateRequest(handler, "GET", path, "", cookie)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s = %d cache=%q", path, response.Code, response.Header().Get("Cache-Control"))
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["currentVersion"] != "go-v1.0.0" || body["latestVersion"] != "go-v1.1.0" || body["updateAvailable"] != true {
			t.Fatalf("wrong version status: %v", body)
		}
		if path == "/api/runtime/updates" && body["supported"] != true {
			t.Fatalf("missing update capability: %v", body)
		}
	}
	for _, action := range []struct{ path, body string }{
		{"/api/runtime/updates/check", ""},
		{"/api/runtime/updates/apply", `{"version":"go-v1.1.0"}`},
		{"/api/runtime/updates/rollback", `{"version":"go-v0.9.0"}`},
	} {
		response := runtimeUpdateRequest(handler, "POST", action.path, action.body, cookie)
		if response.Code != http.StatusAccepted || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("POST %s = %d cache=%q", action.path, response.Code, response.Header().Get("Cache-Control"))
		}
	}
	if updater.checks != 1 || updater.applies != 1 || updater.rollbacks != 1 || updater.applyTarget != "go-v1.1.0" || updater.rollbackTo != "go-v0.9.0" {
		t.Fatalf("wrong update dispatch: %+v", updater)
	}
}

func TestRuntimeUpdateRoutesRejectMalformedAndMapControllerErrors(t *testing.T) {
	server, _, _ := reportSessionServer(t)
	updater := &runtimeUpdaterStub{status: domain.RuntimeUpdateStatus{CurrentVersion: "go-v1.0.0", Supported: true, Phase: "idle", ReleaseURL: runtimeReleasesURL}}
	server.ConfigureRuntimeUpdater(updater)
	handler := server.Handler(nil, nil)
	token, err := server.auth.SetupPassword(context.Background(), "synthetic-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	for _, tc := range []struct{ path, body string }{
		{"/api/runtime/updates/check", `{}`},
		{"/api/runtime/updates/apply", `{}`},
		{"/api/runtime/updates/apply", `null`},
		{"/api/runtime/updates/apply", `{"version":42}`},
		{"/api/runtime/updates/apply", `{"version":"go-v1.1.0","url":"https://attacker.invalid"}`},
		{"/api/runtime/updates/rollback", `{"version":" go-v1.0.0"}`},
		{"/api/runtime/updates/rollback", `{"version":"go-v1.0.0"} {}`},
	} {
		if got := runtimeUpdateRequest(handler, "POST", tc.path, tc.body, cookie).Code; got != http.StatusBadRequest {
			t.Fatalf("accepted malformed %s %q: %d", tc.path, tc.body, got)
		}
	}
	if updater.checks != 0 || updater.applies != 0 || updater.rollbacks != 0 {
		t.Fatalf("malformed request reached updater: %+v", updater)
	}
	for _, tc := range []struct {
		path, body string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"/api/runtime/updates/check", "", domain.ErrUpdateBusy, http.StatusConflict, "update_busy"},
		{"/api/runtime/updates/apply", `{"version":"go-v1.1.0"}`, domain.ErrUpdateUnavailable, http.StatusServiceUnavailable, "update_unavailable"},
		{"/api/runtime/updates/rollback", `{"version":"go-v0.9.0"}`, domain.ErrUpdateTarget, http.StatusConflict, "update_target_unavailable"},
		{"/api/runtime/updates/apply", `{"version":"go-v1.1.0"}`, errors.New("private executable path"), http.StatusInternalServerError, "internal_error"},
	} {
		updater.checkErr, updater.applyErr, updater.rollbackErr = nil, nil, nil
		switch tc.path {
		case "/api/runtime/updates/check":
			updater.checkErr = tc.err
		case "/api/runtime/updates/apply":
			updater.applyErr = tc.err
		case "/api/runtime/updates/rollback":
			updater.rollbackErr = tc.err
		}
		response := runtimeUpdateRequest(handler, "POST", tc.path, tc.body, cookie)
		if response.Code != tc.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+tc.wantCode+`"`) {
			t.Fatalf("%s error = %d %s", tc.path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "private executable path") {
			t.Fatal("private updater error leaked")
		}
	}
}

func TestRuntimeUpdateRoutesWithoutControllerAreExplicitlyUnavailable(t *testing.T) {
	server, _, _ := reportSessionServer(t)
	handler := server.Handler(nil, nil)
	token, err := server.auth.SetupPassword(context.Background(), "synthetic-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	for _, path := range []string{"/api/runtime/updates", "/api/runtime/version"} {
		response := runtimeUpdateRequest(handler, "GET", path, "", cookie)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"updateAvailable":false`) {
			t.Fatalf("fake update state at %s: %d %s", path, response.Code, response.Body.String())
		}
		if path == "/api/runtime/updates" {
			if !strings.Contains(response.Body.String(), `"checkedAt":null`) || !strings.Contains(response.Body.String(), `"supported":false`) || !strings.Contains(response.Body.String(), `"unavailableReason"`) {
				t.Fatalf("unsupported state unclear: %s", response.Body.String())
			}
		} else if !strings.Contains(response.Body.String(), `"checkedAt":"0001-01-01T00:00:00Z"`) {
			t.Fatalf("legacy timestamp shape changed: %s", response.Body.String())
		}
	}
	if got := runtimeUpdateRequest(handler, "POST", "/api/runtime/updates/check", "", cookie).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("unmanaged check = %d", got)
	}
}
