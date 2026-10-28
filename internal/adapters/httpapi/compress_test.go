package httpapi

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/domain"
)

func gunzip(t *testing.T, body io.Reader) string {
	t.Helper()
	reader, err := gzip.NewReader(body)
	if err != nil {
		t.Fatalf("response is not gzip: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func serveCompressed(handler http.Handler, method, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/test", nil)
	if accept != "" {
		req.Header.Set("Accept-Encoding", accept)
	}
	rec := httptest.NewRecorder()
	compressJSON(handler).ServeHTTP(rec, req)
	return rec
}

func jsonHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "999999")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	})
}

func TestCompressJSONGzipsLargeBodies(t *testing.T) {
	large := `{"items":"` + strings.Repeat("codex-lb ", 400) + `"}`
	rec := serveCompressed(jsonHandler(large), http.MethodGet, "gzip, deflate, br")

	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("headers = %v, want gzip with Vary", rec.Header())
	}
	if rec.Header().Get("Content-Length") != "" {
		t.Fatalf("stale Content-Length %q survived compression", rec.Header().Get("Content-Length"))
	}
	if rec.Body.Len() >= len(large) {
		t.Fatalf("compressed body %d bytes is not smaller than %d", rec.Body.Len(), len(large))
	}
	if got := gunzip(t, rec.Body); got != large {
		t.Fatal("decompressed body differs from the handler output")
	}
}

func TestCompressJSONLeavesOtherResponsesUntouched(t *testing.T) {
	large := `{"items":"` + strings.Repeat("x", 4096) + `"}`
	small := `{"ok":true}`
	cases := []struct {
		name    string
		handler http.Handler
		method  string
		accept  string
		body    string
		vary    bool
	}{
		{"small JSON", jsonHandler(small), http.MethodGet, "gzip", small, true},
		{"gzip not accepted", jsonHandler(large), http.MethodGet, "br", large, false},
		{"gzip refused", jsonHandler(large), http.MethodGet, "gzip;q=0, *", large, false},
		{"HEAD", jsonHandler(large), http.MethodHead, "gzip", large, false},
		{"plain text", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, strings.Repeat("e", 4096), http.StatusBadRequest)
		}), http.MethodGet, "gzip", strings.Repeat("e", 4096) + "\n", false},
		{"already encoded", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "identity")
			_, _ = io.WriteString(w, large)
		}), http.MethodGet, "gzip", large, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveCompressed(tc.handler, tc.method, tc.accept)
			if got := rec.Header().Get("Content-Encoding"); got == "gzip" {
				t.Fatal("response must not be gzip-encoded")
			}
			if rec.Body.String() != tc.body {
				t.Fatalf("body changed: %q", rec.Body.String()[:min(rec.Body.Len(), 40)])
			}
			if got := rec.Header().Get("Vary") == "Accept-Encoding"; got != tc.vary {
				t.Fatalf("vary present = %v, want %v", got, tc.vary)
			}
		})
	}
}

func TestCompressJSONFlushStreamsCompressedBytes(t *testing.T) {
	flushed := make(chan string, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"first":`)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush through the wrapper: %v", err)
		}
		flushed <- w.Header().Get("Content-Encoding")
		_, _ = io.WriteString(w, `true}`)
	})
	rec := serveCompressed(handler, http.MethodGet, "gzip")
	if <-flushed != "gzip" || !rec.Flushed {
		t.Fatal("a flushed JSON response must commit to gzip and reach the client")
	}
	if got := gunzip(t, rec.Body); got != `{"first":true}` {
		t.Fatalf("decompressed body = %q", got)
	}
}

func TestCompressJSONOnlyWrapsTheAdminAPI(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, id := range []string{"acct-a", "acct-b", "acct-c", "acct-d"} {
		if err := store.SaveAccount(ctx, domain.Account{ID: id, Kind: domain.AccountChatGPT, Provider: "openai",
			Email: id + "@example.invalid", PlanType: "plus", Status: domain.AccountActive, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	server := New(store, vault, Config{}, nil)
	token, err := server.auth.SetupPassword(ctx, "test", "", "synthetic-test-password", false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	proxyBody := strings.Repeat(`{"delta":"proxied"}`, 200)
	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, proxyBody)
	})
	handler := server.Handler(proxy, nil)
	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("Authorization", "Bearer synthetic-proxy-key")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	overview := request("/api/dashboard/overview")
	if overview.Code != http.StatusOK || overview.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("overview: status %d, encoding %q", overview.Code, overview.Header().Get("Content-Encoding"))
	}
	if overview.Header().Get("Cache-Control") != "no-store" || overview.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers must survive compression: %v", overview.Header())
	}
	var decoded domain.DashboardOverview
	if err := json.Unmarshal([]byte(gunzip(t, overview.Body)), &decoded); err != nil || len(decoded.Accounts) != 4 {
		t.Fatalf("decoded overview: %v, %d accounts", err, len(decoded.Accounts))
	}

	for _, path := range []string{"/api/codex/responses", "/v1/models", "/backend-api/codex/models"} {
		rec := request(path)
		if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != proxyBody {
			t.Fatalf("%s: proxy response was rewritten (encoding %q)", path, rec.Header().Get("Content-Encoding"))
		}
	}

	session := request("/api/dashboard-auth/session")
	if session.Code != http.StatusOK || session.Header().Get("Content-Encoding") != "" {
		t.Fatalf("auth session: status %d, encoding %q", session.Code, session.Header().Get("Content-Encoding"))
	}
}
