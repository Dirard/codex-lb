package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// keyPageFixture is a server with keys "a" and "b" (secrets "synthetic-a"
// and "synthetic-b") and a raw connection to its database file for checks
// and for states the store's writers never produce on purpose.
type keyPageFixture struct {
	server  *Server
	store   *sqlite.Store
	handler http.Handler
	raw     *sql.DB
}

func newKeyPageFixture(t *testing.T) keyPageFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "db.sqlite")
	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, id := range []string{"a", "b"} {
		key := domain.APIKey{ID: id, Name: "Key " + id, KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic-"+id))),
			KeyPrefix: "sk-clb-" + id, IsActive: true, CreatedAt: now, UsageSections: "upstream_limits,account_pool_usage"}
		if err := store.SaveAPIKey(context.Background(), key, now); err != nil {
			t.Fatal(err)
		}
	}
	server := New(store, vault, Config{BootstrapToken: "synthetic-bootstrap-token-0123456789abcdef"}, nil)
	fixture := keyPageFixture{server: server, store: store, handler: server.Handler(nil, nil), raw: raw}
	seedKeyPage(t, fixture)
	return fixture
}

func httptestRequest(method, path string, cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest(method, "http://localhost"+path, nil)
	r.RemoteAddr = "127.0.0.1:1234"
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return r
}

