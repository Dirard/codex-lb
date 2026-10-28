package httpapi_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/domain"
)

// overviewServer is the full handler with a signed-in administrator.
type overviewServer struct {
	t       *testing.T
	store   *sqlite.Store
	handler http.Handler
	cookie  *http.Cookie
	encrypt func([]byte) ([]byte, error)
}

func newOverviewServer(t *testing.T, proxy http.Handler) *overviewServer {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	server := &overviewServer{t: t, store: store, encrypt: vault.Encrypt,
		handler: httpapi.New(store, vault, httpapi.Config{BootstrapToken: "synthetic-bootstrap-token-0123456789abcdef"}, nil).Handler(proxy, nil)}
	result := server.do("POST", "/api/dashboard-auth/password/setup", map[string]string{"password": "synthetic-test-password",
		"bootstrapToken": "synthetic-bootstrap-token-0123456789abcdef"}, nil)
	if result.Code != 200 || len(result.Result().Cookies()) != 1 {
		t.Fatalf("setup %d", result.Code)
	}
	server.cookie = result.Result().Cookies()[0]
	return server
}

func (s *overviewServer) do(method, path string, body any, mutate func(*http.Request)) *httptest.ResponseRecorder {
	s.t.Helper()
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			s.t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, "http://localhost:2455"+path, bytes.NewReader(payload))
	r.RemoteAddr = "127.0.0.1:5678"
	r.Header.Set("Content-Type", "application/json")
	if s.cookie != nil {
		r.AddCookie(s.cookie)
	}
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, r)
	return w
}

