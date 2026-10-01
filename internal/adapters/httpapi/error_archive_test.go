package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
)

func TestArchiveAccessIsAdminOnlyAndCannotReadFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	server := New(store, vault, Config{}, nil)
	server.ConfigureErrorArchives(store)
	token, err := server.auth.SetupPassword(ctx, "synthetic-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.archives.RecordFailure(ctx, application.ErrorDiagnostic{RequestID: "error-request", Status: "error", OccurredAt: time.Now(), Model: "gpt-6-sol", Transport: "http", ErrorCode: "upstream_reset", Request: json.RawMessage(`{"input":"synthetic prompt","Authorization":"Bearer dont-return-this"}`)}); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler(nil, nil)
	call := func(path string, admin bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://localhost:2455"+path, nil)
		r.RemoteAddr = "127.0.0.1:123"
		if admin {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if call("/api/conversation-archive/records?requestId=error-request", false).Code != 401 {
		t.Fatal("error contents exposed unauthenticated")
	}
	response := call("/api/conversation-archive/records?requestId=error-request", true)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "synthetic prompt") || strings.Contains(response.Body.String(), "dont-return-this") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("unsafe or broken archive response")
	}
	var page struct {
		Records []archiveRecordView `json:"records"`
		Total   int                 `json:"total"`
	}
	if json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != 1 || len(page.Records) != 1 {
		t.Fatal("archive wire contract")
	}
	for _, suffix := range []string{"?file=../../encryption.key", "?file=errors-2026-99-99", "?requestId=error-request&requestedAt=bad"} {
		if response := call("/api/conversation-archive/records"+suffix, true); response.Code != 400 {
			t.Fatal("unsafe archive path/time accepted")
		}
	}
	response = call("/api/conversation-archive/files", true)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "errors-") {
		t.Fatal("archive list unavailable")
	}
}