func serve(handler http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

// seedKeyPage gives key "a" a group with one ChatGPT account, a daily cost
// limit whose window has ended and requests on that account; key "b" stays
// ungrouped with requests of its own model on an account only it uses.
func seedKeyPage(t *testing.T, f keyPageFixture) {
	t.Helper()
	store := f.store
	ctx := context.Background()
	now := time.Now().UTC()
	for _, account := range []domain.Account{
		{ID: "acct-shared", Kind: domain.AccountChatGPT, Provider: "openai", Email: "shared@example.invalid", PlanType: "pro", Status: domain.AccountActive, CreatedAt: now},
		{ID: "acct-secret", Kind: domain.AccountChatGPT, Provider: "openai", Email: "secret@example.invalid", PlanType: "plus", Status: domain.AccountActive, CreatedAt: now},
	} {
		if err := store.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		reset := now.Add(2 * time.Hour)
		if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: account.ID, Window: "primary", UsedPercent: 40, ResetAt: &reset, ObservedAt: now}); err != nil {
			t.Fatal(err)
		}
		credential, err := f.server.cipher.Encrypt([]byte("synthetic-provider-token"))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: account.ID,
			AccessTokenEncrypted: credential, RefreshTokenEncrypted: credential, IDTokenEncrypted: credential}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveGroup(ctx, domain.AccountGroup{ID: "grp-a", Name: "Team A", AccountIDs: []string{"acct-shared"}}, now); err != nil {
		t.Fatal(err)
	}
	key, err := store.GetAPIKey(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	group := "grp-a"
	key.GroupID = &group
	key.AssignedAccountIDs, key.AccountAssignmentScopeEnabled = []string{"acct-shared"}, true
	if err := store.SaveAPIKey(ctx, key, now); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveGroup(ctx, domain.AccountGroup{ID: "grp-a", Name: "Team A", AccountIDs: []string{"acct-shared"},
		Limits: []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowDaily, MaxValue: 50_000_000}}}, now); err != nil {
		t.Fatal(err)
	}
	for i, event := range []struct{ key, account, model, status, code string }{
		{"a", "acct-shared", "model-a", "success", ""}, {"a", "acct-shared", "model-a", "error", "upstream_error"},
		{"a", "acct-shared", "model-a", "success", ""}, {"b", "acct-secret", "model-of-b", "success", ""},
	} {
		id := fmt.Sprintf("req-%s-%d", event.key, i)
		usage := domain.UsageAmount{InputTokens: 100, OutputTokens: 20, CostMicrodollars: 1_000_000}
		at := now.Add(-time.Duration(4-i) * time.Minute)
		if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: event.key, AccountID: event.account,
			Model: event.model, Budget: usage, Now: at}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SettleUsage(ctx, id, domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{RequestID: id,
			APIKeyID: event.key, AccountID: event.account, Model: event.model, RequestKind: "normal", Status: event.status,
			ErrorCode: event.code, RequestedAt: at, ClientIP: "203.0.113.9", ConversationID: "conv-secret-" + event.key,
			UserAgent: "secret-agent/1.0 (full string)", UserAgentGroup: "codex", Transport: "http", FirstTokenMS: 300,
			TotalLatencyMS: 1500, Usage: usage}}); err != nil {
			t.Fatal(err)
		}
	}
	// End the daily window in storage; the page shows it as zero without
	// writing.
	if _, err := f.raw.Exec(`UPDATE api_key_limits SET reset_at=?,current_value=7000000 WHERE api_key_id='a'`,
		time.Now().Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

// keyPageStorageState is the stored state a key page read must not change.
func keyPageStorageState(t *testing.T, f keyPageFixture) string {
	t.Helper()
	var state string
	if err := f.raw.QueryRow(`SELECT (SELECT group_concat(api_key_id||':'||current_value||':'||reset_at,',') FROM api_key_limits)
 ||'|'||(SELECT count(*)||':'||group_concat(status,',') FROM usage_reservations)||'|'||(SELECT count(*) FROM usage_events)
 ||'|'||(SELECT group_concat(id||':'||coalesce(last_used_at,''),',') FROM api_keys)`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func ownerLogin(t *testing.T, handler http.Handler, secret string) *http.Cookie {
	t.Helper()
	login := reportSessionRequest(handler, "POST", "/api/key-reports/session", secret, nil)
	if login.Code != 200 || len(login.Result().Cookies()) != 1 {
		t.Fatalf("key login %d", login.Code)
	}
	return login.Result().Cookies()[0]
}

// Key A's owner reads only key A through every parameter, never the fields
// reserved to the administrator, and unknown or repeated parameters are
// rejected (R25, R26).
func TestOwnerKeyPageIsBoundToTheKeyAndRedacted(t *testing.T) {
	f := newKeyPageFixture(t)
	handler := f.handler
	cookie := ownerLogin(t, handler, "synthetic-a")
	before := keyPageStorageState(t, f)
	page := reportSessionRequest(handler, "GET", "/api/key-reports/page?timezone=Europe/Prague", "", cookie)
	if page.Code != 200 {
		t.Fatalf("owner page %d %s", page.Code, page.Body.String())
	}
	requests := reportSessionRequest(handler, "GET", "/api/key-reports/requests?limit=2&errors_only=false", "", cookie)
	if requests.Code != 200 {
		t.Fatalf("owner requests %d %s", requests.Code, requests.Body.String())
	}
	for name, body := range map[string]string{"page": page.Body.String(), "requests": requests.Body.String()} {
		for _, forbidden := range []string{`"accountId"`, `"clientIp"`, `"conversationId"`, `"useragent"`, `"errorMessage"`,
			`"byAccount"`, `"lifetime"`, `"shell"`, `"assignedAccounts"`, `"planType"`, `"modelSourceId"`, "acct-", "@example.invalid",
			"203.0.113.9", "conv-secret", "secret-agent", "model-of-b", "req-b-"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("owner %s exposes %s: %s", name, forbidden, body)
			}
		}
	}
	var decoded domain.KeyPageResponse
	if err := json.Unmarshal(page.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Scope != "owner" || decoded.Key.ID != "a" || decoded.Summary.Requests != 3 || decoded.Summary.Errors != 1 ||
		len(decoded.RecentRequests) != 3 || decoded.RecentRequests[0].UserAgentGroup == nil || *decoded.RecentRequests[0].UserAgentGroup != "codex" ||
		len(decoded.ErrorsByCode) != 1 || decoded.ErrorsByCode[0].Code != "upstream_error" || decoded.Key.AssignedAccountCount != 1 ||
		decoded.Group == nil || decoded.Group.Name != "Team A" || len(decoded.Limits) != 1 || !decoded.Limits[0].Expired || decoded.Limits[0].Current != 0 {
		t.Fatalf("owner page %s", page.Body.String())
	}
	var items domain.KeyRequestsResponse
	if err := json.Unmarshal(requests.Body.Bytes(), &items); err != nil || len(items.Items) != 2 || items.NextCursor == nil {
		t.Fatalf("owner requests %s %v", requests.Body.String(), err)
	}
	more := reportSessionRequest(handler, "GET", "/api/key-reports/requests?cursor="+*items.NextCursor, "", cookie)
	if err := json.Unmarshal(more.Body.Bytes(), &items); more.Code != 200 || err != nil || len(items.Items) != 1 || items.Items[0].RequestID != "req-a-0" {
		t.Fatalf("owner load more %d %s", more.Code, more.Body.String())
	}
	for _, path := range []string{
		"/api/key-reports/page?api_key_id=b", "/api/key-reports/page?key_id=b", "/api/key-reports/page?account_id=acct-secret",
		"/api/key-reports/page?start_date=2026-10-01&start_date=2026-10-02", "/api/key-reports/page?model=model-of-b",
		"/api/key-reports/page/?id=b", "/api/key-reports/requests?api_key_id=b", "/api/key-reports/requests?status=error",
		"/api/key-reports/requests?limit=500", "/api/key-reports/requests?limit=x", "/api/key-reports/requests?cursor=%25%25",
		"/api/key-reports/requests?errors_only=maybe",
	} {
		got := reportSessionRequest(handler, "GET", path, "", cookie)
		if got.Code != 400 || !strings.Contains(got.Body.String(), "invalid_report_filters") {
			t.Fatalf("%s: %d %s", path, got.Code, got.Body.String())
		}
	}
	if got := reportSessionRequest(handler, "GET", "/api/key-reports/page?timezone=Mars/Olympus", "", cookie); got.Code != 400 ||
		!strings.Contains(got.Body.String(), "invalid_timezone") {
		t.Fatalf("unknown zone %d", got.Code)
	}
	if got := reportSessionRequest(handler, "GET", "/api/key-reports/page?start_date=2026-10-02&end_date=2026-10-01", "", cookie); got.Code != 400 ||
		!strings.Contains(got.Body.String(), "invalid_report_date_range") {
		t.Fatalf("inverted range %d", got.Code)
	}
	// No mutation method reaches the owner page: the administrator API
	// behind /api/ rejects the report cookie.
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		for _, path := range []string{"/api/key-reports/page", "/api/key-reports/requests"} {
			if got := reportSessionRequest(handler, method, path, "", cookie); got.Code != http.StatusUnauthorized && got.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s: %d", method, path, got.Code)
			}
		}
	}
	// The administrator routes do not accept the report cookie.
	for _, path := range []string{"/api/api-keys/a/page", "/api/api-keys/b/requests"} {
		if got := reportSessionRequest(handler, "GET", path, "", cookie); got.Code != 401 {
			t.Fatalf("report cookie reached %s: %d", path, got.Code)
		}
	}
	if after := keyPageStorageState(t, f); after != before {
		t.Fatalf("owner reads changed storage:\n%s\n%s", before, after)
	}
}

// The pool or group remaining appears for the owner only while upstream
// quotas are visible to the key, for grouped and ungrouped keys; the
// administrator always sees it (R27).
func TestOwnerKeyPageHidesUpstreamQuotas(t *testing.T) {
	f := newKeyPageFixture(t)
	server, store, handler := f.server, f.store, f.handler
	ctx := context.Background()
	admin, err := server.auth.SetupPassword(ctx, "test", "", "synthetic-password", false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pool := func(cookie *http.Cookie, path string) *domain.KeyPagePool {
		t.Helper()
		got := reportSessionRequest(handler, "GET", path, "", cookie)
		var page domain.KeyPageResponse
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &page) != nil {
			t.Fatalf("%s %d %s", path, got.Code, got.Body.String())
		}
		return page.Pool
	}
	owners := map[string]*http.Cookie{"a": ownerLogin(t, handler, "synthetic-a"), "b": ownerLogin(t, handler, "synthetic-b")}
	adminCookie := &http.Cookie{Name: sessionCookie, Value: admin}
	if p := pool(owners["a"], "/api/key-reports/page"); p == nil || p.Scope != "group" || p.AccountCount != 1 {
		t.Fatalf("grouped owner pool %+v", p)
	}
	if p := pool(owners["b"], "/api/key-reports/page"); p == nil || p.Scope != "pool" || p.AccountCount != 2 {
		t.Fatalf("ungrouped owner pool %+v", p)
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.HideUpstreamQuotaFromKeys = true
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	for id, cookie := range owners {
		if p := pool(cookie, "/api/key-reports/page"); p != nil {
			t.Fatalf("owner %s sees hidden upstream quotas: %+v", id, p)
		}
		if p := pool(adminCookie, "/api/api-keys/"+id+"/page"); p == nil {
			t.Fatalf("administrator lost the pool of %s", id)
		}
	}
	if settings, err = store.LoadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	settings.HideUpstreamQuotaFromKeys = false
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	for id := range owners {
		key, err := store.GetAPIKey(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		key.UsageSections = "upstream_limits"
		if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
			t.Fatal(err)
		}
		if p := pool(owners[id], "/api/key-reports/page"); p != nil {
			t.Fatalf("owner %s without account_pool_usage sees the pool: %+v", id, p)
		}
	}
}

// Owner reads apply the proxy allowlist, are no-store, vary by cookie,
// carry Server-Timing, are gzip-compressed, and end with 401 when the key is
// disabled, expired, regenerated or deleted mid-session (R28).
func TestOwnerKeyPageKeepsTheReportSessionRules(t *testing.T) {
	f := newKeyPageFixture(t)
	server, store := f.server, f.store
	ctx := context.Background()
	firewall, err := application.NewFirewall(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	server.ConfigureFirewall(firewall)
	handler := server.Handler(nil, nil)
	cookie := ownerLogin(t, handler, "synthetic-a")
	for _, path := range []string{"/api/key-reports/page", "/api/key-reports/requests", "/api/key-reports/page/"} {
		r := httptestRequest("GET", path, cookie)
		r.Header.Set("Accept-Encoding", "gzip")
		w := serve(handler, r)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(strings.Join(w.Header().Values("Vary"), ","), "Cookie") ||
			!regexp.MustCompile(`^app;dur=\d+\.\d$`).MatchString(w.Header().Get("Server-Timing")) {
			t.Fatalf("%s: %d headers %v", path, w.Code, w.Header())
		}
		if path == "/api/key-reports/page" {
			if w.Header().Get("Content-Encoding") != "gzip" {
				t.Fatalf("owner page not compressed: %v", w.Header())
			}
			reader, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(reader)
			if err != nil || !json.Valid(body) {
				t.Fatalf("compressed owner page %v", err)
			}
		}
	}
	if _, err := firewall.Add(ctx, "192.0.2.7"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/key-reports/page", "/api/key-reports/requests"} {
		if got := serve(handler, httptestRequest("GET", path, cookie)); got.Code != 403 || !strings.Contains(got.Body.String(), "ip_forbidden") {
			t.Fatalf("firewall did not deny %s: %d", path, got.Code)
		}
	}
	if err := firewall.Delete(ctx, "192.0.2.7"); err != nil {
		t.Fatal(err)
	}
	key, err := store.GetAPIKey(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Minute)
	for name, change := range map[string]func(*domain.APIKey){
		"disabled":    func(k *domain.APIKey) { k.IsActive = false },
		"expired":     func(k *domain.APIKey) { k.ExpiresAt = &expired },
		"regenerated": func(k *domain.APIKey) { k.KeyHash = fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic-rotated"))) },
	} {
		next := key
		change(&next)
		if err := store.SaveAPIKey(ctx, next, time.Now()); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/api/key-reports/page", "/api/key-reports/requests"} {
			if got := serve(handler, httptestRequest("GET", path, cookie)); got.Code != 401 {
				t.Fatalf("%s key read %s: %d", name, path, got.Code)
			}
		}
		if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
			t.Fatal(err)
		}
		if got := serve(handler, httptestRequest("GET", "/api/key-reports/page", cookie)); got.Code != 200 {
			t.Fatalf("restored key %d", got.Code)
		}
	}
	if err := store.DeleteAPIKey(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if got := serve(handler, httptestRequest("GET", "/api/key-reports/page", cookie)); got.Code != 401 {
		t.Fatalf("deleted key %d", got.Code)
	}
}
