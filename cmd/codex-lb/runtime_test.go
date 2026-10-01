package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
)

func testConfig(t *testing.T) config {
	t.Helper()
	return config{dataDir: filepath.Join(t.TempDir(), "data"), listen: "127.0.0.1:0", shutdownGrace: time.Second}
}

func TestDataOwnershipAndMissingKey(t *testing.T) {
	cfg := testConfig(t)
	data, err := openData(context.Background(), cfg.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := openData(context.Background(), cfg.dataDir); err == nil {
		other.close()
		t.Fatal("two processes may own one SQLite")
	}
	keyPath := filepath.Join(cfg.dataDir, "encryption.key")
	if info, _ := os.Stat(keyPath); info.Mode().Perm() != 0600 {
		t.Fatal("key permissions")
	}
	if err := data.close(); err != nil {
		t.Fatal(err)
	}
	data, err = openData(context.Background(), cfg.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	data.close()
	if err := os.Rename(keyPath, keyPath+".saved"); err != nil {
		t.Fatal(err)
	}
	if other, err := openData(context.Background(), cfg.dataDir); err == nil {
		other.close()
		t.Fatal("missing key recreated for existing data")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("missing key was replaced")
	}
	if _, err := credentials.Open(keyPath, true); err != nil {
		t.Fatal(err)
	}
	if other, err := openData(context.Background(), cfg.dataDir); err == nil {
		other.close()
		t.Fatal("replacement key accepted")
	}
}

func TestDataDirectoryCannotBecomeSQLiteURIOptions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private?mode=ro&x=1")
	data, err := openData(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	data.close()
	info, err := os.Stat(filepath.Join(dir, "codex-lb.sqlite3"))
	if err != nil || info.Size() == 0 {
		t.Fatal("SQLite opened a different database from the locked file")
	}
}

func TestRuntimeServesEmbeddedUIAndDrains(t *testing.T) {
	cfg := testConfig(t)
	r, err := openRuntime(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.serveListener(ctx, listener) }()
	client := &http.Client{Timeout: 5 * time.Second}
	base := "http://" + listener.Addr().String()
	for _, path := range []string{"/health/ready", "/health/live", "/health/startup", "/"} {
		response, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 {
			t.Fatalf("%s: %d %v", path, response.StatusCode, err)
		}
		if path == "/" && !bytes.Contains(body, []byte("<html")) {
			t.Fatal("UI is not embedded")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not complete")
	}
	// Closing releases the inode lock. Restart uses exactly the same database/key.
	data, err := openData(context.Background(), cfg.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	data.close()
}

func TestRuntimeTransportAllows256ActiveStreamsToOneHost(t *testing.T) {
	r, err := openRuntime(context.Background(), testConfig(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: ready\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-request.Context().Done():
		}
	}))
	defer upstream.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := &http.Client{Transport: r.transport}
	opened := make(chan error, 256)
	finished := make(chan error, 256)
	for range 256 {
		go func() {
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
			response, err := client.Do(request)
			opened <- err
			if err == nil {
				_, err = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
			}
			finished <- err
		}()
	}
	for count := range 256 {
		if err := <-opened; err != nil {
			t.Fatalf("only %d streams reached upstream before release: %v", count, err)
		}
	}
	cancel()
	for range 256 {
		<-finished
	}
}

func TestRequestLifecycleWaitsForActiveHandler(t *testing.T) {
	var lifecycle requestLifecycle
	entered, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	handler := lifecycle.track(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-finish
	}))
	go func() { handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)); close(done) }()
	<-entered
	lifecycle.beginDrain()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
	if response.Code != 503 {
		t.Fatal("new handler admitted while draining")
	}
	close(finish)
	lifecycle.active.Wait()
	<-done
}

func TestCLIConfigurationAndVersion(t *testing.T) {
	t.Setenv("CODEX_LB_DATA_DIR", filepath.Join(t.TempDir(), "stable"))
	t.Setenv("CODEX_LB_CONNECT_ADDRESS", "  lb.internal:2455  ")
	var output bytes.Buffer
	cfg, err := parseConfig("serve", []string{"--trusted-proxy", "127.0.0.1/32"}, &output)
	if err != nil || len(cfg.trusted) != 1 || !filepath.IsAbs(cfg.dataDir) || cfg.connectAddress != "lb.internal:2455" {
		t.Fatalf("%v", err)
	}
	for _, args := range [][]string{{"--data-dir", "relative"}, {"--shutdown-grace", "-1s"}, {"--trusted-proxy", "bad"}} {
		if _, err := parseConfig("serve", args, &output); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	output.Reset()
	if err := run(context.Background(), []string{"version"}, &output, io.Discard); err != nil || strings.TrimSpace(output.String()) != version {
		t.Fatal("version command")
	}
	if err := run(context.Background(), []string{"--help"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_LB_CONNECT_ADDRESS", "unsafe&command")
	if _, err := parseConfig("serve", nil, io.Discard); err == nil || strings.Contains(err.Error(), "unsafe&command") {
		t.Fatal("unsafe connect address accepted or echoed")
	}
}
