package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/backups"
	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func backupServer(t *testing.T) (http.Handler, *http.Cookie, *backups.Directory, *credentials.Vault) {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "codex-lb.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "encryption.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateRuntime(context.Background(), vault); err != nil {
		t.Fatal(err)
	}
	files, err := backups.OpenDirectory(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	server := New(store, vault, Config{}, nil)
	server.ConfigureBackups(application.NewBackupService(sqlite.NewSnapshotter(store, vault), store, files, time.Now))
	token, err := server.auth.SetupPassword(context.Background(), "synthetic-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return server.Handler(nil, nil), &http.Cookie{Name: sessionCookie, Value: token}, files, vault
}

func backupRequest(handler http.Handler, method, path, body string, headers map[string]string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost:2455"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestBackupRoutesCreateListAndDownloadVerifiedSnapshot(t *testing.T) {
	handler, cookie, files, vault := backupServer(t)
	created := backupRequest(handler, "POST", "/api/backups", "", nil, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	var file domain.BackupFile
	if err := json.Unmarshal(created.Body.Bytes(), &file); err != nil || !backups.ValidName(file.Name) || file.SizeBytes == 0 {
		t.Fatalf("created = %+v %v", file, err)
	}
	listed := backupRequest(handler, "GET", "/api/backups", "", nil, cookie)
	var status domain.BackupStatus
	if err := json.Unmarshal(listed.Body.Bytes(), &status); err != nil || listed.Code != http.StatusOK {
		t.Fatalf("status = %d %v", listed.Code, err)
	}
	if len(status.Backups) != 1 || status.Backups[0].Name != file.Name || status.EncryptionKeyIncluded ||
		status.Schedule != (domain.BackupSchedule{IntervalHours: 24, Keep: 3}) || status.LastAttempt == nil ||
		!status.LastAttempt.Succeeded || status.Storage.DatabaseBytes == 0 || status.Storage.TotalBytes == 0 {
		t.Fatalf("status = %+v", status)
	}
	download := backupRequest(handler, "GET", "/api/backups/"+file.Name, "", map[string]string{"Sec-Fetch-Site": "same-origin"}, cookie)
	if download.Code != http.StatusOK || download.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(download.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download = %d %v", download.Code, download.Header())
	}
	saved := filepath.Join(t.TempDir(), "downloaded.sqlite3")
	if err := os.WriteFile(saved, download.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.VerifySnapshot(context.Background(), saved, vault); err != nil {
		t.Fatalf("downloaded snapshot does not verify: %v", err)
	}
	entries, err := os.ReadDir(files.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "key") {
			t.Fatalf("key material in backups directory: %s", entry.Name())
		}
	}
}

func TestBackupRoutesRequireAdminSameOriginAndValidInput(t *testing.T) {
	handler, cookie, _, _ := backupServer(t)
	for _, endpoint := range []struct{ method, path, body string }{
		{"GET", "/api/backups", ""},
		{"POST", "/api/backups", ""},
		{"PUT", "/api/backups/schedule", `{"intervalHours":0,"keep":3}`},
		{"GET", "/api/backups/codex-lb-20261028T031500Z.sqlite3", ""},
	} {
		if got := backupRequest(handler, endpoint.method, endpoint.path, endpoint.body, nil, nil).Code; got != http.StatusUnauthorized {
			t.Fatalf("anonymous %s %s = %d", endpoint.method, endpoint.path, got)
		}
		if got := backupRequest(handler, endpoint.method, endpoint.path, endpoint.body, map[string]string{"Authorization": "Bearer sk-synthetic"}, nil).Code; got != http.StatusUnauthorized {
			t.Fatalf("proxy key %s %s = %d", endpoint.method, endpoint.path, got)
		}
	}
	if got := backupRequest(handler, "POST", "/api/backups", "", map[string]string{"Origin": "https://attacker.invalid"}, cookie).Code; got != http.StatusForbidden {
		t.Fatalf("cross-origin create = %d", got)
	}
	created := backupRequest(handler, "POST", "/api/backups", "", nil, cookie)
	var file domain.BackupFile
	_ = json.Unmarshal(created.Body.Bytes(), &file)
	if got := backupRequest(handler, "GET", "/api/backups/"+file.Name, "", map[string]string{"Sec-Fetch-Site": "cross-site"}, cookie).Code; got != http.StatusForbidden {
		t.Fatalf("cross-site download = %d", got)
	}
	for _, name := range []string{"..%2Fcodex-lb.sqlite3", "codex-lb.sqlite3", "encryption.key", "codex-lb-20261028T031500Z.sqlite3"} {
		if got := backupRequest(handler, "GET", "/api/backups/"+name, "", nil, cookie).Code; got != http.StatusNotFound {
			t.Fatalf("download %q = %d", name, got)
		}
	}
	if got := backupRequest(handler, "POST", "/api/backups", `{"unexpected":true}`, nil, cookie).Code; got != http.StatusBadRequest {
		t.Fatalf("create with body = %d", got)
	}
	for _, body := range []string{`{"intervalHours":169,"keep":3}`, `{"intervalHours":24,"keep":0}`, `{"intervalHours":24}`} {
		if got := backupRequest(handler, "PUT", "/api/backups/schedule", body, nil, cookie).Code; got != http.StatusUnprocessableEntity {
			t.Fatalf("schedule %s = %d", body, got)
		}
	}
	saved := backupRequest(handler, "PUT", "/api/backups/schedule", `{"intervalHours":0,"keep":5}`, nil, cookie)
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), `"keep":5`) {
		t.Fatalf("schedule save = %d %s", saved.Code, saved.Body.String())
	}
}