func hashHex(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func seedOverviewRoutes(t *testing.T, server *overviewServer) {
	t.Helper()
	store := server.store
	ctx := context.Background()
	now := time.Now().UTC()
	for _, account := range []domain.Account{
		{ID: "acct-a", Kind: domain.AccountChatGPT, Provider: "openai", Email: "a@example.invalid", PlanType: "plus", Status: domain.AccountActive, CreatedAt: now},
		{ID: "acct-r", Kind: domain.AccountChatGPT, Provider: "openai", Email: "r@example.invalid", Alias: "Relogin", PlanType: "pro", Status: domain.AccountReauthRequired, CreatedAt: now},
	} {
		if err := store.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		reset := now.Add(3 * time.Hour)
		if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: account.ID, Window: "primary", UsedPercent: 30, ResetAt: &reset, ObservedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	credential, err := server.encrypt([]byte("synthetic-provider-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: "acct-a",
		AccessTokenEncrypted: credential, RefreshTokenEncrypted: credential, IDTokenEncrypted: credential}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveGroup(ctx, domain.AccountGroup{ID: "grp-1", Name: "Team", AccountIDs: []string{"acct-a", "acct-r"}}, now); err != nil {
		t.Fatal(err)
	}
	group := "grp-1"
	for _, key := range []domain.APIKey{
		{ID: "key-team", Name: "Team key", KeyHash: hashHex("team"), KeyPrefix: "sk-clb-team", GroupID: &group, IsActive: true, AccountAssignmentScopeEnabled: true},
		{ID: "key-solo", Name: "Solo key", KeyHash: hashHex("solo"), KeyPrefix: "sk-clb-solo", IsActive: true,
			Limits: []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowDaily, MaxValue: 100}}},
		{ID: "key-gone", Name: "Gone", KeyHash: hashHex("gone"), KeyPrefix: "sk-clb-gone", IsActive: true},
	} {
		key.CreatedAt = now
		if err := store.SaveAPIKey(ctx, key, now); err != nil {
			t.Fatal(err)
		}
	}
	for i, key := range []string{"key-team", "key-solo", "key-gone"} {
		id := "req-" + key
		usage := domain.UsageAmount{InputTokens: 10, CostMicrodollars: int64(i+1) * 1000000}
		at := now.Add(-time.Duration(i+1) * time.Minute)
		if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: key, AccountID: "acct-a", Model: "gpt-test",
			Budget: usage, Now: at}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SettleUsage(ctx, id, domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{RequestID: id,
			APIKeyID: key, AccountID: "acct-a", Model: "gpt-test", RequestKind: "normal", Status: "success", RequestedAt: at, Usage: usage}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteAPIKey(ctx, "key-gone"); err != nil {
		t.Fatal(err)
	}
}

// GET /api/overview returns the pool, every group and the ungrouped row from
// current membership without deleted keys, alerts with one action each, and
// Server-Timing; it requires an administrator session and a known zone (R5,
// R13, R48, R50).
func TestOverviewRoute(t *testing.T) {
	server := newOverviewServer(t, nil)
	seedOverviewRoutes(t, server)
	anonymous := *server
	anonymous.cookie = nil
	if got := anonymous.do("GET", "/api/overview", nil, nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("anonymous overview %d", got)
	}
	if got := anonymous.do("GET", "/api/directory", nil, nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("anonymous directory %d", got)
	}
	bad := server.do("GET", "/api/overview?timezone=Mars/Olympus", nil, nil)
	if bad.Code != http.StatusBadRequest || !bytes.Contains(bad.Body.Bytes(), []byte("invalid_timezone")) {
		t.Fatalf("unknown zone %d %s", bad.Code, bad.Body.String())
	}
	result := server.do("GET", "/api/overview?timezone=Europe/Prague", nil, nil)
	if result.Code != 200 {
		t.Fatalf("overview %d %s", result.Code, result.Body.String())
	}
	if timing := result.Header().Get("Server-Timing"); !regexp.MustCompile(`^app;dur=\d+\.\d$`).MatchString(timing) {
		t.Fatalf("Server-Timing %q", timing)
	}
	var overview struct {
		Timezone string `json:"timezone"`
		Shell    struct {
			ReauthRequiredAccounts int  `json:"reauthRequiredAccounts"`
			Ready                  bool `json:"ready"`
		} `json:"shell"`
		Pool struct {
			SpendTodayUSD float64 `json:"spendTodayUsd"`
			RequestsToday int64   `json:"requestsToday"`
		} `json:"pool"`
		Groups []struct {
			ID            *string `json:"id"`
			Name          string  `json:"name"`
			Attention     string  `json:"attention"`
			SpendTodayUSD float64 `json:"spendTodayUsd"`
			Keys          struct {
				Total int `json:"total"`
			} `json:"keys"`
			ProblemAccounts []struct {
				ID     string `json:"id"`
				Action string `json:"action"`
			} `json:"problemAccounts"`
		} `json:"groups"`
		Alerts []struct {
			Rule   string `json:"rule"`
			Action struct {
				Kind   string `json:"kind"`
				Target string `json:"target"`
			} `json:"action"`
		} `json:"alerts"`
		AttentionKeys []json.RawMessage `json:"attentionKeys"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if overview.Timezone != "Europe/Prague" || !overview.Shell.Ready || overview.Shell.ReauthRequiredAccounts != 1 {
		t.Fatalf("overview header %+v", overview)
	}
	if overview.Pool.SpendTodayUSD != 6 || overview.Pool.RequestsToday != 3 {
		t.Fatalf("pool spend %+v includes every request", overview.Pool)
	}
	// Both rows are critical (the re-login account belongs to the group and
	// to the whole pool), so the higher spend orders the ungrouped row first.
	if len(overview.Groups) != 2 {
		t.Fatalf("rows %+v", overview.Groups)
	}
	ungrouped, team := overview.Groups[0], overview.Groups[1]
	if team.Name != "Team" || team.Attention != "critical" || team.SpendTodayUSD != 1 || team.Keys.Total != 1 ||
		len(team.ProblemAccounts) != 1 || team.ProblemAccounts[0].Action != "reauth" {
		t.Fatalf("team row %+v", team)
	}
	if ungrouped.ID != nil || ungrouped.SpendTodayUSD != 2 || ungrouped.Keys.Total != 1 {
		t.Fatalf("ungrouped row must hold only the non-deleted ungrouped key: %+v", ungrouped)
	}
	if len(overview.Alerts) == 0 || overview.Alerts[0].Rule != "account_reauth" || overview.Alerts[0].Action.Kind != "reauth" || overview.Alerts[0].Action.Target != "acct-r" {
		t.Fatalf("alerts %+v", overview.Alerts)
	}
	if overview.AttentionKeys == nil {
		t.Fatal("attentionKeys must be a list")
	}
	gzipped := server.do("GET", "/api/overview", nil, func(r *http.Request) { r.Header.Set("Accept-Encoding", "gzip") })
	if gzipped.Header().Get("Content-Encoding") != "gzip" || gzipped.Header().Get("Server-Timing") == "" {
		t.Fatalf("overview must be compressed with Server-Timing: %v", gzipped.Header())
	}
	reader, err := gzip.NewReader(gzipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	if body, err := io.ReadAll(reader); err != nil || !json.Valid(body) {
		t.Fatalf("gzip body %v", err)
	}
}

// GET /api/directory lists non-deleted keys, groups with counts and
// accounts; GET /api/api-keys/{id} returns one key; GET /api/dashboard/status
// returns the shell status with Server-Timing (R1, R3, R17).
func TestDirectoryKeyAndShellStatusRoutes(t *testing.T) {
	server := newOverviewServer(t, nil)
	seedOverviewRoutes(t, server)
	result := server.do("GET", "/api/directory/", nil, nil)
	if result.Code != 200 || result.Header().Get("Server-Timing") == "" {
		t.Fatalf("directory %d %v", result.Code, result.Header())
	}
	var directory domain.Directory
	if err := json.Unmarshal(result.Body.Bytes(), &directory); err != nil {
		t.Fatal(err)
	}
	if len(directory.Keys) != 2 || len(directory.Groups) != 1 || directory.Groups[0].KeyCount != 1 || directory.Groups[0].AccountCount != 2 || len(directory.Accounts) != 2 {
		t.Fatalf("directory %+v", directory)
	}
	key := server.do("GET", "/api/api-keys/key-solo", nil, nil)
	var one domain.APIKey
	if key.Code != 200 || json.Unmarshal(key.Body.Bytes(), &one) != nil || one.ID != "key-solo" || len(one.Limits) != 1 {
		t.Fatalf("key %d %s", key.Code, key.Body.String())
	}
	if got := server.do("GET", "/api/api-keys/key-gone", nil, nil).Code; got != http.StatusNotFound {
		t.Fatalf("deleted key %d", got)
	}
	status := server.do("GET", "/api/dashboard/status", nil, nil)
	var shell domain.ShellStatus
	if status.Code != 200 || json.Unmarshal(status.Body.Bytes(), &shell) != nil || status.Header().Get("Server-Timing") == "" {
		t.Fatalf("status %d %s", status.Code, status.Body.String())
	}
	if !shell.Ready || shell.ReauthRequiredAccounts != 1 || shell.LastSyncAt == nil || !shell.RollupsUsable || shell.RoutingStrategy == "" {
		t.Fatalf("shell %+v", shell)
	}
}

// Proxy routes never carry Server-Timing (R50).
func TestServerTimingIsAbsentOnProxyRoutes(t *testing.T) {
	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	server := newOverviewServer(t, proxy)
	result := server.do("GET", "/v1/models", nil, nil)
	if result.Code != http.StatusTeapot || result.Header().Get("Server-Timing") != "" {
		t.Fatalf("proxy route %d %v", result.Code, result.Header())
	}
}

// The request log accepts 30d and the conversation list 1h and 24h, so one
// period picker drives both (R18).
func TestRequestsPageTimeframes(t *testing.T) {
	server := newOverviewServer(t, nil)
	for _, path := range []string{"/api/request-logs?timeframe=30d", "/api/request-logs/options?timeframe=30d",
		"/api/conversations?timeframe=1h", "/api/conversations?timeframe=24h", "/api/conversations?timeframe=30d"} {
		if got := server.do("GET", path, nil, nil); got.Code != 200 {
			t.Fatalf("%s: %d %s", path, got.Code, got.Body.String())
		}
	}
	if got := server.do("GET", "/api/request-logs?timeframe=90d", nil, nil).Code; got == 200 {
		t.Fatalf("unknown timeframe accepted")
	}
}
